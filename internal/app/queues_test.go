package app_test

import (
	"testing"

	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/design"
	"github.com/alternayte/assay/internal/extract"
	"github.com/alternayte/assay/internal/testkit"
)

func TestSCN_OPS_010_JobsRunOnTheirQueues(t *testing.T) {
	env := testkit.Start(t)
	rc := env.Server.Extract().River
	ctx := t.Context()

	// A second embed job while one waits would embed the same chunks twice.
	for range 2 {
		if _, err := rc.Insert(ctx, content.EmbedArgs{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := env.Count("SELECT count(*) FROM river_job WHERE kind = 'embed' AND queue = 'embed'"); n != 1 {
		t.Fatalf("%d embed jobs on the embed queue, want 1", n)
	}

	if _, err := rc.Insert(ctx, design.BuildArgs{ConnectionID: 1, Trigger: "sync"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := rc.Insert(ctx, extract.Args{Visible: []int64{1}, Items: []int64{1}, Hashes: []string{"00"}, Trigger: extract.TriggerPage}, nil); err != nil {
		t.Fatal(err)
	}
	if n := env.Count("SELECT count(*) FROM river_job WHERE kind = 'design_build' AND queue = 'design'"); n != 1 {
		t.Fatalf("%d design builds on the design queue, want 1", n)
	}
	if n := env.Count("SELECT count(*) FROM river_job WHERE kind = 'extract' AND queue = 'extract' AND priority = 1"); n != 1 {
		t.Fatalf("%d reader extractions on the extract queue with priority 1, want 1", n)
	}
}
