-- +goose Up
-- A thread is a run of questions. thread_id is the ID of the first question
-- of the thread, and turn counts from 1.
ALTER TABLE questions ADD COLUMN thread_id bigint, ADD COLUMN turn integer NOT NULL DEFAULT 1;
UPDATE questions SET thread_id = id;
ALTER TABLE questions ALTER COLUMN thread_id SET NOT NULL;
CREATE INDEX questions_thread ON questions (thread_id, turn);

-- A question without a thread starts its own thread.
-- +goose StatementBegin
CREATE FUNCTION questions_own_thread() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.thread_id := coalesce(NEW.thread_id, NEW.id);
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER questions_own_thread BEFORE INSERT ON questions FOR EACH ROW EXECUTE FUNCTION questions_own_thread();

-- The mark of the asker on one answer. Retention deletes it with its
-- question.
CREATE TABLE answer_feedback (
    question_id bigint PRIMARY KEY REFERENCES questions (id) ON DELETE CASCADE,
    rating text NOT NULL CHECK (rating IN ('up', 'down')),
    reason text NOT NULL DEFAULT '',
    wrong_citation boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE answer_feedback;
DROP TRIGGER questions_own_thread ON questions;
DROP FUNCTION questions_own_thread();
DROP INDEX questions_thread;
ALTER TABLE questions DROP COLUMN turn, DROP COLUMN thread_id;
