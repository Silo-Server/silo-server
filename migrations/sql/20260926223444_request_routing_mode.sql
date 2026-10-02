-- +goose Up
-- How requests find their server. Standard sends each media type to its one
-- server, and 4K copies to its one server marked 4K, with each server's own
-- settings; the routing rules are kept but paused. Advanced routes with the
-- rules. One row.
CREATE TABLE request_routing (
    id boolean PRIMARY KEY DEFAULT true CONSTRAINT request_routing_one_row CHECK (id),
    mode text NOT NULL CONSTRAINT request_routing_mode_check CHECK (mode IN ('standard', 'advanced')),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    revision bigint NOT NULL DEFAULT nextval('request_editor_revision_seq')
);
CREATE TRIGGER request_routing_revision BEFORE INSERT OR UPDATE ON request_routing
    FOR EACH ROW EXECUTE FUNCTION advance_request_editor_revision();

-- An install keeps routing with its rules (Advanced) when Standard would send
-- a request somewhere else: it has an enabled rule; a media type has more than
-- one enabled normal or 4K server; or Everything else changes a server
-- setting, sends HD copies anywhere but the media type's one normal Radarr or
-- Sonarr, or sends 4K copies anywhere (Standard would send them to the server
-- marked 4K) or nowhere while a server marked 4K takes the media type.
-- Everyone else starts on Standard.
-- +goose StatementBegin
WITH servers AS (
    SELECT i.id, m.media_type,
           coalesce((i.plugin_config->>'is_4k')::boolean, false)
               OR coalesce((i.plugin_config->>'is_default_4k')::boolean, false) AS is_4k,
           coalesce(i.plugin_config->>'service_kind', '') <> '' AS arr
    FROM request_integrations i
    CROSS JOIN (VALUES ('movie', 'radarr'), ('series', 'sonarr')) AS m (media_type, kind)
    WHERE i.enabled
      AND CASE WHEN coalesce(i.plugin_config->>'service_kind', '') <> ''
               THEN i.plugin_config->>'service_kind' = m.kind
               ELSE cardinality(i.supported_media_types) = 0 OR m.media_type = ANY(i.supported_media_types) END
)
INSERT INTO request_routing (id, mode)
SELECT true, CASE WHEN
        EXISTS (SELECT 1 FROM request_routes WHERE enabled AND NOT is_fallback)
     OR EXISTS (SELECT 1 FROM servers GROUP BY media_type, is_4k HAVING count(*) > 1)
     OR EXISTS (
            SELECT 1 FROM request_routes f
            WHERE f.is_fallback AND f.hd_integration_id IS NOT NULL
              AND (f.hd_overrides <> '{}'::jsonb OR f.uhd_overrides <> '{}'::jsonb
                   OR f.uhd_integration_id IS NOT NULL
                   OR NOT EXISTS (SELECT 1 FROM servers s WHERE s.id = f.hd_integration_id
                                  AND s.media_type = f.media_type AND s.arr AND NOT s.is_4k)
                   OR EXISTS (SELECT 1 FROM servers s WHERE s.media_type = f.media_type AND s.is_4k)))
    THEN 'advanced' ELSE 'standard' END;
-- +goose StatementEnd

-- +goose Down
DROP TABLE request_routing;
