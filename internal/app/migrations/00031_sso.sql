-- +goose Up
-- One IdP group maps to one Assay group. The IdP then sets the members of
-- the Assay group, and the Groups page refuses manual member changes.
CREATE TABLE group_mappings (
    group_id bigint PRIMARY KEY REFERENCES groups (id) ON DELETE CASCADE,
    provider text NOT NULL,
    external_id text NOT NULL,
    external_name text NOT NULL,
    UNIQUE (provider, external_id)
);

-- Each user linked to the IdP. subject is the directory object ID of the
-- user. directory_enabled is the account state that the directory last
-- reported, because a delta read names only the changes.
CREATE TABLE directory_users (
    user_id uuid PRIMARY KEY REFERENCES auth_users (id) ON DELETE CASCADE,
    provider text NOT NULL,
    subject text NOT NULL,
    directory_enabled boolean NOT NULL DEFAULT true,
    disabled_by_sync boolean NOT NULL DEFAULT false,
    seen_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, subject)
);

-- The delta link and the result of the last directory poll.
CREATE TABLE directory_state (
    provider text PRIMARY KEY,
    config_key text NOT NULL DEFAULT '',
    delta_link text NOT NULL DEFAULT '',
    last_started_at timestamptz,
    last_success_at timestamptz,
    last_error_at timestamptz,
    last_error text NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE directory_state;
DROP TABLE directory_users;
DROP TABLE group_mappings;
