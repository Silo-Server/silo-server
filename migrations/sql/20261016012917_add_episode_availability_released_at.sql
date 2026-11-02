-- +goose NO TRANSACTION
-- +goose Up
-- released_at records the availability detector's decision that an episode was
-- released to the server: the row was inserted after its library's first full
-- scan (notification_library_seed_state) and its file arrived within the
-- release window, so it is news rather than back catalog or a late match. It
-- is set to the row's available_at; NULL means the episode was recorded
-- silently. Next Up orders a series by this time when its next episode was
-- released after the profile finished the previous one.
--
-- The migration runs outside a transaction so the column add holds its
-- exclusive lock only briefly; the backfill below then runs on its own and
-- doesn't block Next Up or the notifications fanout reading the table on
-- other nodes during a rolling upgrade. Both statements are safe to rerun.
ALTER TABLE public.episode_availability
    ADD COLUMN IF NOT EXISTS released_at timestamptz;

-- Backfill with the detector's rule, taking the time the row was recorded as
-- the detection time. The 14 days mirror newReleaseWindow
-- (internal/notifications/release_repo.go). The detector saw the episode's
-- library membership when it recorded the row, which began no later than the
-- row. An episode whose membership is gone now (its file is missing), or
-- began after the row (its file was removed and came back), cannot be judged
-- and stays unreleased.
UPDATE public.episode_availability ea
SET released_at = ea.available_at
FROM public.notification_library_seed_state ss
WHERE ss.library_id = ea.library_id
  AND ea.available_at > ss.seeded_at
  AND ea.released_at IS NULL
  AND EXISTS (
      SELECT 1
      FROM public.episode_libraries el
      WHERE el.media_folder_id = ea.library_id
        AND el.episode_id = ea.episode_id
        AND el.first_seen_at >= ea.available_at - interval '14 days'
        AND el.first_seen_at <= ea.available_at
  );

-- +goose Down
ALTER TABLE public.episode_availability DROP COLUMN IF EXISTS released_at;
