-- +goose Up
-- episode_labels is how clients label a series' episodes: '' for season and
-- episode numbers, 'air_date' for a series whose numbers are synthetic (a
-- sports league Sportarr numbers S2026E296). The numbers still order episodes.
ALTER TABLE media_items
  ADD COLUMN episode_labels TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE media_items
  DROP COLUMN episode_labels;
