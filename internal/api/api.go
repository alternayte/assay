// Package api holds the HTTP conventions that every feature uses: the error
// envelope, the caller of a request and the role checks.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	authall "github.com/alternayte/auth-all"
	"github.com/alternayte/auth-all/apierr"
	"github.com/danielgtaylor/huma/v2"

	"github.com/alternayte/assay/internal/auth"
)

// Error is the error envelope of every Assay route.
type Error struct {
	status int
	Body   ErrorBody `json:"error"`
}

// ErrorBody is the content of the envelope.
type ErrorBody struct {
	Code    string        `json:"code" doc:"Stable error code" example:"not_found"`
	Message string        `json:"message" doc:"Text for people"`
	Fields  []*FieldError `json:"fields,omitempty" doc:"Invalid input fields"`
}

// FieldError names one invalid input field.
type FieldError struct {
	Location string `json:"location"`
	Message  string `json:"message"`
}

func (e *Error) Error() string  { return e.Body.Message }
func (e *Error) GetStatus() int { return e.status }

// Problem returns an error with a status, a code and a message.
func Problem(status int, code, message string) *Error {
	return &Error{status: status, Body: ErrorBody{Code: code, Message: message}}
}

// Common errors.
var (
	ErrNotFound     = Problem(http.StatusNotFound, "not_found", "The object does not exist.")
	ErrUnauthorized = Problem(http.StatusUnauthorized, "unauthorized", "Sign in to continue.")
	ErrForbidden    = Problem(http.StatusForbidden, "forbidden", "Your role does not permit this action.")
)

// Internal logs the cause of a server error and hides it from the client.
func Internal(cause error) error {
	slog.Default().Error("request failed", "error", cause.Error())
	return Problem(http.StatusInternalServerError, "internal", "The server cannot complete the request.")
}

// FromAuth maps an auth-all error to the Assay envelope.
func FromAuth(err error) error {
	var ae *apierr.Error
	if !errors.As(err, &ae) {
		return Internal(err)
	}
	if ae.Status >= http.StatusInternalServerError {
		return Internal(err)
	}
	return Problem(ae.Status, strings.ToLower(string(ae.Code)), ae.Message)
}

// init makes huma use the Assay envelope for its own errors, such as a
// validation failure. huma.NewError is a global, so it is set once.
func init() {
	// Every Assay route returns empty lists, never null.
	huma.DefaultArrayNullable = false
	huma.NewError = func(status int, message string, errs ...error) huma.StatusError {
		e := Problem(status, codeFor(status), message)
		for _, err := range errs {
			var d *huma.ErrorDetail
			if errors.As(err, &d) {
				e.Body.Fields = append(e.Body.Fields, &FieldError{Location: d.Location, Message: d.Message})
				continue
			}
			if err != nil {
				e.Body.Fields = append(e.Body.Fields, &FieldError{Message: err.Error()})
			}
		}
		if status >= http.StatusInternalServerError {
			e.Body.Message = "The server cannot complete the request."
			e.Body.Fields = nil
		}
		return e
	}
}

func codeFor(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusUnprocessableEntity:
		return "invalid"
	case http.StatusTooManyRequests:
		return "rate_limited"
	default:
		if status >= http.StatusInternalServerError {
			return "internal"
		}
		return "error"
	}
}

var rank = map[string]int{auth.RoleMember: 1, auth.RoleReviewer: 2, auth.RoleAdmin: 3}

// Caller is the principal of the request. It is nil for an anonymous request.
func Caller(ctx context.Context) *authall.Principal {
	return authall.PrincipalFrom(ctx)
}

// Role returns the effective role of p, with the default role for an empty
// value.
func Role(p *authall.Principal) string {
	if p.Role == "" {
		return auth.RoleMember
	}
	return p.Role
}

// AtLeast reports whether the caller has role min or higher.
func AtLeast(ctx context.Context, min string) bool {
	p := Caller(ctx)
	return p != nil && rank[Role(p)] >= rank[min]
}

// Require returns a huma middleware that admits a caller with role min or
// higher. A user who must change the password reaches no route.
func Require(min string) func(huma.Context, func(huma.Context)) {
	if rank[min] == 0 {
		panic("api: unknown role " + min)
	}
	return func(ctx huma.Context, next func(huma.Context)) {
		p := Caller(ctx.Context())
		switch {
		case p == nil:
			WriteError(ctx, ErrUnauthorized)
		case p.User.MustChangePassword:
			WriteError(ctx, Problem(http.StatusForbidden, "password_change_required", "Change your password to continue."))
		case rank[Role(p)] < rank[min]:
			WriteError(ctx, ErrForbidden)
		case p.APIKey != nil && !tokenAllows(p, ctx.Operation()):
			WriteError(ctx, Problem(http.StatusForbidden, "scope_missing", "The access token does not have the scope for this request."))
		default:
			next(ctx)
		}
	}
}

// TokenScope is the metadata of an operation that an access token with scope
// may call. A GET operation needs read without it. An access token cannot call
// any other operation.
func TokenScope(scope string) map[string]any { return map[string]any{"tokenScope": scope} }

// tokenAllows reports whether the access token of p may call op.
func tokenAllows(p *authall.Principal, op *huma.Operation) bool {
	scope, _ := op.Metadata["tokenScope"].(string)
	if scope == "" && (op.Method == http.MethodGet || op.Method == http.MethodHead) {
		scope = auth.ScopeRead
	}
	return scope != "" && p.HasScope(scope)
}

// WriteError writes the envelope of e. Middlewares use it.
func WriteError(ctx huma.Context, e *Error) {
	ctx.SetHeader("Content-Type", "application/json")
	ctx.SetStatus(e.status)
	_ = json.NewEncoder(ctx.BodyWriter()).Encode(e)
}
