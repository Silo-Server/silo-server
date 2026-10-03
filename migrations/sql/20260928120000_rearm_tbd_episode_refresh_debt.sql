-- Episodes whose real title is "TBA" or "TBD" were kept in metadata refresh
-- debt as if the title were a placeholder, and after three attempts those rows
-- were parked for 90 days. The completeness check now only treats that title
-- as provisional while the episode is undated or aired recently. Pull forward
-- only the parked rows the corrected check would now clear: provider-matched,
-- non-fallback episodes titled TBA/TBD that aired more than 45 days ago
-- (EpisodeRecentStillWindow), plus the season and series rows carrying
-- episode-incomplete debt for them. The next refresh re-evaluates each target
-- and deletes or recomputes the debt, leaving any other gaps in place.

-- +goose Up
-- +goose StatementBegin
CREATE TEMP TABLE tbd_cleared_episodes ON COMMIT DROP AS
SELECT e.content_id, e.season_id, e.series_id
FROM episodes e
WHERE BTRIM(COALESCE(e.title, '')) ~* '^(tba|tbd)$'
  AND LOWER(BTRIM(COALESCE(e.metadata_source, ''))) <> 'scanner_fallback'
  AND (
      BTRIM(COALESCE(e.tmdb_id, '')) <> ''
      OR BTRIM(COALESCE(e.tvdb_id, '')) <> ''
      OR BTRIM(COALESCE(e.imdb_id, '')) <> ''
  )
  AND e.air_date IS NOT NULL
  AND e.air_date < (NOW() - INTERVAL '45 days');

UPDATE metadata_refresh_debt d
SET next_refresh_at = NOW(),
    updated_at = NOW()
FROM tbd_cleared_episodes c
WHERE d.target_type = 'episode'
  AND d.content_id = c.content_id
  AND d.next_refresh_at > NOW();

UPDATE metadata_refresh_debt d
SET next_refresh_at = NOW(),
    updated_at = NOW()
WHERE d.target_type = 'season'
  AND (d.reason_mask & 1) <> 0
  AND d.next_refresh_at > NOW()
  AND d.content_id IN (SELECT season_id FROM tbd_cleared_episodes WHERE season_id IS NOT NULL);

UPDATE metadata_refresh_debt d
SET next_refresh_at = NOW(),
    updated_at = NOW()
WHERE d.target_type = 'item'
  AND (d.reason_mask & 1) <> 0
  AND d.next_refresh_at > NOW()
  AND d.content_id IN (SELECT series_id FROM tbd_cleared_episodes);
-- +goose StatementEnd

-- +goose Down
-- Nothing to undo: moving a refresh earlier only schedules a re-evaluation.
SELECT 1;
