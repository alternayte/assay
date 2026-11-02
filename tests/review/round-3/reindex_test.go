package round3

import (
	"bytes"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/testkit"
)

// SDD section 4.2 names the operator command assay reindex. The help must
// list it, and it must rebuild a damaged index from the stored versions.
func TestRVW_3_13_ReindexCommandMissing(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "assay")
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", bin, "./cmd/assay")
	build.Dir = abs
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	help, err := exec.Command(bin, "help").CombinedOutput()
	if err != nil || !strings.Contains(string(help), "reindex") {
		t.Errorf("assay help does not list reindex: %v\n%s", err, help)
	}

	db := testkit.DB(t)
	env := testkit.StartOn(t, db)
	owner := env.Setup("owner@acme.test")
	_, base := testkit.FakeSources(t)
	id := testkit.CreateConnection(t, owner, testkit.Connection{Name: "acme-eng", Kind: "confluence", BaseURL: base,
		Path: "/confluence", Scope: map[string]any{"spaces": []string{"ENG"}}})
	if r := env.RunNow(owner, id, "sync"); r.Status != "succeeded" {
		t.Fatalf("sync: %+v", r)
	}
	chunks := env.Count(`SELECT count(*) FROM chunks`)
	passages := env.Count(`SELECT count(*) FROM passages`)
	env.Count(`WITH d AS (DELETE FROM chunks WHERE ordinal > 0 RETURNING 1) SELECT count(*) FROM d`)
	env.Count(`WITH u AS (UPDATE chunks SET text = 'damaged', tsv = to_tsvector('simple', 'damaged') RETURNING 1) SELECT count(*) FROM u`)

	cmd := exec.Command(bin, "reindex")
	cmd.Env = append(os.Environ(), "ASSAY_DATABASE_URL="+db,
		"ASSAY_SECRET_KEY="+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), "ASSAY_BASE_URL="+testkit.BaseURL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("assay reindex: %v\n%s", err, out)
	}
	if n := env.Count(`SELECT count(*) FROM chunks`); n != chunks {
		t.Errorf("after reindex: %d chunks, want %d", n, chunks)
	}
	if n := env.Count(`SELECT count(*) FROM passages`); n != passages {
		t.Errorf("after reindex: %d passages, want %d", n, passages)
	}
	if n := env.Count(`SELECT count(*) FROM chunks WHERE text = 'damaged' OR tsv @@ to_tsquery('simple', 'damaged')`); n != 0 {
		t.Errorf("after reindex: %d damaged chunks", n)
	}
}
