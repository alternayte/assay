# Connectors

A connection is one configured source system with a token, a scope and a visibility. Admins create connections in Settings, Connections. The wizard tests the token, shows the user and a sample count, and lists the scopes live.

Connectors are read-only. The connector HTTP client sends only GET and HEAD requests, only to the host of the connection, and follows no redirect to another host. A Cloud connection with a scoped token also sends to `api.atlassian.com`. A Teams connection also sends one POST to the token endpoint of its Entra ID tenant, because Microsoft Graph has no other way to give an app-only token. Use a token with read access only.

## Sources

| Kind | API | Token | Scope |
|---|---|---|---|
| Jira Data Center | REST v2 | Personal access token (bearer) | Projects, or a JQL query |
| Jira Cloud | REST v3, search with `/rest/api/3/search/jql` | API token (Basic, email and token) | Projects, or a JQL query |
| Confluence Data Center | `/rest/api/content` | Personal access token (bearer) | Spaces, optional ancestors and labels |
| Confluence Cloud | CQL search of `/wiki/rest/api/content/search`, pages and spaces of REST v2 | API token (Basic, email and token) | Spaces, optional ancestors and labels |
| GitHub (cloud and Enterprise Server) | REST v3 | Token (bearer) | Repositories, default branch |
| Azure DevOps Server | Items API, `api-version` 7.1 by default | Personal access token (Basic) | Repositories |
| GitLab (gitlab.com and self-managed) | REST v4 | Personal, group or project access token with `read_api` and `read_repository` (`PRIVATE-TOKEN` header) | Projects, default branch |
| Bitbucket Data Center | REST 1.0 | HTTP access token with Repository read (bearer) | Repositories, default branch |
| Web documentation | Pages over HTTP | Optional auth header, `Name: value` | Seed, path prefixes, link depth and page limit |
| Microsoft Teams | Microsoft Graph v1.0 | Client secret of your own Entra app (client credentials) | Teams, then standard channels |
| Slack | Web API | Bot token of your own internal app (bearer) | Public channels where the bot is a member |

Jira issues give their summary, description, comments, status, type, assignee, reporter, priority, labels, components, fix versions, due date, parent and links. Confluence pages give their title, body, version, ancestors, labels and author.

### Jira comments and history

- Assay reads all comments of a changed issue, oldest first, in pages of 100.
- The connector rule `maxCommentsPerIssue` limits the comments of one item. The default is 500. Above the limit, the item keeps the newest comments and the passage "Assay read the newest N of M comments."
- The item has a `## History` section after the comments. It holds the changes of status, assignee and fix version, oldest first. Each change is one paragraph: `**<yyyy-mm-dd hh:mm>, <author>:** <Field> <from> → <to>`. Assay does not read the changes of other fields.
- Data Center gives the changelog on the issue request with `expand=changelog`. Cloud pages `GET /rest/api/3/issue/{key}/changelog`. Assay does not use the bulk changelog of Cloud, because it needs POST.
- An unchanged issue gets no comment or changelog request.
- The item size limit counts the description, the comments and the History.

### Confluence attachments

- A Confluence connection reads the PDF, Word (DOCX) and draw.io attachments of the pages in its scope. Each attachment is an item of kind `file` with the ID `att:<attachment ID>`, the file name as title and the attachment version as revision.
- The item metadata holds `parent`, `parentTitle`, `mediaType` and `fileSize`. The heading path of each passage starts with the page title.
- A PDF page is a section `Page N`. A scan without text is skipped with `no_text`, an encrypted PDF with `encrypted`, and a file that Assay cannot parse with `unreadable`.
- A DOCX gives headings from the styles Heading 1 to Heading 6, tables and paragraphs.
- A draw.io file gives one section for each diagram tab, with the labels of its shapes and lines as a list. Assay finds draw.io files by the media type `application/vnd.jgraph.mxfile` or the extension `.drawio`.
- The setting `attachments` in Settings, General selects the file types and the largest file. The default types are `pdf`, `docx` and `drawio`, and the default limit is 10240 KB. An empty type list stops attachment reads. A larger file is skipped with `too_large` before any download.
- Assay reads an attachment only when its page is an item of the connection. A sync lists the changed attachments of a space with one CQL query after the pages. A reconciliation deletes the attachments that are gone, and the attachments of a page that is gone.
- A page body shows an attachment link, a `viewpdf` macro or a `view-file` macro as `Attachment: <file name>`.
- A Confluence Cloud download redirects to the Atlassian media host `https://api.media.atlassian.com`. A Cloud connection follows this redirect and sends no credentials to that host. `ASSAY_ATLASSIAN_MEDIA_URL` changes the media host. A redirect to any other host skips the attachment with `foreign_host`.

The base URL of a GitLab connection is `https://gitlab.com` or the URL of the self-managed server. The base URL of a Bitbucket connection is the URL before `/rest`. A GitLab project ID is its path with namespace, such as `acme/payments/billing-api`. A Bitbucket repository ID is the project key and the repository slug, such as `PLAT/billing-gateway`.

Bitbucket lists files without blob IDs. The version of each file is the last commit that changed it, so a sync fetches only the changed files. Bitbucket before 7.14 omits the next page of a long file list. A sync then fails with the message "Bitbucket returned no next page. Upgrade Bitbucket to 7.14 or later."

Repositories give only the files that the include globs select. The defaults are:

`README*`, `docs/**/*.md`, `**/adr/**/*.md`, `**/decisions/**/*.md`, `CODEOWNERS`, `.github/CODEOWNERS`, `.gitlab/CODEOWNERS`, `.bitbucket/CODEOWNERS`, `**/catalog-info.yaml`, `**/openapi*.{yaml,yml,json}`, `**/*.proto`, `go.mod`, `package.json`, `*.csproj`, `Directory.Packages.props`, `Chart.yaml`, `**/values*.yaml`, `deploy/**/*.yaml` and `k8s/**`.

A file above 512 KB, or a repository above 2,000 files, is skipped. Each run lists the skipped items with a reason. Admins change the limits in Settings, General.

## Atlassian Cloud

Jira and Confluence connections read Data Center or Cloud. Select the deployment in the wizard. A site URL that ends in `.atlassian.net` selects Cloud.

- Enter the site URL, such as `https://acme.atlassian.net`, the email of the Atlassian account and an API token of that account.
- A scoped API token reads through `https://api.atlassian.com/ex/jira/<cloud ID>` or `/ex/confluence/<cloud ID>`. Assay reads the cloud ID from `<site>/_edge/tenant_info` when you save. Links still open on the site.
- Cloud items give the same fields, facts and entities as Data Center. Jira Cloud descriptions and comments are Atlassian Document Format. Assay converts them with the rules of wiki markup.
- Jira Cloud people have the key `jira:<host>:user:<account ID>`, with the display name as an alias. Cloud gives the email only when the privacy settings of the account allow it.
- The test shows "N or more items" for Jira Cloud. Its search gives no total.
- The default rate of a Cloud connection is 4 concurrent requests and 10 per second. A 429 waits for `Retry-After`.

### Switch to Cloud

After a migration from Data Center to Cloud, select **Switch to Cloud** on the connection page. Enter the site URL, the email and an API token. Assay tests them, keeps the old host in the option `previousHost`, and starts a full sync. A connection cannot switch back to Data Center.

The first sync rewrites the keys of the connection in one transaction per batch:

- Entity keys that start with `<kind>:<old host>:` get the Cloud host. When the new key exists, the two entities merge.
- Jira items keep their issue keys.
- A Confluence page with an unknown ID takes over the stored page with the same space and title whose ID is not in Cloud. The page and its decision get the new page ID.
- A Cloud person takes over the Data Center person with the same email. Jira Data Center connections store the email of each person for this. A Cloud person without an email, or without a match, becomes a new person.

Entity and item IDs stay, so claims, evidence, review cards, merges and pages stay attached. The run history shows the counts of matched items, entities and people, and of unmatched people. The audit log has one `connections.switch` entry with the hosts and counts. A reconciliation after the sync deletes the items that matched nothing.

## Web documentation

A `web` connection reads a documentation site, such as an internal developer portal or a vendor docs site. Each page is an item with passages. Search, Ask, claims and `get_item` use it.

- **Seed.** The crawl starts from `llms.txt`, `llms-full.txt`, `sitemap.xml` or the base URL. The default `auto` tries `/llms.txt`, then `/sitemap.xml`, then the base URL. The seed files are below the base URL.
- **llms.txt.** The H1 is the site title. Each `[name](url): notes` link under an H2 is a seed page. The links under `## Optional` are seed pages too. Use path prefixes to limit the scope.
- **llms-full.txt.** Each top-level (`#`) section is one item. The item URL is the first link of the section when it is on the same host. Otherwise it is `<base>/llms-full.txt#<slug>`.
- **Scope.** The crawl follows `<a href>` links and Markdown links to pages on the same scheme and host, inside the path prefixes, breadth first. Empty path prefixes mean the path of the base URL. It drops the fragment and the query string. The link depth is 3 and the page limit is 2,000 by default. Links to files, such as PDF and images, get no request.
- **Markdown first.** For each page, Assay sends HEAD to `<url>.md`, or to `<url>index.html.md` for a URL that ends in `/`. On 200 it reads the Markdown. Otherwise it converts the HTML of `<main>`, else `<article>`, else `<body>`, without `nav`, `header`, `footer`, `aside`, scripts, styles, forms and images. Links keep only `http`, `https` and `mailto` targets.
- **robots.txt.** Assay reads `/robots.txt` once per run as the user agent `Assay` and skips the paths that it disallows. `Crawl-delay` lowers the rate of the connection, up to one request each 60 seconds.
- **Redirects.** Assay follows a redirect only to the same scheme and host. A redirect to another host skips the page.
- **Changes.** The item ID is the normalized URL. The revision is the `ETag`, else `Last-Modified`. After the first sync, a sync sends HEAD for each stored page and reads only the pages whose revision changed. A page without a revision is read, and the content hash decides. New pages come with the next reconciliation.
- **Reconciliation.** It crawls from the seed again. It deletes the pages that return 404 or 410, or that the seed no longer reaches. A crawl that reads no page fails, and the stored pages stay.
- **Auth header.** A portal behind a static token takes an auth header, such as `X-Portal-Token: <token>`. Assay stores it encrypted as the connection secret. With an auth header, a 401 or 403 pauses the connection. Without one, only a 401 pauses it, and a 403 skips the page.
- **Limits.** A page above the item size limit is skipped. A page that is not HTML or Markdown is skipped. JavaScript rendering, other hosts, sign-in flows, cookies and linked files are not read.

## Chat sources

A `teams` or a `slack` connection reads the threads of the channels that you select. Each selected channel is one part of a sync. Search, Ask, claims, entity pages and `get_item` use the threads like other items.

- **Items.** One thread is one item of the kind `thread`: the root message and its replies. The item ID is `<team or workspace>/<channel>/<root message ID>`. The title is the channel name and the first 80 characters of the root message. The link is the `webUrl` of the Teams root message, or the Slack permalink `https://<workspace>.slack.com/archives/<channel>/p<ts without the dot>`.
- **Passages.** Each message is one passage. Its text starts with the author and the date, such as "Dana Ito, 2026-05-04:". Teams HTML and Slack mrkdwn become Markdown with the link rules of other sources: only `http`, `https` and `mailto` links stay. User and channel mentions become the display name.
- **Dropped.** Bot and app messages, join and leave events, system events, deleted messages and messages without text.
- **People.** Each author is a person with the key `teams:<tenant>:user:<id>` or `slack:<workspace ID>:user:<id>` and a `posted_in` fact for the thread. Resolve can merge the person with the same person from other sources.
- **Changes.** The revision of a thread is the time of its newest reply. A sync skips a thread with no new reply.
- **Retention.** The option `retainDays` is 0 by default and keeps all threads. With a value, a sync skips the threads whose newest message is older than that number of days, and a reconciliation deletes them. A change of the option starts a reconciliation.
- **Not read.** Private channels, shared channels of another tenant, group chats, direct messages, files, images, reactions, edit history and deleted-message history.

### Microsoft Teams

1. Register an app in Microsoft Entra ID of your tenant. Create a client secret.
2. Give the app the application permissions `ChannelMessage.Read.All`, `Team.ReadBasic.All` and `Channel.ReadBasic.All`, and grant admin consent. The app reads with no signed-in user.
3. In the wizard, enter the Graph URL `https://graph.microsoft.com/v1.0`, the tenant ID or primary domain, the client ID and the client secret. Assay stores the secret encrypted.
4. Select the teams, and then the channels. Assay lists only standard channels.

Microsoft can require approval of an app before it reads channel messages with application permissions. Request it for your app when Graph refuses the reads. A refused secret or permission pauses the connection.

Assay gets the token with the client credentials grant from `https://login.microsoftonline.com/<tenant>/oauth2/v2.0/token`. For a national cloud, set `ASSAY_ENTRA_AUTHORITY_URL` to its sign-in host and use its Graph URL as the base URL.

The first sync of a channel lists `GET /teams/{team}/channels/{channel}/messages` 50 roots at a time with `$expand=replies`, and follows `replies@odata.nextLink` for a thread with more than 200 replies. A delta read covers only the last 8 months, so the first sync lists. It then starts a delta read and stores the delta link as the cursor of the channel. Later syncs follow the delta link and read each thread with a changed root or reply again. An expired delta link starts a new listing. Graph allows 130,000 requests in 10 seconds for each app, and a 429 waits for `Retry-After`.

### Slack

1. Create an app in your own Slack workspace. Do not distribute it: an internal app is exempt from the limit of one history request per minute for apps outside the Slack Marketplace.
2. Give the bot the scopes `channels:read`, `channels:history` and `users:read`, and install the app. Copy the bot token. It starts with `xoxb-`.
3. Invite the bot to each channel that Assay reads, with `/invite @<bot name>` in the channel. Assay does not join channels, because a join is a write.
4. In the wizard, enter `https://slack.com/api` and the bot token, and select the channels.

Each call is a GET with the token as bearer. A sync calls `conversations.history` for each channel with `oldest` 30 days before the cursor, and `conversations.replies` only for the threads whose `latest_reply` is newer than the stored revision. `conversations.history` does not return an old root that gets a new reply, so the reconciliation reads the whole history. It deletes the threads that are gone and reads the threads that sync missed. `users.info` gives the display names, once for each user in a run. The default rate is 2 concurrent requests and 0.8 requests per second, because Tier 3 methods allow about 50 requests per minute. A 429 waits for `Retry-After`. A refused token pauses the connection.

## Facts without a model

Some sources give claims with no model call:

- `catalog-info.yaml` (Backstage kinds Component, API, System, Group, Resource and Domain) gives entities `backstage:<kind>:<namespace>/<name>` and the claims `owned_by`, `part_of`, `depends_on`, `provides_api`, `consumes_api` and `status`. The repository gets `implements`.
- The default rule of `CODEOWNERS` gives an `owned_by` claim for the repository. Path rules give no claim. Each host reads its own file: GitHub and Azure DevOps `.github/CODEOWNERS`, `CODEOWNERS` or `docs/CODEOWNERS`, GitLab `CODEOWNERS`, `docs/CODEOWNERS` or `.gitlab/CODEOWNERS`, and Bitbucket `.bitbucket/CODEOWNERS`. A Bitbucket group `@@group` is a team.
- Jira fields give `status`, `assigned_to`, `fix_version`, `target_date`, `blocks` (the configured link type) and `part_of`. The issue types in the connector rules become initiatives.
- Each status, assignee and fix version change of the History gives a dated value of `status`, `assigned_to` or `fix_version`. The entity timeline shows these values. The current field value has the date of its last change, or the created date of the issue when no change set it. An unassign gives no value. The value of `fix_version` is the comma-joined fix versions after the change.
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
