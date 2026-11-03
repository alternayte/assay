# Contracts from code

## What it does
Assay finds the APIs, operations, events and data models of each indexed repository. It parses spec files that exist in the repository, and it infers contracts from code that has no spec file. Each contract becomes an entity with claims, and each claim links to the passage that states it. Facts from spec files outrank inferred facts. Design pages and entity pages show the contracts, and agents read them through the existing entity and search tools.

## Decisions
- New entity types:
  - `operation`: one HTTP method and path, one RPC method, or one GraphQL query, mutation or subscription.
  - `event`: one message on one channel.
  - `data_model`: one schema, message, type or table.
  - The migration adds them to the entities type check. They go at the end of `claims.EntityTypes`.
- New predicates:
  - `has_operation` (api to operation, multi)
  - `request` (operation to data_model)
  - `response` (operation to data_model, multi)
  - `publishes` and `subscribes_to` (service or repository to event, multi)
  - `payload` (event to data_model)
  - `channel` (event to text)
  - `has_field` (data_model to text `name: type`, with `required` when the source says so, multi)
  - Priority: registry, repository, catalog, others, decision.
- Each predicate gets the flag `Extract`. The new predicates have `Extract: false`. The extract schema and `extract.Version()` use only predicates with `Extract: true` — adding a predicate otherwise changes the extractor version and extracts every passage again.
- These are the keys, scoped to the repository entity so that a spec file and inference of the same repository meet on one entity:
  - `operation:<repo key>:<METHOD> <path>`, where path parameters are normalized to `{}`.
  - `operation:<repo key>:<package.Service>/<Method>` for gRPC.
  - `operation:<repo key>:graphql:<root>.<field>` for GraphQL.
  - `event:<repo key>:<channel>:<message>`.
  - `data_model:<repo key>:<qualified name>`.
  - A spec api is `api:<repo key>:<file path>`, with its document title as name.
- Parsed formats: OpenAPI 3, Swagger 2, AsyncAPI 2 and 3, JSON Schema, and Avro, through `yaml.v3` node trees. Protobuf goes through `bufbuild/protocompile`, and GraphQL SDL through `vektah/gqlparser` — each parser gives a line for each node, and the line selects the evidence passage.
- Each parsed fact is a `content.Fact` with source `repository`. Its quote is the source line of the node, cut to 300 characters. It is written through the existing item facts path, the same way as catalog-info.yaml — a new version of the file retracts facts that it no longer states.
- Only local `$ref` and same-file references are followed. An unresolved reference gives a `data_model` with the name of the reference and no fields — a remote fetch would break the rule that connectors send only to their own source.
- The default include globs add:
  - `**/swagger*.{yaml,yml,json}`
  - `**/asyncapi*.{yaml,yml,json}`
  - `**/*.{graphql,gql}`
  - `**/*.avsc`
  - `**/*.schema.json`
- A YAML or JSON file already in the include globs is also parsed when its top-level key is `openapi`, `swagger` or `asyncapi` — many repositories name the file `api.yaml`.
- A parsed file gives at most 2,000 operations, 2,000 events and 2,000 data models, and at most 500 fields per model. A file past a limit is parsed up to the limit and logs a skip reason.
- The spec api gets `provides_api` from its repository. AsyncAPI v2 `subscribe` and v3 `send` operations of the document's own application give `publishes`. AsyncAPI v2 `publish` and v3 `receive` operations give `subscribes_to`.
- Inference runs inside design builds. The digest answer of each component gains `operations`, `events` and `models`. Each one has a passage marker and a quote — the digest call already reads the component's code, so inference costs no extra model call.
- An inferred contract is kept only when its quote is in the marked passage, as in `extract.check`. It is written as a claim with source `decision` and the passage as evidence — every fact still links to its source passage.
- The digest prompt asks for HTTP routes, RPC handlers, GraphQL resolvers, message producers and consumers with their topic or queue, ORM models, and tables in SQL migrations. It asks for nothing that the code does not state.
- Inferred operations attach to the spec api of the repository for the same protocol when one exists. Otherwise they attach to `api:<repo key>:inferred:<http|grpc|graphql>`, named "<repository> <protocol> API", which gets `provides_api` from the repository.
- Inferred claims need no review card — prose extraction makes evidence-backed claims without cards, and spec-file facts outrank inferred facts when they conflict.
- Each design page gets a fixed section "Contracts". It lists the APIs, operations, events and data models of the repository from the claims, and cites each one. It is built from claims without a model, and it is never a pinned section.
- Entity pages of the new types use the existing entity page. Explore lists the new types. The type names go in `web/src/copy/en.json`.

## Out
- Exposing the new predicates to prose extraction from Confluence and Jira.
- Writing OpenAPI or AsyncAPI files out of Assay.
- Linking a call in one repository to an operation of another repository. The existing edge proposals cover dependencies between repositories.
- The Backstage connector and `spec.definition`. A separate spec covers them.

## How I know it works
- A repository with `openapi.yaml` that declares `POST /payments` gives an api entity, an operation "POST /payments" with `has_operation`, and request and response data models with fields. Each fact opens the YAML passage that states it.
- A repository with only a Go handler for `POST /payments` gives the same operation key after a design build, with source `decision`.
- When both exist, the operation is one entity with both evidences. The entity page shows the spec value when they differ.
- An AsyncAPI file with a `PaymentCreated` message on `payments.events` gives an event with `channel` "payments.events", a payload data model, and `publishes` from the repository.
- A `.proto` file with `service Ledger { rpc Post(...) }` gives `operation:<repo>:acme.Ledger/Post`.
- Removing an operation from the spec file and syncing retracts its spec facts.
- `extract.Version()` is the same before and after this change.
- The design page of the repository shows the Contracts section with cites.
- `just ci` passes.
