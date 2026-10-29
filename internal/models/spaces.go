package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Space is an embedding space: the vectors of one embed model.
type Space = EmbeddingSpace

// Table is the vector table of the space.
func (s *EmbeddingSpace) Table() string { return "emb_" + strconv.FormatInt(s.ID, 10) }

// ActiveSpace returns the active space, or nil when no space is active.
func (g *Gateway) ActiveSpace(ctx context.Context) (*Space, error) {
	s, err := New(g.Pool).ActiveSpace(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// TargetSpace returns the space of the current embed model: the active
// space when its model is current, otherwise a building space. It creates
// the building space and its table when none exists. A building space of
// an earlier model is dropped. It returns nil when embed is off.
func (g *Gateway) TargetSpace(ctx context.Context) (*Space, error) {
	t, err := g.target(ctx, Embed)
	if errors.Is(err, ErrDisabled) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if s, err := g.findSpace(ctx, t); s != nil || err != nil {
		return s, err
	}
	probe, err := g.Embed(ctx, []string{"Assay dimension probe"}, "space")
	if err != nil {
		return nil, err
	}
	dim := len(probe.Vectors[0])
	if dim < 1 || dim > 4096 {
		return nil, fmt.Errorf("models: the embed model returns %d dimensions; Assay supports 1 to 4096", dim)
	}
	var out *Space
	err = pgx.BeginFunc(ctx, g.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('embedding_spaces'))"); err != nil {
			return err
		}
		q := New(tx)
		spaces, err := q.ListSpaces(ctx)
		if err != nil {
			return err
		}
		for _, s := range spaces {
			if s.EndpointID != nil && *s.EndpointID == t.endpointID && s.Model == t.model {
				out = &s
				return nil
			}
			if s.Status == "building" {
				if err := dropSpace(ctx, tx, &s); err != nil {
					return err
				}
			}
		}
		s, err := q.InsertSpace(ctx, InsertSpaceParams{EndpointID: &t.endpointID, Model: t.model, Dim: int32(dim)}) //nolint:gosec // dim is at most 4096.
		if err != nil {
			return err
		}
		tbl := s.Table()
		ddl := fmt.Sprintf(`CREATE TABLE %[1]s (
    ref_kind text NOT NULL CHECK (ref_kind IN ('chunk', 'profile')),
    ref_id bigint NOT NULL,
    connection_id bigint NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
    chunk_id bigint REFERENCES chunks (id) ON DELETE CASCADE,
    embedding vector(%[2]d) NOT NULL,
    PRIMARY KEY (ref_kind, ref_id)
);
CREATE INDEX %[1]s_hnsw ON %[1]s USING hnsw (embedding vector_cosine_ops);
CREATE INDEX %[1]s_chunk ON %[1]s (chunk_id);
CREATE INDEX %[1]s_connection ON %[1]s (connection_id);`, tbl, dim)
		if _, err := tx.Exec(ctx, ddl); err != nil {
			return err
		}
		out = &s
		return nil
	})
	return out, err
}

func (g *Gateway) findSpace(ctx context.Context, t *target) (*Space, error) {
	spaces, err := New(g.Pool).ListSpaces(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range spaces {
		if s.EndpointID != nil && *s.EndpointID == t.endpointID && s.Model == t.model {
			return &s, nil
		}
	}
	return nil, nil
}

func dropSpace(ctx context.Context, tx pgx.Tx, s *Space) error {
	if err := New(tx).RetireSpace(ctx, s.ID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "DROP TABLE IF EXISTS "+s.Table())
	return err
}

// ActivateSpace makes a complete building space the active space. In one
// transaction it retires the old active space and drops its table, so that
// queries never mix spaces.
func (g *Gateway) ActivateSpace(ctx context.Context, id int64) error {
	return pgx.BeginFunc(ctx, g.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('embedding_spaces'))"); err != nil {
			return err
		}
		q := New(tx)
		old, err := q.ActiveSpace(ctx)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case old.ID == id:
			return nil
		default:
			if err := dropSpace(ctx, tx, &old); err != nil {
				return err
			}
		}
		return q.ActivateSpace(ctx, ActivateSpaceParams{ID: id, Now: ptr(g.now())})
	})
}

// SetSpaceProgress records the progress of a space.
func (g *Gateway) SetSpaceProgress(ctx context.Context, id int64, done, total int, msg string) error {
	return New(g.Pool).SetSpaceProgress(ctx, SetSpaceProgressParams{ID: id, Done: clamp(done), Total: clamp(total), Error: msg})
}

// Vector formats a vector as a pgvector literal.
func Vector(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

func ptr[T any](v T) *T { return &v }

// EmbedQuery returns the vector of a text in a space. It uses the endpoint
// and model of the space, also while a space of a newer model builds, with
// the prices and budget of the embed function.
func (g *Gateway) EmbedQuery(ctx context.Context, s *Space, text string) ([]float32, error) {
	t, err := g.target(ctx, Embed)
	if err != nil {
		return nil, err
	}
	if s.EndpointID == nil {
		return nil, ErrDisabled
	}
	if t.endpointID != *s.EndpointID || t.model != s.Model {
		e, err := New(g.Pool).GetEndpoint(ctx, *s.EndpointID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrDisabled
		}
		if err != nil {
			return nil, err
		}
		t.endpointID, t.kind, t.baseURL, t.model, t.key = e.ID, e.Kind, strings.TrimRight(e.BaseUrl, "/"), s.Model, ""
		t.extra = Extra{}
		_ = json.Unmarshal(e.Extra, &t.extra)
		if e.SecretID != nil {
			k, err := g.Secrets.GetByID(ctx, g.Pool, *e.SecretID)
			if err != nil {
				return nil, fmt.Errorf("models: read the key of the endpoint: %w", err)
			}
			t.key = string(k)
		}
	}
	res, err := g.embed(ctx, t, []string{text}, "query")
	if err != nil {
		return nil, err
	}
	if len(res.Vectors[0]) != int(s.Dim) {
		return nil, fmt.Errorf("models: the query vector has %d dimensions; the space has %d", len(res.Vectors[0]), s.Dim)
	}
	return res.Vectors[0], nil
}
