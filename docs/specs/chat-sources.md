# Chat sources: Microsoft Teams and Slack

## What it does
An admin adds a `teams` or a `slack` connection and selects the channels to read. Sync reads each thread of those channels as one item, and each message of the thread as one passage. Ask, search, extraction and entity pages then use chat threads like other items. A retention setting per connection removes threads whose last message is older than the limit.

## Decisions
- New connection kinds `teams` and `slack` in `sync.Kinds`, in the `connections.kind` check, and in the `sourceType` enums — chat is a new source system, not a variant of an existing one.
- Item = one thread: the root message and its replies. Kind is `thread`. External ID is `<team or workspace>/<channel>/<root message id>` — a thread is the unit that a question and its answer share.
- Passage = one message. Its text starts with the author display name and the date, such as "Dana Ito, 2026-05-04:" — a claim from chat needs who said it and when.
- The item title is the channel name and the first 80 characters of the root message — search results then show where and what.
- The item URL is the `webUrl` of the Teams root message, or the Slack permalink `https://<workspace>.slack.com/archives/<channel>/p<ts without the dot>` — this needs no extra request per thread.
- The item revision is the newest reply time — sync skips a thread with no new reply.
- Each message author becomes a person entity with the key `teams:<tenant>:user:<id>` or `slack:<workspace>:user:<id>` and the fact `posted_in` to the thread — resolve can then merge the person with the same person from other sources.
- Teams uses the customer's own Entra app with the application permissions `ChannelMessage.Read.All`, `Team.ReadBasic.All` and `Channel.ReadBasic.All` — app-only access needs no signed-in user. `docs/connectors.md` says that Microsoft can require approval of the app for channel message access.
- Teams scope: the admin picks teams, then channels, from the lists that Graph returns.
- Teams first sync lists `GET /teams/{id}/channels/{id}/messages?$top=50&$expand=replies` and follows `@odata.nextLink`, and `replies@odata.nextLink` for threads with more than 200 replies — delta returns only the last 8 months.
- Teams incremental sync uses `/messages/delta` per channel and stores each delta link in the connection cursor as a JSON map by channel — delta returns changed roots and replies since the last run.
- Teams reads the token from `https://login.microsoftonline.com/<tenant>/oauth2/v2.0/token` with the client credentials grant — Graph app-only access has no other way to get a token.
- Slack uses an internal app that the customer creates in its own workspace with a bot token and the scopes `channels:read`, `channels:history` and `users:read` — an internal app is exempt from the 2025 limit of one history request per minute for apps outside the Marketplace.
- Slack scope: the admin picks from the public channels where the bot is a member. The setup text tells the admin to invite the bot — joining a channel is a write, and connectors are read-only.
- Slack sync calls `conversations.history` per channel with `oldest` set to 30 days before the cursor, and `conversations.replies` for each thread whose `latest_reply` is newer than the stored revision — `conversations.history` does not return an old root that got a new reply.
- Slack reconcile reads the whole history of each channel. It tombstones threads that are gone and fetches threads that sync missed.
- Slack connections default to `rate_per_second` 0.8 and `rate_concurrency` 2, and the client waits for `Retry-After` on 429 — Tier 3 allows about 50 requests per minute.
- Slack resolves user IDs to display names with `users.info`, cached for the run — messages carry only user IDs.
- Retention: the connection option `retainDays`, 0 by default, which keeps all threads. With a value, sync skips threads whose newest message is older than the limit, and reconcile tombstones them — a customer can keep chat for a shorter time than documents.
- Bot messages, join and leave events, and messages with no text are dropped — they hold no knowledge.
- Message Markdown: Slack mrkdwn and Teams HTML convert to the normalized Markdown with the existing link rules. User and channel mentions become the display name.
- The connection form, the setup text and all copy go in `web/src/copy/en.json`.
- The Acme fixtures get a Teams team and a Slack workspace with threads that state a decision. Contract tests use those fixtures only.

## Out
- Private channels, shared channels from another tenant, group chats and direct messages.
- Files and images attached to messages.
- Reactions, edit history and deleted-message history.
- Webhooks or the Events API. A separate spec covers webhooks.

## How I know it works
- Add a Slack connection to the fixture workspace and select one channel. After the sync, Explore shows each thread of that channel as one item, with one passage per message.
- Ask "What did the payments team decide about retries?" cites a passage of a fixture thread.
- A decision stated in a fixture Teams thread shows on the entity page of the service it names.
- Add a reply to an old fixture Slack thread. The next sync stores a new version of that thread.
- Set `retainDays` to 30. The next reconcile tombstones fixture threads older than 30 days.
- The leak suite covers a `slack` connection shared with one group only.
