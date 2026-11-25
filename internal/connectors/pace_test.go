package connectors_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	gosync "sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/outbound"
	"github.com/alternayte/assay/internal/testkit"
)

// hitLog records the arrival time of each request of a test server.
type hitLog struct {
	mu    gosync.Mutex
	times map[string][]time.Time
}

func (h *hitLog) add(path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.times == nil {
		h.times = map[string][]time.Time{}
	}
	h.times[path] = append(h.times[path], time.Now())
}

func (h *hitLog) of(path string) []time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]time.Time(nil), h.times[path]...)
}

// storedConnection returns a pool on a fresh database with one connection
// row, whose rate the clients share.
func storedConnection(t *testing.T) (*pgxpool.Pool, int64) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), testkit.DB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var id int64
	if err := pool.QueryRow(t.Context(), "INSERT INTO connections (kind, name) VALUES ('jira', 'acme-jira') RETURNING id").Scan(&id); err != nil {
		t.Fatal(err)
	}
	return pool, id
}

// processClient is the client of one worker process for the connection.
func processClient(t *testing.T, base string, pool *pgxpool.Pool, id int64, perSecond float64) *connectors.Client {
	t.Helper()
	c, err := connectors.NewClient(outbound.Settings{}, connectors.ClientOptions{BaseURL: base, Concurrency: 4, PerSecond: perSecond,
		Pool: pool, ConnectionID: id})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSCN_SYN_016_RateHoldsAcrossProcesses(t *testing.T) {
	pool, id := storedConnection(t)
	hits := &hitLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.add("all")
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()
	// Three processes send 10 requests each, 4 at a time, at 20 per second
	// for the connection.
	const perSecond, each = 20, 10
	var wg gosync.WaitGroup
	errs := make(chan error, 3*each)
	for range 3 {
		c := processClient(t, srv.URL, pool, id, perSecond)
		for range each {
			wg.Go(func() {
				var out map[string]any
				if err := c.GetJSON(context.Background(), "/x", nil, &out); err != nil {
					errs <- err
				}
			})
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	times := hits.of("all")
	if len(times) != 3*each {
		t.Fatalf("%d requests, want %d", len(times), 3*each)
	}
	// 30 slots at 50 ms take 1.45 s from the first to the last. Each
	// process alone would take less than 0.5 s.
	span := times[len(times)-1].Sub(times[0])
	if want := time.Duration(float64(len(times)-1) / perSecond * 0.9 * float64(time.Second)); span < want {
		t.Fatalf("%d requests in %s, want %s or more at %d per second", len(times), span, want, perSecond)
	}
	for i := perSecond; i < len(times); i++ {
		if d := times[i].Sub(times[i-perSecond]); d < 900*time.Millisecond {
			t.Fatalf("%d requests in %s, more than the rate of %d per second", perSecond+1, d, perSecond)
		}
	}
}

func TestSCN_SYN_017_RetryAfterHoldsEveryProcess(t *testing.T) {
	pool, id := storedConnection(t)
	hits := &hitLog{}
	var once gosync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.add(r.URL.Path)
		limited := false
		if r.URL.Path == "/limited" {
			once.Do(func() { limited = true })
		}
		if limited {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()
	a := processClient(t, srv.URL, pool, id, 50)
	b := processClient(t, srv.URL, pool, id, 50)
	done := make(chan error, 1)
	go func() {
		var out map[string]any
		done <- a.GetJSON(context.Background(), "/limited", nil, &out)
	}()
	for end := time.Now().Add(10 * time.Second); len(hits.of("/limited")) == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the first client sent no request")
		}
	}
	refused := hits.of("/limited")[0]
	// The second process sends after the 429 of the first. It waits for
	// the Retry-After time too.
	time.Sleep(50 * time.Millisecond)
	var out map[string]any
	if err := b.GetJSON(context.Background(), "/other", nil, &out); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("the retry after the hold: %v", err)
	}
	other := hits.of("/other")
	if len(other) != 1 {
		t.Fatalf("%d requests to /other", len(other))
	}
	if d := other[0].Sub(refused); d < 1800*time.Millisecond {
		t.Fatalf("the second process sent %s after the 429 with Retry-After 2", d)
	}
	if retry := hits.of("/limited"); len(retry) != 2 || retry[1].Sub(refused) < 1800*time.Millisecond {
		t.Fatalf("the retry of the first process: %v", retry)
	}
}
