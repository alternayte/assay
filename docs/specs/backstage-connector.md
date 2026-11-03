# Backstage connector

## What it does
An admin can add a Backstage connection with a backend URL and a static token. Assay reads the software catalog through the Backstage catalog API with GET requests only. Each catalog entity becomes an item with the same entities and facts as a `catalog-info.yaml` file in a repository. The `spec.definition` of each API entity goes through the contracts parsers, so its operations, events and data models become entities. Entities removed from the catalog become tombstones.

## Decisions
- The connection kind is `backstage`. The migration adds it to the connections kind check. `sync.Kinds` and the connections page list it as "Backstage".
- The base URL is the Backstage backend. The token is a Backstage static external access token, sent as `Bearer` — Backstage uses this form for service access, and the client already sends it.
- Option `frontendUrl` sets the item links `<frontendUrl>/catalog/<namespace>/<kind>/<name>`. Its default is the base URL — the backend and the frontend share one origin in a default deploy, but not in every deploy.
- `Changed` reads `GET /api/catalog/entities/by-query` with a filter on the kinds Component, API, System, Group, User, Resource and Domain, with `limit=500`, and with paging by `pageInfo.nextCursor`. It returns full items in `Ref.Item`, and `metadata.etag` is the revision — the catalog has no updated-since filter, so the revision skip finds the changed entities, as for repository trees.
- `ListIDs` reads the same query with `fields=kind,metadata.namespace,metadata.name`.
- `Fetch` reads `GET /api/catalog/entities/by-name/{kind}/{namespace}/{name}`.
- `Validate` reads one entity and reports `totalItems`.
- `Scopes` lists the namespaces from `GET /api/catalog/entity-facets?facet=metadata.namespace`. An empty scope means all namespaces.
- The external id is the entity ref `<kind>:<namespace>/<name>`, not `metadata.uid` — a uid changes when an entity is registered again, and the ref is what other entities point at.
- The item kind is `catalog_entity`. The title is `metadata.title`, or else `metadata.name`. The body is the entity as YAML in a fenced block, split as a code file — the quotes of the facts are then lines of the passages, as in a repository catalog file.
- The connector calls `catalog.Facts` and `catalog.Entities` on that YAML. The facts have source `catalog`, and the keys stay `backstage:<kind>:<namespace>/<name>` — a repository catalog file and the Backstage connection then meet on the same entities.
- `catalog` gains the User kind, as a `person` entity named by `spec.profile.displayName`, with no facts — owners such as `user:jdoe` then get a readable name. Email addresses are not stored.
- `catalog.Facts` reads `spec.type` and `spec.definition` of API documents. A definition whose type is `openapi`, `asyncapi`, `graphql` or `grpc` goes to the matching contracts parser. The node lines are offset by the line where the definition starts in the YAML — every contract fact then quotes a line of its own item.
- This also applies to an inline definition in a repository `catalog-info.yaml`. A `$text` or `$ref` placeholder in a repository file is not followed, because the referenced file is parsed on its own when the globs include it.
- The Backstage catalog API returns definitions with their placeholders already resolved, so the connector sends no extra request for them.
- The contract keys of a definition use the repository key that the API entity's annotations name, through `RepositoryOf` and `github.com/project-slug`. They meet the contracts of the same repository from code. Without a linked repository, they use the API entity key.
- The operations of a definition attach to the Backstage API entity with `has_operation`. No separate `api:<repo>:<path>` entity is made for them.
- Location, Template and other kinds are ignored.
- The connector sends only GET requests through the existing connector client, with the connection's rate limits — connectors are read-only.
- The test fixtures use an Acme catalog. The contract tests use a fake Backstage server.

## Out
- Group membership (`spec.memberOf`, `spec.members`) as claims.
- TechDocs content.
- Backstage permissions or per-entity visibility. The connection visibility applies to all its items.
- Writing to Backstage.

## How I know it works
- Add a Backstage connection to the fake server with an Acme catalog. The run history shows one item per Component, API, System, Group, User, Resource and Domain entity.
- The entity `backstage:component:default/payments-api` shows `owned_by`, `part_of`, `provides_api` and `depends_on` with source catalog. Each fact opens the YAML line that states it.
- The same component in a repository `catalog-info.yaml` and in Backstage is one entity with evidence from both connections.
- An API entity with an inline OpenAPI definition gives operations with `has_operation`, and each operation quotes its line in the catalog item.
- Remove an entity from the fake catalog and reconcile. Its item becomes a tombstone.
- Sync twice with no catalog change. The second run reports every item as unchanged.
- The fake server records only GET requests.
- `just ci` passes.
