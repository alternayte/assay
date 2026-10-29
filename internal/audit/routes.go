package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/auth"
)

// Record is one audit entry as the API shows it.
type Record struct {
	ID         int64           `json:"id"`
	At         time.Time       `json:"at"`
	ActorEmail string          `json:"actorEmail" doc:"Empty for the system."`
	ActorToken string          `json:"actorToken,omitempty" doc:"The access token of the request, if any."`
	Action     string          `json:"action"`
	Target     string          `json:"target"`
	Details    json.RawMessage `json:"details"`
}

// Register adds the read-only audit route. No route changes or deletes an
// entry.
func Register(a huma.API, pool *pgxpool.Pool) {
	type listInput struct {
		Action string `query:"action" doc:"An action, such as user.role, or a prefix, such as user."`
		Before int64  `query:"before" doc:"Return entries older than this ID."`
		Limit  int32  `query:"limit" default:"50" minimum:"1" maximum:"200"`
	}
	type listOutput struct {
		Body struct {
			Entries []Record `json:"entries"`
			Next    int64    `json:"next,omitempty" doc:"Pass as before for the next page."`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listAudit", Method: http.MethodGet, Path: "/api/v1/audit", Tags: []string{"audit"},
		Summary: "List audit entries, newest first", Middlewares: huma.Middlewares{api.Require(auth.RoleAdmin)},
	}, func(ctx context.Context, in *listInput) (*listOutput, error) {
		p := ListParams{Lim: in.Limit + 1}
		if in.Action != "" {
			p.Action = &in.Action
		}
		if in.Before > 0 {
			p.BeforeID = &in.Before
		}
		rows, err := New(pool).List(ctx, p)
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &listOutput{}
		out.Body.Entries = []Record{}
		for i, r := range rows {
			if int32(i) == in.Limit { //nolint:gosec // i is below the limit of 201.
				out.Body.Next = rows[i-1].ID
				break
			}
			rec := Record{ID: r.ID, At: r.At, ActorEmail: r.ActorEmail, Action: r.Action, Target: r.Target, Details: r.Details}
			if r.ActorTokenID != nil {
				rec.ActorToken = *r.ActorTokenID
			}
			out.Body.Entries = append(out.Body.Entries, rec)
		}
		return out, nil
	})
}
