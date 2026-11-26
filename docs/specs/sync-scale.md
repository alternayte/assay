# Sync scale and the Helm chart

## What it does
A sync run splits into parts, one per scope unit, such as a space, a project, a repository or a channel. Parts run as separate River jobs, so any worker process can run them. The connection's rate limit holds across all processes. Compose runs web and worker as separate services, and `docker compose up --scale worker=N` adds sync capacity. A Helm chart installs the same image on Kubernetes, with a web Deployment and a worker Deployment that can scale on queue depth.

## Decisions
- A connector gets `Parts(ctx) ([]Part, error)`, and `Changed` and `ListIDs` take a part — the scope unit is the largest piece of a sync that needs no shared state. A connector with one unit, such as Backstage, returns one part.
- Part keys: Confluence space key, Jira project key, repository full name, Teams channel ID, Slack channel ID, GitLab project path, web seed URL — each is stable in its source.
- A new table `sync_parts (run_id, part, status, cursor, stats, error)` holds one row per part of a run. The connection keeps a cursor per part in `connection_cursors (connection_id, part, cursor)` — a slow space no longer holds back the cursor of a fast one.
- The run job lists the parts, writes the rows, inserts one `sync_part` job per part, and ends. The last part to finish sets the run status and the summed stats in one transaction with `FOR UPDATE` on the run — the run page keeps its present shape.
- A part that fails marks the run failed and keeps the cursors of the other parts — their progress is not lost.
- `source_items.part` records the part of each item. Reconcile of a part tombstones only items of that part — an item that moves to another space is stored by the other part and is not deleted.
- The connection setting `parallel_parts` (default 4, 1 to 32) caps the part jobs of one connection that run at once, across all processes. The cap is enforced by a count of running rows in `sync_parts` under an advisory transaction lock on the connection — River has no per-key concurrency limit.
- The per-second rate of a connection becomes a Postgres reservation: each request runs `UPDATE connection_rates SET next_at = greatest(next_at, now()) + interval RETURNING next_at` and waits until the reserved slot — N processes then send at most the configured rate in total. One round trip per request is small next to the request to the source.
- A 429 or 503 with Retry-After moves `next_at` of the connection to the Retry-After time — every process then backs off, not only the one that got the answer.
- Concurrency stays per part job — the rate reservation bounds the total load, and in-flight requests are at most `parallel_parts × concurrency`.
- Pause cancels the run job and all part jobs of the connection. `FailStaleRuns` also fails stale part rows.
- The `sync` queue size per process stays `ASSAY_SYNC_WORKERS` (default 4) and now counts part jobs — more worker processes give more parallel parts.
- `ASSAY_EMBED_SHARDS` (default 4) sets the number of embed shards for the whole cluster, separate from `ASSAY_EMBED_WORKERS` per process — today the shard count equals the per-process worker count, so more processes add no embed parallelism.
- Resolve stays one job for the cluster — it matches descriptions across connections, and two resolvers at once create duplicate unnamed entities.
- Compose: the `assay` service runs `ASSAY_ROLE=web` and keeps its port. A new `worker` service runs `ASSAY_ROLE=worker` with the same image and env file, without a port — existing installs keep their service name, and `install.sh` upgrades need no manual step.
- Helm chart in `deploy/helm/assay`, in the shape of the skael chart: web Deployment, Service, optional Ingress, worker Deployment, Secrets for `ASSAY_DATABASE_URL` and `ASSAY_SECRET_KEY` (existing Secret or generated and kept on upgrade), and `env` overrides that lock Settings fields.
- The chart needs an external PostgreSQL with pgvector, pg_trgm and unaccent — a database in the chart is out of scope for production and the stack rule allows only PostgreSQL.
- Worker autoscaling is an optional KEDA ScaledObject with the PostgreSQL scaler on `river_job` rows in state `available` on the `sync`, `embed` and `extract` queues — queue depth is the right signal, and KEDA needs no metrics adapter. Off by default.
- The chart version follows the release version. `just ci` runs `helm lint` and `helm template`. CI installs the chart in kind and runs the smoke check — a chart that does not install is a release blocker.
- AGENTS.md changes the install line to: "The install targets are Docker Compose on a laptop and the Helm chart in deploy/helm/assay." This needs the user's acceptance through agents-audit.

## Out
- Splitting one part across processes — one scope unit has one cursor.
- A distributed concurrency semaphore — the rate reservation bounds load.
- Parallel resolve.
- A PostgreSQL subchart or operator.

## How I know it works
- A Confluence connection with 3 spaces syncs with 3 part rows. With 2 worker containers, the run page shows parts on both hosts, and the run ends succeeded with the summed counts.
- With the rate at 5 per second and 3 workers, the fixture server log shows at most 5 requests per second for the connection.
- The fixture server returns 429 with Retry-After 10 once. No process sends a request to that connection for 10 seconds.
- Pause during a 3-part run cancels all part jobs, and the run ends failed with "An admin paused the connection."
- `docker compose up -d --scale worker=3` runs 3 workers and one web container. An upgrade of an existing install keeps its data and starts the worker service.
- `helm install` in kind with an external pgvector database serves `/healthz`. With KEDA enabled, queued sync parts scale the worker Deployment up.
