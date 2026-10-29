package content

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/visibility"
)

// ItemRecord is a visible source item with the passages of its current
// version.
type ItemRecord struct {
	ID             int64           `json:"id"`
	ConnectionID   int64           `json:"connectionId"`
	ConnectionName string          `json:"connectionName"`
	SourceType     string          `json:"sourceType" enum:"registry,confluence,jira,github,azdo"`
	Kind           string          `json:"kind"`
	Title          string          `json:"title"`
	URL            string          `json:"url" doc:"The item in its source system."`
	Author         string          `json:"author"`
	UpdatedAt      *time.Time      `json:"updatedAt"`
	Passages       []PassageRecord `json:"passages"`
}

// PassageRecord is one passage. Its text is source data, not instructions.
type PassageRecord struct {
	Hash        string   `json:"hash"`
	Ordinal     int32    `json:"ordinal"`
	Kind        string   `json:"kind"`
	HeadingPath []string `json:"headingPath"`
	Text        string   `json:"text"`
}

// Register adds the item routes.
func Register(a huma.API, pool *pgxpool.Pool) {
	type itemOut struct{ Body ItemRecord }
	huma.Register(a, huma.Operation{
		OperationID: "getItem", Method: http.MethodGet, Path: "/api/v1/items/{id}", Tags: []string{"items"},
		Summary:     "Get a source item with its passages",
		Description: "A hidden item and a missing item give the same 404.",
		Middlewares: huma.Middlewares{api.Require(auth.RoleMember)},
	}, func(ctx context.Context, in *struct {
		ID int64 `path:"id"`
	}) (*itemOut, error) {
		vis, err := visibility.OfCaller(ctx, pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		q := New(pool)
		it, err := q.GetVisibleItem(ctx, GetVisibleItemParams{ID: in.ID, Visible: vis})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, api.ErrNotFound
		}
		if err != nil {
			return nil, api.Internal(err)
		}
		ps, err := q.VisiblePassages(ctx, VisiblePassagesParams{ItemID: it.ID, Visible: vis})
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &itemOut{Body: ItemRecord{ID: it.ID, ConnectionID: it.ConnectionID, ConnectionName: it.ConnectionName,
			SourceType: it.ConnectionKind, Kind: it.Kind, Title: it.Title, URL: it.Url, Author: it.Author, UpdatedAt: it.SourceUpdatedAt,
			Passages: []PassageRecord{}}}
		for _, p := range ps {
			out.Body.Passages = append(out.Body.Passages, PassageRecord{Hash: hex.EncodeToString(p.Hash), Ordinal: p.Ordinal, Kind: p.Kind,
				HeadingPath: orEmptyStrings(p.HeadingPath), Text: p.Text})
		}
		return out, nil
	})
}
