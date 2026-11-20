-- +goose Up
-- +goose StatementBegin
-- A request server records which plugin it belongs to, so a reinstalled plugin
-- (new installation id) gets its servers back and no other plugin does.
ALTER TABLE public.request_integrations
    ADD COLUMN plugin_id text NOT NULL DEFAULT '';

UPDATE public.request_integrations i
SET plugin_id = p.plugin_id
FROM public.plugin_installations p
WHERE p.id = i.installation_id;

-- Servers whose installation was already removed: infer the plugin when
-- exactly one installed plugin offers their request router capability.
WITH inferred AS (
    SELECT i.id, min(p.plugin_id) AS plugin_id, count(DISTINCT p.plugin_id) AS matches
    FROM public.request_integrations i
    JOIN public.plugin_capabilities pc
      ON pc.capability_type = 'request_router.v1'
     AND pc.capability_id = i.capability_id
    JOIN public.plugin_installations p
      ON p.id = pc.plugin_installation_id
    WHERE i.plugin_id = ''
      AND i.installation_id IS NOT NULL
    GROUP BY i.id
)
UPDATE public.request_integrations i
SET plugin_id = inferred.plugin_id
FROM inferred
WHERE i.id = inferred.id
  AND inferred.matches = 1;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.request_integrations
    DROP COLUMN plugin_id;
-- +goose StatementEnd
