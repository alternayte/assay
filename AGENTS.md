# AGENTS.md

## What this is
Assay is a self-hosted company intelligence platform that connects knowledge from business and technical systems and links each fact to its source passage.
People use the web app, and AI agents use MCP and REST with the permissions of their user.

## Run
No code exists yet. Add the run commands here when the justfile exists.

## Test
No code exists yet. The gate will be the just ci recipe.

## Stack rules
- The install target is Docker Compose on a laptop, not Kubernetes.
- One Go binary serves the API, the embedded SPA, MCP and the workers. A role flag on serve selects which parts run.
- PostgreSQL with pgvector, pg_trgm and unaccent is the only datastore. Jobs run on River. No Redis, queue, search engine or blob store.
- Each feature owns its SQL in its own sqlc query file. No shared repository layer.
- Sign-in and bearer tokens come from github.com/alternayte/auth-all. When it lacks a capability, open an upstream issue. Do not fork it or build a parallel system.
- Model calls use OpenAI-compatible HTTP and the OpenRouter Decisions API. No AI framework. Tests call only the fake endpoint.
- Connectors are read-only. The connector HTTP client sends only GET and HEAD.
- The repository is public. Code, fixtures, tests and commit messages use only the synthetic Acme corpus.
- Dependencies are pinned and use MIT-compatible licences only.
- UI copy follows ASD-STE100.

## Domain words
- connection: one configured source system with a token, a scope and a visibility.
- visible set: the connections a user owns or that are shared with one of the user's groups. Every content query filters by it.
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
