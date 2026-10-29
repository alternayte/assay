//go:build bench

package app_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/app"
	"github.com/alternayte/assay/internal/testkit"
)

// TestSCN_OPS_008_ReadyOnMigratedDatabase measures the time from start to a
// ready /readyz on a migrated database.
func TestSCN_OPS_008_ReadyOnMigratedDatabase(t *testing.T) {
	cfg := testConfig(testkit.DB(t))
	cfg.Listen = "127.0.0.1:0"

	start := time.Now()
	s, err := app.New(context.Background(), cfg, app.NewLogger(io.Discard, 0), app.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	addr := make(chan string, 1)
	go func() { _ = s.RunOn(ctx, addr) }()
	base := "http://" + <-addr
	for {
		resp, err := http.Get(base + "/readyz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Since(start) > 10*time.Second {
			t.Fatal("not ready after 10 seconds")
		}
		time.Sleep(20 * time.Millisecond)
	}
	ms := time.Since(start).Milliseconds()
	if f := os.Getenv("ASSAY_BENCH_REPORT"); f != "" {
		line := fmt.Sprintf("{\"scn\":\"SCN-OPS-008\",\"pass\":true,\"ms\":%d}\n", ms)
		fh, err := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fh.WriteString(line)
			_ = fh.Close()
		}
	}
	t.Logf("ready after %d ms", ms)
}
