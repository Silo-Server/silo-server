-- +goose Up
ALTER TABLE history_import_runs
    ADD COLUMN dispatch_version integer,
    ADD COLUMN dispatch_source_id integer,
    ADD COLUMN dispatch_source_revision bigint,
    ADD COLUMN dispatch_mapping_id integer,
    ADD COLUMN dispatch_mapping_revision bigint,
    ADD COLUMN dispatch_external_user_id text,
    ADD COLUMN claim_generation bigint NOT NULL DEFAULT 0,
    ADD COLUMN cancel_requested_at timestamptz;

-- Existing duplicates remain intact. New admissions check every active row,
-- including legacy rows; this index independently protects durable admissions.
CREATE UNIQUE INDEX history_import_durable_active_mapping
    ON history_import_runs(mapping_id)
    WHERE dispatch_version IS NOT NULL AND mapping_id IS NOT NULL
      AND status IN ('queued', 'running');
CREATE INDEX history_import_dispatch_queue
    ON history_import_runs(created_at, id)
    WHERE status = 'queued' AND dispatch_version IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION history_import_guard_run() RETURNS trigger
LANGUAGE plpgsql VOLATILE AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        -- A deleted mapping may detach its runs via ON DELETE SET NULL, but a
        -- run must never be reassigned to a different mapping or target.
        IF NEW.mapping_id IS DISTINCT FROM OLD.mapping_id AND NEW.mapping_id IS NOT NULL THEN
            RAISE EXCEPTION 'history import run mapping is immutable' USING ERRCODE = '23514';
        END IF;
        IF ROW(NEW.user_id, NEW.profile_id, NEW.source_type, NEW.connection_mode,
               NEW.dispatch_version, NEW.dispatch_source_id, NEW.dispatch_source_revision,
               NEW.dispatch_mapping_id, NEW.dispatch_mapping_revision, NEW.dispatch_external_user_id)
           IS DISTINCT FROM
           ROW(OLD.user_id, OLD.profile_id, OLD.source_type, OLD.connection_mode,
               OLD.dispatch_version, OLD.dispatch_source_id, OLD.dispatch_source_revision,
               OLD.dispatch_mapping_id, OLD.dispatch_mapping_revision, OLD.dispatch_external_user_id) THEN
            RAISE EXCEPTION 'history import run intent is immutable' USING ERRCODE = '23514';
        END IF;
        IF OLD.status NOT IN ('queued', 'running') AND
           ROW(NEW.status, NEW.fetched, NEW.matched, NEW.unmatched, NEW.progress_updated,
               NEW.history_created, NEW.watchlist_added, NEW.favorites_imported, NEW.skipped,
               NEW.warnings, NEW.unmatched_samples, NEW.error_message, NEW.started_at,
               NEW.completed_at, NEW.last_heartbeat_at, NEW.claim_generation, NEW.cancel_requested_at)
           IS DISTINCT FROM
           ROW(OLD.status, OLD.fetched, OLD.matched, OLD.unmatched, OLD.progress_updated,
               OLD.history_created, OLD.watchlist_added, OLD.favorites_imported, OLD.skipped,
               OLD.warnings, OLD.unmatched_samples, OLD.error_message, OLD.started_at,
               OLD.completed_at, OLD.last_heartbeat_at, OLD.claim_generation, OLD.cancel_requested_at) THEN
            RAISE EXCEPTION 'history import terminal state is immutable' USING ERRCODE = '23514';
        END IF;
    END IF;
    IF NEW.mapping_id IS NOT NULL AND NEW.status IN ('queued', 'running') THEN
        IF TG_OP = 'INSERT' OR OLD.status NOT IN ('queued', 'running') THEN
            -- All admission paths, including legacy INSERTs, serialize here.
            -- The separate SELECT below takes a fresh READ COMMITTED snapshot
            -- after this lock wait (the trigger function is VOLATILE).
            PERFORM id FROM history_import_user_mappings WHERE id = NEW.mapping_id FOR UPDATE;
            IF EXISTS (SELECT 1 FROM history_import_runs
                       WHERE mapping_id = NEW.mapping_id AND status IN ('queued', 'running')
                         AND id <> NEW.id) THEN
                RAISE EXCEPTION 'an import run for this mapping is already active'
                    USING ERRCODE = '23505', CONSTRAINT = 'history_import_active_mapping';
            END IF;
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER history_import_run_guard BEFORE INSERT OR UPDATE ON history_import_runs
    FOR EACH ROW EXECUTE FUNCTION history_import_guard_run();

-- +goose Down
DROP TRIGGER history_import_run_guard ON history_import_runs;
DROP FUNCTION history_import_guard_run();
DROP INDEX history_import_dispatch_queue;
DROP INDEX history_import_durable_active_mapping;
ALTER TABLE history_import_runs
    DROP COLUMN dispatch_version,
    DROP COLUMN dispatch_source_id,
    DROP COLUMN dispatch_source_revision,
    DROP COLUMN dispatch_mapping_id,
    DROP COLUMN dispatch_mapping_revision,
    DROP COLUMN dispatch_external_user_id,
    DROP COLUMN claim_generation,
    DROP COLUMN cancel_requested_at;
