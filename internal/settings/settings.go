// Package settings holds the operational settings that admins edit in the
// UI. Each change is a new revision with its author, time, old and new value.
package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/auth"
)

// Def defines one setting.
type Def struct {
	Key         string
	Description string
	Default     any
	// Check decodes and validates a value.
	Check func(json.RawMessage) error
}

// Define returns a setting of type T. check validates a decoded value; it can
// be nil.
func Define[T any](key, description string, def T, check func(T) error) Def {
	return Def{
		Key: key, Description: description, Default: def,
		Check: func(raw json.RawMessage) error {
			var v T
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&v); err != nil {
				return err
			}
			if check != nil {
				return check(v)
			}
			return nil
		},
	}
}

// Read returns the current value of a setting of type T. A value that does
// not decode gives the default.
func Read[T any](ctx context.Context, db DBTX, def Def) (T, error) {
	r, err := New(db).Get(ctx, def.Key)
	if errors.Is(err, pgx.ErrNoRows) {
		v, _ := def.Default.(T)
		return v, nil
	}
	if err != nil {
		var zero T
		return zero, err
	}
	var v T
	if err := json.Unmarshal(r.Value, &v); err != nil {
		v, _ = def.Default.(T)
	}
	return v, nil
}

// Service serves the settings routes.
type Service struct {
	Pool *pgxpool.Pool
	Defs []Def
	Now  func() time.Time
}

func (s *Service) def(key string) (Def, bool) {
	for _, d := range s.Defs {
		if d.Key == key {
			return d, true
		}
	}
	return Def{}, false
}

// Seed writes the default of each setting that has no row, as revision 1.
func (s *Service) Seed(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := New(tx)
		for _, d := range s.Defs {
			v, err := json.Marshal(d.Default)
			if err != nil {
				return fmt.Errorf("settings: %s: %w", d.Key, err)
			}
			n, err := q.Seed(ctx, SeedParams{Key: d.Key, Value: v})
			if err != nil {
				return err
			}
			if n == 1 {
				if err := q.SeedHistory(ctx, SeedHistoryParams{Key: d.Key, Value: v}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// Setting is a setting as the API shows it.
type Setting struct {
	Key         string          `json:"key"`
	Description string          `json:"description"`
	Value       json.RawMessage `json:"value"`
	Default     json.RawMessage `json:"default"`
	Revision    int32           `json:"revision"`
	UpdatedBy   string          `json:"updatedBy" doc:"Email of the author. Empty for the default."`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

// Change is one revision of a setting.
type Change struct {
	Revision  int32           `json:"revision"`
	OldValue  json.RawMessage `json:"oldValue"`
	NewValue  json.RawMessage `json:"newValue"`
	UpdatedBy string          `json:"updatedBy"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

// Register adds the settings routes.
func (s *Service) Register(a huma.API) {
	admins := huma.Middlewares{api.Require(auth.RoleAdmin)}

	type listOutput struct {
		Body struct {
			Settings []Setting `json:"settings"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listSettings", Method: http.MethodGet, Path: "/api/v1/settings", Tags: []string{"settings"},
		Summary: "List settings", Middlewares: admins,
	}, func(ctx context.Context, _ *struct{}) (*listOutput, error) {
		rows, err := New(s.Pool).List(ctx)
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &listOutput{}
		out.Body.Settings = []Setting{}
		for _, r := range rows {
			d, ok := s.def(r.Key)
			if !ok {
				continue
			}
			def, _ := json.Marshal(d.Default)
			out.Body.Settings = append(out.Body.Settings, Setting{Key: r.Key, Description: d.Description, Value: r.Value, Default: def,
				Revision: r.Revision, UpdatedBy: r.UpdatedBy, UpdatedAt: r.UpdatedAt})
		}
		sort.Slice(out.Body.Settings, func(i, j int) bool { return out.Body.Settings[i].Key < out.Body.Settings[j].Key })
		return out, nil
	})

	type putInput struct {
		Key  string `path:"key"`
		Body struct {
			Value    json.RawMessage `json:"value"`
			Revision int32           `json:"revision" doc:"The revision that the change is based on. A newer revision gives 409."`
		}
	}
	type settingOutput struct {
		Body Setting
	}
	huma.Register(a, huma.Operation{
		OperationID: "updateSetting", Method: http.MethodPut, Path: "/api/v1/settings/{key}", Tags: []string{"settings"},
		Summary: "Change a setting", Middlewares: admins,
	}, func(ctx context.Context, in *putInput) (*settingOutput, error) {
		d, ok := s.def(in.Key)
		if !ok {
			return nil, api.ErrNotFound
		}
		if err := d.Check(in.Body.Value); err != nil {
			return nil, api.Problem(http.StatusUnprocessableEntity, "invalid_value", "The value is not valid: "+err.Error())
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, in.Body.Value); err != nil {
			return nil, api.Problem(http.StatusUnprocessableEntity, "invalid_value", err.Error())
		}
		caller := api.Caller(ctx).User.ID
		now := s.Now()
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			q := New(tx)
			cur, err := q.Lock(ctx, in.Key)
			if err != nil {
				return err
			}
			if cur.Revision != in.Body.Revision {
				return api.Problem(http.StatusConflict, "stale_revision",
					fmt.Sprintf("The setting changed. The current revision is %d. Reload and try again.", cur.Revision))
			}
			if err := q.Update(ctx, UpdateParams{Key: in.Key, Value: compact.Bytes(), UpdatedBy: &caller, UpdatedAt: now}); err != nil {
				return err
			}
			if err := q.InsertHistory(ctx, InsertHistoryParams{Key: in.Key, Revision: cur.Revision + 1, OldValue: cur.Value,
				NewValue: compact.Bytes(), UpdatedBy: &caller, UpdatedAt: now}); err != nil {
				return err
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "settings.update", Target: "setting:" + in.Key,
				Details: map[string]any{"old": cur.Value, "new": json.RawMessage(compact.Bytes()), "revision": cur.Revision + 1}})
		})
		if err != nil {
			var ae *api.Error
			if errors.As(err, &ae) {
				return nil, ae
			}
			return nil, api.Internal(err)
		}
		r, err := New(s.Pool).Get(ctx, in.Key)
		if err != nil {
			return nil, api.Internal(err)
		}
		def, _ := json.Marshal(d.Default)
		return &settingOutput{Body: Setting{Key: r.Key, Description: d.Description, Value: r.Value, Default: def,
			Revision: r.Revision, UpdatedBy: r.UpdatedBy, UpdatedAt: r.UpdatedAt}}, nil
	})

	type keyInput struct {
		Key string `path:"key"`
	}
	type historyOutput struct {
		Body struct {
			Changes []Change `json:"changes"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "settingHistory", Method: http.MethodGet, Path: "/api/v1/settings/{key}/history", Tags: []string{"settings"},
		Summary: "List the revisions of a setting", Middlewares: admins,
	}, func(ctx context.Context, in *keyInput) (*historyOutput, error) {
		if _, ok := s.def(in.Key); !ok {
			return nil, api.ErrNotFound
		}
		rows, err := New(s.Pool).History(ctx, in.Key)
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &historyOutput{}
		out.Body.Changes = make([]Change, 0, len(rows))
		for _, r := range rows {
			out.Body.Changes = append(out.Body.Changes, Change{Revision: r.Revision, OldValue: r.OldValue, NewValue: r.NewValue, UpdatedBy: r.UpdatedBy, UpdatedAt: r.UpdatedAt})
		}
		return out, nil
	})
}
