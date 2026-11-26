//go:build contract

package sync_test

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/testkit"
)

// setAttachments changes the attachments setting.
func setAttachments(t *testing.T, admin *testkit.Client, value map[string]any) {
	t.Helper()
	var cur struct {
		Revision int32 `json:"revision"`
	}
	var list struct {
		Settings []struct {
			Key      string `json:"key"`
			Revision int32  `json:"revision"`
		} `json:"settings"`
	}
	admin.Must(http.StatusOK, http.MethodGet, "/api/v1/settings", nil).JSON(t, &list)
	for _, s := range list.Settings {
		if s.Key == "attachments" {
			cur.Revision = s.Revision
		}
	}
	admin.Must(http.StatusOK, http.MethodPut, "/api/v1/settings/attachments", map[string]any{"value": value, "revision": cur.Revision})
}

// TestSCN_SYN_026_JiraHistoryTimeline gives the entity page of an
// initiative a timeline of dated statuses that cite the History passages,
// shows a past assignee without a conflict, and reads no comments or
// changelog of an unchanged issue.
func TestSCN_SYN_026_JiraHistoryTimeline(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-jira", Kind: "jira", BaseURL: base,
		Scope: map[string]any{"projects": []string{"PAY"}}})
	if run := env.RunNow(admin, conn, "sync"); run.Status != "succeeded" || run.Created != 5 {
		t.Fatalf("run %+v", run)
	}
	host := strings.TrimPrefix(base, "http://")
	type detail struct {
		Claims []struct {
			Predicate  string `json:"predicate"`
			ObjectName string `json:"objectName"`
			Status     string `json:"status"`
		} `json:"claims"`
		Timeline []struct {
			Predicate  string     `json:"predicate"`
			ObjectText string     `json:"objectText"`
			ObjectName string     `json:"objectName"`
			ValidFrom  *time.Time `json:"validFrom"`
			Current    bool       `json:"current"`
			Status     string     `json:"status"`
			Evidence   []struct {
				Quote string `json:"quote"`
			} `json:"evidence"`
		} `json:"timeline"`
	}
	entity := func(key string) detail {
		var d detail
		admin.Must(http.StatusOK, http.MethodGet, "/api/v1/entities/"+url.PathEscape("jira:"+host+":"+key), nil).JSON(t, &d)
		return d
	}

	pay1 := entity("PAY-1")
	var statuses []string
	for _, c := range pay1.Timeline {
		if c.Predicate != "status" {
			continue
		}
		statuses = append(statuses, c.ObjectText)
		history := slices.ContainsFunc(c.Evidence, func(e struct {
			Quote string `json:"quote"`
		}) bool {
			return strings.Contains(e.Quote, "Status ") && strings.Contains(e.Quote, "→ "+c.ObjectText)
		})
		if !history || c.ValidFrom == nil || c.Current != (c.ObjectText == "Done") || c.Status != "supported" {
			t.Fatalf("the timeline value %+v", c)
		}
	}
	// The timeline is newest first.
	if !slices.Equal(statuses, []string{"Done", "In Progress", "To Do"}) {
		t.Fatalf("the status timeline is %v", statuses)
	}

	pay12 := entity("PAY-12")
	var assignees []string
	for _, c := range pay12.Timeline {
		if c.Predicate == "assigned_to" {
			assignees = append(assignees, c.ObjectName+map[bool]string{true: " current", false: ""}[c.Current])
			if c.Status == "conflicting" {
				t.Fatalf("the assignee %s conflicts", c.ObjectName)
			}
		}
	}
	if !slices.Equal(assignees, []string{"Priya Acme current", "Sam Acme"}) {
		t.Fatalf("the assignee timeline is %v", assignees)
	}
	current := 0
	for _, c := range pay12.Claims {
		if c.Predicate == "assigned_to" {
			current++
		}
	}
	if current != 1 {
		t.Fatalf("%d current assignees", current)
	}
	if n := env.Count("SELECT count(*) FROM proposals WHERE kind = 'conflict'"); n != 0 {
		t.Fatalf("%d conflict cards", n)
	}

	start := time.Now()
	if run := env.RunNow(admin, conn, "sync"); run.Status != "succeeded" || run.Updated != 0 {
		t.Fatalf("second run %+v", run)
	}
	for _, h := range src.Hits() {
		if !h.At.Before(start) && (strings.HasSuffix(h.Path, "/comment") || strings.HasSuffix(h.Path, "/changelog") ||
			strings.Contains(h.Query, "changelog")) {
			t.Fatalf("an unchanged issue sent %s?%s", h.Path, h.Query)
		}
	}
}

// TestSCN_SYN_025_ConfluenceAttachmentItems stores the attachments of a
// page as child items that search finds by page, records the skips, and
// tombstones an attachment that is gone and the attachments of a page that
// is gone.
func TestSCN_SYN_025_ConfluenceAttachmentItems(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)

	// With the limit at 1 KB, every attachment is skipped before a download.
	setAttachments(t, admin, map[string]any{"types": []string{"pdf", "docx", "drawio"}, "maxKb": 1})
	conn := publicConnection(t, env, admin, base)
	run := env.RunNow(admin, conn, "sync")
	if run.Status != "succeeded" || run.Created != 9 {
		t.Fatalf("run %+v", run)
	}
	skipped := map[string]string{}
	for _, s := range run.Skipped {
		skipped[s.ID] = s.Reason
	}
	for _, id := range []string{"att:att5001", "att:att5002", "att:att5003", "att:att5004"} {
		if skipped[id] != "too_large" {
			t.Fatalf("the skips are %v", skipped)
		}
	}
	for _, h := range src.Hits() {
		if strings.Contains(h.Path, "/download/") {
			t.Fatalf("a file was downloaded: %s", h.Path)
		}
	}

	// A reconciliation with the default limit reads the attachments that
	// the sync skipped.
	setAttachments(t, admin, map[string]any{"types": []string{"pdf", "docx", "drawio"}, "maxKb": 10240})
	rec := env.RunNow(admin, conn, "reconcile")
	if rec.Status != "succeeded" || rec.Created != 3 || len(rec.Skipped) != 1 || rec.Skipped[0].ID != "att:att5004" || rec.Skipped[0].Reason != "no_text" {
		t.Fatalf("reconcile %+v", rec)
	}
	if n := env.Count("SELECT count(*) FROM source_items WHERE connection_id = $1 AND kind = 'file' AND external_id LIKE 'att:%'", conn); n != 3 {
		t.Fatalf("%d attachment items", n)
	}

	var res struct {
		Results []struct {
			ItemID      int64    `json:"itemId"`
			HeadingPath []string `json:"headingPath"`
		} `json:"results"`
	}
	admin.Must(http.StatusOK, http.MethodGet, "/api/v1/search?limit=10&q="+url.QueryEscape("failed card call retries three times with exponential backoff"),
		nil).JSON(t, &res)
	pdf := itemID(t, env, conn, "att:att5001")
	found := false
	for _, r := range res.Results {
		if r.ItemID == pdf && slices.Equal(r.HeadingPath, []string{"Payments architecture", "Page 2"}) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the search finds %+v, not the PDF on page 2", res.Results)
	}

	// An attachment that is gone is a tombstone, and its page stays.
	src.DeleteAttachment("att5002")
	if rec := env.RunNow(admin, conn, "reconcile"); rec.Status != "succeeded" || rec.Deleted != 1 {
		t.Fatalf("reconcile %+v", rec)
	}
	if n := env.Count("SELECT count(*) FROM source_items WHERE connection_id = $1 AND external_id IN ('att:att5002', '1009')", conn); n != 1 {
		t.Fatalf("%d of the attachment and its page are stored", n)
	}
	// The tombstone of a page also tombstones its attachments.
	src.DeletePage("1009")
	if rec := env.RunNow(admin, conn, "reconcile"); rec.Status != "succeeded" || rec.Deleted != 3 {
		t.Fatalf("reconcile %+v", rec)
	}
	if n := env.Count("SELECT count(*) FROM source_items WHERE connection_id = $1 AND (external_id LIKE 'att:%' OR external_id = '1009')", conn); n != 0 {
		t.Fatalf("%d items of the page are stored", n)
	}
}
