-- +goose Up
-- poster_source_path keeps the provider-origin artwork path (tmdb:// /
-- tvdb:// plugin scheme or absolute provider URL) when image caching rewrites
-- poster_path to a local storage key. Outbound notification embeds (Discord)
-- build public provider-CDN poster URLs from it, since local storage URLs
-- must never appear in outbound payloads. Empty for rows whose poster_path
-- still is the provider path, and for locally sourced artwork; populated on
-- the next metadata refresh after upgrade.
ALTER TABLE media_items ADD COLUMN poster_source_path text;

-- +goose Down
ALTER TABLE media_items DROP COLUMN poster_source_path;
