# GitLab and Bitbucket connections

## What it does
An admin creates a GitLab connection or a Bitbucket Data Center connection with a URL and a token. Assay reads the selected repositories of that host as it reads GitHub and Azure DevOps: knowledge files by the globs, source code by the code globs, catalog and CODEOWNERS facts, and contracts. Both work with self-hosted servers. GitLab also works with gitlab.com.

## Decisions
- Both implement `tree.Host` in `internal/connectors/gitlab` and `internal/connectors/bitbucket` — `tree.Connector` then gives globs, limits, blob skip, code passages and contracts with no new sync code.
- Connection kinds are `gitlab` and `bitbucket`, added to `sync.Kinds` and the connector switch — each host has its own token form and URL rules.
- GitLab sends the token in the `PRIVATE-TOKEN` header — GitLab recommends it, and a personal, group or project access token with `read_api` and `read_repository` works.
- GitLab repo ID is the project path with namespace, such as `acme/payments/api`. API calls URL-encode it as the project ID.
- GitLab `Repos` lists `/projects?membership=true&archived=false` with keyset paging by `id` — offset paging omits totals above 10,000 records.
- GitLab `Tree` reads `/projects/:id/repository/tree?recursive=true&ref=<branch>&pagination=keyset&per_page=100`, and follows the `Link` header — a large repository has more than 100 entries.
- GitLab tree entries carry a blob `id` and no size. `Entry.Size` is -1, and the item limits apply after `Blob`.
- GitLab `Blob` reads `/projects/:id/repository/blobs/:sha/raw` — the blob ID is in the tree, so the read needs no path encoding and always matches the listed version.
- GitLab `Commit` reads `/projects/:id/repository/branches/:branch` and takes `commit.id`.
- Bitbucket sends the HTTP access token as `Authorization: Bearer` — Bitbucket Data Center supports it, and Git LFS is not used.
- Bitbucket repo ID is `<project key>/<repo slug>`. `Repos` lists `/rest/api/1.0/repos?permission=REPO_READ`, and `Repo` reads the default branch from `/rest/api/1.0/projects/:key/repos/:slug/default-branch`.
- Bitbucket pages with `start` and `limit=500`, and stops when `isLastPage` is true. It continues from `nextPageStart`. When a response has no `nextPageStart`, paging stops with the error "Bitbucket returned no next page. Upgrade Bitbucket to 7.14 or later." — versions before 7.14 have this bug in `/browse`, and a silent stop loses files.
- Bitbucket `Tree` reads `/rest/api/1.0/projects/:key/repos/:slug/files?at=<commit>` for paths. That endpoint has no blob IDs, so the blob ID of each file is the last commit that changed it, from `/rest/api/1.0/projects/:key/repos/:slug/last-modified?at=<commit>` per directory — the blob skip needs a version per file that changes only when the file changes.
- Bitbucket `Blob` reads `/projects/:key/repos/:slug/raw/<path>?at=<commit>` — `/browse` returns JSON lines, not bytes.
- `Provider` is `gitlab` or `bitbucket`, and `WebHost` is the host of the connection URL — entity keys follow the same form as GitHub keys.
- `FileURL` and `CommitURL` build the browse URLs of each host: GitLab `/-/blob/<ref>/<path>`, Bitbucket `/projects/:key/repos/:slug/browse/<path>?at=<ref>`.
- The connection form, the copy in `web/src/copy/en.json` and `docs/connectors.md` add both kinds with the token scopes that each needs.
- Both send only GET and HEAD, through `connectors.Client`.

## Out
- Bitbucket Cloud.
- Merge requests and pull requests of these hosts. The repository history spec covers GitHub and Azure DevOps only.
- Webhooks from these hosts.
- GitLab wikis and snippets.

## How I know it works
- Create a GitLab connection to the Acme GitLab fixture. Validate shows the user and the file count. A sync stores the knowledge files and the code items, and a second sync with no change fetches no blob.
- Change one file in the fixture. The next sync fetches only that file.
- Create a Bitbucket connection to the Acme Bitbucket fixture with more than 500 files. Every file that matches a glob is an item.
- A Bitbucket fixture response without `nextPageStart` fails the run with the upgrade message.
- The entity of a GitLab repository has the key `gitlab:<host>:<path>` and its catalog facts.
- The request log of the fixture server shows only GET and HEAD.
