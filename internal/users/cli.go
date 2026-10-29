package users

import (
	"context"
	"fmt"

	"github.com/alternayte/auth-all/plugins/admin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/auth"
)

// CreateAdmin creates an admin without the UI. An empty password generates
// one, which the function returns.
func CreateAdmin(ctx context.Context, pool *pgxpool.Pool, a *auth.Auth, email, name, password string) (string, error) {
	u, generated, err := a.Admin.CreateUser(ctx, admin.CreateUserInput{Email: email, Name: name, Role: auth.RoleAdmin, Password: password})
	if err != nil {
		return "", err
	}
	if err := audit.Write(ctx, pool, audit.Entry{Action: "user.cli_create", Target: "user:" + u.ID, Details: map[string]any{"role": auth.RoleAdmin}}); err != nil {
		return "", err
	}
	return generated, nil
}

// ResetPassword sets the password of a user without the UI and ends the
// sessions of the user. An empty password generates one, which the function
// returns.
func ResetPassword(ctx context.Context, pool *pgxpool.Pool, a *auth.Auth, email, password string) (string, error) {
	u, err := a.GetUserByEmail(ctx, email)
	if err != nil || u == nil {
		return "", fmt.Errorf("no user has the email %s", email)
	}
	generated, err := a.Admin.ResetPassword(ctx, u.ID, admin.ResetOptions{Password: password})
	if err != nil {
		return "", err
	}
	if err := audit.Write(ctx, pool, audit.Entry{Action: "user.cli_reset_password", Target: "user:" + u.ID}); err != nil {
		return "", err
	}
	return generated, nil
}
