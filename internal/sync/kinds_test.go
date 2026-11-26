package sync_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/assay/internal/sync"
	"github.com/alternayte/assay/internal/testkit"
)

// TestSCN_SYN_023_EveryKindIsAllowedByTheSchema stores a connection of each
// kind that an admin can create. A migration that rewrites the kind check
// without a kind of an earlier migration fails here.
func TestSCN_SYN_023_EveryKindIsAllowedByTheSchema(t *testing.T) {
	pool, err := pgxpool.New(t.Context(), testkit.DB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, kind := range sync.Kinds {
		if _, err := pool.Exec(t.Context(), "INSERT INTO connections (kind, name) VALUES ($1, $2)", kind, "acme-"+kind); err != nil {
			t.Errorf("kind %s: %v", kind, err)
		}
	}
}
