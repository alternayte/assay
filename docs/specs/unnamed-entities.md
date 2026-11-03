# Unnamed entities

## What it does
Resolve makes an unnamed entity only for a description that names one thing, in a claim role that has specific entity types. The same description in one connection makes one unnamed entity and one card, not one per passage. A reviewer sets the name and the type when they accept the card. Explore shows only active entities by default. An item with an empty title gets a title that people can read.

## Decisions
- Resolve does not make an unnamed entity for a role that allows any entity type: the subject of `stated` and the object of `relates_to`. The describe step still runs for these roles and can still link to an existing entity — `types[0]` of all entity types is `person`, and these roles produce most of the person entities that have sentence fragments as names.
- A description goes to describe and unnamed only if it passes these rules, which resolve checks before any model call: at most 10 words, no `.`, `!`, `?`, `;` or `:`, and at least 2 words left after the leading determiners (the, a, an, this, that, these, those, our, their, its, his, her, my, your) are removed. A description that fails the rules is dropped, with no card — a sentence fragment or "the team" cannot name one thing, and the rules cost nothing.
- The key of an unnamed entity is `registry:unnamed:<hash of connection id, type and normalized description>`, not the hash of the mention target — the same description in 50 passages then makes one entity.
- A mention whose description matches an existing unnamed key adds its claim to that entity, and makes no card. This applies when the entity is proposed, and also when it is active after an accept.
- A mention whose unnamed key has a rejected unnamed card is dropped — a reviewer said no once, so resolve does not ask again.
- The default type of an unnamed entity stays the first allowed type of the role.
- Accepting an unnamed card takes an optional type, which must be one of the types that the claim role allows. The review card shows a type select with those types, and the default type selected. `acceptUnnamed` sets the type and keeps the key.
- Explore lists active entities when the status filter is empty. The "Proposed" filter value still lists proposed entities — proposed entities have no review yet and should not fill the default list.
- Each resolve run cleans up open unnamed cards against these rules. When a card's mention fails a rule, resolve deletes the proposed entity with its claims and closes the card as rejected with the reason "The description does not name one thing." When open cards have the same new key, resolve keeps the oldest entity and its card, moves the claims of the others onto it, and closes the other cards as rejected with the reason "Same description as card <id>." — the cards that already exist get the same rules as new ones.
- The cleanup closes cards without a reviewer, and skips band calibration — unnamed cards have no decision, so calibration never reads them.
- Active unnamed entities stay as they are — a reviewer accepted them.
- The extract prompt and schema stay the same — a change of either changes the extractor version and extracts every passage again.
- `content.Store` sets a title for an item with an empty title: the first heading or first line of the body, cut to 80 characters. If the body is also empty, the title is `<kind> <external id>` — every reader, including MCP, page markdown and search, then shows the same title.

## Out
- Renaming, retyping or deleting an active entity outside a card. A separate spec covers entity editing.
- Dedupe of mention_link, merge and other cards. A separate spec covers review dedupe.
- Changing how name mentions resolve. An unresolved name already makes no entity.
- A model call to choose the type of an unnamed entity.

## How I know it works
- A passage with "the reminder service stated that it retries three times" gives no proposed person entity.
- The description "the service that sends reminder emails" in 3 passages of one Confluence connection, as the object of `depends_on`, gives one proposed service entity with 3 claims and one unnamed card.
- The description "the team" in an `owned_by` object gives no entity and no card.
- Reject that unnamed card, then sync a new passage with the same description. No new card appears.
- Accept an unnamed card with the type `api`. The entity page shows the type api and the given name.
- After one resolve run on an existing database, the open unnamed cards for `stated` subjects are closed as rejected, and each set of open cards with the same description in one connection becomes one card.
- Explore with no filter shows no proposed entities. Explore with the Proposed filter shows them.
- A Confluence item stored with an empty title shows its first heading as its title on the entity page source list.
- `just ci` passes.
