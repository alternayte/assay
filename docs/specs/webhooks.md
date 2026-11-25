# Webhook sync

## What it does
A source system can tell Assay that an item changed. Assay then reads only that item, with GET, within seconds. Polling stays as before and catches what a webhook misses. Each connection of kind GitHub, Azure DevOps, Jira or Confluence Data Center shows its webhook URL and secret on the connection page, with the steps to register it in the source.

## Decisions
- The endpoint is `POST /hooks/<connection id>/<token>`. The token is 32 random bytes per connection, stored hashed — it authenticates sources that do not sign, and a leaked URL can be rotated.
- The route is outside `/api/v1` and outside the auth-all allowlist. It takes no session and no access token, and the auth-all middleware does not run — it is not a user route, so it must not create a principal.
- When the source signs, the endpoint also checks the signature with a separate per-connection secret, in constant time, over the raw body: GitHub `X-Hub-Signature-256` (HMAC-SHA256), Jira and Confluence Data Center `X-Hub-Signature` (HMAC-SHA256, with or without the `sha256=` prefix) — a URL token alone leaks through proxy logs.
- Azure DevOps service hooks do not sign. The connection uses HTTP Basic credentials that Assay generates, over HTTPS only — that is the only secret that Azure DevOps sends.
- Jira Cloud and Confluence Cloud are out — Confluence Cloud has no admin webhooks, and Jira Cloud signs only webhooks that an app registers.
- A webhook body only names items. The endpoint maps the event to item IDs: GitHub `push` to the changed paths of the default branch, `pull_request` and `issues` to their items; Azure DevOps `git.push`, `git.pullrequest.*` and `workitem.*`; Jira `jira:issue_*` and `comment_*` to the issue key; Confluence `page_*` to the page ID. Other events return 202 and do nothing.
- The endpoint never stores the body. It reads each named item again from the source with the connector `Fetch` — the source stays the only truth, and a forged body can only cause a read.
- The endpoint inserts a River job `sync_items` on the sync queue with the connection ID and at most 100 item IDs, and returns 202. The job calls `fetchOne` for each ID — a full sync run can take hours, and a webhook must not wait behind it.
- The job uses the rate limit and concurrency of the connection, and a paused connection drops the job — pause stops every read of the source.
- `sync_items` is unique by its args for 1 minute — a source that sends one event per field change then causes one read.
- A deleted item from a delete event becomes a tombstone only when `Fetch` returns 404.
- The endpoint answers 404 for an unknown connection or token and 401 for a bad signature, with no body detail, and limits each connection to 10 requests per second — the route is public.
- Each accepted webhook adds to a counter per connection, and the connection page shows the time of the last webhook — an admin sees that the source sends.
- Settings, Connections, Webhook shows the URL, the secret once, Rotate, and the steps per source kind. The URL uses `ASSAY_BASE_URL`. When it is a localhost or private address, the panel says: "Set ASSAY_BASE_URL to a public URL. The source system must reach this URL." — a laptop install behind NAT cannot receive webhooks.
- `docs/connectors.md` adds a Webhooks section with the steps per source and the public URL rule.

## Out
- Webhooks for GitLab, Bitbucket, web, Backstage, Jira Cloud and Confluence Cloud.
- Registering webhooks in the source through its API. Connectors are read-only.
- Fewer polling runs when webhooks work.

## How I know it works
- Send a signed GitHub `push` fixture for one file. Within 5 seconds the item has the new version, and the fixture log shows one GET of that blob.
- Send the same body with a wrong signature. The endpoint returns 401, and no job is queued.
- Send an Azure DevOps `workitem.updated` fixture with the generated Basic credentials. The work item is read again.
- Send ten Jira `jira:issue_updated` events for one issue in one second. The fixture log shows one GET of that issue.
- Pause the connection and send a webhook. No request reaches the source.
- Rotate the token. The old URL returns 404.
