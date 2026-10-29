package claims_test

import (
	"testing"

	"github.com/alternayte/assay/internal/claims"
)

func TestSCN_VOC_001_TermPredicatesValidate(t *testing.T) {
	ok := []struct{ pred, subj, obj string }{
		{"defined_as", "term", claims.ObjText},
		{"preferred_term", "term", "term"},
		{"broader", "term", "term"},
		{"relates_to", "term", "service"},
		{"owned_by", "term", "team"},
		{"status", "term", claims.ObjText},
	}
	for _, c := range ok {
		if err := claims.Check(c.pred, c.subj, c.obj); err != nil {
			t.Errorf("%s %s %s: %v", c.subj, c.pred, c.obj, err)
		}
	}
	bad := []struct{ pred, subj, obj string }{
		{"defined_as", "service", claims.ObjText},
		{"defined_as", "term", "team"},
		{"preferred_term", "term", "service"},
		{"broader", "service", "term"},
	}
	for _, c := range bad {
		if err := claims.Check(c.pred, c.subj, c.obj); err == nil {
			t.Errorf("%s %s %s passes", c.subj, c.pred, c.obj)
		}
	}
	// Only registry records and accepted reviews set the status of a term.
	if err := claims.CheckExtracted("status", "term", claims.ObjText); err == nil {
		t.Error("an extracted claim sets the status of a term")
	}
	if err := claims.CheckExtracted("status", "issue", claims.ObjText); err != nil {
		t.Errorf("an extracted issue status fails: %v", err)
	}
	if err := claims.CheckExtracted("defined_as", "term", claims.ObjText); err != nil {
		t.Errorf("an extracted definition fails: %v", err)
	}
}
