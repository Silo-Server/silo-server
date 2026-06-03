DROP TABLE IF EXISTS public.autoscan_sources;
DROP TABLE IF EXISTS public.autoscan_settings;

CREATE TABLE public.autoscan_settings (
    id boolean PRIMARY KEY DEFAULT true,
    enabled boolean NOT NULL DEFAULT false,
    default_poll_interval_seconds integer NOT NULL DEFAULT 600,
    debounce_seconds integer NOT NULL DEFAULT 60,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT autoscan_settings_singleton CHECK (id),
    CONSTRAINT autoscan_settings_interval_pos CHECK (default_poll_interval_seconds > 0),
    CONSTRAINT autoscan_settings_debounce_nonneg CHECK (debounce_seconds >= 0)
);
INSERT INTO public.autoscan_settings (id) VALUES (true) ON CONFLICT (id) DO NOTHING;

CREATE TABLE public.autoscan_connections (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL,
    kind text NOT NULL,
    base_url text,
    api_key_ref text,
    request_integration_id text REFERENCES public.request_integrations(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
    -- creation-time validity (own creds OR a link) is enforced at the application layer; a linked connection may become orphaned (both null) when its Requests integration is deleted (ON DELETE SET NULL), surfacing as "needs attention".
);

CREATE TABLE public.autoscan_sources (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    installation_id integer NOT NULL,
    capability_id text NOT NULL,
    connection_id uuid REFERENCES public.autoscan_connections(id) ON DELETE RESTRICT,
    enabled boolean NOT NULL DEFAULT false,
    poll_interval_seconds integer,
    marker text,
    last_run_at timestamptz,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT autoscan_sources_interval_pos CHECK (poll_interval_seconds IS NULL OR poll_interval_seconds > 0),
    CONSTRAINT autoscan_sources_capability_unique UNIQUE (installation_id, capability_id)
);
