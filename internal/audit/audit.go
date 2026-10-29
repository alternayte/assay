// Package audit records who did what. The application only appends to the
// log. The retention job is the only delete.
package audit

import (
	"context"
	"encoding/json"

	authall "github.com/alternayte/auth-all"
)

// Entry is one audit record.
type Entry struct {
	Action  string
	Target  string
	Details map[string]any
}

// Write appends an entry for the caller of ctx. A context with no caller
// records the system as the actor.
func Write(ctx context.Context, db DBTX, e Entry) error {
	details, err := json.Marshal(orEmpty(e.Details))
	if err != nil {
		return err
	}
	var userID, tokenID *string
	if p := authall.PrincipalFrom(ctx); p != nil {
		userID = &p.User.ID
		if p.APIKey != nil {
			tokenID = &p.APIKey.ID
		}
	}
	_, err = New(db).Insert(ctx, InsertParams{
		ActorUserID:  userID,
		ActorTokenID: tokenID,
		Action:       e.Action,
		Target:       e.Target,
		Details:      details,
	})
	return err
}

// WriteAs appends an entry with an explicit actor, for an action that has no
// signed-in caller, such as the first setup or an accepted invite.
func WriteAs(ctx context.Context, db DBTX, actorUserID string, e Entry) error {
	details, err := json.Marshal(orEmpty(e.Details))
	if err != nil {
		return err
	}
	_, err = New(db).Insert(ctx, InsertParams{ActorUserID: &actorUserID, Action: e.Action, Target: e.Target, Details: details})
	return err
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}
