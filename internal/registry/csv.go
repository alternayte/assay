package registry

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"
)

// CSVColumns are the columns of the term CSV.
var CSVColumns = []string{"key", "name", "synonyms", "definition", "domain", "owner", "preferred", "related", "status"}

func joinList(ss []string) string { return strings.Join(ss, "; ") }

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ";") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ExportTerms writes the registry terms as CSV, ordered by name. Lists use
// "; " between values. Domains are names; entities are keys.
func (s *Service) ExportTerms(ctx context.Context) ([]byte, error) {
	q := New(s.Pool)
	reg, err := q.RegistryID(ctx)
	if err != nil {
		return nil, err
	}
	kind := KindTerm
	rows, err := q.ListRecords(ctx, ListRecordsParams{Visible: []int64{reg}, Kind: &kind})
	if err != nil {
		return nil, err
	}
	doms, err := q.DomainNames(ctx)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	for _, d := range doms {
		names[d.ID] = d.Name
	}
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	if err := w.Write(CSVColumns); err != nil {
		return nil, err
	}
	for _, r := range rows {
		rec, err := recordOf(r.Metadata)
		if err != nil || rec.Term == nil {
			continue
		}
		t := rec.Term
		if err := w.Write([]string{r.Key, t.Name, joinList(t.Synonyms), t.Definition, names[t.DomainID], t.Owner, t.Preferred,
			joinList(t.Related), t.Status}); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return b.Bytes(), w.Error()
}

// ImportResult counts the rows of an import. Rows with an error change
// nothing.
type ImportResult struct {
	Created   int           `json:"created"`
	Updated   int           `json:"updated"`
	Unchanged int           `json:"unchanged"`
	Proposals int           `json:"proposals" doc:"Member edits of approved definitions that became proposals."`
	Errors    []ImportError `json:"errors"`
}

// ImportError is one rejected row.
type ImportError struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// ImportTerms reads term CSV in the export format. A row with a key saves
// that record; a row without one creates a record or saves the record with
// its name. Each row is its own save with the role rules of the editor.
func (s *Service) ImportTerms(ctx context.Context, data []byte, ed Editor) (*ImportResult, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	head, err := r.Read()
	if err != nil {
		return nil, invalid("the file has no header row")
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF")))] = i
	}
	if _, ok := col["name"]; !ok {
		return nil, invalid("the header has no name column")
	}
	get := func(row []string, name string) string {
		if i, ok := col[name]; ok && i < len(row) {
			return strings.TrimSpace(row[i])
		}
		return ""
	}
	out := &ImportResult{Errors: []ImportError{}}
	for line := 2; ; line++ {
		row, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			out.Errors = append(out.Errors, ImportError{Line: line, Message: err.Error()})
			continue
		}
		if err := s.importRow(ctx, line, row, get, ed, out); err != nil {
			var inv *InvalidError
			switch {
			case errors.As(err, &inv):
				out.Errors = append(out.Errors, ImportError{Line: line, Message: inv.Msg})
			case errors.Is(err, ErrStatusRole):
				out.Errors = append(out.Errors, ImportError{Line: line, Message: "Only reviewers approve and deprecate terms."})
			default:
				return nil, err
			}
		}
	}
	return out, nil
}

func (s *Service) importRow(ctx context.Context, line int, row []string, get func([]string, string) string, ed Editor, out *ImportResult) error {
	t := &TermRecord{Name: get(row, "name"), Synonyms: splitList(get(row, "synonyms")), Definition: get(row, "definition"),
		Owner: get(row, "owner"), Preferred: get(row, "preferred"), Related: splitList(get(row, "related")), Status: get(row, "status")}
	if t.Status != "" && t.Status != StatusDraft && t.Status != StatusApproved && t.Status != StatusDeprecated {
		return invalid("line %d: the status %q is not draft, approved or deprecated", line, t.Status)
	}
	q := New(s.Pool)
	if d := get(row, "domain"); d != "" {
		id, err := q.DomainByName(ctx, d)
		if errors.Is(err, pgx.ErrNoRows) {
			return invalid("line %d: the domain %q does not exist", line, d)
		}
		if err != nil {
			return err
		}
		t.DomainID = id
	}
	rec := RegistryRecord{Kind: KindTerm, Term: t}
	key := get(row, "key")
	if key == "" {
		key = RecordKey(rec)
	}
	reg, err := q.RegistryID(ctx)
	if err != nil {
		return err
	}
	item, err := q.RecordItem(ctx, RecordItemParams{Visible: []int64{reg}, Key: key})
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	before := int64(0)
	if exists && item.VersionID != nil {
		before = *item.VersionID
	}
	saveKey := ""
	if exists {
		saveKey = key
	}
	saved, err := s.Save(ctx, saveKey, rec, ed)
	if err != nil {
		var inv *InvalidError
		if errors.As(err, &inv) {
			return invalid("line %d: %s", line, inv.Msg)
		}
		return err
	}
	switch {
	case saved.Proposal != 0:
		out.Proposals++
	case !exists:
		out.Created++
	case saved.Version != before:
		out.Updated++
	default:
		out.Unchanged++
	}
	return nil
}

