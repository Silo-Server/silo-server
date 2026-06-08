-- +goose Up
-- +goose StatementBegin
-- Autoscan: connections-based scan-source model. The host pulls changed paths
-- from installed scan_source.v1 plugins (Sonarr/Radarr, etc.) on a timer,
-- applies per-source path rewrites, and enqueues targeted library rescans.
--
-- Three tables:
--   * autoscan_settings    - singleton global config (enable, poll cadence, debounce)
--   * autoscan_connections - reachable upstreams: own credentials, OR a live link
--                            to a Requests integration (reused arr server)
--   * autoscan_sources     - operator-created rows, each binding one installed
--                            scan_source capability to a connection + rewrites +
--                            cadence (many sources may share one capability)

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
    -- Creation-time validity (own creds OR a link) is enforced at the application
    -- layer. A linked connection may become orphaned (both null) when its Requests
    -- integration is deleted (ON DELETE SET NULL), surfacing as "needs attention".
);

CREATE TABLE public.autoscan_sources (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    installation_id integer NOT NULL,
    capability_id text NOT NULL,
    -- Nullable: a freshly-discovered source has no connection until the operator
    -- binds one. The engine skips an enabled source with no connection.
    connection_id uuid REFERENCES public.autoscan_connections(id) ON DELETE RESTRICT,
    enabled boolean NOT NULL DEFAULT false,
    poll_interval_seconds integer,
    -- Host-owned per-source prefix rewrites: [{"from": "...", "to": "..."}].
    -- Applied to the raw source-namespace paths the scan_source plugin returns,
    -- before resolving/enqueueing.
    path_rewrites jsonb NOT NULL DEFAULT '[]'::jsonb,
    marker text,
    last_run_at timestamptz,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT autoscan_sources_interval_pos CHECK (poll_interval_seconds IS NULL OR poll_interval_seconds > 0)
    -- No UNIQUE(installation_id, capability_id): one installed scan_source plugin
    -- capability backs MANY sources, each bound to a different connection (e.g. 4
    -- arr servers from one plugin install). Operators create sources explicitly.
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.autoscan_sources;
DROP TABLE IF EXISTS public.autoscan_connections;
DROP TABLE IF EXISTS public.autoscan_settings;
-- +goose StatementEnd
