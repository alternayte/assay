-- +goose Up
-- The profile of an entity: deterministic text from its own sources only.
-- connections are those sources. A new hash embeds the profile again.
CREATE TABLE entity_profiles (
    entity_id bigint PRIMARY KEY REFERENCES entities (id) ON DELETE CASCADE,
    text text NOT NULL,
    hash bytea NOT NULL,
    connections bigint[] NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- A decision is one typed model call with its probabilities. raw_p is the
-- probability of yes, or of the chosen option. connections are the
-- connections of its inputs (SI-18).
CREATE TABLE decisions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('same_entity', 'mention_link', 'describes', 'equivalent', 'still_supports', 'term_worthy')),
    subject_refs text[] NOT NULL,
    input_hash bytea NOT NULL,
    model text NOT NULL DEFAULT '',
    probs jsonb NOT NULL DEFAULT '{}',
    raw_p double precision,
    band text CHECK (band IN ('high', 'medium', 'low')),
    connections bigint[] NOT NULL,
    outcome text NOT NULL DEFAULT 'open' CHECK (outcome IN ('open', 'accepted', 'rejected', 'auto', 'undone')),
    reasons jsonb NOT NULL DEFAULT '[]',
    at timestamptz NOT NULL DEFAULT now(),
    reviewed_at timestamptz,
    merge_id bigint REFERENCES merges (id) ON DELETE SET NULL
);
CREATE INDEX decisions_kind ON decisions (kind, outcome, reviewed_at);
CREATE INDEX decisions_input ON decisions (kind, input_hash);

-- The calibration of a decision kind from reviewed outcomes in 10 bins.
CREATE TABLE calibration (
    kind text PRIMARY KEY,
    bins jsonb NOT NULL DEFAULT '[]',
    reviewed integer NOT NULL DEFAULT 0,
    high_precision double precision,
    auto_apply boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- A reviewer verdict on a pair. A pair with a verdict is not suggested
-- again.
CREATE TABLE pair_verdicts (
    a_id bigint NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
    b_id bigint NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
    kind text NOT NULL,
    verdict text NOT NULL CHECK (verdict IN ('same', 'different')),
    by uuid REFERENCES auth_users (id) ON DELETE SET NULL,
    at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (a_id, b_id, kind),
    CHECK (a_id < b_id)
);

-- The cached equivalent decision of two normalized text values.
CREATE TABLE text_equivalence (
    a_norm text NOT NULL,
    b_norm text NOT NULL,
    equivalent boolean NOT NULL,
    decision_id bigint REFERENCES decisions (id) ON DELETE CASCADE,
    PRIMARY KEY (a_norm, b_norm),
    CHECK (a_norm < b_norm)
);

-- Resolution marks the extractions whose open mentions it handled.
ALTER TABLE passage_extractions ADD COLUMN resolved_at timestamptz;

ALTER TABLE proposals ADD COLUMN decision_id bigint REFERENCES decisions (id) ON DELETE CASCADE;

-- A purge removes the model outputs and cards whose inputs came from the
-- connection.
-- +goose StatementBegin
CREATE FUNCTION purge_connection_outputs() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM decisions WHERE OLD.id = ANY(connections);
    DELETE FROM proposals WHERE OLD.id = ANY(connections);
    RETURN OLD;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER connections_purge_outputs BEFORE DELETE ON connections
    FOR EACH ROW EXECUTE FUNCTION purge_connection_outputs();

-- +goose Down
DROP TRIGGER connections_purge_outputs ON connections;
DROP FUNCTION purge_connection_outputs();
ALTER TABLE proposals DROP COLUMN decision_id;
ALTER TABLE passage_extractions DROP COLUMN resolved_at;
DROP TABLE text_equivalence;
DROP TABLE pair_verdicts;
DROP TABLE calibration;
DROP TABLE decisions;
DROP TABLE entity_profiles;
