# Entity editing

## What it does
From an entity page, a person can rename the entity, add and remove aliases, change its type, and delete an entity that no source system owns. A reviewer's edit applies at once. A member's edit becomes an `entity_edit` card for a reviewer. Edits stay in place when a sync runs again. An edit never makes an entity or a name visible to someone who cannot see the entity's sources.

## Decisions
- Reviewers apply edits at once. Members get an `entity_edit` card that holds the requested change — this is the same rule as merge.
- Routes:
  - `PUT /api/v1/entities/{key}/name`
  - `POST /api/v1/entities/{key}/aliases`
  - `DELETE /api/v1/entities/{key}/aliases/{aliasId}`
  - `PUT /api/v1/entities/{key}/type`
  - `DELETE /api/v1/entities/{key}`
  - They use huma, the error envelope and api.Require, and each one writes an audit entry.
- A rename writes an `is_name` alias with `edited = true` in each source connection of the entity, not in the registry connection — a registry alias is visible to everyone, so it would leak the entity into name lookup and extraction context for users who cannot see its sources.
- `entity_aliases` gets the column `edited boolean NOT NULL DEFAULT false`. `entity_display_name` ranks an edited name first, before the registry name — the person who edited the entity chose that name for it.
- A rename never adds an `entity_sources` row — a source row would change who can see the entity.
- An added alias is written with `edited = true` in each source connection of the entity, for the same reason as a rename.
- Removing an alias deletes the row and adds a row to `alias_suppressions (entity_id, alias_norm, by, at)`. The connector alias upsert and `AddMentionAlias` skip suppressed aliases — without this, the next sync or mention accept adds the alias again.
- A user cannot remove the last name of an entity. They rename it instead.
- `entities` gets the column `type_edited_at timestamptz`. `UpsertEntity` keeps the type of an entity whose `type_edited_at` is set — the connector upsert otherwise overwrites the type on each sync.
- A type change is refused when a current claim of the entity uses a predicate that does not take the new type. The 409 lists those predicates — retracting claims without a person checking them destroys evidence.
- A type change to or from `term` or `repository` is refused — terms belong to the glossary and registry flow, and repositories belong to design pages and connectors.
- Delete applies only to entities that have no source in a connector connection and no registry record, such as active unnamed entities. For any other entity, the route returns 409 with the reason "A source system owns this entity." — a sync would make the entity again, and a registry record has its own revert.
- Delete removes the entity, its aliases and the claims where it is the subject or the object, together with their evidence. It closes its open cards with "The entity was deleted.", and writes its key to `entity_deletions (key, by, at)` — the deletion is permanent and needs a clear trace.
- Resolve does not make an unnamed entity whose key is in `entity_deletions` — this prevents the same mention from making it again.
- A merge loser can have its aliases removed in the same way, on the winner's page — the bad aliases of a merged entity then stop linking wrong mentions.
- Each edit marks the cached pages of the entity as stale, so the next open of the page compiles it again.
- The entity page gets an Edit control for members and reviewers: name, alias list with add and remove, and a type select that lists only allowed types. Delete is a separate action with a confirmation, shown only when delete applies.
- An entity whose key starts with `registry:` and is not an unnamed key opens the existing registry form, not this one — the registry form is already the editor for registry records.
- All copy goes in `web/src/copy/en.json`. Run `just gen` after the route change.

## Out
- Editing claims. The report-a-problem flow covers this.
- Editing the domain of an entity.
- MCP tools for entity edits.
- Undo, except by editing again.

## How I know it works
- As a reviewer, rename an unnamed entity. Its page, Explore and search show the new name, and a sync of its connection does not change the name.
- As a member, rename an entity. An entity_edit card appears, and the name changes only after a reviewer accepts the card.
- Remove a Jira alias from an entity, then sync Jira. The alias does not come back.
- Change a Jira-sourced entity from `person` to `team`, then sync Jira. The type stays `team`.
- Change a type when an `assigned_to` claim needs `person`. The API returns 409 and names `assigned_to`.
- Delete an active unnamed entity. Its claims are gone, and a new passage with the same description makes no new entity.
- Delete a Jira issue entity. The API returns 409 with "A source system owns this entity."
- A user who cannot see an entity's connection does not find the new name in search, Explore or the extraction context of their own passages.
- `just ci` passes.
