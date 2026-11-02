-- +goose Up
-- pg_stat_statements records per-query call counts and time. The omnibus image
-- and the shipped Compose files preload its library. Create the extension only
-- where it is preloaded: elsewhere its view fails on every read. A role that is
-- not a superuser can neither read that setting nor create the extension; on
-- such a server the operator creates it instead.
-- +goose StatementBegin
DO $$
BEGIN
    IF 'pg_stat_statements' = ANY (string_to_array(replace(current_setting('shared_preload_libraries'), ' ', ''), ',')) THEN
        CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
    END IF;
EXCEPTION WHEN insufficient_privilege THEN
    RAISE NOTICE 'pg_stat_statements not enabled: this role needs superuser to check for and create it';
END
$$;
-- +goose StatementEnd

-- +goose Down
-- Intentionally a no-op: the extension holds no Silo data, and a superuser may
-- have created it, in which case this role cannot drop it.
SELECT 1;
