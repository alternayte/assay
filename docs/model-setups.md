# Model setups

This page gives three example setups for the model functions: a mixed setup, an OpenAI setup and an Anthropic setup. [models.md](models.md) explains the functions, endpoints, budgets and bands.

Model names and prices change often. Check the model list of your provider before you copy a model ID. The prices on this page are from November 2026.

## Before you start

- Add one endpoint for each provider in Settings, Models. Then select "Test" on each endpoint.
- Write the parameters of a function as JSON in the parameters field of the function.
- Assay sets a small output limit for some calls: 200 tokens for `decide`, 400 for `compile`, 600 for `answer` and 1,500 for `extract`. A reasoning model uses the same limit for its reasoning tokens. Turn reasoning down or off for these functions. Otherwise an answer can be empty.
- Keep the embedding size at 2,000 dimensions or less. The vector index of Assay accepts no larger vectors.
- A change of the `embed` model builds all vectors again. Select the embed model first and keep it.
- The default budget is 5 USD per function per month. `extract` and `decide` make the most calls. Raise their budgets before a large sync. The extraction estimate on a connection shows the cost before you start.
- `rerank` is optional. Search works without it.

## Endpoints

| Provider | Kind | Base URL | Notes |
|---|---|---|---|
| OpenAI | OpenAI-compatible | `https://api.openai.com/v1` | |
| Anthropic | OpenAI-compatible | `https://api.anthropic.com/v1` | Anthropic calls this the OpenAI SDK compatibility layer. A key that is not scoped to a workspace also needs the header `anthropic-workspace-id` in the extra headers. |
| OpenRouter | OpenAI-compatible | `https://openrouter.ai/api/v1` | One key for models from many providers. |
| OpenRouter Decisions | Decisions | `https://openrouter.ai/api` | Decision models that return probabilities. Only for `decide`. |
| Voyage AI | OpenAI-compatible | `https://api.voyageai.com/v1` | Embeddings only. Anthropic has no embedding models and points to Voyage. |
| Cohere | OpenAI-compatible | `https://api.cohere.com/v2` | Only for `rerank`. Its `/rerank` request and answer match what Assay sends and reads. |

## Mixed setup

Use this setup when you can use more than one provider. Each function gets the provider that fits it best.

| Function | Endpoint | Model | Parameters | Why |
|---|---|---|---|---|
| `answer` | Anthropic | `claude-sonnet-5-5` | `{"thinking":{"type":"disabled"}}` | People read the answers. The model follows the citation rules well. |
| `extract` | OpenAI | `gpt-6-luna` | `{"reasoning_effort":"none"}` | The function with the most calls. It needs strict JSON and exact quotes, not reasoning. |
| `compile` | OpenAI | `gpt-6-luna` | `{"reasoning_effort":"none"}` | A short summary of claims that Assay already checked. |
| `embed` | OpenAI | `text-embedding-3-small` | `{}` | Low cost. 1,536 dimensions. |
| `rerank` | Cohere | `rerank-v3.5` or later | `{}` | Better order of search results. |
| `decide` | OpenRouter Decisions | A decision model, such as `~typesafe/jev-latest` | `{}` | Decision models return probabilities. Compare models with `assay eval`. |
| `design` | Anthropic | `claude-sonnet-5-5`, or `claude-opus-5-5` for large repositories | `{"thinking":{"type":"disabled"}}` | Long input. Each sentence must cite its source. |

## OpenAI only

Use this setup when you have only an OpenAI key. OpenAI has no rerank endpoint, so `rerank` stays off.

| Function | Model | Parameters | Price per million tokens (input / output) | Why |
|---|---|---|---|---|
| `answer` | `gpt-6.1-terra` | `{"reasoning_effort":"low"}` | 1.00 / 5.00 USD | Fast and good enough for answers that people read. |
| `extract` | `gpt-6-luna` | `{"reasoning_effort":"none"}` | 0.10 / 0.50 USD | The function with the most calls. |
| `compile` | `gpt-6-luna` | `{"reasoning_effort":"none"}` | 0.10 / 0.50 USD | A short summary. |
| `embed` | `text-embedding-3-small` | `{}` | 0.02 USD | 1,536 dimensions. For `text-embedding-3-large`, set `{"dimensions":1536}`. |
| `rerank` | Off | | | OpenAI has no rerank endpoint. |
| `decide` | `gpt-6-luna` | `{"reasoning_effort":"none"}` | 0.10 / 0.50 USD | Many small JSON calls. Assay calibrates the bands from reviewer outcomes. If `assay eval` shows low precision, use `gpt-6.1-terra`. |
| `design` | `gpt-6.1-sol` | `{"reasoning_effort":"low"}` | 2.00 / 10.00 USD | A few long calls for each repository. The token budget of each repository limits the cost. |

GPT-5 and later models refuse `max_tokens`. Assay changes to `max_completion_tokens` after the first refusal for each model. You see that refusal one time in the log.

## Anthropic only

Use this setup when you use Claude models for all text functions. Anthropic has no embedding or rerank models. Use Voyage AI for `embed`, and leave `rerank` off or use Cohere.

| Function | Endpoint | Model | Parameters | Why |
|---|---|---|---|---|
| `answer` | Anthropic | `claude-sonnet-5-5` | `{"thinking":{"type":"disabled"}}` | Strong answers that cite their sources. |
| `extract` | Anthropic | `claude-haiku-5-5` | `{"thinking":{"type":"disabled"}}` | The function with the most calls. Haiku has the lowest cost. |
| `compile` | Anthropic | `claude-haiku-5-5` | `{"thinking":{"type":"disabled"}}` | A short summary. |
| `embed` | Voyage AI | `voyage-4` | `{}` | 1,024 dimensions. Anthropic points to Voyage for embeddings. |
| `rerank` | Off, or Cohere | `rerank-v3.5` or later | `{}` | Optional. |
| `decide` | Anthropic | `claude-haiku-5-5` | `{"thinking":{"type":"disabled"}}` | Many small JSON calls. If `assay eval` shows low precision, use `claude-sonnet-5-5`. |
| `design` | Anthropic | `claude-sonnet-5-5`, or `claude-opus-5-5` for large repositories | `{"thinking":{"type":"disabled"}}` | Long input. Each sentence must cite its source. |

Claude 5 models think by default. The thinking uses the same output limit as the answer. The parameter `{"thinking":{"type":"disabled"}}` turns thinking off. The Anthropic page of the compatibility layer shows only the `enabled` form of this field. Make one call for each function after you set it up. If the answer is empty or the call fails, remove the parameter and open an issue.

The compatibility layer ignores `reasoning_effort`. It accepts `response_format` only as a JSON schema with `strict` set to true. Assay sends only that form. Do not set `temperature` or `top_p` for Claude 5 models: values other than the defaults return an error.
