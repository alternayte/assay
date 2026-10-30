# Models

Assay calls models through OpenAI-compatible HTTP endpoints. The default endpoint kind is OpenRouter. Admins add endpoints in Settings, Models, and give each model function its own endpoint, model, parameters, prices, monthly budget and an enabled flag. Every function is off until an admin turns it on.

| Function | Use | Timeout |
|---|---|---|
| `answer` | Answers in Ask | 60 s |
| `extract` | Claims and term drafts from passages | 60 s |
| `compile` | The summaries of knowledge pages | 60 s |
| `embed` | Vectors of chunks and entity profiles | 30 s |
| `rerank` | Optional rerank of search results | 15 s |
| `decide` | Typed decisions with probabilities | 15 s |

Each call retries twice on 429 and 5xx.

## Endpoints

An endpoint has a name, a kind, a base URL, a key, extra headers and body defaults. Assay stores the key encrypted. "Test" lists the models of the endpoint and makes one minimal call.

## Budgets and cost

Each call records its tokens and its cost from the usage of the response and the prices of the function. Settings, Models shows the spend of the month per function. Assay refuses a call before it sends it when the spend of the month plus the estimate is above the budget. Admins then see a banner.

## Embeddings

A change of the embed model builds a new vector space in the background. The UI shows the progress. When the space is complete, Assay switches to it in one transaction and drops the old one. A query never mixes two spaces.

## Decisions and bands

`decide` asks typed questions, such as "are these two entities the same?", and gets a probability for each option. It refuses an input above 1,500 tokens. Its adapters are the OpenRouter Decisions API and a fallback that asks a chat model for JSON.

Each decision gets a band: High, Medium or Low. After 50 reviewed outcomes of a kind, the bands use the measured precision of the reviewers. An admin can turn on auto-apply for a kind when its High precision is 0.95 or more. Assay turns auto-apply off again when the precision drops.

## Without models

Assay works with every function off:

- Without `embed`, search uses full-text and trigram matches.
- Without `answer`, Ask shows the results and a notice.
- Without `compile`, pages show only the sections that come from claims.
- Without `decide`, resolution creates review cards with no band.

## Evaluation

`assay eval` asks a question set with an access token and writes a Markdown report: citation precision and recall, fact recall, no-evidence accuracy, latency and cost.

```sh
assay eval --url http://localhost:8080 --token assay_... --file evals/questions.yaml --out report.md
```

`evals/questions.yaml` holds the Acme questions for the demo. `just eval` runs the set against the instance in `ASSAY_EVAL_URL` with the token in `ASSAY_EVAL_TOKEN`.
