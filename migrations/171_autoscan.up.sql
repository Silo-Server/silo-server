CREATE TABLE IF NOT EXISTS public.autoscan_settings (
    id boolean PRIMARY KEY DEFAULT true,
    enabled boolean NOT NULL DEFAULT false,
    poll_interval_minutes integer NOT NULL DEFAULT 10,
    debounce_seconds integer NOT NULL DEFAULT 60,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT autoscan_settings_singleton CHECK (id),
    CONSTRAINT autoscan_settings_interval_positive CHECK (poll_interval_minutes > 0),
    CONSTRAINT autoscan_settings_debounce_nonneg CHECK (debounce_seconds >= 0)
);

INSERT INTO public.autoscan_settings (id) VALUES (true) ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS public.autoscan_sources (
    integration_id text PRIMARY KEY
        REFERENCES public.request_integrations(id) ON DELETE CASCADE,
    enabled boolean NOT NULL DEFAULT false,
    path_rewrites jsonb NOT NULL DEFAULT '[]'::jsonb,
    last_poll_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);
