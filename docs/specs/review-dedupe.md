# Review dedupe

## What it does
A reviewer answers each question once. A card stands for one question, such as "is mention X in connection C the entity E?" It does not stand for one place where the question was found. New occurrences of an open question join its card, and occurrences of a decided question follow the decision. Cards whose question no longer applies close by themselves. The review page keeps its place after a decision. Resolve and extraction never do the same work twice at the same time.

## Decisions
- `proposals` gets a `dedupe_key` column and a unique index on `(kind, dedupe_key)` where `status = 'open'` and `dedupe_key` is not null — the database then refuses a duplicate open card, whatever path inserts it.
- Each card insert from resolve uses `ON CONFLICT DO NOTHING`, or it adds its occurrence to the open card — two writers can no longer both pass a check-then-insert.
- These are the dedupe keys:
  - merge: `merge:<low entity id>:<high entity id>`, after each id follows `merged_into` to its winner. A member's merge proposal for a pair with an open card returns that card.
  - mention_link: `mention_link:<connection id>:<normalized mention>:<candidate entity id>`.
  - unnamed: `entity:<unnamed key>`, from the unnamed entities spec.
  - conflict: `conflict:<subject entity id>:<predicate>:<scope>`.
  - new_term: `new_term:<normalized phrase>`.
  - repo_system: the current target.
  - Keys use entity ids, not keys, because a merge changes the visible key and not the id.
- A card holds a list of occurrences in its payload. An occurrence is the item, the passage hash, the claim index and the quote. The card shows the number of occurrences and the first three quotes — the reviewer sees the evidence once.
- A mention_link card groups occurrences per connection only — a card is visible only when all its connections are visible, so grouping across connections would hide it from reviewers who see one connection.
- Accepting a grouped card applies the decision to every occurrence. Rejecting it rejects every occurrence.
- Before resolve asks a model, it looks for a card with the same key:
  - An open card gets the occurrence, with no model call.
  - An accepted card applies its decision to the occurrence.
  - A rejected card drops the occurrence.
  - These checks look at all card statuses, not only open cards — a decided question does not come back as a new card.
- A conflict card that is open gets the new claims of its set. When no card is open, a new conflict card opens only if the set holds a claim that did not exist when the last conflict card for the key was decided — new evidence is a new question, but the same set is not.
- Add the card status `closed`, with a reason, no reviewer, and no outcome on the decision — closing is not a reviewer outcome, so band calibration must not read it.
- Each resolve run sweeps the open cards. It drops occurrences whose passage is no longer in the current version of the item. It closes a card with no occurrences left ("The source no longer says this."). It closes a merge card whose two entities are now one entity ("The entities are merged."). It closes a conflict card with fewer than two current claims ("The conflict no longer exists."). It closes a card whose entity no longer exists ("The entity no longer exists."). It moves a merge card to its new key after a merge, and closes it as a duplicate when that key already has an open card.
- The migration adds the column and the index with null keys. The first resolve run sets the keys on open cards. For each key, the run keeps the oldest card, moves the occurrences of the others onto it, and closes the others with "Same question as card <id>." — key normalization is Go code, so the backfill cannot run in SQL.
- Resolve runs on its own River queue `resolve`, with one worker. It is unique while available, scheduled, pending, retryable or running, not per period — River uniqueness is global across processes, and a run longer than 15 minutes no longer overlaps the next.
- The extraction cache reuses an extraction result for the same passage hash, extractor version and connection when the item differs. The claims are written with the evidence of the new item, and there is no model call — boilerplate text in a large Confluence space is extracted once per connection.
- An extraction job checks the cache again just before each model call, not only at job start. It holds a per-passage lease row while it calls the model. A second job skips a passage with a live lease. A lease expires after the job timeout — two jobs never pay for the same passage.
- `PendingBatch` excludes passages with a live lease. The admin extraction start returns 409 while an extraction of the connection is running — a second start queued every pending passage again.
- Reader-triggered extraction jobs are unique by sorted items and hashes, and not by the visible set or the trigger — two users who open the same page queue one job.
- On the review page, the decided card stays in the pending list until its request and the refetch finish — the card no longer comes back for a moment.
- After a decision or a skip, the page shows the card that came after the decided card at the time of the decision. If that card is gone, the page shows the card at the same position — the reviewer keeps their place when the server order changes.
- The search field on the review page waits 300 ms after typing before it changes the filter — each keystroke no longer remounts the list and sends the pending decisions.

## Out
- Dedupe of cards that people propose: correction, finding, term_edit and design cards, except merge.
- needs_edit cards per visibility key. Each one holds different page text.
- Faster embedding or sync.
- A new order for the queue score.

## How I know it works
- The name "Payments" in 50 passages of one connection, with one candidate entity, gives one mention_link card that shows 50 occurrences.
- Accept that card. All 50 claims link to the candidate. A new passage with "Payments" links with no card and no model call.
- Reject a mention_link card, then sync a new passage with the same mention. No card appears, and the decision log shows no new decision.
- Edit the source page so the quoted passage is gone, then sync. After the next resolve run, the card is closed with "The source no longer says this."
- `SELECT kind, dedupe_key, count(*) FROM proposals WHERE status = 'open' GROUP BY 1, 2 HAVING count(*) > 1` returns no rows after one resolve run on an existing database.
- `SELECT count(*) FROM river_job WHERE kind = 'resolve' AND state = 'running'` never returns more than 1.
- Two users open the same page with too few claims at the same time. One extraction job runs.
- On the review page, accept a card that is not first in the list. The next card in the list shows, and the decided card does not come back.
- `just ci` passes.
