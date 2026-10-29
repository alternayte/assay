// Command testreport prints a summary of a go test -json report: the output of
// each failed test, and the counts. It exits non-zero when a test failed.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

type event struct {
	Action  string
	Package string
	Test    string
	Output  string
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: testreport <report.json>")
		os.Exit(2)
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer func() { _ = f.Close() }()

	output := map[string]*strings.Builder{}
	var failed []string
	var passed, skipped int
	var buildErrors strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			buildErrors.WriteString(sc.Text() + "\n")
			continue
		}
		key := e.Package + " " + e.Test
		switch e.Action {
		case "output":
			if output[key] == nil {
				output[key] = &strings.Builder{}
			}
			output[key].WriteString(e.Output)
		case "pass":
			if e.Test != "" {
				passed++
			}
		case "skip":
			if e.Test != "" {
				skipped++
			}
		case "fail":
			failed = append(failed, key)
		}
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	sort.Strings(failed)
	for _, key := range failed {
		fmt.Printf("--- FAIL %s\n", key)
		if b := output[key]; b != nil {
			fmt.Print(b.String())
		}
	}
	if buildErrors.Len() > 0 {
		fmt.Print(buildErrors.String())
	}
	fmt.Printf("tests: %d passed, %d failed, %d skipped\n", passed, len(failed), skipped)
	if len(failed) > 0 || buildErrors.Len() > 0 {
		os.Exit(1)
	}
}
