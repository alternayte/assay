//go:build contract

package confluence_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/confluence"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/outbound"
	"github.com/alternayte/assay/internal/testkit"
)

// TestSCN_CON_019_ConfluenceAttachments reads the PDF, Word and draw.io
// attachments of a page as child items on Data Center and Cloud, skips a
// scan with no_text, skips large files before any download and reads only
// the types of the settings.
func TestSCN_CON_019_ConfluenceAttachments(t *testing.T) {
	src, base := testkit.FakeSources(t)
	ctx := context.Background()
	scope := []byte(`{"spaces":["ENG"]}`)
	limits := connectors.Limits{MaxItemBytes: 512 << 10}
	media := strings.Replace(base, "127.0.0.1", "localhost", 1)
	cloud := func(also []string) *connectors.Client {
		c, err := connectors.NewClient(outbound.Settings{}, connectors.ClientOptions{BaseURL: base + fixtures.CloudSitePath, AlsoURLs: also,
			Authorization: "Basic " + base64.StdEncoding.EncodeToString([]byte("assay.reader@acme.test:"+fixtures.SourceToken)),
			Concurrency:   4, PerSecond: 1000})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	for _, tc := range []struct {
		name   string
		client *connectors.Client
		open   func(*connectors.Client, json.RawMessage, connectors.Rules, connectors.Limits) (*confluence.Connector, error)
		id     func(string) string
		page   string
	}{
		{"data center", testkit.SourceClient(t, base+"/confluence"), confluence.New, func(id string) string { return id }, "1009"},
		{"cloud", cloud([]string{media}), confluence.NewCloud, fixtures.CloudAttachmentID, fixtures.CloudPageID("1009")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			open := func(rules connectors.Rules) *confluence.Connector {
				k, err := tc.open(tc.client, scope, rules, limits)
				if err != nil {
					t.Fatal(err)
				}
				return k
			}
			part := connectors.Part{Key: "ENG"}
			changed := func(k *confluence.Connector, since time.Time) map[string]connectors.Ref {
				out := map[string]connectors.Ref{}
				if err := k.Changed(ctx, part, since, func(r connectors.Ref) error { out[r.ID] = r; return nil }); err != nil {
					t.Fatal(err)
				}
				return out
			}
			att := func(id string) string { return "att:" + tc.id(id) }

			k := open(testkit.DefaultRules)
			refs := changed(k, time.Time{})
			for _, id := range []string{"att5001", "att5002", "att5003", "att5004"} {
				if r, ok := refs[att(id)]; !ok || r.Item != nil || r.Skip != "" || r.Version != "1" {
					t.Fatalf("the listing of %s is %+v (%v)", id, r, ok)
				}
			}
			if _, ok := refs[att("att5005")]; ok {
				t.Fatal("the listing has the PNG attachment")
			}
			page := refs[tc.page].Item
			if page == nil {
				t.Fatal("the listing has no page 1009")
			}
			for _, s := range []string{"Attachment: payments-spec.pdf", "Attachment: payments-design.docx", "Attachment: Payments diagram"} {
				if !strings.Contains(page.Body, s) {
					t.Fatalf("the page body has no %q:\n%s", s, page.Body)
				}
			}

			pdf, err := k.Fetch(ctx, att("att5001"))
			if err != nil {
				t.Fatal(err)
			}
			if pdf.Kind != "file" || pdf.Title != "payments-spec.pdf" || pdf.Revision != "1" || pdf.Metadata["parent"] != tc.page ||
				pdf.Metadata["parentTitle"] != "Payments architecture" || pdf.Metadata["mediaType"] != "application/pdf" || pdf.Metadata["fileSize"] == int64(0) {
				t.Fatalf("the PDF item is %+v", pdf)
			}
			if p := passage(pdf, "A failed card call retries three times with exponential backoff."); p == nil ||
				!slices.Equal(p.HeadingPath, []string{"Payments architecture", "Page 2"}) {
				t.Fatalf("the PDF passage is %+v", p)
			}
			docx, err := k.Fetch(ctx, att("att5002"))
			if err != nil {
				t.Fatal(err)
			}
			if p := passage(docx, "Team Hermes"); p == nil || p.Kind != "table" ||
				!slices.Equal(p.HeadingPath, []string{"Payments architecture", "Payments design", "Owners"}) {
				t.Fatalf("the DOCX table passage is %+v in\n%s", p, docx.Body)
			}
			diagram, err := k.Fetch(ctx, att("att5003"))
			if err != nil {
				t.Fatal(err)
			}
			if p := passage(diagram, "Ledger service"); p == nil || p.Kind != "list" || !strings.Contains(p.Norm, "Card gateway") ||
				!slices.Equal(p.HeadingPath, []string{"Payments architecture", "Systems"}) || strings.Contains(p.Text, "<b>") {
				t.Fatalf("the compressed diagram passage is %+v", p)
			}
			if p := passage(diagram, "Checkout page Acme Journals"); p == nil || !slices.Equal(p.HeadingPath, []string{"Payments architecture", "Checkout flow"}) {
				t.Fatalf("the plain diagram passage is %+v in\n%s", p, diagram.Body)
			}
			var skip *connectors.SkipError
			if _, err := k.Fetch(ctx, att("att5004")); !errors.As(err, &skip) || skip.Reason != "no_text" {
				t.Fatalf("the scan gives %v", err)
			}
			var ids []string
			if err := k.ListIDs(ctx, part, func(id string) error { ids = append(ids, id); return nil }); err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(ids, att("att5001")) || slices.Contains(ids, att("att5005")) {
				t.Fatalf("the IDs are %v", ids)
			}

			// A file larger than the limit is skipped before any download.
			start := time.Now()
			small := testkit.DefaultRules
			small.MaxAttachmentBytes = 1 << 10
			k = open(small)
			refs = changed(k, time.Time{})
			for _, id := range []string{"att5001", "att5002", "att5003", "att5004"} {
				if refs[att(id)].Skip != "too_large" {
					t.Fatalf("the listing of %s is %+v", id, refs[att(id)])
				}
				if _, err := k.Fetch(ctx, att(id)); !errors.As(err, &skip) || skip.Reason != "too_large" {
					t.Fatalf("the fetch of %s gives %v", id, err)
				}
			}
			for _, h := range src.Hits() {
				if !h.At.Before(start) && (strings.Contains(h.Path, "/download/") || strings.HasPrefix(h.Path, fixtures.MediaPath)) {
					t.Fatalf("a file was downloaded: %s", h.Path)
				}
			}

			// Only the types of the settings are read.
			pdfOnly := testkit.DefaultRules
			pdfOnly.AttachmentTypes = []string{"pdf"}
			refs = changed(open(pdfOnly), time.Time{})
			if _, ok := refs[att("att5002")]; ok || refs[att("att5001")].ID == "" || refs[att("att5004")].ID == "" {
				t.Fatalf("the PDF listing is %v", refs)
			}
			none := testkit.DefaultRules
			none.AttachmentTypes = []string{}
			k = open(none)
			for id := range changed(k, time.Time{}) {
				if strings.HasPrefix(id, "att:") {
					t.Fatalf("an empty type list lists %s", id)
				}
			}
			if _, err := k.Fetch(ctx, att("att5001")); !errors.Is(err, connectors.ErrNotFound) {
				t.Fatalf("an empty type list reads the PDF: %v", err)
			}

			// An attachment is read only when its page is stored or listed.
			later := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
			a := fixtures.Attachment{ID: "att5006", Page: "1009", Title: "payments-notes.pdf", MediaType: "application/pdf", Author: "alex.acme",
				Updated: later, Data: mustPDF(t, src)}
			src.PutAttachment(a)
			defer src.DeleteAttachment(a.ID)
			k = open(testkit.DefaultRules)
			if _, ok := changed(k, later.Add(connectors.Overlap))[att("att5006")]; ok {
				t.Fatal("the attachment of a page that the run does not read is listed")
			}
			k.Recall(part, []connectors.Known{{ID: tc.page}})
			if _, ok := changed(k, later.Add(connectors.Overlap))[att("att5006")]; !ok {
				t.Fatal("the attachment of a stored page is not listed")
			}
		})
	}

	t.Run("cloud without the media host", func(t *testing.T) {
		k, err := confluence.NewCloud(cloud(nil), scope, testkit.DefaultRules, limits)
		if err != nil {
			t.Fatal(err)
		}
		var skip *connectors.SkipError
		if _, err := k.Fetch(ctx, "att:"+fixtures.CloudAttachmentID("att5001")); !errors.As(err, &skip) || skip.Reason != "foreign_host" {
			t.Fatalf("a redirect to another host gives %v", err)
		}
	})
}

// passage returns the passage of an item that holds a text.
func passage(it *content.Item, text string) *content.Passage {
	for i, p := range it.Passages {
		if strings.Contains(p.Norm, text) {
			return &it.Passages[i]
		}
	}
	return nil
}

// mustPDF returns the bytes of the PDF fixture.
func mustPDF(t *testing.T, src *fixtures.Sources) []byte {
	t.Helper()
	a, ok := src.Attachment("att5001")
	if !ok {
		t.Fatal("the fixture has no PDF")
	}
	return a.Data
}
