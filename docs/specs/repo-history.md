# Repository history

## What it does
A GitHub or Azure DevOps connection also reads the history of its repositories: merged pull requests with their review comments and linked issues, and the commit message of the last change to each file. A GitHub connection reads the issues of its repositories. An Azure DevOps connection reads the work items and the wiki pages of its projects. Each of these is an item with passages, so search, Ask, claims and get_item use it. Design pages cite pull request passages as sources for the reasons of a design.

## Decisions
- A pull request, an issue, a work item and a wiki page are each one item, of kind `pull_request`, `issue`, `work_item` and `page` — they use the item model and the visible set of the connection, and need no new storage.
- Item IDs are `<repo>#pr/<number>`, `<repo>#issue/<number>`, `<project>#wi/<id>` and `<project>#wiki/<page id>` — they cannot collide with file IDs, which are `<repo>:<path>`.
- A pull request item has the title, the body, and the review comments and thread comments as passages under the heading "Review". Its metadata lists the changed file paths and the merge commit — design pages use the paths, and the commit links the item to the code.
- GitHub lists pull requests with `state=closed`, sorted by `updated`, descending, and keeps those with `merged_at` set — the API has no merged filter. Sync stops paging at the first pull request older than the cursor.
- GitHub lists issues with `since=<cursor>` and drops entries that carry `pull_request` — the issues endpoint also returns pull requests.
- Azure DevOps lists pull requests with `searchCriteria.status=completed`, and reads threads per pull request — the threads endpoint is not paged, so one request reads all.
- Azure DevOps work items: WIQL returns IDs, and `workitemsbatch` reads them in batches of 200. The WIQL query windows on `System.ChangedDate` by month — a query over 20,000 items fails.
- Azure DevOps wiki: `pagesbatch` lists pages with its continuation token, and a GET of each page with `includeContent=true` reads the Markdown.
- A new connection setting `historyMonths` sets the window, with a default of 24 — older pull requests seldom explain the current code, and the window bounds the first sync. 0 turns history off.
- Settings per connection turn on pull requests, issues and work items, and the wiki, each on by default — an admin can keep a noisy tracker out.
- When the blob of a file changes, sync reads the last commit of that path (GitHub `commits?path=&per_page=1`, Azure DevOps `commits?searchCriteria.itemPath=&$top=1`) and adds the message as the passage "Last change" — this costs one request per changed file, not one per file.
- A pull request becomes an entity of the new type `change`, keyed `<provider>:<host>:<repo>#pr/<number>`. The migration adds `change` to the type check.
- The predicate `implements` adds the subject `change` and the objects `issue` and `initiative`. A link from a pull request to an issue gives a deterministic `implements` fact: GitHub closing keywords and the timeline cross-reference, and Azure DevOps work item links of the pull request — the link is in the source, so it needs no model.
- An issue key of a Jira connection in a pull request title or body gives the same fact with the Jira issue key as the object — most teams link pull requests to Jira, not to GitHub issues.
- The design build adds pull request passages with `kind="doc"` to the context of a component when a changed path of the pull request is inside the component path. It takes the newest 5 per component — a reason needs a cite that is not code, and pull requests hold most reasons.
- The connector sends only GET and HEAD, through `connectors.Client`.
- Reconcile tombstones a pull request, an issue, a work item or a wiki page that the source no longer returns inside the window. An item that leaves the window stays.

## Out
- Open and abandoned pull requests, draft pull requests.
- Commit messages other than the last change per file.
- Pipelines, builds and releases.
- GitHub Discussions and GitHub wikis.
- Work item attachments.

## How I know it works
- Sync the Acme GitHub fixture. Search finds a merged pull request by a word of its review comment, and get_item returns its body and the "Review" passages.
- The entity of the pull request has an `implements` claim to the Acme issue that its body closes, with the body passage as evidence.
- A design page section of the Acme fixture cites a pull request passage for a "because" sentence.
- Sync the Acme Azure DevOps fixture with more than 200 work items. Every work item is an item, and the wiki pages are items with their Markdown.
- Set `historyMonths` to 0. The next sync reads no pull requests, and reconcile keeps the items that exist.
- The request log of the fixture server shows only GET and HEAD.
