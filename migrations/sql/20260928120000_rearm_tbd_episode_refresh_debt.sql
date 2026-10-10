-- Episodes whose real title is "TBA" or "TBD" were kept in metadata refresh
-- debt as if the title were a placeholder, and after three attempts those rows
-- were parked for 90 days. The completeness check now only treats that title
-- as provisional while the episode is undated or aired recently
-- (EpisodeRecentStillWindow, 45 days), and the refresh debt for such an
-- episode is due again once that window closes.
--
-- Pull forward only the parked rows the corrected check would now clear:
-- provider-matched, non-fallback episodes titled TBA/TBD, plus the season and
-- series rows carrying episode-incomplete debt for them. Rows for episodes that
-- aired more than 45 days ago come due in one hour, not immediately, so
-- application instances still running the previous release during an upgrade
-- do not claim them and park them again with the old check. Rows for episodes
-- still inside the window come due when it closes, as new debt does. Each
-- refresh re-evaluates its target and deletes or recomputes the debt, leaving
-- any other gaps in place.

-- +goose Up
-- +goose StatementBegin
CREATE TEMP TABLE tbd_cleared_episodes ON COMMIT DROP AS
SELECT e.content_id,
       e.season_id,
       e.series_id,
       CASE
           WHEN e.air_date < (NOW() - INTERVAL '45 days') THEN NOW() + INTERVAL '1 hour'
           ELSE e.air_date + INTERVAL '45 days 1 minute'
       END AS due_at
FROM episodes e
WHERE BTRIM(COALESCE(e.title, '')) ~* '^(tba|tbd)$'
  AND LOWER(BTRIM(COALESCE(e.metadata_source, ''))) <> 'scanner_fallback'
  AND (
      BTRIM(COALESCE(e.tmdb_id, '')) <> ''
      OR BTRIM(COALESCE(e.tvdb_id, '')) <> ''
      OR BTRIM(COALESCE(e.imdb_id, '')) <> ''
  )
  AND e.air_date IS NOT NULL;

UPDATE metadata_refresh_debt d
SET next_refresh_at = c.due_at,
    updated_at = NOW()
FROM tbd_cleared_episodes c
WHERE d.target_type = 'episode'
  AND d.content_id = c.content_id
  AND d.next_refresh_at > c.due_at;

UPDATE metadata_refresh_debt d
SET next_refresh_at = c.due_at,
    updated_at = NOW()
FROM (
    SELECT season_id, MIN(due_at) AS due_at
    FROM tbd_cleared_episodes
    WHERE season_id IS NOT NULL
    GROUP BY season_id
) c
WHERE d.target_type = 'season'
  AND (d.reason_mask & 1) <> 0
  AND d.content_id = c.season_id
  AND d.next_refresh_at > c.due_at;

UPDATE metadata_refresh_debt d
SET next_refresh_at = c.due_at,
    updated_at = NOW()
FROM (
    SELECT series_id, MIN(due_at) AS due_at
    FROM tbd_cleared_episodes
    GROUP BY series_id
) c
WHERE d.target_type = 'item'
  AND (d.reason_mask & 1) <> 0
  AND d.content_id = c.series_id
  AND d.next_refresh_at > c.due_at;
-- +goose StatementEnd

-- +goose Down
-- Nothing to undo: moving a refresh earlier only schedules a re-evaluation.
SELECT 1;
