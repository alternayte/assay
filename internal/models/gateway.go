// Package models holds the model endpoints and functions, the budgets, the
// usage records and the gateway that every model call goes through.
package models

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/assay/internal/outbound"
	"github.com/alternayte/assay/internal/secrets"
)

// Function names a model function.
type Function string

// The model functions.
const (
	Answer  Function = "answer"
	Extract Function = "extract"
	Compile Function = "compile"
	Embed   Function = "embed"
	Rerank  Function = "rerank"
	Decide  Function = "decide"
)

// Functions lists the functions in display order.
var Functions = []Function{Answer, Extract, Compile, Embed, Rerank, Decide}

// Errors of the gateway. Callers use them for the degraded modes.
var (
	ErrDisabled       = errors.New("models: the function is off")
	ErrBudgetExceeded = errors.New("models: the monthly budget of the function is used up")
	ErrInputTooLong   = errors.New("models: the input is too long")
)

// CallError is an error response of an endpoint.
type CallError struct {
	Status  int
	Message string
}

func (e *CallError) Error() string {
	return fmt.Sprintf("the endpoint answered %d: %s", e.Status, e.Message)
}

// defaultTimeouts are the per-function request timeouts (REQ-MOD-007).
var defaultTimeouts = map[Function]time.Duration{
	Answer: 60 * time.Second, Extract: 60 * time.Second, Compile: 60 * time.Second,
	Embed: 30 * time.Second, Rerank: 15 * time.Second, Decide: 15 * time.Second,
}

const retries = 2

// Gateway sends model calls.
type Gateway struct {
	Pool     *pgxpool.Pool
	Secrets  *secrets.Store
	Outbound outbound.Settings
	Now      func() time.Time
	// Timeouts override the default timeouts. Tests set them.
	Timeouts map[Function]time.Duration
	// Backoff is the first retry delay. Zero means 500 ms.
	Backoff time.Duration
}

// Extra is the extra configuration of an endpoint.
type Extra struct {
	Headers map[string]string `json:"headers,omitempty" doc:"Headers that go with each request."`
	Body    map[string]any    `json:"body,omitempty" doc:"Body fields that go with each request, unless the request sets them."`
}

// target is the resolved configuration of one function.
type target struct {
	function   Function
	endpointID int64
	kind       string
	baseURL    string
	model      string
	params     map[string]any
	priceIn    float64
	priceOut   float64
	budget     float64
	key        string
	extra      Extra
}

func (g *Gateway) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// Enabled reports whether a function has an endpoint, a model and the
// enabled flag.
func (g *Gateway) Enabled(ctx context.Context, fn Function) bool {
	_, err := g.target(ctx, fn)
	return err == nil
}

func (g *Gateway) target(ctx context.Context, fn Function) (*target, error) {
	r, err := New(g.Pool).GetFunction(ctx, string(fn))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("models: unknown function %q", fn)
	}
	if err != nil {
		return nil, err
	}
	if !r.Enabled || r.EndpointID == nil || r.Model == "" || r.BaseUrl == nil {
		return nil, ErrDisabled
	}
	t := &target{function: fn, endpointID: *r.EndpointID, kind: deref(r.EndpointKind), baseURL: strings.TrimRight(*r.BaseUrl, "/"), model: r.Model,
		priceIn: r.PriceIn, priceOut: r.PriceOut, budget: r.Budget}
	_ = json.Unmarshal(r.Params, &t.params)
	_ = json.Unmarshal(r.Extra, &t.extra)
	if r.SecretID != nil {
		k, err := g.Secrets.GetByID(ctx, g.Pool, *r.SecretID)
		if err != nil {
			return nil, fmt.Errorf("models: read the key of the endpoint: %w", err)
		}
		t.key = string(k)
	}
	return t, nil
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// EstimateTokens estimates the tokens of a text before a call (D-111).
func EstimateTokens(s string) int {
	runes := (utf8.RuneCountInString(s) + 3) / 4
	return max(runes, len(strings.Fields(s)))
}

// Cost is the price of a call in USD. Prices are per million tokens.
func Cost(tokensIn, tokensOut int, priceIn, priceOut float64) float64 {
	return (float64(tokensIn)*priceIn + float64(tokensOut)*priceOut) / 1e6
}

// checkBudget refuses a call when the spend of the month plus the estimate
// exceeds the budget (REQ-MOD-003).
func (g *Gateway) checkBudget(ctx context.Context, t *target, estIn, estOut int) error {
	spent, err := New(g.Pool).MonthSpend(ctx, MonthSpendParams{Function: string(t.function), MonthStart: monthStart(g.now())})
	if err != nil {
		return err
	}
	if spent+Cost(estIn, estOut, t.priceIn, t.priceOut) > t.budget {
		if err := New(g.Pool).InsertRefusal(ctx, InsertRefusalParams{Function: string(t.function), At: g.now()}); err != nil {
			return err
		}
		return ErrBudgetExceeded
	}
	return nil
}

func (g *Gateway) record(ctx context.Context, t *target, model string, in, out int, ref string) error {
	if model == "" {
		model = t.model
	}
	return New(g.Pool).InsertUsage(ctx, InsertUsageParams{
		Function: string(t.function), Model: model, TokensIn: clamp(in), TokensOut: clamp(out),
		CostUsd: Cost(in, out, t.priceIn, t.priceOut), Ref: ref, At: g.now(),
	})
}

func clamp(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	if n < 0 {
		return 0
	}
	return int32(n)
}

// post sends a JSON request with the timeout of the function and up to two
// retries on 429 and 5xx. The extra body fields of the endpoint fill fields
// that the request does not set.
func (g *Gateway) post(ctx context.Context, t *target, path string, body map[string]any, out any) error {
	for k, v := range t.extra.Body {
		if _, set := body[k]; !set {
			body[k] = v
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	timeout := defaultTimeouts[t.function]
	if d, ok := g.Timeouts[t.function]; ok {
		timeout = d
	}
	client, err := outbound.New(g.Outbound, outbound.Options{BaseURL: t.baseURL})
	if err != nil {
		return err
	}
	backoff := g.Backoff
	if backoff == 0 {
		backoff = 500 * time.Millisecond
	}
	var last error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			wait := backoff << (attempt - 1)
			wait += time.Duration(rand.Int64N(int64(wait)/2 + 1)) //nolint:gosec // Jitter needs no secure source.
			var ra *retryAfter
			if errors.As(last, &ra) && ra.wait > wait {
				wait = ra.wait
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		last = g.once(ctx, client, t, path, payload, timeout, out)
		var ra *retryAfter
		if last == nil || !errors.As(last, &ra) {
			return last
		}
	}
	var ra *retryAfter
	if errors.As(last, &ra) {
		return ra.err
	}
	return last
}

// retryAfter marks an error that a retry can fix.
type retryAfter struct {
	err  error
	wait time.Duration
}

func (r *retryAfter) Error() string { return r.err.Error() }
func (r *retryAfter) Unwrap() error { return r.err }

func (g *Gateway) once(ctx context.Context, client *http.Client, t *target, path string, payload []byte, timeout time.Duration, out any) error {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, t.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range t.extra.Headers {
		req.Header.Set(k, v)
	}
	if t.key != "" {
		req.Header.Set("Authorization", "Bearer "+t.key)
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(cctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return fmt.Errorf("models: %s did not answer within %s", t.function, timeout)
		}
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		var wait time.Duration
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 && s <= 30 {
			wait = time.Duration(s) * time.Second
		}
		return &retryAfter{err: &CallError{Status: resp.StatusCode, Message: errorMessage(b)}, wait: wait}
	}
	if resp.StatusCode >= 400 {
		return &CallError{Status: resp.StatusCode, Message: errorMessage(b)}
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("models: the endpoint answer is not valid JSON: %w", err)
	}
	return nil
}

func errorMessage(b []byte) string {
	var e struct {
		Error any `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != nil {
		switch v := e.Error.(type) {
		case string:
			return v
		case map[string]any:
			if m, ok := v["message"].(string); ok {
				return m
			}
		}
	}
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func (t *target) withParams(body map[string]any) map[string]any {
	for k, v := range t.params {
		if _, set := body[k]; !set {
			body[k] = v
		}
	}
	body["model"] = t.model
	return body
}

// stream sends a streaming request and calls onData with the data of each
// server-sent event until [DONE]. It retries 429 and 5xx answers, but only
// before the stream starts.
func (g *Gateway) stream(ctx context.Context, t *target, path string, body map[string]any, onData func([]byte) error) error {
	for k, v := range t.extra.Body {
		if _, set := body[k]; !set {
			body[k] = v
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	timeout := defaultTimeouts[t.function]
	if d, ok := g.Timeouts[t.function]; ok {
		timeout = d
	}
	client, err := outbound.New(g.Outbound, outbound.Options{BaseURL: t.baseURL})
	if err != nil {
		return err
	}
	backoff := g.Backoff
	if backoff == 0 {
		backoff = 500 * time.Millisecond
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(cctx, http.MethodPost, t.baseURL+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		for k, v := range t.extra.Headers {
			req.Header.Set(k, v)
		}
		if t.key != "" {
			req.Header.Set("Authorization", "Bearer "+t.key)
		}
		resp, err = client.Do(req)
		if err != nil {
			if errors.Is(cctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
				return fmt.Errorf("models: %s did not answer within %s", t.function, timeout)
			}
			return err
		}
		if resp.StatusCode < 400 {
			break
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		callErr := &CallError{Status: resp.StatusCode, Message: errorMessage(b)}
		if (resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500) || attempt >= retries {
			return callErr
		}
		wait := backoff << attempt
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 && s <= 30 {
			wait = max(wait, time.Duration(s)*time.Second)
		}
		select {
		case <-cctx.Done():
			return cctx.Err()
		case <-time.After(wait):
		}
	}
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			return nil
		}
		if err := onData([]byte(data)); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(cctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return fmt.Errorf("models: %s did not finish within %s", t.function, timeout)
		}
		return err
	}
	return nil
}

// Prices returns the prices per million tokens of a function, and whether
// the function is on.
func (g *Gateway) Prices(ctx context.Context, fn Function) (in, out float64, on bool, err error) {
	r, err := New(g.Pool).GetFunction(ctx, string(fn))
	if err != nil {
		return 0, 0, false, err
	}
	return r.PriceIn, r.PriceOut, r.Enabled && r.EndpointID != nil && r.Model != "", nil
}
