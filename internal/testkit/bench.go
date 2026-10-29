package testkit

import (
	"encoding/json"
	"os"
)

// BenchReport appends a benchmark result line to the report file of
// ASSAY_BENCH_REPORT, when it is set.
func BenchReport(scn string, pass bool, ms int64, extra map[string]any) {
	f := os.Getenv("ASSAY_BENCH_REPORT")
	if f == "" {
		return
	}
	line := map[string]any{"scn": scn, "pass": pass, "ms": ms}
	for k, v := range extra {
		line[k] = v
	}
	b, _ := json.Marshal(line)
	fh, err := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // The report path comes from the bench recipe.
	if err != nil {
		return
	}
	_, _ = fh.Write(append(b, '\n'))
	_ = fh.Close()
}
