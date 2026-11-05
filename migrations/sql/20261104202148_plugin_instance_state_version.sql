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
-- past and silently discard that write.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.advance_plugin_instance_state_version() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.version := OLD.version + 1;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS plugin_instance_state_advance_version ON public.plugin_instance_state;
CREATE TRIGGER plugin_instance_state_advance_version
    BEFORE UPDATE ON public.plugin_instance_state
    FOR EACH ROW
    EXECUTE FUNCTION public.advance_plugin_instance_state_version();

-- +goose Down
DROP TRIGGER IF EXISTS plugin_instance_state_advance_version ON public.plugin_instance_state;
DROP FUNCTION IF EXISTS public.advance_plugin_instance_state_version();
ALTER TABLE plugin_instance_state DROP COLUMN IF EXISTS version;
