package ask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/search"
	"github.com/alternayte/assay/internal/visibility"
	"github.com/alternayte/assay/internal/vocab"
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

// QuestionSummary is one thread of the history.
type QuestionSummary struct {
	ID     int64     `json:"id" doc:"The thread ID: the ID of the first question."`
	Text   string    `json:"text" doc:"The first question of the thread."`
	Turns  int       `json:"turns"`
	LastAt time.Time `json:"lastAt" doc:"The time of the last turn."`
}

// Thread is a thread with all its turns in order.
type Thread struct {
	ThreadID int64    `json:"threadId"`
	Turns    []Answer `json:"turns"`
}

var askEvents = map[string]any{"results": ResultsEvent{}, "delta": DeltaEvent{}, "answer": Answer{}, "error": ErrorEvent{}}

// Register adds the Ask, question history and feedback routes.
func (s *Service) Register(a huma.API) {
	members := huma.Middlewares{api.Require(auth.RoleMember)}
	admins := huma.Middlewares{api.Require(auth.RoleAdmin)}
	tags := []string{"ask"}
	type askIn struct {
		Body struct {
			Question string `json:"question" minLength:"1" maxLength:"1000"`
			ThreadID int64  `json:"threadId,omitempty" doc:"A thread of the caller. The question is a follow-up in it. A full thread starts a new thread."`
		}
	}
	op := huma.Operation{
		OperationID: "ask", Method: http.MethodPost, Path: "/api/v1/ask", Tags: tags,
		Summary: "Ask a question",
		Description: "Streams server-sent events: results first, then delta events with the answer text, then the validated answer. " +
			"Without the answer model, the answer event has the results and a notice. The thread of another user gives 404.",
		Middlewares: members, Metadata: api.TokenScope(auth.ScopeRead),
	}
	names := documentEvents(a, &op, askEvents)
	huma.Register(a, op, func(ctx context.Context, in *askIn) (*huma.StreamResponse, error) {
		p := api.Caller(ctx)
		var prior []ThreadTurnsRow
		if in.Body.ThreadID != 0 {
			var err error
			prior, err = s.Thread(ctx, p.User.ID, in.Body.ThreadID)
			if errors.Is(err, ErrNotFound) {
				return nil, api.ErrNotFound
			}
			if err != nil {
				return nil, api.Internal(err)
			}
		}
		return eventStream(names, func(ctx context.Context, send func(any)) {
			vis, err := visibility.OfCaller(ctx, s.Pool)
			if err != nil {
				s.Log.Error("ask failed", "error", err.Error())
				send(ErrorEvent{Message: "The server cannot answer the question."})
				return
			}
			ans, err := s.Ask(ctx, p.User.ID, vis, strings.TrimSpace(in.Body.Question), prior, Events{
				Results: func(es []search.Entity, rs []search.Result, degraded bool) {
					send(ResultsEvent{Entities: es, Results: rs, Degraded: degraded})
				},
				Delta: func(d string) { send(DeltaEvent{Text: d}) },
			})
			if err != nil {
				if ctx.Err() == nil {
					s.Log.Error("ask failed", "error", err.Error())
					send(ErrorEvent{Message: "The server cannot answer the question."})
				}
				return
			}
			send(*ans)
		}), nil
	})

	type listOut struct {
		Body struct {
			Questions []QuestionSummary `json:"questions"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listQuestions", Method: http.MethodGet, Path: "/api/v1/questions", Tags: tags,
		Summary: "List your question threads", Description: "The history is private. Each user sees only their own questions. Each row is one thread.",
		Middlewares: members,
	}, func(ctx context.Context, _ *struct{}) (*listOut, error) {
		rows, err := New(s.Pool).ListThreads(ctx, ListThreadsParams{UserID: api.Caller(ctx).User.ID, Lim: 100})
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &listOut{}
		out.Body.Questions = []QuestionSummary{}
		for _, r := range rows {
			out.Body.Questions = append(out.Body.Questions, QuestionSummary{ID: r.ID, Text: r.Text, Turns: int(r.Turns), LastAt: r.LastAt})
		}
		return out, nil
	})

	type oneOut struct{ Body Thread }
	huma.Register(a, huma.Operation{
		OperationID: "getQuestion", Method: http.MethodGet, Path: "/api/v1/questions/{id}", Tags: tags,
		Summary: "Get the thread of one of your questions",
		Description: "Any turn gives the whole thread. The question of another user gives 404. " +
			"When a source of a turn is no longer visible, the answer of that turn is hidden.",
		Middlewares: members,
	}, func(ctx context.Context, in *struct {
		ID int64 `path:"id"`
	}) (*oneOut, error) {
		rows, err := s.Thread(ctx, api.Caller(ctx).User.ID, in.ID)
		if errors.Is(err, ErrNotFound) {
			return nil, api.ErrNotFound
		}
		if err != nil {
			return nil, api.Internal(err)
		}
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &oneOut{Body: Thread{ThreadID: rows[0].ThreadID, Turns: []Answer{}}}
		for _, r := range rows {
			a, err := turnAnswer(vis, r)
			if err != nil {
				return nil, api.Internal(err)
			}
			out.Body.Turns = append(out.Body.Turns, a)
		}
		return out, nil
	})

	type feedbackIn struct {
		ID   int64 `path:"id"`
		Body struct {
			Rating        string `json:"rating" enum:"up,down"`
			Reason        string `json:"reason,omitempty" maxLength:"2000" doc:"Only a down mark keeps it."`
			WrongCitation bool   `json:"wrongCitation,omitempty" doc:"A citation of the answer is wrong. Only a down mark keeps it."`
		}
	}
	type feedbackOut struct{ Body Feedback }
	huma.Register(a, huma.Operation{
		OperationID: "setFeedback", Method: http.MethodPut, Path: "/api/v1/questions/{id}/feedback", Tags: tags,
		Summary: "Mark one of your answers as helpful or not helpful",
		Description: "One mark per answer. A new mark replaces the old one. Only a down mark keeps the reason and the wrong-citation flag. " +
			"The question of another user gives 404.",
		Middlewares: members,
	}, func(ctx context.Context, in *feedbackIn) (*feedbackOut, error) {
		_, err := New(s.Pool).GetQuestion(ctx, GetQuestionParams{ID: in.ID, UserID: api.Caller(ctx).User.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, api.ErrNotFound
		}
		if err != nil {
			return nil, api.Internal(err)
		}
		f := Feedback{Rating: in.Body.Rating, Reason: strings.TrimSpace(in.Body.Reason), WrongCitation: in.Body.WrongCitation}
		if f.Rating == "up" {
			f.Reason, f.WrongCitation = "", false
		}
		if err := New(s.Pool).SetFeedback(ctx, SetFeedbackParams{QuestionID: in.ID, Rating: f.Rating, Reason: f.Reason,
			WrongCitation: f.WrongCitation, Now: s.now()}); err != nil {
			return nil, api.Internal(err)
		}
		return &feedbackOut{Body: f}, nil
	})

	type improveOut struct {
		Body struct {
			Groups []ImproveGroup `json:"groups"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listQuestionsToImprove", Method: http.MethodGet, Path: "/api/v1/diagnostics/questions", Tags: tags,
		Summary: "List the questions to improve",
		Description: "Questions of all users of the last 90 days with no evidence or a not-helpful mark, grouped by the first thing that each names. " +
			"A question shows only when all connections of its answer are visible to the caller. No row names the asker or holds the answer.",
		Middlewares: admins,
	}, func(ctx context.Context, _ *struct{}) (*improveOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		gs, err := s.ToImprove(ctx, vis)
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &improveOut{}
		out.Body.Groups = gs
		return out, nil
	})

	type ratedOut struct {
		Body struct {
			Questions []RatedQuestion `json:"questions"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listFeedbackQuestions", Method: http.MethodGet, Path: "/api/v1/feedback/questions", Tags: tags,
		Summary: "List the rated questions for an evaluation set",
		Description: "assay eval --from-feedback reads it. Each question has a mark, and all connections of its answer are visible to the caller. " +
			"An up-marked answer gives the titles of its cited items. No row names the asker.",
		Middlewares: admins,
	}, func(ctx context.Context, _ *struct{}) (*ratedOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		qs, err := s.Rated(ctx, vis)
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &ratedOut{}
		out.Body.Questions = qs
		return out, nil
	})
}

// turnAnswer is one stored turn for the reader. A turn that used a
// connection that the reader cannot see now is hidden.
func turnAnswer(vis visibility.Set, r ThreadTurnsRow) (Answer, error) {
	var fb *Feedback
	if r.Rating != "" {
		fb = &Feedback{Rating: r.Rating, Reason: r.Reason, WrongCitation: r.WrongCitation}
	}
	var flags Flags
	_ = json.Unmarshal(r.Flags, &flags)
	for _, c := range flags.Connections {
		if !vis.Has(c) {
			return Answer{QuestionID: r.ID, ThreadID: r.ThreadID, Turn: int(r.Turn), Question: r.Text, Feedback: fb, Cited: []string{},
				Uncited: []string{}, Conflicts: []ConflictBlock{}, Unknowns: []string{}, Sources: []Source{}, Claims: []ContextClaim{},
				Results: []search.Result{}, Links: vocab.LinkText(nil, "", 0), Lint: []vocab.Finding{}, CreatedAt: r.CreatedAt,
				Notice: hiddenAnswer}, nil
		}
	}
	var a Answer
	if err := json.Unmarshal(r.Citations, &a); err != nil {
		return Answer{}, err
	}
	a.QuestionID, a.ThreadID, a.Turn, a.Feedback = r.ID, r.ThreadID, int(r.Turn), fb
	return a, nil
}

// documentEvents adds the event stream response of an operation to the
// OpenAPI document, as the sse package of huma does, and returns the event
// name of each data type. Ask streams without that package so that a
// missing thread gives 404 before the stream starts.
func documentEvents(a huma.API, op *huma.Operation, events map[string]any) map[reflect.Type]string {
	names := map[reflect.Type]string{}
	keys := make([]string, 0, len(events))
	for k := range events {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	schemas := make([]*huma.Schema, 0, len(keys))
	for _, k := range keys {
		t := reflect.TypeOf(events[k])
		names[t] = k
		schemas = append(schemas, &huma.Schema{
			Title: "Event " + k, Type: huma.TypeObject,
			Properties: map[string]*huma.Schema{
				"id":    {Description: "The event ID.", OneOf: []*huma.Schema{{Type: huma.TypeInteger}, {Type: huma.TypeString}}},
				"event": {Type: huma.TypeString, Description: "The event name.", Extensions: map[string]any{"const": k}},
				"data":  a.OpenAPI().Components.Schemas.Schema(t, true, k),
				"retry": {Type: huma.TypeInteger, Description: "The retry time in milliseconds."},
			},
			Required: []string{"data", "event"},
		})
	}
	if op.Responses == nil {
		op.Responses = map[string]*huma.Response{}
	}
	op.Responses["200"] = &huma.Response{Description: "OK", Content: map[string]*huma.MediaType{"text/event-stream": {Schema: &huma.Schema{
		Title: "Server Sent Events",
		Description: "Each oneOf object in the array represents one possible Server Sent Events (SSE) message, " +
			"serialized as UTF-8 text according to the SSE specification.",
		Type:  huma.TypeArray,
		Items: &huma.Schema{Extensions: map[string]any{"oneOf": schemas}},
	}}}}
	return names
}

// eventStream writes each sent value as one server-sent event, named by
// its type, and flushes it.
func eventStream(names map[reflect.Type]string, run func(ctx context.Context, send func(any))) *huma.StreamResponse {
	return &huma.StreamResponse{Body: func(hc huma.Context) {
		hc.SetHeader("Content-Type", "text/event-stream")
		hc.SetStatus(http.StatusOK)
		w := hc.BodyWriter()
		var flusher http.Flusher
		for x := any(w); x != nil; {
			if f, ok := x.(http.Flusher); ok {
				flusher = f
				break
			}
			u, ok := x.(interface{ Unwrap() http.ResponseWriter })
			if !ok {
				break
			}
			x = u.Unwrap()
		}
		flush := func() {
			if flusher != nil {
				flusher.Flush()
			}
		}
		flush()
		run(hc.Context(), func(v any) {
			b, err := json.Marshal(v)
			if err != nil {
				return
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", names[reflect.TypeOf(v)], b)
			flush()
		})
	}}
}
