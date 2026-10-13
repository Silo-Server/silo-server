-- +goose Up
-- A server added from now on becomes Everything else for its media type when
-- no other server takes that media type. Give installs that already have
-- exactly one usable Radarr (Sonarr), no other enabled server for movies
-- (series) and no Everything else the same setup, so a single-server install
-- needs no routing. Usable is what the routing migration required: enabled,
-- bound to a plugin installation, with an API key. A server flagged 4K is left
-- alone, since Everything else needs an HD server.
-- +goose StatementBegin
WITH kinds (media_type, kind) AS (
    VALUES ('movie', 'radarr'), ('series', 'sonarr')
),
sole AS (
    SELECT k.media_type, min(i.id) AS id
    FROM kinds k
    JOIN request_integrations i ON i.plugin_config->>'service_kind' = k.kind
    WHERE i.enabled AND i.installation_id IS NOT NULL AND i.api_key_ref <> ''
      AND NOT coalesce((i.plugin_config->>'is_4k')::boolean, false)
      AND NOT coalesce((i.plugin_config->>'is_default_4k')::boolean, false)
    GROUP BY k.media_type
    HAVING count(*) = 1
)
INSERT INTO request_routes (id, media_type, position, name, is_fallback, hd_integration_id)
SELECT 'fallback-' || s.media_type, s.media_type, 1000, 'Everything else', true, s.id
FROM sole s
WHERE NOT EXISTS (SELECT 1 FROM request_routes r WHERE r.media_type = s.media_type AND r.is_fallback)
  -- No other enabled server takes the media type: another of the kind, or a
  -- connection of another plugin (Seerr, say) that serves it.
  AND NOT EXISTS (
    SELECT 1 FROM request_integrations i
    WHERE i.id <> s.id AND i.enabled
      AND CASE WHEN coalesce(i.plugin_config->>'service_kind', '') <> ''
               THEN i.plugin_config->>'service_kind' = CASE s.media_type WHEN 'movie' THEN 'radarr' ELSE 'sonarr' END
               ELSE cardinality(i.supported_media_types) = 0 OR s.media_type = ANY(i.supported_media_types) END)
ON CONFLICT DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- The routes it made are ordinary routes by now; an admin may have edited
-- them. Nothing to undo.
SELECT 1;
