package ask

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"
	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/search"
	"github.com/alternayte/assay/internal/visibility"
)

// ResultsEvent is the first event of an answer: the search results.
type ResultsEvent struct {
	Entities []search.Entity `json:"entities"`
	Results  []search.Result `json:"results"`
	Degraded bool            `json:"degraded"`
}

// DeltaEvent is a piece of the answer text as the model writes it. The
// answer event replaces the text with the validated answer.
type DeltaEvent struct {
	Text string `json:"text"`
}

// ErrorEvent ends an answer that failed.
type ErrorEvent struct {
	Message string `json:"message"`
}

// QuestionSummary is one question of the history.
type QuestionSummary struct {
	ID        int64     `json:"id"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
}

// Register adds the Ask and question history routes.
func (s *Service) Register(a huma.API) {
	members := huma.Middlewares{api.Require(auth.RoleMember)}
	tags := []string{"ask"}
	type askIn struct {
		Body struct {
			Question string `json:"question" minLength:"1" maxLength:"1000"`
		}
	}
	sse.Register(a, huma.Operation{
		OperationID: "ask", Method: http.MethodPost, Path: "/api/v1/ask", Tags: tags,
		Summary: "Ask a question",
		Description: "Streams server-sent events: results first, then delta events with the answer text, then the validated answer. " +
			"Without the answer model, the answer event has the results and a notice.",
		Middlewares: members,
	}, map[string]any{"results": ResultsEvent{}, "delta": DeltaEvent{}, "answer": Answer{}, "error": ErrorEvent{}},
		func(ctx context.Context, in *askIn, send sse.Sender) {
			p := api.Caller(ctx)
			vis, err := visibility.OfCaller(ctx, s.Pool)
			if err != nil {
				s.Log.Error("ask failed", "error", err.Error())
				_ = send.Data(ErrorEvent{Message: "The server cannot answer the question."})
				return
			}
			ans, err := s.Ask(ctx, p.User.ID, vis, strings.TrimSpace(in.Body.Question), Events{
				Results: func(es []search.Entity, rs []search.Result, degraded bool) {
					_ = send.Data(ResultsEvent{Entities: es, Results: rs, Degraded: degraded})
				},
				Delta: func(d string) { _ = send.Data(DeltaEvent{Text: d}) },
			})
			if err != nil {
				if ctx.Err() == nil {
					s.Log.Error("ask failed", "error", err.Error())
					_ = send.Data(ErrorEvent{Message: "The server cannot answer the question."})
				}
				return
			}
			_ = send.Data(*ans)
		})

	type listOut struct {
		Body struct {
			Questions []QuestionSummary `json:"questions"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listQuestions", Method: http.MethodGet, Path: "/api/v1/questions", Tags: tags,
		Summary: "List your questions", Description: "The history is private. Each user sees only their own questions.",
		Middlewares: members,
	}, func(ctx context.Context, _ *struct{}) (*listOut, error) {
		rows, err := New(s.Pool).ListQuestions(ctx, ListQuestionsParams{UserID: api.Caller(ctx).User.ID, Lim: 100})
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &listOut{}
		out.Body.Questions = []QuestionSummary{}
		for _, r := range rows {
			out.Body.Questions = append(out.Body.Questions, QuestionSummary{ID: r.ID, Text: r.Text, CreatedAt: r.CreatedAt})
		}
		return out, nil
	})

	type oneOut struct{ Body Answer }
	huma.Register(a, huma.Operation{
		OperationID: "getQuestion", Method: http.MethodGet, Path: "/api/v1/questions/{id}", Tags: tags,
		Summary:     "Get one of your questions with its answer",
		Description: "The question of another user gives 404. When a source of the answer is no longer visible, the answer is hidden.",
		Middlewares: members,
	}, func(ctx context.Context, in *struct {
		ID int64 `path:"id"`
	}) (*oneOut, error) {
		q, err := New(s.Pool).GetQuestion(ctx, GetQuestionParams{ID: in.ID, UserID: api.Caller(ctx).User.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, api.ErrNotFound
		}
		if err != nil {
			return nil, api.Internal(err)
		}
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		var flags Flags
		_ = json.Unmarshal(q.Flags, &flags)
		for _, c := range flags.Connections {
			if !vis.Has(c) {
				return &oneOut{Body: Answer{QuestionID: q.ID, Question: q.Text, Cited: []string{}, Uncited: []string{},
					Conflicts: []ConflictBlock{}, Unknowns: []string{}, Sources: []Source{}, Claims: []ContextClaim{},
					Results: []search.Result{}, CreatedAt: q.CreatedAt,
					Notice: &Notice{Code: "hidden", Message: "The answer used content that you cannot read now. Ask the question again."}}}, nil
			}
		}
		var a Answer
		if err := json.Unmarshal(q.Citations, &a); err != nil {
			return nil, api.Internal(err)
		}
		a.QuestionID = q.ID
		return &oneOut{Body: a}, nil
	})
}
