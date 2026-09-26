-- +goose Up
-- Version 1 remains admin-only: older dispatchers select it without a kind
-- predicate. Personal version 2 cannot be claimed by those binaries. Legacy
-- null-version rows retain their status and previous execution semantics.
ALTER TABLE history_import_runs ADD COLUMN dispatch_kind text NOT NULL DEFAULT 'admin';
ALTER TABLE history_import_runs ADD CONSTRAINT history_import_dispatch_kind CHECK (
    (dispatch_kind = 'admin' AND dispatch_version IS DISTINCT FROM 2) OR
    (dispatch_kind = 'personal' AND dispatch_version IS NOT DISTINCT FROM 2
     AND mapping_id IS NULL AND dispatch_mapping_id IS NULL AND dispatch_mapping_revision IS NULL
     AND dispatch_external_user_id IS NULL
     AND source_type IN ('emby', 'jellyfin', 'plex')
     AND connection_mode IN ('connect', 'custom', 'predefined', 'plex_oauth')
     AND ((dispatch_source_id IS NULL AND dispatch_source_revision IS NULL)
       OR (dispatch_source_id IS NOT NULL AND dispatch_source_id > 0
           AND dispatch_source_revision IS NOT NULL AND dispatch_source_revision > 0)))
);
CREATE TABLE history_import_run_credentials (
    run_id text PRIMARY KEY REFERENCES history_import_runs(id) ON DELETE CASCADE,
    envelope_version integer NOT NULL CHECK (envelope_version = 1),
    payload text NOT NULL CHECK (payload LIKE 'enc:v1:%')
);

-- +goose StatementBegin
CREATE FUNCTION history_import_guard_personal_intent() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.dispatch_kind IS DISTINCT FROM OLD.dispatch_kind OR NEW.id IS DISTINCT FROM OLD.id THEN
        RAISE EXCEPTION 'history import identity and dispatch kind are immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE FUNCTION history_import_guard_credentials() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND NOT EXISTS (SELECT 1 FROM history_import_runs
        WHERE id=OLD.run_id AND status IN ('queued','running')) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'history import credentials are immutable' USING ERRCODE = '23514';
END;
$$;
-- Check the final transaction state in both directions. Run and ciphertext can
-- be inserted in either order, but an active personal acceptance cannot commit
-- without its credential. A terminal row cannot retain credentials.
CREATE FUNCTION history_import_check_personal_credentials() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    target_id text;
    active_personal boolean;
    has_secret boolean;
BEGIN
    IF TG_TABLE_NAME = 'history_import_runs' THEN
        target_id := COALESCE(NEW.id, OLD.id);
    ELSE
        IF TG_OP = 'DELETE' THEN target_id := OLD.run_id;
        ELSE target_id := NEW.run_id; END IF;
    END IF;
    SELECT EXISTS(SELECT 1 FROM history_import_runs WHERE id=target_id
        AND dispatch_kind='personal' AND dispatch_version=2 AND status IN ('queued','running')) INTO active_personal;
    SELECT EXISTS(SELECT 1 FROM history_import_run_credentials WHERE run_id=target_id) INTO has_secret;
    IF active_personal IS DISTINCT FROM has_secret THEN
        RAISE EXCEPTION 'personal import active intent requires its credential' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END;
$$;
CREATE FUNCTION history_import_erase_terminal_credentials() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status NOT IN ('queued','running') THEN
        DELETE FROM history_import_run_credentials WHERE run_id=NEW.id;
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER history_import_personal_intent_guard BEFORE UPDATE ON history_import_runs
    FOR EACH ROW EXECUTE FUNCTION history_import_guard_personal_intent();
CREATE TRIGGER history_import_credentials_guard BEFORE UPDATE OR DELETE ON history_import_run_credentials
    FOR EACH ROW EXECUTE FUNCTION history_import_guard_credentials();
CREATE CONSTRAINT TRIGGER history_import_personal_credentials_required AFTER INSERT OR UPDATE ON history_import_runs
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION history_import_check_personal_credentials();
CREATE CONSTRAINT TRIGGER history_import_credentials_owner_required AFTER INSERT OR DELETE ON history_import_run_credentials
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION history_import_check_personal_credentials();
CREATE TRIGGER history_import_terminal_credentials_cleanup AFTER INSERT OR UPDATE OF status ON history_import_runs
    FOR EACH ROW EXECUTE FUNCTION history_import_erase_terminal_credentials();

-- +goose Down
DROP TRIGGER history_import_terminal_credentials_cleanup ON history_import_runs;
DROP TRIGGER history_import_credentials_owner_required ON history_import_run_credentials;
DROP TRIGGER history_import_personal_credentials_required ON history_import_runs;
DROP TRIGGER history_import_credentials_guard ON history_import_run_credentials;
DROP TRIGGER history_import_personal_intent_guard ON history_import_runs;
DROP FUNCTION history_import_erase_terminal_credentials();
DROP FUNCTION history_import_check_personal_credentials();
DROP FUNCTION history_import_guard_credentials();
DROP FUNCTION history_import_guard_personal_intent();
DROP TABLE history_import_run_credentials;
ALTER TABLE history_import_runs DROP CONSTRAINT history_import_dispatch_kind, DROP COLUMN dispatch_kind;
