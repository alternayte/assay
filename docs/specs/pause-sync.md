# Pause sync

## What it does
Pause stops Assay from reading a connection's source system. The content that Assay already stored stays visible, searchable and reviewable. Embedding, extraction, resolve, pages and design pages continue to process that stored content. Resume makes Assay read the source again, and the content stays visible during and after the resume.

## Decisions
- Pause controls only work that reads the source: sync runs and reconcile runs — pausing is a way to free source and model capacity for stored content, not a way to hide that content.
- The visible set ignores the connection status, and every content query that checks `c.status = 'active'` stops checking it: visibility, the Everyone lists of entities, registry and resolve, and the connection list that resolve reads — this makes Explore, Review, Glossary, Updates and entity pages show paused content.
- To hide content, an admin changes the connection visibility or purges the connection — those controls already exist, and pause stops being a second way to hide content.
- Pause stops a queued or running sync or reconcile run of the connection, and cancels its River job — a large Confluence sync otherwise continues for hours after the admin presses Pause.
- A run that pause stops ends as failed with the error "An admin paused the connection." — the run history then shows why the run ended, and no new run status is needed.
- The items that a stopped run stored stay, and the cursor does not move — the next sync skips items whose revision is unchanged, so no work is lost.
- The check at the start of a run that fails a paused connection stays — it catches a job that starts after the pause.
- Resume sets the status to `active`, and queues one sync and one reconcile — the sync gets the changes since the cursor, and the reconcile makes tombstones for items deleted during the pause.
- Remove the `resuming` status, `connections.status_epoch` and `sync_runs.status_epoch`, and remove the status logic from FinishReconcile — nothing reads them after this change.
- The migration changes each `resuming` connection to `active` and changes the check constraint to `('active', 'paused')`.
- An automatic pause after a 401 or 403 from the source keeps the content visible and keeps its reason — the content does not change, and pages already show when permissions were last checked.
- The connections page shows "Paused" with the reason. The copy for "resuming" is removed. `pausedHelp` becomes: "Assay does not sync a paused connection. Its content stays visible. Resume it to sync again."
- The Sync now and Reconcile buttons stay disabled while the connection is paused — a manual run of a paused connection fails at start.
- The API doc of `PUT /api/v1/connections/{id}/paused` says that pause stops syncing and that the content stays visible. Run `just gen` after the change.

## Out
- A way to hide a connection's content without purging it or changing its visibility.
- Stopping embedding, extraction or design jobs per connection.
- Faster sync or embedding. A separate spec covers ingest throughput.
- Pausing the registry, which stays refused.

## How I know it works
- Pause a Confluence connection that holds content. Explore, Review, Glossary, Updates, search and its entity pages still show the content.
- Pause a connection while a sync runs. The run ends as failed with "An admin paused the connection." within a few seconds, and its River job is cancelled.
- While the connection is paused, the embedding count still rises, and no sync or reconcile run starts for it.
- Resume the connection. The status shows "Active" at once, a sync run and a reconcile run appear in its run history, and the content stays visible all the time.
- Delete a page in the source while the connection is paused, then resume. After the reconcile, the item is a tombstone.
- A source token that returns 401 pauses the connection with the source error as its reason, and its content stays visible.
- `SELECT DISTINCT status FROM connections` returns only `active` and `paused` after the migration.
- `just ci` passes.
