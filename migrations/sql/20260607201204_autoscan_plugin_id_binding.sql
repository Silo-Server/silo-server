-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.autoscan_sources
    ADD COLUMN plugin_id text NOT NULL DEFAULT '';

ALTER TABLE public.autoscan_events
    ADD COLUMN plugin_id text NOT NULL DEFAULT '';

UPDATE public.autoscan_sources s
SET plugin_id = pi.plugin_id
FROM public.plugin_installations pi
WHERE s.installation_id = pi.id
  AND s.plugin_id = '';

WITH inferred AS (
    SELECT s.id, min(pi.plugin_id) AS plugin_id, count(*) AS matches
    FROM public.autoscan_sources s
    JOIN public.plugin_capabilities pc
      ON pc.capability_type = 'scan_source.v1'
     AND pc.capability_id = s.capability_id
    JOIN public.plugin_installations pi
      ON pi.id = pc.plugin_installation_id
    WHERE s.plugin_id = ''
    GROUP BY s.id
)
UPDATE public.autoscan_sources s
SET plugin_id = inferred.plugin_id
FROM inferred
WHERE s.id = inferred.id
  AND inferred.matches = 1;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.autoscan_sources
        WHERE enabled = true
          AND btrim(plugin_id) = ''
    ) THEN
        RAISE EXCEPTION 'enabled autoscan source could not be migrated to plugin_id';
    END IF;
END $$;

UPDATE public.autoscan_events e
SET plugin_id = s.plugin_id
FROM public.autoscan_sources s
WHERE e.source_id = s.id
  AND e.plugin_id = ''
  AND btrim(s.plugin_id) <> '';

UPDATE public.autoscan_events e
SET plugin_id = pi.plugin_id
FROM public.plugin_installations pi
WHERE e.installation_id = pi.id
  AND e.plugin_id = '';

WITH inferred AS (
    SELECT e.id, min(pi.plugin_id) AS plugin_id, count(*) AS matches
    FROM public.autoscan_events e
    JOIN public.plugin_capabilities pc
      ON pc.capability_type = 'scan_source.v1'
     AND pc.capability_id = e.capability_id
    JOIN public.plugin_installations pi
      ON pi.id = pc.plugin_installation_id
    WHERE e.plugin_id = ''
    GROUP BY e.id
)
UPDATE public.autoscan_events e
SET plugin_id = inferred.plugin_id
FROM inferred
WHERE e.id = inferred.id
  AND inferred.matches = 1;

CREATE INDEX idx_autoscan_sources_plugin_capability
    ON public.autoscan_sources (plugin_id, capability_id);

CREATE INDEX idx_autoscan_events_plugin_capability_completed
    ON public.autoscan_events (plugin_id, capability_id, completed_at DESC);

ALTER TABLE public.autoscan_sources
    ALTER COLUMN plugin_id DROP DEFAULT;

ALTER TABLE public.autoscan_events
    ALTER COLUMN plugin_id DROP DEFAULT;

ALTER TABLE public.autoscan_sources
    DROP COLUMN installation_id;

ALTER TABLE public.autoscan_events
    DROP COLUMN installation_id;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.autoscan_sources
    ADD COLUMN installation_id integer NOT NULL DEFAULT 0;

ALTER TABLE public.autoscan_events
    ADD COLUMN installation_id integer NOT NULL DEFAULT 0;

WITH resolved AS (
    SELECT s.id, min(pc.plugin_installation_id)::integer AS installation_id, count(*) AS matches
    FROM public.autoscan_sources s
    JOIN public.plugin_installations pi
      ON pi.plugin_id = s.plugin_id
    JOIN public.plugin_capabilities pc
      ON pc.plugin_installation_id = pi.id
     AND pc.capability_type = 'scan_source.v1'
     AND pc.capability_id = s.capability_id
    WHERE btrim(s.plugin_id) <> ''
    GROUP BY s.id
)
UPDATE public.autoscan_sources s
SET installation_id = resolved.installation_id
FROM resolved
WHERE s.id = resolved.id
  AND resolved.matches = 1;

WITH resolved AS (
    SELECT e.id, min(pc.plugin_installation_id)::integer AS installation_id, count(*) AS matches
    FROM public.autoscan_events e
    JOIN public.plugin_installations pi
      ON pi.plugin_id = e.plugin_id
    JOIN public.plugin_capabilities pc
      ON pc.plugin_installation_id = pi.id
     AND pc.capability_type = 'scan_source.v1'
     AND pc.capability_id = e.capability_id
    WHERE btrim(e.plugin_id) <> ''
    GROUP BY e.id
)
UPDATE public.autoscan_events e
SET installation_id = resolved.installation_id
FROM resolved
WHERE e.id = resolved.id
  AND resolved.matches = 1;

DROP INDEX IF EXISTS public.idx_autoscan_events_plugin_capability_completed;
DROP INDEX IF EXISTS public.idx_autoscan_sources_plugin_capability;

ALTER TABLE public.autoscan_events
    DROP COLUMN plugin_id;

ALTER TABLE public.autoscan_sources
    DROP COLUMN plugin_id;
-- +goose StatementEnd
