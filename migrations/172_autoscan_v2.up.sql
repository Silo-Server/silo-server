-- Autoscan v2: connections-based model. The v1 tables (autoscan_settings,
-- autoscan_sources from migration 171) ALWAYS exist when this runs (171 created
-- them sequentially). Rather than DROP them and lose the operator's config, we
-- rename them aside, create the v2 schema, backfill what carries forward, then
-- drop the renamed v1 tables.
--
-- What carries forward:
--   * settings: enabled, poll cadence (minutes -> seconds), debounce
--   * each distinct v1 source arr integration -> a reusable LINKED v2 connection
-- What does NOT carry forward (intentional):
--   * v1 autoscan_sources rows -> v2 autoscan_sources: v2 sources are keyed on a
--     plugin (installation_id, capability_id) that did not exist in v1, so they
--     cannot be backfilled and are left to runtime discovery.
--   * v1 path_rewrites: in v2 the HOST owns per-source prefix rewrites
--     (autoscan_sources.path_rewrites jsonb). They can NOT be backfilled from v1:
--     v2 sources are keyed on a plugin (installation_id, capability_id) that has
--     no mapping to a v1 source row, so there is no row to copy the v1 rewrites
--     onto. Operators must re-enter path rewrites per source after upgrading.

-- (a) Rename v1 tables aside.
ALTER TABLE IF EXISTS public.autoscan_settings RENAME TO autoscan_settings_v1;
ALTER TABLE IF EXISTS public.autoscan_sources RENAME TO autoscan_sources_v1;

-- (b) Create the v2 schema.
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
    -- Host-owned per-source prefix rewrites: [{"from": "...", "to": "..."}].
    -- Applied to raw source-namespace paths from the scan_source plugin before
    -- resolving/enqueueing. NOT backfilled from v1 (see header note); operators
    -- re-enter these per source after upgrading.
    path_rewrites jsonb NOT NULL DEFAULT '[]'::jsonb,
    marker text,
    last_run_at timestamptz,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT autoscan_sources_interval_pos CHECK (poll_interval_seconds IS NULL OR poll_interval_seconds > 0),
    CONSTRAINT autoscan_sources_capability_unique UNIQUE (installation_id, capability_id)
);

-- (c) Backfill from v1.
-- Settings: carry enable flag, poll cadence (minutes -> seconds), and debounce.
UPDATE public.autoscan_settings AS s
SET enabled = v1.enabled,
    default_poll_interval_seconds = v1.poll_interval_minutes * 60,
    debounce_seconds = v1.debounce_seconds,
    updated_at = now()
FROM public.autoscan_settings_v1 AS v1
WHERE s.id = true;

-- Connections: each distinct v1 source arr integration becomes a reusable LINKED
-- v2 connection (own creds live in the Requests integration via the link).
INSERT INTO public.autoscan_connections (name, kind, request_integration_id)
SELECT ri.name, ri.kind, ri.id
FROM public.autoscan_sources_v1 s
JOIN public.request_integrations ri ON ri.id = s.integration_id
GROUP BY ri.name, ri.kind, ri.id;

-- (d) Drop the renamed v1 tables now that everything carryable has been salvaged.
DROP TABLE IF EXISTS public.autoscan_sources_v1;
DROP TABLE IF EXISTS public.autoscan_settings_v1;
