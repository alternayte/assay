# Retrieval eval

## What it does
`assay eval --retrieval` scores search alone, with no answer model. For each labelled query, it calls the MCP tool `search_context` on a running instance and compares the ranked items with the expected items. The report gives MRR, Hit@1, Hit@3, recall@5, recall@20 and nDCG@10, with one row per query. A run makes no model calls other than the query embedding and, when the install enables it, the rerank call.

## Decisions
- The eval calls `search_context` over MCP with the Go SDK client and an access token — it measures what agents and Ask get from `Ask.Retrieve`, not the item search of `GET /api/v1/search`.
- The query file is the existing YAML. A question gets an optional field `expected: [{source: <pattern>, grade: 1-3}]`. `source` matches the title or the URL of an item, as `sources` does now — one file serves both evals.
- `Parse` accepts `expected` next to the current fields. `--retrieval` uses only questions with `expected` and skips the others. The answer eval ignores `expected`.
- Grade 3 is the item that answers the query, 2 is an item that helps, 1 is an item that is related. Hit, MRR and recall count grade 2 or more — a related item is not a hit.
- The eval converts chunk results to an item ranking: the first time an item shows is its rank — several chunks of one item count once.
- nDCG@10 uses the grades as gains with `log2(rank + 1)` discount — this is the only metric that uses all grades.
- Each query sends `limit: 20`, `kinds: ["results"]` and `max_tokens: 50000` — the eval scores the ranking, so the output cut must not drop results.
- The report header names the embedding model and whether vector search or rerank ran, from the `degraded` state and the models settings — a score is comparable only with the same pipeline.
- The Markdown report follows the table layout of the answer report: one summary table, then one row per query with its first expected rank.
- `just eval-retrieval` runs it against `ASSAY_EVAL_URL` with `ASSAY_EVAL_TOKEN` and `ASSAY_EVAL_FILE`, and writes `retrieval.md` to the reports directory.
- `evals/questions.yaml` gets `expected` on the Acme questions that have sources, from the same patterns with grade 3, plus grade 2 items where the corpus has them.
- A unit test scores a fixed set of rankings with known MRR, Hit@k, recall@k and nDCG values — the metric code is the part a person cannot check by hand.
- An integration test runs the retrieval eval against testkit with the fake model endpoint and checks that every Acme query with `expected` gets a row.

## Out
- A pass or fail threshold in `just ci`.
- Tuning the search weights. This spec only measures them.
- Labelling real customer queries in the repository.

## How I know it works
- `just eval-retrieval` against the demo install writes `retrieval.md` with the six metrics and one row per labelled query.
- Two runs on the same install and data give the same numbers.
- Change `RRFK` and run again. The numbers change, and the report shows which queries moved.
- A query whose expected item is not in the first 20 items shows rank "none" and counts as a miss.
