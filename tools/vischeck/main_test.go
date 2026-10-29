package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSCN_VIS_002_VischeckFailsOnUnfilteredQuery(t *testing.T) {
	root := t.TempDir()
	write(t, root, "internal/search/queries.sql", `-- name: GoodChunks :many
SELECT c.id FROM chunks c WHERE c.connection_id = ANY(@visible::bigint[]);

-- name: LeakyChunks :many
SELECT c.id, c.text FROM chunks c WHERE c.tsv @@ to_tsquery('simple', @q);

-- name: HalfJoin :many
SELECT p.text FROM passages p JOIN chunks c ON c.item_id = p.item_id
WHERE p.connection_id = ANY(@visible::bigint[]);

-- name: SystemOutsideSync :exec
-- vis:system
DELETE FROM passages WHERE item_id = @item_id;
`)
	write(t, root, "internal/content/queries.sql", `-- name: Tombstone :exec
-- vis:system
DELETE FROM source_items WHERE id = @id;
`)
	write(t, root, "internal/search/search.go", "package search\n\nconst q = `SELECT id FROM entity_aliases WHERE alias_norm = $1`\n")

	findings, err := Check(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range findings {
		got[f.Query] = f.String()
	}
	for _, want := range []string{"LeakyChunks", "HalfJoin", "SystemOutsideSync", "literal"} {
		if _, ok := got[want]; !ok {
			t.Errorf("no finding for %s; findings: %v", want, findings)
		}
	}
	if len(findings) != 4 {
		t.Errorf("want 4 findings, got %v", findings)
	}
	if !strings.Contains(got["LeakyChunks"], "internal/search/queries.sql:4: query LeakyChunks reads chunks") {
		t.Errorf("the finding does not name the query and file: %s", got["LeakyChunks"])
	}

	real, err := Check(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if len(real) != 0 {
		t.Fatalf("the real query files fail: %v", real)
	}
}
