-- +goose Up
-- Watch-progress model change: `completed` is a one-way watched latch and
-- completed rows hold no resume point (position_seconds = 0), mirroring
-- Jellyfin. The Continue Watching predicate becomes position_seconds > 0, so
-- a rewatch of a watched item re-enters the row naturally while keeping its
-- watched state. Legacy completed rows pinned position_seconds to
-- duration_seconds; reset them so they don't surface as phantom resume
-- entries under the new predicate.
UPDATE user_watch_progress
SET position_seconds = 0
WHERE completed = TRUE
  AND position_seconds <> 0;

-- +goose Down
-- Restore the legacy convention (completed rows pinned to duration) so the
-- old `completed = FALSE` resume predicate doesn't surface watched items.
UPDATE user_watch_progress
SET position_seconds = duration_seconds
WHERE completed = TRUE
  AND duration_seconds > 0;
