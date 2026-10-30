package sync

import (
	"context"
	"strings"

	"github.com/alternayte/assay/internal/fixtures"
)

// Demo creates the Acme demo connections for the first admin and queues
// their first sync (REQ-INS-005). base is the URL of the fixture server.
// The engineering space, Jira, GitHub and Azure DevOps are shared with
// Everyone; the restricted space stays with the owner.
func (s *Service) Demo(ctx context.Context, ownerID, base string) error {
	base = strings.TrimRight(base, "/")
	everyone, err := New(s.Pool).EveryoneGroup(ctx)
	if err != nil {
		return err
	}
	conns := []struct {
		body   connectionBody
		shared bool
	}{
		{connectionBody{Kind: "confluence", Name: "acme-engineering", BaseURL: base + "/confluence",
			Scope: map[string]any{"spaces": []string{"ENG"}}}, true},
		{connectionBody{Kind: "confluence", Name: "acme-restricted", BaseURL: base + "/confluence",
			Scope: map[string]any{"spaces": []string{"RST"}}}, false},
		{connectionBody{Kind: "jira", Name: "acme-jira", BaseURL: base + "/jira",
			Scope: map[string]any{"projects": []string{"PAY", "REM"}}}, true},
		{connectionBody{Kind: "github", Name: "acme-github", BaseURL: base + "/github",
			Scope: map[string]any{"repositories": []string{"acme/payments-api", "acme/reminder-service"}}}, true},
		{connectionBody{Kind: "azdo", Name: "acme-azdo", BaseURL: base + "/azdo/" + fixtures.AzdoCollection,
			Scope: map[string]any{"repositories": []string{"Platform/ledger-service"}}}, true},
	}
	for _, c := range conns {
		c.body.Token = fixtures.SourceToken
		c.body.SyncMinutes, c.body.ReconcileHours, c.body.RateConcurrency, c.body.RatePerSecond = 30, 24, 4, 50
		id, err := s.create(ctx, ownerID, c.body)
		if err != nil {
			return err
		}
		if c.shared {
			if err := s.setVisibility(ctx, id, []int64{everyone}, true, ownerID); err != nil {
				return err
			}
		}
		if _, err := s.Queue(ctx, id, "sync", "schedule"); err != nil {
			return err
		}
	}
	return nil
}
