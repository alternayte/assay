-- +goose Up
-- Embedding, extraction and design jobs move from the default queue to
-- their own queues. Waiting jobs move with them. Of the waiting embed jobs
-- one stays: each run embeds all pending chunks. River creates river_job
-- after these migrations, so a new install has no table yet.
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('river_job') IS NULL THEN
        RETURN;
    END IF;
    DELETE FROM river_job
    WHERE kind = 'embed' AND state IN ('available', 'scheduled', 'retryable', 'pending')
      AND id <> (SELECT min(id) FROM river_job WHERE kind = 'embed' AND state IN ('available', 'scheduled', 'retryable', 'pending'));
    UPDATE river_job SET queue = CASE
            WHEN kind = 'embed' THEN 'embed'
            WHEN kind IN ('extract', 'extract_connection') THEN 'extract'
            ELSE 'design' END
    WHERE queue = 'default' AND state IN ('available', 'scheduled', 'retryable', 'pending')
      AND kind IN ('embed', 'extract', 'extract_connection', 'design_build', 'design_section');
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('river_job') IS NULL THEN
        RETURN;
    END IF;
    UPDATE river_job SET queue = 'default'
    WHERE queue IN ('embed', 'extract', 'design') AND state IN ('available', 'scheduled', 'retryable', 'pending');
END
$$;
-- +goose StatementEnd
