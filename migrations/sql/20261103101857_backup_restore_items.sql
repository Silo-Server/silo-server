-- +goose Up
-- A selective restore's item outcomes, one row each, written in the item's
-- own fenced transaction. The job's result_payload keeps only counts, the
-- library map, and the latest outcomes while the restore runs, so each
-- item's write stays the same size however many came before it; the full
-- list goes into the payload once, when the restore ends or stale recovery
-- marks it interrupted, and the rows are then removed.
CREATE TABLE IF NOT EXISTS backup_restore_items (
    run_id text NOT NULL,
    seq integer NOT NULL,
    item jsonb NOT NULL,
    PRIMARY KEY (run_id, seq)
);

-- +goose Down
DROP TABLE IF EXISTS backup_restore_items;
