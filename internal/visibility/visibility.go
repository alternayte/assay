// Package visibility resolves the visible set of a user: the connections
// whose content the user can read. Every content query filters by it.
package visibility

import (
	"context"
	"slices"

	"github.com/alternayte/assay/internal/api"
)

// Set is a visible set: connection IDs in ascending order.
type Set []int64

// For returns the visible set of a user. A disabled or unknown user has an
// empty set. No role widens it.
func For(ctx context.Context, db DBTX, userID string) (Set, error) {
	ids, err := New(db).VisibleSet(ctx, userID)
	if err != nil {
		return nil, err
	}
	if ids == nil {
		ids = []int64{}
	}
	return Set(ids), nil
}

// OfCaller returns the visible set of the caller of ctx. An anonymous
// caller has an empty set. A token has the set of its user.
func OfCaller(ctx context.Context, db DBTX) (Set, error) {
	p := api.Caller(ctx)
	if p == nil {
		return Set{}, nil
	}
	return For(ctx, db, p.User.ID)
}

// Has reports whether the set holds a connection.
func (s Set) Has(id int64) bool {
	_, ok := slices.BinarySearch(s, id)
	return ok
}
