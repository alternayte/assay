# Connectors

A connection is one configured source system with a token, a scope and a visibility. Admins create connections in Settings, Connections. The wizard tests the token, shows the user and a sample count, and lists the scopes live.

Connectors are read-only. The connector HTTP client sends only GET and HEAD requests, only to the host of the connection, and follows no redirect to another host. Use a token with read access only.

## Sources

| Kind | API | Token | Scope |
|---|---|---|---|
| Jira Data Center | REST v2 | Personal access token (bearer) | Projects, or a JQL query |
| Confluence Data Center | `/rest/api/content` | Personal access token (bearer) | Spaces, optional ancestors and labels |
| GitHub (cloud and Enterprise Server) | REST v3 | Token (bearer) | Repositories, default branch |
| Azure DevOps Server | Items API, `api-version` 7.1 by default | Personal access token (Basic) | Repositories |

Jira issues give their summary, description, the newest 50 comments, status, type, assignee, reporter, priority, labels, components, fix versions, due date, parent and links. Confluence pages give their title, body, version, ancestors, labels and author.

Repositories give only the files that the include globs select. The defaults are:

`README*`, `docs/**/*.md`, `**/adr/**/*.md`, `**/decisions/**/*.md`, `CODEOWNERS`, `.github/CODEOWNERS`, `**/catalog-info.yaml`, `**/openapi*.{yaml,yml,json}`, `**/*.proto`, `go.mod`, `package.json`, `*.csproj`, `Directory.Packages.props`, `Chart.yaml`, `**/values*.yaml`, `deploy/**/*.yaml` and `k8s/**`.

A file above 512 KB, or a repository above 2,000 files, is skipped. Each run lists the skipped items with a reason. Admins change the limits in Settings, General.

## Facts without a model

Some sources give claims with no model call:

- `catalog-info.yaml` (Backstage kinds Component, API, System, Group, Resource and Domain) gives entities `backstage:<kind>:<namespace>/<name>` and the claims `owned_by`, `part_of`, `depends_on`, `provides_api`, `consumes_api` and `status`. The repository gets `implements`.
- The default rule of `CODEOWNERS` gives an `owned_by` claim for the repository. Path rules give no claim.
- Jira fields give `status`, `assigned_to`, `target_date`, `blocks` (the configured link type) and `part_of`. The issue types in the connector rules become initiatives.
- Confluence pages with a decision label (default `decision` and `adr`) become decisions. Tables with Term and Definition columns on pages with a glossary label (default `glossary`) give terms.

The extract model finds more claims in the text. See [models.md](models.md).

## Sync

- **Sync** reads the changed items since the cursor, every 30 minutes by default, with a 5-minute overlap. A version is written only when the content changes.
- **Reconciliation** lists all IDs in scope, every 24 hours by default. It deletes the items that no longer exist and records the permissions check time.
- Admins start a sync or a reconciliation from the connection page. The page shows the last and next sync, the counts, the errors and the run history.
- A 401 or 403 pauses the connection and hides its content at once. After you fix the token and resume, the content shows again after a successful reconciliation.
- Purge deletes all data of a connection.

Connectors obey the rate limits of the connection (default 4 concurrent requests and 5 per second), `Retry-After`, and back off with jitter.

## Visibility

A new connection is visible only to its owner, the admin who creates it. To share it, add groups on the connection page and confirm. Everyone is the built-in group of all active users. Every content query filters by the visible set of the reader. See [security.md](security.md).
