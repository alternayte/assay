# Ask feedback and unanswered questions

## What it does
Under each answer, the user marks it as helpful or not helpful. A not-helpful mark takes an optional reason and a "A citation is wrong" flag. From a not-helpful answer, the user can report a problem with a cited claim, or propose a finding from a cited source. Settings, Diagnostics lists questions with no evidence and questions marked not helpful, grouped by the thing they name. `assay eval --from-feedback` writes rated questions to a private question file.

## Decisions
- New table `answer_feedback` (`question_id` primary key, `rating` up or down, `reason`, `wrong_citation`, `created_at`, `updated_at`). The user can change the mark — one mark per answer keeps the counts honest.
- Only the user who asked can rate the answer — the question history is private.
- `PUT /api/v1/questions/{id}/feedback` sets the mark, and `getQuestion` returns it. The SPA uses the generated client.
- A not-helpful mark shows "Report a problem" next to each cited claim and "Propose a finding" next to each cited source. They open the existing correction and finding forms, with the claim or the source item filled in — the review queue keeps its card kinds (REQ-REV-002), and a finding still needs exact quotes.
- A free-text reason with no form stays feedback only. It creates no card — a card needs a claim or a quote that a reviewer can check.
- The Diagnostics page gets a section "Questions to improve". It lists questions from all users with `noEvidence` true, or with a not-helpful mark, in the last 90 days.
- An admin sees a question in that list only when all connections in its `flags.connections` are in the admin's visible set — this is the history rule, applied to the reader.
- The list never shows who asked — the history is private, and the list is for content gaps.
- Group = the first visible entity that the question names, from `MentionedEntities`. A question that names none goes under "No named thing" — this needs no model call.
- Each row shows the question, the date, the mark, the reason and the wrong-citation flag. A row opens no answer text.
- The Ask page says once, under the marks: "Admins can see questions with no answer or a not-helpful mark. They do not see who asked."
- `assay eval --from-feedback --url --token --out <file>` calls a new admin route `GET /api/v1/feedback/questions`. It writes the existing eval YAML: the question, `sources` from the titles of the cited items of up-marked answers, `facts` empty with a `# TODO` comment, and `no_evidence: true` for no-evidence questions with a down mark — a person adds the facts, and `Parse` refuses the file until then.
- The export holds only questions whose connections the token user can see, with the same rule as the list.
- The output file is never in the repository. The command prints that the file holds real questions.
- `DeleteQuestionsBefore` also deletes their feedback, by the foreign key with `ON DELETE CASCADE` — retention covers feedback too.

## Out
- Marks on MCP calls or on REST answers outside the web app.
- Clustering of questions with a model.
- Showing other users' answers in Diagnostics.
- Committing exported question files.

## How I know it works
- Mark an answer as not helpful with a reason. Reload the question. The mark and the reason are still there.
- From a not-helpful answer, click "Report a problem" on a cited claim. A correction card for that claim shows in Review.
- Ask a question with no evidence as a member. An admin who can see the connections sees it in Diagnostics, under the thing it names, with no name of the asker.
- An admin whose visible set lacks a connection of that question does not see it.
- `assay eval --from-feedback` writes a YAML file. `assay eval --file` on that file refuses it until each question has facts.
