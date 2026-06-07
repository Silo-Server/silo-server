-- +goose Up
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_media_items_studios
ON public.media_items USING gin (studios);

CREATE INDEX IF NOT EXISTS idx_media_items_networks
ON public.media_items USING gin (networks);

CREATE INDEX IF NOT EXISTS idx_media_items_countries
ON public.media_items USING gin (countries);

CREATE INDEX IF NOT EXISTS idx_media_items_original_language
ON public.media_items USING btree (original_language)
WHERE (original_language <> ''::text);

CREATE INDEX IF NOT EXISTS idx_media_items_content_rating
ON public.media_items USING btree (content_rating)
WHERE ((content_rating IS NOT NULL) AND (content_rating <> ''::text));

CREATE INDEX IF NOT EXISTS idx_media_files_active_content_folder
ON public.media_files USING btree (content_id, media_folder_id)
WHERE (missing_since IS NULL);

CREATE INDEX IF NOT EXISTS idx_episodes_series_air_date
ON public.episodes USING btree (series_id, air_date DESC)
WHERE (air_date IS NOT NULL);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.idx_episodes_series_air_date;

DROP INDEX IF EXISTS public.idx_media_files_active_content_folder;

DROP INDEX IF EXISTS public.idx_media_items_content_rating;

DROP INDEX IF EXISTS public.idx_media_items_original_language;

DROP INDEX IF EXISTS public.idx_media_items_countries;

DROP INDEX IF EXISTS public.idx_media_items_networks;

DROP INDEX IF EXISTS public.idx_media_items_studios;
-- +goose StatementEnd
