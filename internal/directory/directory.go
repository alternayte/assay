// Package directory reads the users and groups of the company directory for
// the directory sync. Entra ID through Microsoft Graph is the one provider
// now. The directory is an external system, so tests replace it with the
// fixture server behind the Provider interface.
package directory

import (
	"context"
	"errors"
)

// User is one user of a users page. A delta page carries only the changed
// fields, so Enabled is nil when the page does not name the account state.
type User struct {
	// ID is the directory object ID. The ID token carries it as oid.
	ID      string
	Mail    string
	Enabled *bool
	// Removed is true for a user that the directory deleted.
	Removed bool
}

// Users is the result of one users read.
type Users struct {
	Users []User
	// Full is true when the read listed every user, not only the changes.
	Full bool
	// Next is the delta link of the next read.
	Next string
}

// Group is one directory group of the group picker.
type Group struct {
	ID   string
	Name string
}

// Provider reads one directory. It never writes to it.
type Provider interface {
	// Users lists the users that changed after since, a delta link of an
	// earlier read. An empty since lists every user.
	Users(ctx context.Context, since string) (Users, error)
	// Members lists the IDs of the users in a group, also through nested
	// groups.
	Members(ctx context.Context, groupID string) ([]string, error)
	// Groups lists up to 50 groups whose name starts with prefix.
	Groups(ctx context.Context, prefix string) ([]Group, error)
}

// ErrResync means that the delta link expired. The caller reads every user
// again with an empty since.
var ErrResync = errors.New("directory: the delta link expired")

// ErrGroupNotFound means that a group of a members read does not exist.
var ErrGroupNotFound = errors.New("directory: the group does not exist")
