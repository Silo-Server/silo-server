-- +goose Up
-- +goose StatementBegin
DO $$
DECLARE
    refresh_text text;
    refresh_ns numeric;
BEGIN
    SELECT COALESCE(NULLIF(value, ''), '30d') INTO refresh_text
    FROM public.server_settings WHERE key = 'auth.refresh_token_expiry';
    refresh_text := COALESCE(refresh_text, '30d');

    -- Match config.parseDuration: integer days or Go duration components.
    -- Compare units explicitly instead of using PostgreSQL interval syntax.
    IF refresh_text ~ '^[0-9]+d$' THEN
        refresh_ns := trim(trailing 'd' FROM refresh_text)::numeric * 86400000000000;
    ELSIF refresh_text ~ '^[+]?(([0-9]+([.][0-9]*)?|[.][0-9]+)(ns|us|µs|μs|ms|s|m|h))+$' THEN
        SELECT sum(trunc(parts[1]::numeric * CASE parts[2]
            WHEN 'h' THEN 3600000000000 WHEN 'm' THEN 60000000000
            WHEN 's' THEN 1000000000 WHEN 'ms' THEN 1000000
            WHEN 'us' THEN 1000 WHEN 'µs' THEN 1000 WHEN 'μs' THEN 1000
            WHEN 'ns' THEN 1 END)) INTO refresh_ns
        FROM regexp_matches(refresh_text, '([0-9]+[.]?[0-9]*|[.][0-9]+)(ns|us|µs|μs|ms|s|m|h)', 'g') AS parts;
    END IF;

    IF refresh_ns >= 86400000000000 THEN
        -- Both exact legacy defaults move to 24h; other explicit values stay.
        UPDATE public.server_settings SET value = '24h'
        WHERE key = 'auth.access_token_expiry' AND value IN ('1h', '8h');
    ELSE
        -- Preserve the old 8h fallback when a shorter (or unrecognized) refresh
        -- lifetime prevents the upgrade. Existing explicit access values stay.
        INSERT INTO public.server_settings (key, value)
        VALUES ('auth.access_token_expiry', '8h')
        ON CONFLICT (key) DO UPDATE SET value = '8h'
        WHERE server_settings.value = '';
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- Keep configured lifetimes on rollback: a 24h value may have been selected
-- by an administrator, and the previous binary accepts it.
SELECT 1;
