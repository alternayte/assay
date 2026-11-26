# Jira comments and history

## What it does
A Jira issue item holds all its comments, up to a cap, and a History section of its status, assignee and fix version changes. Each change becomes a dated value, so the entity timeline shows when an issue or initiative moved between states and who held it. This works on Jira DC and on Jira Cloud.

## Decisions
- Fetch pages through all comments, oldest first, with `startAt` and `maxResults=100`: DC through `/rest/api/2/issue/{key}/comment` and Cloud through `/rest/api/3/issue/{key}/comment` — the newest 50 drop older decisions.
- The connector rules get `maxCommentsPerIssue`, with a default of 500. Above the cap, the item keeps the newest comments and adds the passage "Assay read the newest N of M comments." — a bot thread must not fill the item, and the reader must see that comments are missing.
- DC reads the changelog with `expand=changelog` on the issue request it already sends — DC returns all histories there, and this costs no extra request.
- Cloud reads the changelog with `GET /rest/api/3/issue/{key}/changelog`, paged by `startAt` — Cloud cuts the expanded changelog at 100 entries. The bulk changelog endpoint needs POST, so the connector does not use it.
- The connector reads changes to `status`, `assignee` and `Fix Version` only — these are the fields that the timeline shows. Other changes are not read.
- The item body gets a `## History` section after Comments. Each change is one paragraph: `**<yyyy-mm-dd hh:mm>, <author>:** <Field> <from> → <to>`, oldest first — each change is then one passage, and a fact quotes it.
- `content.Fact` gets `At time.Time`. A time-scoped fact with `At` set creates or finds the claim with `valid_from = At` and skips the "keep the newest value when it is unchanged" check — the history adds older values in any order, and the upsert key already includes `valid_from`.
- Each status change gives a `status` fact with `At` at the change time, quoting its History passage.
- `assigned_to` becomes time-scoped in the predicate registry. Each assignee change gives an `assigned_to` fact with `At` — a past assignee must show on the timeline, not conflict with the current one. An unassign gives no fact.
- A new predicate `fix_version` has text objects, subjects issue and initiative, is time-scoped and single-valued, and has priority jira then others. Its value is the comma-joined fix versions after the change. The current field also gives one `fix_version` fact — one value per change keeps the timeline readable, and the evaluation of time-scoped claims supports single values only.
- The current-field `status`, `assigned_to` and `fix_version` facts get `At` = the time of the last change to that value, or the issue created time when the changelog has none — the current value and the newest history value are then the same claim.
- The size check `MaxItemBytes` counts the description, the comments and the History — an issue that exceeds it is skipped with `too_large`, as today.
- Comments and the changelog are read only for issues in `Changed` — an unchanged issue costs no extra request.

## Out
- History of fields other than status, assignee and fix version.
- Worklogs and attachments of Jira issues.
- Use of the Cloud bulk changelog endpoint.

## How I know it works
- The Acme fixture issue `PAY-12` has 120 comments. The item holds all 120 in order. With `maxCommentsPerIssue` 50, it holds the newest 50 and the note passage.
- The entity page of initiative `PAY-1` shows a timeline: To Do on one date, In Progress on a later date, Done on the latest date. Each value cites its History passage.
- A past assignee of `PAY-12` shows in the timeline, and only the current assignee shows as current. No conflict card appears for the assignee.
- The same checks pass against the Jira Cloud fixture, which pages the changelog over two requests.
- A second sync of an unchanged issue sends no comment or changelog request.
