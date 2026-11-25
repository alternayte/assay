# Multi-turn Ask

## What it does
After an answer, the user asks a follow-up question in the same thread on the Ask page. The follow-up answer reads the earlier questions, the earlier answers and their cited sources as context, and it cites sources the same way as a first answer. The question history lists threads, and a thread opens with all its turns. MCP gets no `ask` tool.

## Decisions
- `questions` gets `thread_id bigint` (the ID of the first question of the thread) and `turn integer`. A first question has `thread_id = id` and turn 1 — a thread is a run of question rows, and the history keeps one row per answer.
- `POST /api/v1/ask` takes an optional `threadId`. The thread must belong to the caller, or the route returns 404 — the history is private.
- A thread holds at most 6 turns. A seventh question starts a new thread, and the page says so — the context of each turn grows with the thread.
- Retrieval for a follow-up searches the follow-up text plus the question text of the previous turn — a short follow-up such as "and who reviews it?" has no subject of its own, and this needs no extra model call.
- The context of a follow-up holds, in this order: the earlier turns as `<turn>` blocks with the question and the answer text, then the sources and claims that the previous turn cited, then the new retrieval results. Marker IDs stay unique across the context.
- Earlier turns in the context are limited to 1,500 tokens. The oldest turns are cut first — the answer budget pays for every input token.
- The earlier answer text is model output. The prompt says that only `<source>` and `<claim>` blocks are evidence, and that a `<turn>` block is not evidence — a follow-up must not cite an earlier answer as a source.
- Cited sources of the previous turn are read again from the database with the caller's visible set — a source that is no longer visible drops out.
- A follow-up is refused with the notice "The thread used content that you cannot read now. Start a new question." when a connection in `flags.connections` of any earlier turn is not visible — this is the rule that hides a stored answer today.
- `flags.connections` of a follow-up holds the connections of its own context and of all earlier turns — hiding then works for the whole thread.
- A follow-up uses the `answer` model function and its monthly budget, the same as a first question. When the budget is used up, the follow-up gets the results and the budget notice, as a first question does.
- `listQuestions` returns one row per thread, with the first question, the turn count and the time of the last turn. `getQuestion` of any turn returns the whole thread.
- Retention deletes whole threads by the time of their last turn — deleting part of a thread would break its context.
- The Ask page shows the turns in order with a follow-up field under the last answer, and a "New question" button. All copy goes in `web/src/copy/en.json`.
- Feedback marks apply per turn.

## Out
- An `ask` tool on MCP, or multi-turn Ask on MCP.
- Sharing a thread with another user.
- Editing or deleting one turn.
- A model call that rewrites the follow-up into a stand-alone question.

## How I know it works
- Ask "Who owns the Payments API?", then "What does that team also run?". The second answer names the reminder service and cites its catalog file.
- The history shows one row for the two questions, and it opens both turns.
- Remove the caller from the group that shares a connection that the first turn cited. A follow-up in that thread returns the notice, and the thread shows as hidden.
- A seventh question in a thread starts a new thread.
- With the answer budget used up, a follow-up shows the results and the budget notice.
