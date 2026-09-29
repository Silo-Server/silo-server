-- Episodes whose real title is "TBA" or "TBD" were kept in metadata refresh
-- debt as if the title were a placeholder, and after three attempts those rows
-- were parked for 90 days. The completeness check now only treats that title
-- as provisional while the episode is undated or aired recently. Pull the
-- parked episode rows and their parent series rows forward so the next refresh
-- re-evaluates them with the corrected check; it deletes the false-positive
-- episode debt and recomputes the series flag, leaving any other gaps in place.

-- +goose Up
-- +goose StatementBegin
UPDATE metadata_refresh_debt d
SET next_refresh_at = NOW(),
    updated_at = NOW()
FROM episodes e
WHERE d.target_type = 'episode'
  AND d.content_id = e.content_id
  AND BTRIM(e.title) ~* '^(tba|tbd)$'
  AND d.next_refresh_at > NOW();

UPDATE metadata_refresh_debt d
SET next_refresh_at = NOW(),
    updated_at = NOW()
FROM episodes e
WHERE d.target_type = 'item'
  AND d.content_id = e.series_id
  AND BTRIM(e.title) ~* '^(tba|tbd)$'
  AND d.next_refresh_at > NOW();
-- +goose StatementEnd

-- +goose Down
-- Nothing to undo: moving a refresh earlier only schedules a re-evaluation.
SELECT 1;
