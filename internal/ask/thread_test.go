package ask_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/testkit"
)

type thread struct {
	ThreadID int64 `json:"threadId"`
	Turns    []struct {
		QuestionID int64  `json:"questionId"`
		Turn       int    `json:"turn"`
		Markdown   string `json:"markdown"`
		Notice     *struct {
			Code string `json:"code"`
		} `json:"notice"`
		Feedback *struct {
			Rating        string `json:"rating"`
			Reason        string `json:"reason"`
			WrongCitation bool   `json:"wrongCitation"`
		} `json:"feedback"`
		Claims []struct {
			ID      string `json:"id"`
			ClaimID int64  `json:"claimId"`
		} `json:"claims"`
		Cited []string `json:"cited"`
	} `json:"turns"`
}

type threadAnswer struct {
	answer
	ThreadID int64 `json:"threadId"`
	Turn     int   `json:"turn"`
}

func askIn(t *testing.T, cl *testkit.Client, q string, threadID int64) threadAnswer {
	t.Helper()
	evs := ask(t, cl, q, threadID)
	var a threadAnswer
	if err := json.Unmarshal(evs[len(evs)-1].Data, &a); err != nil {
		t.Fatal(err)
	}
	if evs[len(evs)-1].Name != "answer" {
		t.Fatalf("the last event is %s: %s", evs[len(evs)-1].Name, evs[len(evs)-1].Data)
	}
	return a
}

// shared is a member who sees the GitHub connection through a group.
type shared struct {
	setup
	member   *testkit.Client
	memberID string
	group    int64
}

func startShared(t *testing.T) shared {
	t.Helper()
	s := start(t)
	member, memberID := s.env.Invite(s.admin, "member@acme.test", "member", "member-password-1")
	var g struct {
		ID int64 `json:"id"`
	}
	s.admin.Must(http.StatusCreated, http.MethodPost, "/api/v1/groups", map[string]string{"name": "Payments"}).JSON(t, &g)
	s.admin.Must(http.StatusNoContent, http.MethodPut, fmt.Sprintf("/api/v1/groups/%d/members/%s", g.ID, memberID), nil)
	s.admin.Must(http.StatusNoContent, http.MethodPut, fmt.Sprintf("/api/v1/connections/%d/visibility", s.conn),
		map[string]any{"groupIds": []int64{g.ID}, "confirm": true})
	return shared{setup: s, member: member, memberID: memberID, group: g.ID}
}

func TestSCN_ASK_008_ThreadHidesWhenAConnectionIsHidden(t *testing.T) {
	s := startShared(t)
	first := askIn(t, s.member, "Who owns the Payments API?", 0)
	if first.QuestionID == 0 || first.ThreadID != first.QuestionID || first.Turn != 1 || len(first.Cited) == 0 {
		t.Fatalf("first answer %+v", first)
	}
	next := askIn(t, s.member, "What does that team also run?", first.ThreadID)
	if next.ThreadID != first.ThreadID || next.Turn != 2 || next.Notice != nil {
		t.Fatalf("follow-up %+v", next)
	}
	reqs := s.fake.RequestsTo("/v1/chat/completions")
	user := reqs[len(reqs)-1].Body["messages"].([]any)[1].(map[string]any)["content"].(string)
	if !strings.Contains(user, `<turn n="1">`) || strings.Index(user, "<turn") > strings.Index(user, "<source") {
		t.Fatalf("the follow-up context has no earlier turn before its sources:\n%s", user)
	}

	// Another user cannot ask in the thread.
	s.admin.Must(http.StatusNotFound, http.MethodPost, "/api/v1/ask", map[string]any{"question": "And who reviews it?", "threadId": first.ThreadID})

	var list struct {
		Questions []struct {
			ID    int64 `json:"id"`
			Turns int   `json:"turns"`
		} `json:"questions"`
	}
	s.member.Must(http.StatusOK, http.MethodGet, "/api/v1/questions", nil).JSON(t, &list)
	if len(list.Questions) != 1 || list.Questions[0].ID != first.ThreadID || list.Questions[0].Turns != 2 {
		t.Fatalf("history %+v", list)
	}

	s.admin.Must(http.StatusNoContent, http.MethodDelete, fmt.Sprintf("/api/v1/groups/%d/members/%s", s.group, s.memberID), nil)
	before := len(s.fake.RequestsTo("/v1/chat/completions"))
	refused := askIn(t, s.member, "And who reviews it?", next.QuestionID)
	if refused.Notice == nil || refused.Notice.Code != "thread_hidden" || refused.QuestionID != 0 {
		t.Fatalf("follow-up in a hidden thread %+v", refused)
	}
	if n := len(s.fake.RequestsTo("/v1/chat/completions")); n != before {
		t.Fatalf("the refused follow-up called the model")
	}
	var th thread
	s.member.Must(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/v1/questions/%d", next.QuestionID), nil).JSON(t, &th)
	if th.ThreadID != first.ThreadID || len(th.Turns) != 2 {
		t.Fatalf("thread %+v", th)
	}
	for _, turn := range th.Turns {
		if turn.Notice == nil || turn.Notice.Code != "hidden" || turn.Markdown != "" {
			t.Fatalf("turn %d is not hidden: %+v", turn.Turn, turn)
		}
	}
}

func TestSCN_ASK_009_QuestionsToImproveNeverShowTheAskerOrHiddenSources(t *testing.T) {
	s := startShared(t)
	other, _ := s.env.Invite(s.admin, "admin2@acme.test", "admin", "admin2-password-1")
	s.fake.OnChat(func(map[string]any) string { return "Nobody knows the on-call rota." })
	none := askIn(t, s.member, "What is the on-call rota of the Payments API?", 0)
	if !none.NoEvidence {
		t.Fatalf("answer %+v", none)
	}
	s.fake.OnChat(nil)
	down := askIn(t, s.member, "Who owns the Payments API?", 0)
	s.member.Must(http.StatusOK, http.MethodPut, fmt.Sprintf("/api/v1/questions/%d/feedback", down.QuestionID),
		map[string]any{"rating": "down", "reason": "It names the old team.", "wrongCitation": true})
	up := askIn(t, s.member, "What does the Payments API do?", 0)
	s.member.Must(http.StatusOK, http.MethodPut, fmt.Sprintf("/api/v1/questions/%d/feedback", up.QuestionID), map[string]any{"rating": "up"})

	r := s.admin.Must(http.StatusOK, http.MethodGet, "/api/v1/diagnostics/questions", nil)
	for _, asker := range []string{s.memberID, "member@acme.test", "member"} {
		if strings.Contains(string(r.Body), asker) {
			t.Fatalf("the list names the asker with %q: %s", asker, r.Body)
		}
	}
	var got struct {
		Groups []struct {
			EntityName string `json:"entityName"`
			Questions  []struct {
				ID            int64  `json:"id"`
				Rating        string `json:"rating"`
				Reason        string `json:"reason"`
				WrongCitation bool   `json:"wrongCitation"`
				NoEvidence    bool   `json:"noEvidence"`
			} `json:"questions"`
		} `json:"groups"`
	}
	r.JSON(t, &got)
	if len(got.Groups) != 1 || !strings.Contains(strings.ToLower(got.Groups[0].EntityName), "payments") || len(got.Groups[0].Questions) != 2 {
		t.Fatalf("groups %s", r.Body)
	}
	for _, q := range got.Groups[0].Questions {
		switch q.ID {
		case none.QuestionID:
			if !q.NoEvidence || q.Rating != "" {
				t.Fatalf("no-evidence row %+v", q)
			}
		case down.QuestionID:
			if q.Rating != "down" || q.Reason != "It names the old team." || !q.WrongCitation {
				t.Fatalf("down row %+v", q)
			}
		default:
			t.Fatalf("row %+v", q)
		}
	}
	if strings.Contains(string(r.Body), "markdown") || strings.Contains(string(r.Body), down.Markdown) {
		t.Fatalf("the list holds answer text: %s", r.Body)
	}

	// An admin whose visible set lacks the connection sees no question.
	other.Must(http.StatusOK, http.MethodGet, "/api/v1/diagnostics/questions", nil).JSON(t, &got)
	if len(got.Groups) != 0 {
		t.Fatalf("an admin without the share sees %+v", got)
	}
	s.member.Must(http.StatusForbidden, http.MethodGet, "/api/v1/diagnostics/questions", nil)
}

func TestSCN_ASK_010_NotHelpfulAnswerReportsACitedClaim(t *testing.T) {
	s := start(t)
	a := askIn(t, s.admin, "Who owns the Payments API?", 0)
	path := fmt.Sprintf("/api/v1/questions/%d", a.QuestionID)
	s.admin.Must(http.StatusOK, http.MethodPut, path+"/feedback", map[string]any{"rating": "down", "reason": "The owner is wrong."})
	member, _ := s.env.Invite(s.admin, "member@acme.test", "member", "member-password-1")
	member.Must(http.StatusNotFound, http.MethodPut, path+"/feedback", map[string]any{"rating": "up"})

	var th thread
	s.admin.Must(http.StatusOK, http.MethodGet, path, nil).JSON(t, &th)
	turn := th.Turns[0]
	if turn.Feedback == nil || turn.Feedback.Rating != "down" || turn.Feedback.Reason != "The owner is wrong." {
		t.Fatalf("the mark after a reload %+v", turn.Feedback)
	}
	var claimID int64
	for _, c := range turn.Claims {
		for _, id := range turn.Cited {
			if c.ID == id {
				claimID = c.ClaimID
			}
		}
	}
	if claimID == 0 {
		t.Fatalf("the answer cites no claim: %+v", turn)
	}
	s.admin.Must(http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/v1/claims/%d/problems", claimID),
		map[string]any{"problem": "outdated", "reason": "The owner is wrong."})
	if n := s.env.Count("SELECT count(*) FROM proposals WHERE kind = 'correction' AND payload ->> 'claim' = $1", fmt.Sprint(claimID)); n != 1 {
		t.Fatalf("%d correction cards for claim %d", n, claimID)
	}
}
