-- +goose Up
CREATE TABLE model_endpoints (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL UNIQUE,
    kind text NOT NULL CHECK (kind IN ('openai', 'decisions')),
    base_url text NOT NULL,
    secret_id bigint REFERENCES secrets (id) ON DELETE SET NULL,
    extra jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE model_functions (
    function text PRIMARY KEY CHECK (function IN ('answer', 'extract', 'compile', 'embed', 'rerank', 'decide')),
    endpoint_id bigint REFERENCES model_endpoints (id) ON DELETE SET NULL,
    model text NOT NULL DEFAULT '',
    params jsonb NOT NULL DEFAULT '{}',
    price_in_per_mtok numeric(12, 6) NOT NULL DEFAULT 0,
    price_out_per_mtok numeric(12, 6) NOT NULL DEFAULT 0,
    monthly_budget_usd numeric(12, 4) NOT NULL DEFAULT 5,
    enabled boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO model_functions (function) VALUES ('answer'), ('extract'), ('compile'), ('embed'), ('rerank'), ('decide');

CREATE TABLE model_usage (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    function text NOT NULL,
    model text NOT NULL,
    tokens_in integer NOT NULL,
    tokens_out integer NOT NULL,
    cost_usd numeric(14, 8) NOT NULL,
    ref text NOT NULL DEFAULT '',
    at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX model_usage_function_at ON model_usage (function, at);
CREATE INDEX model_usage_at ON model_usage (at);

-- A refused call. The banner shows while a refusal is newer than the last
-- change of the function.
CREATE TABLE model_refusals (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    function text NOT NULL,
    at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX model_refusals_function_at ON model_refusals (function, at DESC);

-- +goose Down
DROP TABLE model_refusals;
DROP TABLE model_usage;
DROP TABLE model_functions;
DROP TABLE model_endpoints;
