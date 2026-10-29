package settings_test

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/testkit"
)

type setting struct {
	Key      string          `json:"key"`
	Value    json.RawMessage `json:"value"`
	Revision int32           `json:"revision"`
}

func get(t *testing.T, c *testkit.Client, key string) setting {
	t.Helper()
	var list struct {
		Settings []setting `json:"settings"`
	}
	c.Must(http.StatusOK, http.MethodGet, "/api/v1/settings", nil).JSON(t, &list)
	for _, s := range list.Settings {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("no setting %s", key)
	return setting{}
}

func TestSCN_SET_001_SettingHistory(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	admin2, _ := env.Invite(admin, "admin2@acme.test", "admin", "admin2-password-1")

	cur := get(t, admin, "retention")
	if cur.Revision != 1 {
		t.Fatalf("default revision %d", cur.Revision)
	}
	first := map[string]int{"questionsDays": 30, "syncRunsDays": 30, "modelUsageDays": 400, "auditDays": 365}
	second := map[string]int{"questionsDays": 60, "syncRunsDays": 30, "modelUsageDays": 400, "auditDays": 365}
	admin.Must(http.StatusOK, http.MethodPut, "/api/v1/settings/retention", map[string]any{"value": first, "revision": 1})
	// A change based on an old revision fails.
	r := admin2.Must(http.StatusConflict, http.MethodPut, "/api/v1/settings/retention", map[string]any{"value": second, "revision": 1})
	if r.Code() != "stale_revision" {
		t.Fatalf("code %q", r.Code())
	}
	admin2.Must(http.StatusOK, http.MethodPut, "/api/v1/settings/retention", map[string]any{"value": second, "revision": 2})
	// A value that does not validate fails.
	admin.Must(http.StatusUnprocessableEntity, http.MethodPut, "/api/v1/settings/retention", map[string]any{"value": map[string]int{"questionsDays": 0}, "revision": 3})

	if got := get(t, admin, "retention").Revision; got != 3 {
		t.Fatalf("revision %d, want 3", got)
	}
	var h struct {
		Changes []struct {
			Revision  int32           `json:"revision"`
			OldValue  json.RawMessage `json:"oldValue"`
			NewValue  json.RawMessage `json:"newValue"`
			UpdatedBy string          `json:"updatedBy"`
			UpdatedAt time.Time       `json:"updatedAt"`
		} `json:"changes"`
	}
	admin.Must(http.StatusOK, http.MethodGet, "/api/v1/settings/retention/history", nil).JSON(t, &h)
	if len(h.Changes) != 3 {
		t.Fatalf("want 3 revisions, got %d", len(h.Changes))
	}
	c3, c2 := h.Changes[0], h.Changes[1]
	if c3.Revision != 3 || c3.UpdatedBy != "admin2@acme.test" || !strings.Contains(string(c3.OldValue), `"questionsDays":30`) || !strings.Contains(string(c3.NewValue), `"questionsDays":60`) {
		t.Fatalf("revision 3: %+v", c3)
	}
	if c2.Revision != 2 || c2.UpdatedBy != "owner@acme.test" || !strings.Contains(string(c2.OldValue), `"questionsDays":90`) || c2.UpdatedAt.IsZero() {
		t.Fatalf("revision 2: %+v", c2)
	}
}

func TestSCN_SET_006_SMTPTestEmail(t *testing.T) {
	smtp := testkit.StartSMTP(t)
	host, port, _ := net.SplitHostPort(smtp.Addr)
	p, _ := strconv.Atoi(port)
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")

	r := admin.Must(http.StatusConflict, http.MethodPost, "/api/v1/settings/smtp/test", map[string]string{"to": "ops@acme.test"})
	if r.Code() != "smtp_off" {
		t.Fatalf("code %q", r.Code())
	}
	cur := get(t, admin, "smtp")
	admin.Must(http.StatusOK, http.MethodPut, "/api/v1/settings/smtp", map[string]any{
		"value":    map[string]any{"host": host, "port": p, "username": "", "from": "assay@acme.test", "security": "none"},
		"revision": cur.Revision,
	})
	admin.Must(http.StatusNoContent, http.MethodPost, "/api/v1/settings/smtp/test", map[string]string{"to": "ops@acme.test"})

	msgs := smtp.Messages()
	if len(msgs) != 1 {
		t.Fatalf("want 1 message, got %d", len(msgs))
	}
	m := msgs[0]
	if m.From != "assay@acme.test" || len(m.To) != 1 || m.To[0] != "ops@acme.test" || !strings.Contains(m.Data, "Subject: Assay test email") {
		t.Fatalf("message %+v", m)
	}

	// With SMTP set, an invite also goes by email.
	var inv struct {
		Emailed bool `json:"emailed"`
	}
	admin.Must(http.StatusCreated, http.MethodPost, "/api/v1/invites", map[string]string{"email": "new@acme.test", "role": "member"}).JSON(t, &inv)
	if !inv.Emailed || len(smtp.Messages()) != 2 || !strings.Contains(smtp.Messages()[1].Data, testkit.BaseURL+"/invite#") {
		t.Fatalf("invite email: emailed=%v messages=%d", inv.Emailed, len(smtp.Messages()))
	}
}
