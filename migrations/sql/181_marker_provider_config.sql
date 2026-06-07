-- +goose Up
-- +goose StatementBegin
-- Per-provider marker behavior: which providers are queried for reads (Layer B
-- multi-source dispatch) and which may receive contributions (off by default).
-- Credentials stay in server_settings; only behavior lives here.
CREATE TABLE public.marker_provider_config (
    provider                  text PRIMARY KEY,
    fetch_enabled             boolean NOT NULL DEFAULT true,
    fetch_priority            integer NOT NULL DEFAULT 100,
    contribute_enabled        boolean NOT NULL DEFAULT false,
    contribute_auto_local     boolean NOT NULL DEFAULT false,
    contribute_min_confidence double precision NOT NULL DEFAULT 0.95,
    updated_at                timestamptz NOT NULL DEFAULT now()
);

-- Seed the only provider that exists today. Fetch on, contribution off.
INSERT INTO public.marker_provider_config (provider, fetch_enabled)
VALUES ('introdb', true)
ON CONFLICT (provider) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.marker_provider_config;
-- +goose StatementEnd
