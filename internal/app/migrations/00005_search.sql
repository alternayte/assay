-- +goose Up
-- An embedding space holds the vectors of one embed model. One space is
-- active. A model change builds a new space, then switches in one
-- transaction. The vectors of space n live in the table emb_<n>.
CREATE TABLE embedding_spaces (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    endpoint_id bigint REFERENCES model_endpoints (id) ON DELETE SET NULL,
    model text NOT NULL,
    dim integer NOT NULL CHECK (dim BETWEEN 1 AND 4096),
    status text NOT NULL DEFAULT 'building' CHECK (status IN ('building', 'active', 'retired')),
    done integer NOT NULL DEFAULT 0,
    total integer NOT NULL DEFAULT 0,
    error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    activated_at timestamptz
);
CREATE UNIQUE INDEX embedding_spaces_one_active ON embedding_spaces (status) WHERE status = 'active';

-- The entity that an item is, such as an issue or a repository.
ALTER TABLE source_items ADD COLUMN entity_id bigint REFERENCES entities (id) ON DELETE SET NULL;

-- The private question history of each user.
CREATE TABLE questions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES auth_users (id) ON DELETE CASCADE,
    text text NOT NULL,
    answer_md text NOT NULL DEFAULT '',
    citations jsonb NOT NULL DEFAULT '{}',
    flags jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX questions_user ON questions (user_id, id DESC);
CREATE INDEX questions_created_at ON questions (created_at);

-- +goose Down
DROP TABLE questions;
ALTER TABLE source_items DROP COLUMN entity_id;
DROP TABLE embedding_spaces;
