# get_item MCP tool

## What it does
An agent reads the passages of one visible item with the MCP tool `get_item`. The agent names the item by its ID, from a search result or an evidence row, or by its URL, from a footnote of a knowledge page or a design page. The agent can ask for one section, for a line range of a code file, or for the next page of a long item. The output has the item, its entity key, and its passages in `excerpt` fields with the notice. A hidden item and a missing item give the same error.

## Decisions
- The tool name is `get_item`, with the `read` scope — it follows the `get_entity` naming, and reading needs no other power (D-207).
- Input: `item_id` or `url`, exactly one, plus `section?`, `lines?`, `from?` and `max_tokens?` — two inputs or none give the user error "Give item_id or url." because no item is named otherwise.
- `url` matches `source_items.url` exactly after the input is trimmed, inside the visible set — footnotes print the stored URL, so no normalization is necessary.
- When two visible items have the same URL, the tool returns the item with the newest `source_updated_at`, then the lowest ID — one URL can be in two connections, and the newest copy is the best source.
- A new migration adds an index on `source_items (url)` — the URL lookup otherwise scans all items.
- `section` is a heading path, and the tool returns the passages whose heading path starts with it — search results carry `headingPath`, so an agent opens the section of a hit.
- `lines` is `{start, end}`, and the tool returns the passages whose line range overlaps it — design page footnotes cite a file and a line range. On an item with no line ranges, `lines` gives the user error "The item has no line ranges."
- `from` is a passage ordinal, and the tool returns passages with an ordinal of `from` or more — this pages through an item larger than the output limit.
- The tool adds whole passages in ordinal order until the next passage does not fit `max_tokens` (default 4,000). It then sets `truncated: true` and `next` to the ordinal of the first passage left out — an agent calls again with `from: next` and misses no passage.
- When the first passage alone does not fit, the tool shortens its excerpt with `shorten` and sets `truncated` and `next` — the agent always gets some text and a way forward.
- Output: `notice`, `item` (`itemId`, `title`, `url`, `kind`, `sourceType`, `connection`, `author`, `updatedAt`, `entity` as a Ref or null), `passages` (`ordinal`, `kind`, `headingPath`, `startLine?`, `endLine?`, `excerpt`), `truncated`, `next?` — passage text is only in `excerpt`, as REQ-AGT-004 requires.
- The output gives `entity` from the item entity — the agent then calls `get_entity` with no second search.
- The tool reads the current version only — passages of older versions are not stored per item.
- The tool reuses `GetVisibleItem` and `VisiblePassages` of `internal/content` and adds a URL query and the line columns — the REST route `getItem` and the tool then read the same rows.
- A paused connection's item stays readable, and a tombstoned item gives the not-found error — this matches the visible set.
- Each call writes an audit entry with the input, through `add` — REQ-AGT-001.
- REQ-AGT-002 and the tool table in `.build/SDD.md` §17 add `get_item`. SCN-AGT-001 changes to nine tools. `docs/mcp.md` adds a row and the paging rule.

## Out
- A server `ask` tool on MCP.
- The OpenAI `search` and `fetch` tool names.
- Passages of an older version of an item.
- Changes to the REST route `getItem` or to footnote text.
- Item IDs in knowledge page or design page Markdown.

## How I know it works
- An MCP client lists nine tools, and `get_item` has the schema above.
- `get_item` with the `itemId` of a `search_context` result returns the passages of that item in ordinal order, with the notice.
- `get_item` with the URL of a footnote from `get_design_page` and its `lines` returns the passages of that line range.
- `get_item` with `section` set to the `headingPath` of a result returns only passages of that section.
- On an item larger than 4,000 tokens, calls with `from: next` until `truncated` is false return every passage once.
- `get_item` with the ID of an item in a connection outside the token user's visible set returns "The object does not exist.", the same as a missing ID.
- `EXPLAIN` of the URL lookup shows the new index.
