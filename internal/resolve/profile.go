// Package resolve finds duplicates, links mentions and names unnamed
// things (SDD section 12). Deterministic steps find candidates, typed
// decisions rank them, and reviewers confirm.
package resolve

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/assay/internal/entities"
	"github.com/alternayte/assay/internal/models"
)

// MaxProfileTokens is the size limit of a profile (REQ-RES-002).
const MaxProfileTokens = 1500

// Service runs resolution.
type Service struct {
	Pool     *pgxpool.Pool
	Models   *models.Gateway
	Entities *entities.Service
	Log      *slog.Logger
	Now      func() time.Time
}

// ProfileText is the deterministic profile of an entity: display name,
// type, aliases, domain, up to 10 claims and up to 5 source titles, cut to
// MaxProfileTokens.
func ProfileText(r ProfileInputsRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Name: %s\nType: %s\n", oneLine(r.Name), r.Type)
	if len(r.Aliases) > 0 {
		as := make([]string, len(r.Aliases))
		for i, a := range r.Aliases {
			as[i] = oneLine(a)
		}
		fmt.Fprintf(&b, "Aliases: %s\n", strings.Join(as, ", "))
	}
	if r.Domain != "" {
		fmt.Fprintf(&b, "Domain: %s\n", oneLine(r.Domain))
	}
	if len(r.Claims) > 0 {
		b.WriteString("Claims:\n")
		for _, c := range r.Claims {
			fmt.Fprintf(&b, "- %s\n", oneLine(c))
		}
	}
	if len(r.Titles) > 0 {
		b.WriteString("Sources:\n")
		for _, t := range r.Titles {
			fmt.Fprintf(&b, "- %s\n", oneLine(t))
		}
	}
	return cut(strings.TrimSpace(b.String()), MaxProfileTokens)
}

// cut shortens the text until it has limit tokens or less.
func cut(s string, limit int) string {
	for models.EstimateTokens(s) > limit {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:len(r)*9/10]))
	}
	return s
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// Profiles writes the profile of each active entity whose text changed and
// returns how many changed. A changed profile loses its vectors, so the
// embed job embeds it again.
func (s *Service) Profiles(ctx context.Context) (int, error) {
	q := New(s.Pool)
	rows, err := q.ProfileInputs(ctx)
	if err != nil {
		return 0, err
	}
	old, err := q.ProfileHashes(ctx)
	if err != nil {
		return 0, err
	}
	have := make(map[int64][]byte, len(old))
	for _, o := range old {
		have[o.EntityID] = o.Hash
	}
	var tables []string
	for _, fetch := range []func(context.Context) (*models.Space, error){s.Models.ActiveSpace, s.Models.TargetSpace} {
		sp, err := fetch(ctx)
		if err != nil {
			return 0, err
		}
		if sp != nil {
			tables = append(tables, sp.Table())
		}
	}
	changed := 0
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := New(tx)
		for _, r := range rows {
			text := ProfileText(r)
			h := sha256.Sum256([]byte(text))
			if bytes.Equal(have[r.ID], h[:]) {
				continue
			}
			changed++
			if err := q.UpsertProfile(ctx, UpsertProfileParams{EntityID: r.ID, Text: text, Hash: h[:], Connections: r.Connections}); err != nil {
				return err
			}
			for _, t := range tables {
				if _, err := tx.Exec(ctx, fmt.Sprintf(`-- vis:system
DELETE FROM %s WHERE ref_kind = 'profile' AND ref_id = $1`, t), r.ID); err != nil {
					return err
				}
			}
		}
		return q.DeleteStaleProfiles(ctx)
	})
	return changed, err
}
