package sso

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/directory"
)

// SyncArgs is the directory_sync job. A periodic job inserts it every
// minute, and the job polls when the interval of the setting has passed.
type SyncArgs struct {
	// Force polls at once, after a change of a group mapping.
	Force bool `json:"force,omitempty"`
}

// Kind is the River job kind.
func (SyncArgs) Kind() string { return "directory_sync" }

// InsertOpts puts the job on the default queue.
func (SyncArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: river.QueueDefault} }

// Worker runs the directory sync.
type Worker struct {
	river.WorkerDefaults[SyncArgs]
	S *Service
}

// Work runs one poll. A failed poll records the error and returns nil: the
// next period retries it.
func (w *Worker) Work(ctx context.Context, job *river.Job[SyncArgs]) error {
	return w.S.Sync(ctx, job.Args.Force)
}

// Timeout bounds one poll.
func (w *Worker) Timeout(*river.Job[SyncArgs]) time.Duration { return 30 * time.Minute }

// tick is the period of the periodic job.
const tick = time.Minute

// PeriodicJobs inserts the directory_sync job every minute, unique by the
// minute.
func (s *Service) PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(tick), func() (river.JobArgs, *river.InsertOpts) {
		return SyncArgs{}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByPeriod: tick}}
	}, &river.PeriodicJobOpts{RunOnStart: true, ID: "directory_sync"})}
}

// QueuePoll inserts a forced poll.
func (s *Service) QueuePoll(ctx context.Context) error {
	if s.River == nil {
		return nil
	}
	_, err := s.River.Insert(ctx, SyncArgs{Force: true}, nil)
	return err
}

// Sync polls the directory when the directory sync is on and the interval
// has passed, or at once with force. A failed poll keeps the current state
// and records the error.
func (s *Service) Sync(ctx context.Context, force bool) error {
	e, err := s.Effective(ctx, s.Pool)
	if err != nil {
		return err
	}
	if !e.DirectoryOn() {
		return nil
	}
	q := New(s.Pool)
	st, err := q.GetState(ctx, ProviderID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	now := s.now()
	interval := time.Duration(e.IntervalMinutes) * time.Minute
	if !force && st.LastStartedAt != nil && now.Sub(*st.LastStartedAt) < interval-tick/2 {
		return nil
	}
	if err := q.StartPoll(ctx, StartPollParams{Provider: ProviderID, Now: &now}); err != nil {
		return err
	}
	if err := s.poll(ctx, e, st); err != nil {
		s.Log.Error("directory poll failed", "error", err.Error())
		at := s.now()
		return q.PollFailed(ctx, PollFailedParams{Provider: ProviderID, Now: &at, Error: err.Error()})
	}
	at := s.now()
	return q.PollSucceeded(ctx, PollSucceededParams{Provider: ProviderID, Now: &at})
}

// configKey changes when the tenant or the app changes. A delta link of
// another configuration is not used.
func configKey(e Effective) string {
	sum := sha256.Sum256([]byte(e.Issuer + "\x00" + e.ClientID + "\x00" + e.GraphURL))
	return hex.EncodeToString(sum[:8])
}

// poll reads the users, the access group and the mapped groups first. Only
// a complete read changes the state, so a directory outage disables nobody.
func (s *Service) poll(ctx context.Context, e Effective, st DirectoryState) error {
	p, err := s.directoryProvider(e)
	if err != nil {
		return err
	}
	key := configKey(e)
	since := ""
	if st.ConfigKey == key {
		since = st.DeltaLink
	}
	users, err := p.Users(ctx, since)
	if errors.Is(err, directory.ErrResync) {
		users, err = p.Users(ctx, "")
	}
	if err != nil {
		return fmt.Errorf("read the users: %w", err)
	}
	if users.Full && len(users.Users) == 0 {
		return errors.New("the directory returned no users")
	}
	var access map[string]bool
	if e.AccessGroup != "" {
		ids, err := p.Members(ctx, e.AccessGroup)
		if err != nil {
			return fmt.Errorf("read the access group: %w", err)
		}
		access = set(ids)
	}
	maps, err := New(s.Pool).ListMappings(ctx, ProviderID)
	if err != nil {
		return err
	}
	members := make(map[int64]map[string]bool, len(maps))
	for _, m := range maps {
		ids, err := p.Members(ctx, m.ExternalID)
		if err != nil {
			return fmt.Errorf("read the group %s: %w", m.ExternalName, err)
		}
		members[m.GroupID] = set(ids)
	}

	now := s.now()
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := New(tx)
		seen := make([]string, 0, len(users.Users))
		for _, u := range users.Users {
			if u.Removed {
				if err := q.MarkRemoved(ctx, MarkRemovedParams{Provider: ProviderID, Subject: u.ID}); err != nil {
					return err
				}
				continue
			}
			seen = append(seen, u.ID)
			if err := q.MarkDirectoryUser(ctx, MarkDirectoryUserParams{Provider: ProviderID, Subject: u.ID, Enabled: u.Enabled, Now: now}); err != nil {
				return err
			}
		}
		if users.Full {
			if _, err := q.MarkMissing(ctx, MarkMissingParams{Provider: ProviderID, Seen: seen}); err != nil {
				return err
			}
		}
		return q.SaveDelta(ctx, SaveDeltaParams{Provider: ProviderID, DeltaLink: users.Next, ConfigKey: key})
	})
	if err != nil {
		return err
	}
	return errors.Join(s.applyUsers(ctx, access), s.applyGroups(ctx, maps, members))
}

// applyUsers disables each linked user that the directory disabled or
// removed, or that left the access group. It enables a user again only when
// the sync disabled it.
func (s *Service) applyUsers(ctx context.Context, access map[string]bool) error {
	q := New(s.Pool)
	rows, err := q.ListDirectoryUsers(ctx, ProviderID)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range rows {
		reason := ""
		switch {
		case !r.DirectoryEnabled:
			reason = "directory"
		case access != nil && !access[r.Subject]:
			reason = "access_group"
		}
		switch {
		case reason != "" && r.DisabledAt == nil:
			// The mark comes first. A crash between the two writes then
			// leaves a mark that the next poll can clear.
			if err := q.SetDisabledBySync(ctx, SetDisabledBySyncParams{UserID: r.UserID, DisabledBySync: true}); err != nil {
				return err
			}
			if _, err := s.Auth.Admin.Disable(ctx, r.UserID); err != nil {
				errs = append(errs, fmt.Errorf("disable %s: %w", r.Email, err))
				continue
			}
			if err := audit.Write(ctx, s.Pool, audit.Entry{Action: "user.deactivate", Target: "user:" + r.UserID,
				Details: map[string]any{"source": "directory", "reason": reason}}); err != nil {
				return err
			}
		case reason == "" && r.DisabledAt != nil && r.DisabledBySync:
			if _, err := s.Auth.Admin.Enable(ctx, r.UserID); err != nil {
				errs = append(errs, fmt.Errorf("enable %s: %w", r.Email, err))
				continue
			}
			if err := q.SetDisabledBySync(ctx, SetDisabledBySyncParams{UserID: r.UserID, DisabledBySync: false}); err != nil {
				return err
			}
			if err := audit.Write(ctx, s.Pool, audit.Entry{Action: "user.reactivate", Target: "user:" + r.UserID,
				Details: map[string]any{"source": "directory"}}); err != nil {
				return err
			}
		}
	}
	return errors.Join(errs...)
}

// applyGroups sets the members of each mapped group to the directory
// members that have an Assay user.
func (s *Service) applyGroups(ctx context.Context, maps []ListMappingsRow, members map[int64]map[string]bool) error {
	rows, err := New(s.Pool).ListDirectoryUsers(ctx, ProviderID)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := New(tx)
		for _, m := range maps {
			want := map[string]bool{}
			for _, r := range rows {
				if members[m.GroupID][r.Subject] {
					want[r.UserID] = true
				}
			}
			have, err := q.GroupMembers(ctx, m.GroupID)
			if err != nil {
				return err
			}
			for _, u := range have {
				if !want[u] {
					if err := setMember(ctx, tx, m.GroupID, u, false, "directory"); err != nil {
						return err
					}
				}
				delete(want, u)
			}
			for u := range want {
				if err := setMember(ctx, tx, m.GroupID, u, true, "directory"); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func set(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}
