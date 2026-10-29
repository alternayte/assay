-- +goose Up
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS unaccent;

CREATE TABLE groups (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL,
    builtin boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX groups_name_key ON groups (lower(name));

-- Everyone has no member rows. Its members are the active users.
INSERT INTO groups (name, builtin) VALUES ('Everyone', true);

CREATE TABLE group_members (
    group_id bigint NOT NULL REFERENCES groups (id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES auth_users (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, user_id)
);

CREATE TABLE invites (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email text NOT NULL,
    role text NOT NULL,
    token_hash bytea NOT NULL UNIQUE,
    created_by uuid NOT NULL REFERENCES auth_users (id),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    accepted_at timestamptz,
    accepted_user_id uuid REFERENCES auth_users (id),
    revoked_at timestamptz
);

CREATE TABLE password_resets (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES auth_users (id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    created_by uuid NOT NULL REFERENCES auth_users (id),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at timestamptz
);

CREATE TABLE settings (
    key text PRIMARY KEY,
    value jsonb NOT NULL,
    revision integer NOT NULL,
    updated_by uuid REFERENCES auth_users (id),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE settings_history (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    key text NOT NULL REFERENCES settings (key),
    revision integer NOT NULL,
    old_value jsonb,
    new_value jsonb NOT NULL,
    updated_by uuid REFERENCES auth_users (id),
    updated_at timestamptz NOT NULL,
    UNIQUE (key, revision)
);

CREATE TABLE secrets (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL UNIQUE,
    ciphertext bytea NOT NULL,
    nonce bytea NOT NULL,
    key_version integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE audit_log (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at timestamptz NOT NULL DEFAULT now(),
    actor_user_id uuid,
    actor_token_id text,
    action text NOT NULL,
    target text NOT NULL DEFAULT '',
    details jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_log_at ON audit_log (at DESC);
CREATE INDEX audit_log_action ON audit_log (action, at DESC);

-- +goose StatementBegin
CREATE FUNCTION audit_log_no_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only';
END
$$;
-- +goose StatementEnd
CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_no_update();

-- +goose Down
DROP TABLE audit_log;
DROP FUNCTION audit_log_no_update;
DROP TABLE secrets;
DROP TABLE settings_history;
DROP TABLE settings;
DROP TABLE password_resets;
DROP TABLE invites;
DROP TABLE group_members;
DROP TABLE groups;
