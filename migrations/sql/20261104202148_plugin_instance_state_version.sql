-- +goose Up
-- +goose StatementBegin
-- Write counter per plugin_instance_state row: 1 after the first write, one
-- more after each later write. A plugin passes the version it read as
-- WriteInstanceState.expected_version so that two API servers changing one
-- key cannot overwrite each other. Existing rows start at 1. The version is
-- not part of the state_value AAD, so existing ciphertexts stay readable.
ALTER TABLE plugin_instance_state
    ADD COLUMN version BIGINT NOT NULL DEFAULT 1 CHECK (version >= 1);
-- +goose StatementEnd

-- The trigger, not the application, advances the version, so every writer
-- does: this release, a server from an older release during a rolling deploy
-- (whose upsert knows nothing of the column), and manual SQL. Otherwise a
-- conditional write could land on a version an older server already wrote
-- past and silently discard that write. An update that sets the column to
-- a higher version keeps it: a full restore carries the live server's rows
-- over the restored ones with their versions, and a carried row must not
-- drop back to a version a plugin process that outlives the swap read.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION advance_plugin_instance_state_version() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.version := GREATEST(OLD.version + 1, NEW.version);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS plugin_instance_state_advance_version ON plugin_instance_state;
CREATE TRIGGER plugin_instance_state_advance_version
    BEFORE UPDATE ON plugin_instance_state
    FOR EACH ROW
    EXECUTE FUNCTION advance_plugin_instance_state_version();

-- +goose Down
DROP TRIGGER IF EXISTS plugin_instance_state_advance_version ON plugin_instance_state;
DROP FUNCTION IF EXISTS advance_plugin_instance_state_version();
ALTER TABLE plugin_instance_state DROP COLUMN IF EXISTS version;
