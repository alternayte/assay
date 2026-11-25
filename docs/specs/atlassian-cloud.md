# Atlassian Cloud connections

## What it does
A Jira or Confluence connection reads Atlassian Cloud as well as Data Center (DC). The admin selects the deployment, enters the site URL, an email and an API token, and Assay reads the same items, facts and entities as on DC. An admin can switch a DC connection to Cloud. The next sync then rewrites the keys of the connection's items and entities, and claims, evidence, reviews and pages stay on the same entities.

## Decisions
- Cloud is a deployment of the existing `jira` and `confluence` kinds, not a new kind. The connection options get `deployment` (`dc` or `cloud`). A site URL that ends in `.atlassian.net` selects `cloud` by default — one kind keeps the scope form, rules, facts and globs the same.
- Cloud auth is Basic with `email:token`. The email is a connection option, and the token stays in secrets. DC keeps the bearer personal access token — Atlassian Cloud accepts API tokens only as Basic.
- A scoped API token calls `https://api.atlassian.com/ex/jira/{cloudId}` or `/ex/confluence/{cloudId}`. Assay reads the cloud ID from `GET <site>/_edge/tenant_info` when the admin saves the connection. Item links keep the site URL — a scoped token works only on the API host, and people open items on the site.
- The connector client allows two hosts for a Cloud connection with a scoped token: the site host and `api.atlassian.com`. It still follows no redirect to any other host, and it still sends only GET and HEAD.
- Jira Cloud lists issues with `GET /rest/api/3/search/jql`, paged by `nextPageToken` — Atlassian removed `/rest/api/2/search` and `/rest/api/3/search` on Cloud.
- Jira Cloud reads an issue with `GET /rest/api/3/issue/{key}`. A new `adf.go` converts Atlassian Document Format (ADF) descriptions and comments to Markdown, with the same rules as `wiki.go`: images go, and links keep only http, https and mailto.
- Validate on Jira Cloud reads one page of search and shows "N or more issues", with no total — `search/jql` returns no total, and the count endpoint needs POST.
- Jira Cloud people use `accountId`. The person key is `jira:<host>:user:<accountId>`, with the display name as an alias.
- The Jira DC connector stores the email of each person entity as entity metadata from this release on — the switch to Cloud needs the email to match people.
- Confluence Cloud lists changed pages and page IDs with CQL search at `/wiki/rest/api/content/search`, with the same CQL, expansion and `_links.next` paging as DC — the v2 API has no CQL and no modified-since filter.
- Confluence Cloud reads one page with `GET /wiki/api/v2/pages/{id}?body-format=storage`, and lists spaces with `GET /wiki/api/v2/spaces`, paged by the cursor in `_links.next`. `storage.go` converts the body, the same as on DC.
- The Cloud rate defaults are concurrency 4 and 10 requests per second. A 429 waits for `Retry-After`, which the client already reads — API-token traffic has no points quota, and the burst limit is 100 requests per second per endpoint.
- Switch to Cloud is an action on a DC connection. The admin enters the Cloud site URL, the email and the token. Assay validates them, saves the old host in the connection options as `previousHost`, and queues a full sync.
- The first sync after a switch rewrites keys in one transaction per batch:
  - every entity key of the connection that starts with `<kind>:<previousHost>:` gets the new host;
  - Jira items keep their external ID, because issue keys do not change in a migration;
  - a Confluence page with an unknown ID matches the stored item of the connection with the same space key and title whose ID is not in Cloud, and that item and its decision entity get the new page ID;
  - a Cloud person matches a DC person entity by email when Cloud returns the email, and the person key changes to the `accountId` key; a person with no match becomes a new entity.
- The rewrite changes key and external ID columns in place — entity IDs and item IDs stay, so claims, evidence, review cards, merges and pages stay attached.
- When a rewritten key already exists, the rewrite merges the two entities with the merge of `internal/entities` — two syncs of one source must not leave two entities.
- The switch writes one audit entry with the old and new host and the counts of rewritten items, entities and people. The run detail shows the same counts.
- A reconcile after the rewrite tombstones the items that matched nothing.

## Out
- OAuth 2.0 (3LO) for Atlassian Cloud.
- Jira Service Management and Jira Align.
- A switch back from Cloud to DC.
- Matching people by display name.

## How I know it works
- Contract tests replay recorded Cloud answers of the Acme fixture site for Jira and Confluence. Each returns the same items, facts and entity types as the DC fixtures.
- A Jira Cloud connection with a scoped token reads issues through `api.atlassian.com/ex/jira/<cloudId>`, and each item link opens on the site.
- The fixture server answers Jira Cloud with 429 and `Retry-After: 2`. The sync waits and then completes.
- Switch the Acme DC connections to the Acme Cloud fixture. After the sync, the entity count stays the same. The claim IDs of `Payments API` stay, and its page shows the same evidence with Cloud links.
- A Cloud person with no email in the answer appears as a new person entity, and the run detail counts it as unmatched.
- The audit log has one switch entry with the counts.
