// Command trace links the specification, the ledger and the test reports.
//
//	trace trace    --sdd SPEC --ledger LEDGER --reports DIR
//	trace evidence --sdd SPEC --ledger LEDGER --reports DIR --out DIR
//	trace check    --sdd SPEC --evidence DIR
//
// trace fails when a requirement has no scenario, a scenario has no passing
// test, a test names an undefined scenario, or the ledger and the
// specification list other IDs. evidence writes <out>/<HEAD sha>.md with the
// gate results, durations, counts, tool versions and the specification
// hash. check passes when the tree is clean and the evidence for HEAD
// exists, passed and matches the specification hash.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: trace trace|evidence|check [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("trace "+os.Args[1], flag.ExitOnError)
	sdd := fs.String("sdd", "", "the specification")
	ledger := fs.String("ledger", "", "the ledger")
	reports := fs.String("reports", "", "the reports directory")
	out := fs.String("out", "", "the evidence directory to write")
	evidence := fs.String("evidence", "", "the evidence directory to check")
	_ = fs.Parse(os.Args[2:])
	if *sdd == "" {
		fail(errors.New("--sdd is required"))
	}
	if _, err := os.Stat(*sdd); err != nil {
		fail(fmt.Errorf("the specification is missing: %w", err))
	}
	var err error
	switch os.Args[1] {
	case "trace":
		var r *Result
		r, err = Trace(*sdd, *ledger, *reports)
		if r != nil {
			fmt.Print(r.Summary())
		}
	case "evidence":
		var r *Result
		if r, err = Trace(*sdd, *ledger, *reports); err == nil {
			var path string
			path, err = WriteEvidence(r, *sdd, *reports, *out)
			fmt.Println("evidence:", path)
		}
	case "check":
		err = Check(*sdd, *evidence)
		if err == nil {
			fmt.Println("evidence-check: passed")
		}
	default:
		err = fmt.Errorf("unknown mode %q", os.Args[1])
	}
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "trace:", err)
	os.Exit(1)
}

var (
	reqRow   = regexp.MustCompile(`(?m)^\| ((?:REQ|NFR)-[A-Z0-9]+(?:-[0-9]+)?|SI-[0-9]+) \|`)
	scnLine  = regexp.MustCompile(`(?m)^- \*\*(SCN-[A-Z]+-[0-9]+)\*\* \[([A-Z])\] \(([^)]*)\)`)
	ledRow   = regexp.MustCompile(`(?m)^\| ((?:REQ|NFR|SCN)-[A-Z0-9-]+|SI-[0-9]+) \| ([A-Z_]+) \|`)
	goTest   = regexp.MustCompile(`^TestSCN_([A-Z]+)_([0-9]+)_`)
	scnTitle = regexp.MustCompile(`^(SCN-[A-Z]+-[0-9]+) `)
)

// Spec is the IDs of the specification.
type Spec struct {
	Reqs      []string
	Scenarios map[string][]string // scenario ID → requirement IDs
	Layers    map[string]string   // scenario ID → layer letter
}

// ParseSpec reads requirement rows and scenario lines.
func ParseSpec(text string) Spec {
	s := Spec{Scenarios: map[string][]string{}, Layers: map[string]string{}}
	for _, m := range reqRow.FindAllStringSubmatch(text, -1) {
		if !slices.Contains(s.Reqs, m[1]) {
			s.Reqs = append(s.Reqs, m[1])
		}
	}
	for _, m := range scnLine.FindAllStringSubmatch(text, -1) {
		var refs []string
		for _, r := range strings.Split(m[3], ",") {
			refs = append(refs, strings.TrimSpace(r))
		}
		s.Scenarios[m[1]] = refs
		s.Layers[m[1]] = m[2]
	}
	return s
}

// Tests are the results of the test reports by scenario ID.
type Tests struct {
	Passed map[string][]string // scenario ID → passing test names
	Failed map[string][]string
	Count  map[string]int // report file → number of results
	Took   map[string]time.Duration
}

// ReadReports reads go test -json files, the Playwright JSON report and the
// JSON lines of the smoke and bench layers.
func ReadReports(dir string) (Tests, error) {
	t := Tests{Passed: map[string][]string{}, Failed: map[string][]string{}, Count: map[string]int{}, Took: map[string]time.Duration{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return t, err
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		switch {
		case e.Name() == "e2e.json":
			err = t.readPlaywright(path)
		case strings.HasSuffix(e.Name(), ".jsonl"):
			err = t.readLines(path)
		case strings.HasSuffix(e.Name(), ".json"):
			err = t.readGo(path)
		}
		if err != nil {
			return t, fmt.Errorf("%s: %w", e.Name(), err)
		}
	}
	return t, nil
}

func (t *Tests) add(name, id string, pass bool) {
	if pass {
		t.Passed[id] = append(t.Passed[id], name)
	} else {
		t.Failed[id] = append(t.Failed[id], name)
	}
}

func (t *Tests) readGo(path string) error {
	f, err := os.Open(path) //nolint:gosec // The recipe names the report.
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	var first, last time.Time
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		var ev struct {
			Time   time.Time
			Action string
			Test   string
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if first.IsZero() {
			first = ev.Time
		}
		last = ev.Time
		if ev.Test == "" || strings.Contains(ev.Test, "/") || (ev.Action != "pass" && ev.Action != "fail") {
			continue
		}
		t.Count[filepath.Base(path)]++
		if m := goTest.FindStringSubmatch(ev.Test); m != nil {
			t.add(ev.Test, "SCN-"+m[1]+"-"+m[2], ev.Action == "pass")
		}
	}
	t.Took[filepath.Base(path)] = last.Sub(first)
	return sc.Err()
}

func (t *Tests) readPlaywright(path string) error {
	b, err := os.ReadFile(path) //nolint:gosec // The recipe names the report.
	if err != nil {
		return err
	}
	type spec struct {
		Title string `json:"title"`
		OK    bool   `json:"ok"`
	}
	type suite struct {
		Specs  []spec            `json:"specs"`
		Suites []json.RawMessage `json:"suites"`
	}
	var rep struct {
		Suites []json.RawMessage `json:"suites"`
		Stats  struct {
			Duration float64 `json:"duration"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		return err
	}
	var walk func([]json.RawMessage) error
	walk = func(raw []json.RawMessage) error {
		for _, r := range raw {
			var s suite
			if err := json.Unmarshal(r, &s); err != nil {
				return err
			}
			for _, sp := range s.Specs {
				t.Count["e2e.json"]++
				if m := scnTitle.FindStringSubmatch(sp.Title); m != nil {
					t.add(sp.Title, m[1], sp.OK)
				}
			}
			if err := walk(s.Suites); err != nil {
				return err
			}
		}
		return nil
	}
	t.Took["e2e.json"] = time.Duration(rep.Stats.Duration * float64(time.Millisecond))
	return walk(rep.Suites)
}

func (t *Tests) readLines(path string) error {
	f, err := os.Open(path) //nolint:gosec // The recipe names the report.
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l struct {
			Scn  string `json:"scn"`
			Pass *bool  `json:"pass"`
			Ms   int64  `json:"ms"`
		}
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.Pass == nil || !strings.HasPrefix(l.Scn, "SCN-") {
			continue
		}
		t.Count[filepath.Base(path)]++
		t.Took[filepath.Base(path)] += time.Duration(l.Ms) * time.Millisecond
		t.add(l.Scn+" ("+filepath.Base(path)+")", l.Scn, *l.Pass)
	}
	return sc.Err()
}

// Result is the outcome of a trace.
type Result struct {
	Spec     Spec
	Tests    Tests
	Ledger   map[string]string
	Problems []string
}

// Summary prints the counts and the problems.
func (r *Result) Summary() string {
	var b strings.Builder
	statuses := map[string]int{}
	for _, s := range r.Ledger {
		statuses[s]++
	}
	fmt.Fprintf(&b, "trace: %d requirements, %d scenarios, %d scenarios with a passing test\n", len(r.Spec.Reqs), len(r.Spec.Scenarios),
		len(r.Tests.Passed))
	var keys []string
	for k := range statuses {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "trace: ledger %s %d\n", k, statuses[k])
	}
	for _, p := range r.Problems {
		fmt.Fprintf(&b, "trace: %s\n", p)
	}
	return b.String()
}

// Trace checks the specification against the ledger and the reports.
func Trace(sddPath, ledgerPath, reportsDir string) (*Result, error) {
	text, err := os.ReadFile(sddPath) //nolint:gosec // The recipe names the specification.
	if err != nil {
		return nil, err
	}
	r := &Result{Spec: ParseSpec(string(text)), Ledger: map[string]string{}}
	if r.Tests, err = ReadReports(reportsDir); err != nil {
		return nil, err
	}
	covered := map[string]bool{}
	for _, refs := range r.Spec.Scenarios {
		for _, ref := range refs {
			covered[ref] = true
		}
	}
	for _, id := range r.Spec.Reqs {
		if !covered[id] {
			r.Problems = append(r.Problems, id+" has no scenario")
		}
	}
	var scns []string
	for id := range r.Spec.Scenarios {
		scns = append(scns, id)
	}
	sort.Strings(scns)
	for _, id := range scns {
		switch {
		case len(r.Tests.Failed[id]) > 0:
			r.Problems = append(r.Problems, fmt.Sprintf("%s has a failing test: %s", id, strings.Join(r.Tests.Failed[id], "; ")))
		case len(r.Tests.Passed[id]) == 0:
			r.Problems = append(r.Problems, id+" has no passing test")
		}
	}
	var named []string
	for id := range r.Tests.Passed {
		named = append(named, id)
	}
	for id := range r.Tests.Failed {
		named = append(named, id)
	}
	sort.Strings(named)
	for _, id := range slices.Compact(named) {
		if _, ok := r.Spec.Scenarios[id]; !ok {
			r.Problems = append(r.Problems, id+" is named by a test but is not in the specification")
		}
	}
	if ledgerPath != "" {
		lt, err := os.ReadFile(ledgerPath) //nolint:gosec // The recipe names the ledger.
		if err != nil {
			return nil, err
		}
		for _, m := range ledRow.FindAllStringSubmatch(string(lt), -1) {
			r.Ledger[m[1]] = m[2]
		}
		want := append(slices.Clone(r.Spec.Reqs), scns...)
		for _, id := range want {
			if _, ok := r.Ledger[id]; !ok {
				r.Problems = append(r.Problems, id+" is not in the ledger")
			}
		}
		var extra []string
		for id := range r.Ledger {
			if !slices.Contains(want, id) {
				extra = append(extra, id)
			}
		}
		sort.Strings(extra)
		for _, id := range extra {
			r.Problems = append(r.Problems, id+" is in the ledger but not in the specification")
		}
	}
	if len(r.Problems) > 0 {
		return r, fmt.Errorf("%d problems", len(r.Problems))
	}
	return r, nil
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // The recipe names the file.
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func command(name string, args ...string) string {
	out, err := exec.CommandContext(context.Background(), name, args...).CombinedOutput() //nolint:gosec // Fixed tool names.
	if err != nil {
		return "unavailable"
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line
}

func head() (string, error) {
	out, err := exec.CommandContext(context.Background(), "git", "rev-parse", "HEAD").Output()
	return strings.TrimSpace(string(out)), err
}

// WriteEvidence writes the evidence of a passed verify for HEAD.
func WriteEvidence(r *Result, sddPath, reportsDir, dir string) (string, error) {
	if dir == "" {
		return "", errors.New("--out is required")
	}
	sha, err := head()
	if err != nil {
		return "", err
	}
	sum, err := fileHash(sddPath)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Evidence %s\n\n", sha)
	fmt.Fprintf(&b, "- Result: passed\n- Commit: %s\n- Date: %s\n- sdd_sha256: %s\n\n", sha, time.Now().UTC().Format(time.RFC3339), sum)
	b.WriteString("## Gates\n\n| Gate | Result |\n|---|---|\n| ci (gen-check, fmt-check, lint, test, contract, e2e, smoke, bench, forbid) | passed |\n| trace | passed |\n\n")
	b.WriteString("## Reports\n\n| Report | Results | Duration |\n|---|---|---|\n")
	var files []string
	for f := range r.Tests.Count {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		fmt.Fprintf(&b, "| %s | %d | %s |\n", f, r.Tests.Count[f], r.Tests.Took[f].Round(time.Second))
	}
	statuses := map[string]int{}
	for _, s := range r.Ledger {
		statuses[s]++
	}
	fmt.Fprintf(&b, "\n## Counts\n\n- Requirements: %d\n- Scenarios: %d\n- Scenarios with a passing test: %d\n", len(r.Spec.Reqs),
		len(r.Spec.Scenarios), len(r.Tests.Passed))
	for _, k := range []string{"PASS", "IN_PROGRESS", "OPEN", "BLOCKED"} {
		fmt.Fprintf(&b, "- Ledger %s: %d\n", k, statuses[k])
	}
	b.WriteString("\n## Tools\n\n")
	for _, t := range [][]string{{"go", "version"}, {"bun", "--version"}, {"just", "--version"}, {"docker", "--version"},
		{"bin/golangci-lint", "--version"}, {"bin/sqlc", "version"}, {"bin/goreleaser", "--version"}} {
		fmt.Fprintf(&b, "- %s: %s\n", filepath.Base(t[0]), command(t[0], t[1:]...))
	}
	fmt.Fprintf(&b, "\nReports: %s\n", filepath.Base(reportsDir))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	path := filepath.Join(dir, sha+".md")
	return path, os.WriteFile(path, []byte(b.String()), 0o600)
}

// Check passes when the tree is clean and the evidence for HEAD exists,
// passed and names the current specification hash.
func Check(sddPath, dir string) error {
	status, err := exec.CommandContext(context.Background(), "git", "status", "--porcelain").Output()
	if err != nil {
		return err
	}
	if s := strings.TrimSpace(string(status)); s != "" {
		return fmt.Errorf("the tree is not clean:\n%s", s)
	}
	sha, err := head()
	if err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(dir, sha+".md")) //nolint:gosec // The recipe names the directory.
	if err != nil {
		return fmt.Errorf("no evidence for %s: %w", sha, err)
	}
	sum, err := fileHash(sddPath)
	if err != nil {
		return err
	}
	text := string(b)
	if !strings.Contains(text, "- Result: passed\n") {
		return fmt.Errorf("the evidence for %s did not pass", sha)
	}
	if !strings.Contains(text, "- sdd_sha256: "+sum+"\n") {
		return fmt.Errorf("the evidence for %s has another specification hash", sha)
	}
	return nil
}
