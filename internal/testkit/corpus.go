package testkit

import (
	"context"
	"fmt"
	"testing"
)

// benchWords are the words of a generated corpus.
var benchWords = []string{"payments", "ledger", "reminder", "service", "queue", "retry", "invoice", "author", "editor", "journal",
	"review", "manuscript", "submission", "billing", "refund", "account", "team", "owner", "deploy", "cluster", "region", "latency",
	"timeout", "schema", "migration", "postgres", "kafka", "cache", "token", "secret", "audit", "export", "import", "report",
	"dashboard", "alert", "incident", "oncall", "runbook", "release", "version", "branch", "pipeline", "build", "test", "contract",
	"gateway", "proxy", "router", "worker", "scheduler", "batch", "stream", "event", "webhook", "email", "notice", "decision"}

// BenchCorpus inserts a connection of the owner with items*per chunks of
// generated text, and returns the connection ID. Each chunk has 60 words.
func (e *Env) BenchCorpus(t testing.TB, ownerEmail string, items, per int) int64 {
	t.Helper()
	ctx := context.Background()
	pool := e.Server.Pool()
	var conn int64
	if err := pool.QueryRow(ctx, `INSERT INTO connections (kind, name, base_url, owner_id)
SELECT 'confluence', 'bench', 'http://bench.acme.test', id FROM auth_users WHERE email = $1 RETURNING id`, ownerEmail).Scan(&conn); err != nil {
		t.Fatal(err)
	}
	steps := []string{
		`INSERT INTO source_items (connection_id, external_id, kind, title, url, content_hash, source_updated_at)
SELECT $1, 'b' || g, 'page', 'Note ' || g || ' ' || ($2::text[])[1 + g % array_length($2::text[], 1)] || ' ' ||
       ($2::text[])[1 + (g * 7) % array_length($2::text[], 1)], 'http://bench.acme.test/' || g, '\x00', now() - g * interval '1 minute'
FROM generate_series(1, $3::int) g`,
		`INSERT INTO source_versions (item_id, connection_id, content_hash, title, body)
SELECT id, connection_id, '\x00', title, '' FROM source_items WHERE connection_id = $1`,
		`UPDATE source_items i SET version_id = v.id FROM source_versions v WHERE v.item_id = i.id AND i.connection_id = $1`,
		`INSERT INTO chunks (item_id, version_id, connection_id, ordinal, text, passage_hashes, tokens, tsv)
SELECT i.id, i.version_id, i.connection_id, n, i.title || E'\n\n' || w.text, ARRAY[sha256((i.id || '-' || n)::text::bytea)], 80,
       setweight(to_tsvector('simple', i.title), 'A') || setweight(to_tsvector('simple', w.text), 'C')
FROM source_items i, generate_series(1, $3::int) n,
LATERAL (SELECT string_agg(($2::text[])[1 + ((i.id * 31 + n * 17 + k * k) % array_length($2::text[], 1))], ' ') AS text
         FROM generate_series(1, 60) k) w
WHERE i.connection_id = $1`,
	}
	for _, q := range steps {
		args := []any{conn}
		switch {
		case q == steps[0]:
			args = append(args, benchWords, items)
		case q == steps[3]:
			args = append(args, benchWords, per)
		}
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("bench corpus: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, "ANALYZE source_items; ANALYZE chunks"); err != nil {
		t.Fatal(err)
	}
	return conn
}

// BenchQuery returns query i of a generated query set: two or three corpus
// words.
func BenchQuery(i int) string {
	n := len(benchWords)
	q := fmt.Sprintf("%s %s", benchWords[(i*7)%n], benchWords[(i*13+5)%n])
	if i%3 == 0 {
		q += " " + benchWords[(i*29+11)%n]
	}
	return q
}
