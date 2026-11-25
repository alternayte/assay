# SSO and directory sync

## What it does
A user signs in with the company identity provider (IdP) through OpenID Connect (OIDC). Entra ID is the first IdP, and any OIDC issuer works. The first sign-in creates the Assay user. An admin maps IdP groups to Assay groups, so a connection share follows the directory. A periodic job reads the directory, disables leavers and sets the members of mapped groups. A disabled user loses web sessions, MCP access and access tokens at the next poll. An admin can turn off password sign-in.

## Decisions
- Assay upgrades to auth-all v0.9.0 and the matching humaauth — v0.9.0 gives `Identity.Claims`, `OnBeforeOAuthSignIn` and runtime provider sources.
- Sign-in uses `oauth/oidc` through `authall.WithProviderSource`, with the provider ID `sso` — a runtime source applies a change in Settings with no restart, and a fixed ID keeps `auth_accounts` rows valid when the issuer changes.
- The allowlist in `internal/app/http.go` adds `GET /oauth/sso` and `GET /oauth/sso/callback` only — AGENTS.md permits only allowlisted auth-all routes.
- Settings, Sign-in holds: issuer URL, client ID, client secret, the "Sign in with" button label, password sign-in on or off, the access group, and the directory provider. The client secret is stored with `internal/secrets`.
- Each field has an env var (`ASSAY_OIDC_ISSUER`, `ASSAY_OIDC_CLIENT_ID`, `ASSAY_OIDC_CLIENT_SECRET`, `ASSAY_PASSWORD_SIGNIN`, `ASSAY_DIRECTORY_*`). A set env var wins, and the UI shows the field as locked with the env var name — the owner default for configuration.
- The scopes are `openid email profile`. Entra ID sends `groups` as object IDs when the app registration enables group claims — object IDs do not change when a group is renamed.
- The first sign-in creates the user with the role member, through auth-all — this is just-in-time creation. An existing user links only by a verified address, as auth-all does.
- `OnBeforeOAuthSignIn` reads `groups`. When an access group is set and the user is not in it, the hook refuses the sign-in with "Your organization has not given you access to Assay." Then the hook sets the user's members in mapped groups from the claim.
- Password sign-in off removes `POST /sign-in/email` at run time, and the sign-in page shows only the SSO button. The setting refuses "off" until one admin has a linked SSO account — an admin must not lock out all admins.
- The table `group_mappings (group_id, provider, external_id, external_name)` maps one IdP group to one Assay group. The Groups page shows the mapping and a group picker that reads the directory.
- The members of a mapped group come only from the IdP. The Groups page refuses a manual member change on a mapped group — the next poll would undo it.
- The table `directory_users (user_id, provider, subject, disabled_by_sync, seen_at)` records each user linked to the IdP.
- The job `directory_sync` runs on River every 15 minutes by default (setting: 5 to 1,440 minutes) on the default queue, unique by period.
- The directory provider has one implementation now, `entra`, behind the interface `directory.Provider` with `Users(ctx, since)` and `Members(ctx, groupID)`. Okta is the planned second implementation — the interface boundary exists because the directory is an external system that tests replace with the fixture server.
- `entra` reads Microsoft Graph with `GET /users/delta?$select=id,accountEnabled,mail` and stores the delta link. It reads `GET /groups/{id}/transitiveMembers?$select=id` for each mapped group. It needs the application permissions User.Read.All and GroupMember.Read.All.
- Graph reads use only GET through `internal/outbound`. The client-credentials token request is one POST to `login.microsoftonline.com/<tenant>/oauth2/v2.0/token` through a separate client that allows only that URL — Graph has no GET token flow.
- When a user is deleted, has `accountEnabled` false, or has left the access group, the job disables the user with `Admin.Disable`. auth-all then revokes every session and refuses the user's access tokens.
- The job enables a user again only when `disabled_by_sync` is true — a disable by an admin stays.
- The job sets members of mapped groups to the directory members that have an Assay user. It writes one audit entry per change.
- A failed poll keeps the current state, shows the error and the last good poll on Settings, Sign-in, and retries at the next period — a Graph outage must not disable everyone.
- The fixture server gets a fake OIDC issuer and a fake Graph API with Acme users and groups.

## Out
- SAML. A separate issue tracks it.
- SCIM provisioning.
- Mapping IdP groups to Assay roles.
- Okta as a directory provider in this release.
- More than one SSO issuer.

## How I know it works
- With the fake issuer, an Acme user with no Assay account signs in with SSO and lands in Explore as a member of the mapped groups.
- A user outside the access group gets "Your organization has not given you access to Assay."
- Set `accountEnabled` false for a user in the fake Graph. After the next poll, the user's browser session gets 401, and an MCP call with the user's access token gets 401.
- Set it true again. After the next poll, the user signs in again. A user that an admin disabled stays disabled.
- Remove a user from a mapped IdP group. After the next poll, the user no longer sees the connections shared with that group. The leak suite covers this.
- Set `ASSAY_OIDC_ISSUER`. Settings, Sign-in shows the issuer field as locked.
- With password sign-in off, `POST /api/auth/sign-in/email` returns 404.
