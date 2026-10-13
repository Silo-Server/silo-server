-- +goose Up
-- Routing rules for requests: which server (and which root folder, quality
-- profile, tags, ...) each quality tier of a request goes to. Silo evaluates
-- them and hands the router plugin only the chosen server, so rules can send
-- anime, a genre, a decade, a language or a requester's titles to their own
-- Sonarr or Radarr. Per tier, the first enabled route whose conditions match
-- and that has a destination for the tier wins; the fallback route (one per
-- media type, no conditions) comes last.
CREATE TABLE request_routes (
    id text PRIMARY KEY,
    media_type text NOT NULL,
    position integer NOT NULL,
    name text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    is_fallback boolean NOT NULL DEFAULT false,
    conditions jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- RESTRICT: deleting a server a route sends to must not silently
    -- reroute its titles; the admin changes the route first.
    hd_integration_id text REFERENCES request_integrations (id) ON DELETE RESTRICT,
    hd_overrides jsonb NOT NULL DEFAULT '{}'::jsonb,
    uhd_integration_id text REFERENCES request_integrations (id) ON DELETE RESTRICT,
    uhd_overrides jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- skip_uhd stops a matching title from getting a 4K copy at all, rather
    -- than letting the 4K tier fall through to a later route.
    skip_uhd boolean NOT NULL DEFAULT false,
    revision bigint NOT NULL DEFAULT nextval('request_editor_revision_seq'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT request_routes_media_type_check CHECK (media_type IN ('movie', 'series'))
);

CREATE UNIQUE INDEX request_routes_one_fallback ON request_routes (media_type) WHERE is_fallback;
CREATE INDEX request_routes_order ON request_routes (media_type, position);
CREATE TRIGGER request_routes_revision BEFORE INSERT OR UPDATE ON request_routes
    FOR EACH ROW EXECUTE FUNCTION advance_request_editor_revision();

-- Which route sent each target, so an admin can see why a request went where
-- it did. The name is a snapshot: renaming or deleting the route later does not
-- rewrite history.
ALTER TABLE media_request_targets
    ADD COLUMN route_id text,
    ADD COLUMN route_name text NOT NULL DEFAULT '';

-- Carry the Sonarr/Radarr plugin's routing over unchanged. It sent each tier to
-- the first usable server of the media type's kind (by name: enabled, bound to
-- a plugin installation, with an API key) flagged default (HD) or default 4K,
-- and applied that server's anime settings to anime titles. That becomes a
-- fallback route per media type and an Anime route.
-- +goose StatementBegin
WITH kinds (media_type, kind) AS (
    VALUES ('movie', 'radarr'), ('series', 'sonarr')
),
defaults AS (
    SELECT k.media_type, k.kind,
        (SELECT i.id FROM request_integrations i
          WHERE i.enabled AND i.plugin_config->>'service_kind' = k.kind
            AND i.installation_id IS NOT NULL AND i.api_key_ref <> ''
            AND coalesce((i.plugin_config->>'is_default')::boolean, false)
          ORDER BY i.name, i.id LIMIT 1) AS hd_id,
        (SELECT i.id FROM request_integrations i
          WHERE i.enabled AND i.plugin_config->>'service_kind' = k.kind
            AND i.installation_id IS NOT NULL AND i.api_key_ref <> ''
            AND coalesce((i.plugin_config->>'is_default_4k')::boolean, false)
          ORDER BY i.name, i.id LIMIT 1) AS uhd_id
    FROM kinds k
),
fallbacks AS (
    INSERT INTO request_routes (id, media_type, position, name, is_fallback, hd_integration_id, uhd_integration_id)
    SELECT 'fallback-' || d.media_type, d.media_type, 1000, 'Everything else', true, d.hd_id, d.uhd_id
    FROM defaults d
    WHERE d.hd_id IS NOT NULL OR d.uhd_id IS NOT NULL
    RETURNING media_type
),
anime AS (
    SELECT d.media_type, d.kind,
        CASE WHEN coalesce((hd.plugin_config->>'anime_enabled')::boolean, false) THEN hd.id END AS hd_id,
        hd.plugin_config AS hd_config,
        CASE WHEN coalesce((uhd.plugin_config->>'anime_enabled')::boolean, false) THEN uhd.id END AS uhd_id,
        uhd.plugin_config AS uhd_config
    FROM defaults d
    LEFT JOIN request_integrations hd ON hd.id = d.hd_id
    LEFT JOIN request_integrations uhd ON uhd.id = d.uhd_id
)
INSERT INTO request_routes (id, media_type, position, name, conditions,
    hd_integration_id, hd_overrides, uhd_integration_id, uhd_overrides)
SELECT 'anime-' || a.media_type, a.media_type, 0, 'Anime', '{"anime": true}'::jsonb,
    a.hd_id,
    CASE WHEN a.hd_id IS NULL THEN '{}'::jsonb ELSE jsonb_strip_nulls(jsonb_build_object(
        'root_folder', nullif(a.hd_config->>'anime_root_folder', ''),
        'quality_profile_id', a.hd_config->'anime_quality_profile_id',
        'tags', CASE WHEN jsonb_typeof(a.hd_config->'anime_tags') = 'array'
                      AND jsonb_array_length(a.hd_config->'anime_tags') > 0 THEN a.hd_config->'anime_tags' END,
        'series_type', CASE WHEN a.kind = 'sonarr' THEN 'anime' END)) END,
    a.uhd_id,
    CASE WHEN a.uhd_id IS NULL THEN '{}'::jsonb ELSE jsonb_strip_nulls(jsonb_build_object(
        'root_folder', nullif(a.uhd_config->>'anime_root_folder', ''),
        'quality_profile_id', a.uhd_config->'anime_quality_profile_id',
        'tags', CASE WHEN jsonb_typeof(a.uhd_config->'anime_tags') = 'array'
                      AND jsonb_array_length(a.uhd_config->'anime_tags') > 0 THEN a.uhd_config->'anime_tags' END,
        'series_type', CASE WHEN a.kind = 'sonarr' THEN 'anime' END)) END
FROM anime a
WHERE a.hd_id IS NOT NULL OR a.uhd_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
ALTER TABLE media_request_targets DROP COLUMN route_name, DROP COLUMN route_id;
DROP TABLE request_routes;
