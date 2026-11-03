# Ingest throughput

## What it does
A Confluence sync reads page bodies in the listing requests, not with one request per page. A long first sync saves its progress as it runs. An edit to a page embeds only the chunks whose text changed. Chunks with the same text share one vector. Embedding runs in parallel shards, and progress reporting no longer counts every row after each batch.

## Decisions
- The Confluence `Changed` listing expands `body.storage,version,ancestors,metadata.labels,space,history`, uses `limit=50`, and returns each page as a full item in `Ref.Item` — this replaces one request per page with one request per 50 pages.
- Sync stores a `Ref` that carries an item without calling `Fetch`. The revision skip and the `MaxItemBytes` skip still apply. Other connectors leave `Ref.Item` empty and keep `Fetch`.
- Confluence paging follows `_links.next`, not "fewer results than the limit" — the server can cap the page size below the limit, and then paging would stop early.
- Sync streams the listing. It stores the items of one listing page before it reads the next page, with the connection's concurrency inside the page — keeping all bodies in memory does not fit a large space.
- After all items of a listing page are stored, sync saves the cursor as the newest `lastmodified` in that page — a run that fails or times out after hours then continues from there.
- `ListIDs` for reconcile keeps its listing without bodies.
- `content.Store` computes an index hash of the title, the body and the given passages. When the index hash is unchanged, Store writes the version and the item fields, and skips `writeIndex` — a change only to metadata or the editor no longer rewrites passages and chunks.
- `chunks` gets the column `text_hash bytea`, which is `sha256` of the chunk text, with an index.
- `writeIndex` keeps each existing chunk row whose text hash is in the new chunk set, and updates its version, ordinal, heading path and passage hashes. It deletes the other old chunks and inserts only the new ones — a kept row keeps its vector, so an edit to one section embeds that section only.
- All new chunks of an item are inserted in one statement, not one statement per chunk.
- The migration adds `text_hash` as nullable. A River job on the default queue fills it in batches of 10,000 rows — a single UPDATE of millions of rows in a migration blocks start-up. A chunk with a null hash never matches, so it is embedded again once at most.
- Before the embed job calls the model for a batch, it copies the vector of any chunk in the same space with the same text hash — boilerplate and moved sections then cost no model call. A vector holds no text, and both chunks hold the same text, so the copy shows nothing new to anyone.
- The embed job splits into N shards. Shard i embeds chunks with `id % N = i`. N is `EmbedWorkers`, with a default of 4 from config, and it is also the `embed` queue's MaxWorkers. Each shard job is unique by its args while it is unfinished — a fixed split needs no row locks and works across processes.
- A shard reads pending chunks with keyset paging (`c.id > last`), not by scanning from the first id for each batch.
- A shard writes the vectors of a batch in one multi-row insert.
- A shard adds its count to `done` with an increment. Shard 0 counts `done` and `total` exactly every 5 minutes, and at job start — the two full counts after each batch cost more than the batch itself.
- Shard 0 embeds the profiles. A shard that finds no pending chunks checks once for pending chunks in any shard, and then activates a building space — activation must wait for all shards.
- The embedding page shows `done`, `total` and the time of the last exact count.
- The rate limit defaults stay at 5 requests per second and concurrency 4 — the right values depend on the Atlassian tenant, and the listing change already removes most requests.

## Out
- Changes to Jira, GitHub or Azure DevOps fetching.
- A larger embed batch than 64.
- Splitting the extraction or resolve work.
- A second datastore or queue system.

## How I know it works
- A first sync of a Confluence space with 1,000 pages sends about 20 content requests, not about 1,010.
- Stop the process during a first sync of a large space, then start it again. The next run lists from the saved cursor, not from the start.
- Change a label on a Confluence page, then sync. The chunk ids of the item stay the same, and the embedding `done` count does not drop.
- Edit one section of a page with 10 chunks, then sync. One chunk gets a new id and a new vector, and the other 9 keep their ids.
- Two items with the same passage text get one model call for that chunk text.
- With `EmbedWorkers` set to 4, `SELECT count(*) FROM river_job WHERE kind = 'embed' AND state = 'running'` returns 4 while chunks are pending, and no chunk gets two vector rows.
- A building space becomes active only after every shard finds no pending chunks.
- `just bench` shows that the embed loop sends no `count(*)` query between batches.
- `just ci` passes.
