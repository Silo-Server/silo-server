-- +goose NO TRANSACTION
-- +goose Up
-- Jellyfin user avatars are fetched anonymously by pseudo-user id, and the
-- compat session rows are what map that id back to its account and profile.
-- A failed concurrent build leaves an invalid index, so drop before building.
DROP INDEX CONCURRENTLY IF EXISTS idx_jellycompat_sessions_pseudo_user_id;
CREATE INDEX CONCURRENTLY idx_jellycompat_sessions_pseudo_user_id
ON jellycompat_sessions (pseudo_user_id);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_jellycompat_sessions_pseudo_user_id;
