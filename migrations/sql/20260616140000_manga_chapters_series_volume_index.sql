-- +goose Up
-- +goose StatementBegin
-- The browse manga count-chip subqueries filter manga_chapters by
-- series_content_id and read/aggregate `volume` (chapter_count = volume IS NULL,
-- volume_count = count(DISTINCT volume)). The existing
-- manga_chapters_series (series_content_id, chapter_index) index doesn't include
-- volume, so count(DISTINCT volume) does a heap fetch per chapter row. Add a
-- covering (series_content_id, volume) index so both count subqueries are
-- index-only.
CREATE INDEX IF NOT EXISTS idx_manga_chapters_series_volume
ON public.manga_chapters USING btree (series_content_id, volume);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_manga_chapters_series_volume;
-- +goose StatementEnd
