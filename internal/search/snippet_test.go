package search

import "testing"

func TestPlainSnippet(t *testing.T) {
	for in, want := range map[string]string{
		"Payments API** takes card payments. > **Info** > > The API":            "Payments API takes card payments. Info The API",
		"| Endpoint | Method | Use | |---|---|---| | /payments | POST | Take |": "Endpoint Method Use /payments POST Take",
		"Refund a _settled_ payment of payments_db with `make run`":             "Refund a settled payment of payments_db with make run",
		"```json {\"amount\": 1200} ```":                                        "{\"amount\": 1200}",
	} {
		if got := plainSnippet(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	segs := Segments("a **payments** b")
	if len(segs) != 3 || segs[1].Text != "payments" || !segs[1].Hit {
		t.Fatalf("segments %+v", segs)
	}
}
