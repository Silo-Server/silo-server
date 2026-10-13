-- +goose Up
-- The routing migration seeded routes from the default Radarr and Sonarr
-- servers of any plugin. Before routing, a media type's requests all went to
-- the plugin that owns the first usable connection by name (enabled, bound to a
-- plugin installation, with an API key, serving the media type), and that
-- plugin picked among its own servers. Where a seeded route sends requests to
-- a server outside that owner (the first connection is Seerr, say), remove the
-- media type's seeded routes together: a media type without routes goes back
-- to that plugin, as before, while one left with only some of them would be
-- routed by Silo with no fallback for the titles they do not match.
-- A media type with a route an admin has saved or added since is left alone:
-- the admin has set up routing for it, and removing the seeded fallback would
-- leave the titles their routes do not match with nowhere to go.
-- +goose StatementBegin
WITH owners AS (
    SELECT m.media_type, o.installation_id, o.capability_id
    FROM (VALUES ('movie'), ('series')) AS m (media_type)
    LEFT JOIN LATERAL (
        SELECT i.installation_id, i.capability_id
        FROM request_integrations i
        WHERE i.enabled AND i.capability_id <> '' AND i.installation_id IS NOT NULL
          AND i.api_key_ref <> ''
          AND (cardinality(i.supported_media_types) = 0 OR m.media_type = ANY(i.supported_media_types))
        ORDER BY i.name, i.id
        LIMIT 1
    ) o ON true
),
routes AS (
    SELECT r.*,
        r.id IN ('fallback-' || r.media_type, 'anime-' || r.media_type)
            AND r.updated_at = r.created_at AS seeded
    FROM request_routes r
),
repaired AS (
    SELECT o.media_type
    FROM owners o
    WHERE EXISTS (
        SELECT 1 FROM routes r
        JOIN request_integrations i ON i.id IN (r.hd_integration_id, r.uhd_integration_id)
        WHERE r.media_type = o.media_type AND r.seeded
          AND (i.installation_id IS DISTINCT FROM o.installation_id
               OR i.capability_id IS DISTINCT FROM o.capability_id))
      AND NOT EXISTS (
        SELECT 1 FROM routes r WHERE r.media_type = o.media_type AND NOT r.seeded)
)
DELETE FROM request_routes r
USING repaired p
WHERE r.media_type = p.media_type
  AND r.id IN ('fallback-' || r.media_type, 'anime-' || r.media_type)
  AND r.updated_at = r.created_at;
-- +goose StatementEnd

-- +goose Down
-- The removed routes are not recreated: the plugin that owned the media type
-- routes it again, as it did before routing existed.
SELECT 1;
