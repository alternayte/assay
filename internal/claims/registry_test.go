package claims_test

import (
	"testing"

	"github.com/alternayte/assay/internal/claims"
)

func TestSCN_CLM_001_RegistryValidatesTypes(t *testing.T) {
	ok := []struct{ pred, subj, obj string }{
		{"owned_by", "service", "team"},
		{"owned_by", "term", "person"},
		{"depends_on", "api", "resource"},
		{"status", "initiative", claims.ObjText},
		{"target_date", "issue", claims.ObjDate},
		{"stated", "repository", claims.ObjText},
		{"relates_to", "term", "service"},
	}
	for _, c := range ok {
		if err := claims.Check(c.pred, c.subj, c.obj); err != nil {
			t.Errorf("%s(%s, %s): %v", c.pred, c.subj, c.obj, err)
		}
	}
	bad := []struct{ pred, subj, obj string }{
		{"owned_by", "person", "team"},
		{"owned_by", "service", "service"},
		{"depends_on", "team", "service"},
		{"implements", "service", "api"},
		{"blocks", "issue", "person"},
		{"target_date", "issue", "team"},
		{"owned_by", "gadget", "team"},
		{"maintained_by", "service", "team"},
	}
	for _, c := range bad {
		if err := claims.Check(c.pred, c.subj, c.obj); err == nil {
			t.Errorf("%s(%s, %s) passes", c.pred, c.subj, c.obj)
		}
	}
	if !claims.Registry["depends_on"].Multi || claims.Registry["owned_by"].Multi || !claims.Registry["status"].TimeScoped {
		t.Error("the cardinality or time scope of the registry is wrong")
	}
}
