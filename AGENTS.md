# AGENTS.md

## What this is
Assay is a self-hosted company intelligence platform that connects knowledge from business and technical systems and links each fact to its source passage.
People use the web app, and AI agents use MCP and REST with the permissions of their user.

## Run
- `just setup` installs the pinned tools into bin/ and the web packages.
- `just dev` builds the web app and runs `go run ./cmd/assay serve`. It needs ASSAY_DATABASE_URL and ASSAY_SECRET_KEY.
- `go run ./cmd/assay help` lists the operator commands.
- `sh scripts/install.sh` installs the Compose stack. ASSAY_IMAGE selects a local image.

## Test
- `just test` runs the unit and integration tests against PostgreSQL in testcontainers. Docker must run.
- `just e2e`, `just smoke` and `just bench` run the browser, install and benchmark layers.
- `just contract` and `just bench` select tests from files with the build tag contract or bench.
- `just gen` regenerates sqlc code, the OpenAPI document and the TypeScript client after a query or route change.
- `just ci` is the gate. It writes reports to tmp/reports, or to the private build directory when it exists.

## Stack rules
- The install target is Docker Compose on a laptop, not Kubernetes.
- One Go binary serves the API, the embedded SPA, MCP and the workers. ASSAY_ROLE selects which parts run.
- PostgreSQL with pgvector, pg_trgm and unaccent is the only datastore. Jobs run on River. No Redis, queue, search engine or blob store.
- Each feature owns its SQL in `internal/<feature>/queries.sql`. Add the file to `sqlc.yaml`. No shared repository layer.
- Migrations live in `internal/app/migrations`. auth-all applies its own units from the library at start.
- Users, sessions, roles and bearer tokens come from github.com/alternayte/auth-all. When it lacks a capability, open an upstream issue. Do not fork it or build a parallel system.
- Only the auth-all routes in the allowlist of `internal/app/http.go` are served.
- Routes use huma and return the error envelope of `internal/api`. Role checks use api.Require.
- The SPA calls the API only through the generated client in `web/src/api/client.ts`.
- Every scenario test name carries its scenario ID: TestSCN_AREA_NNN_words in Go, "SCN-AREA-NNN words" in Playwright.
- Integration tests get a fresh migrated database from testkit.DB or testkit.Start.
- Model calls use OpenAI-compatible HTTP and the OpenRouter Decisions API. No AI framework. Tests call only the fake endpoint.
- Connectors are read-only. The connector HTTP client sends only GET and HEAD.
- The repository is public. Code, fixtures, tests and commit messages use only the synthetic Acme corpus.
- Dependencies are pinned and use MIT-compatible licences only.
- All UI copy lives in `web/src/copy/en.json` and follows ASD-STE100.
- Every UI value comes from the tokens in `web/src/styles.css`. Follow its direction sentence.

## Domain words
- connection: one configured source system with a token, a scope and a visibility.
- visible set: the connections a user owns or that are shared with one of the user's groups. Every content query filters by it.
- Everyone: the built-in group that holds all active users. It has no member rows.
- registry: the built-in connection where members write records for things and terms.
- item: one source object, such as a page, an issue or a file. Each content change is a new version.
- passage: one block of an item. Its identity is the hash of its normalized text.
- chunk: a run of passages inside one top-level section, used for search.
- entity: a thing with a provider-qualified key and aliases.
- term: an entity of type term. Synonyms and acronyms are its aliases.
- claim: a subject, a predicate and an object, backed by evidence rows.
- evidence: a link from a claim to a passage quote. It is current or stale.
- tombstone: the delete of an item that no longer exists in its source.
- decision: a typed model call that returns probabilities, not text.
- band: the confidence level of a decision, calibrated from reviewer outcomes.
- auto-apply: acting on High decisions without review, enabled per decision kind once precision is earned.
- card: one item in the review queue.
- page: a knowledge page for one entity, cached per visibility key.
