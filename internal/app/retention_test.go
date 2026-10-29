package app_test

import (
	"testing"

	"github.com/alternayte/assay/internal/testkit"
)

func TestSCN_SET_005_RetentionDeletesOldQuestions(t *testing.T) {
	env := testkit.Start(t)
	env.Setup("owner@acme.test")
	pool := env.Server.Pool()
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `INSERT INTO questions (user_id, text, created_at)
SELECT id, 'old', now() - interval '91 days' FROM auth_users
UNION ALL SELECT id, 'new', now() - interval '89 days' FROM auth_users`); err != nil {
		t.Fatal(err)
	}
	if err := env.Server.ApplyRetention(ctx); err != nil {
		t.Fatal(err)
	}
	if n := env.Count("SELECT count(*) FROM questions WHERE text = 'old'"); n != 0 {
		t.Fatal("the 91-day-old question stays")
	}
	if n := env.Count("SELECT count(*) FROM questions WHERE text = 'new'"); n != 1 {
		t.Fatal("the 89-day-old question is gone")
	}
	// The retention is a setting.
	if _, err := pool.Exec(ctx, `UPDATE settings SET value = jsonb_set(value, '{questionsDays}', '30') WHERE key = 'retention'`); err != nil {
		t.Fatal(err)
	}
	if err := env.Server.ApplyRetention(ctx); err != nil {
		t.Fatal(err)
	}
	if n := env.Count("SELECT count(*) FROM questions"); n != 0 {
		t.Fatalf("%d questions stay with a 30-day retention", n)
	}
}
