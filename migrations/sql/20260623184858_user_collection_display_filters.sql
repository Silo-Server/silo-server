-- +goose Up
ALTER TABLE public.user_personal_collections
    ADD COLUMN IF NOT EXISTS watch_filter text NOT NULL DEFAULT 'all',
    ADD COLUMN IF NOT EXISTS media_filter text NOT NULL DEFAULT 'all';

ALTER TABLE public.user_personal_collections
    DROP CONSTRAINT IF EXISTS user_personal_collections_watch_filter_check;
ALTER TABLE public.user_personal_collections
    ADD CONSTRAINT user_personal_collections_watch_filter_check
    CHECK (watch_filter IN ('all', 'unwatched', 'watched'));

ALTER TABLE public.user_personal_collections
    DROP CONSTRAINT IF EXISTS user_personal_collections_media_filter_check;
ALTER TABLE public.user_personal_collections
    ADD CONSTRAINT user_personal_collections_media_filter_check
    CHECK (media_filter IN ('all', 'movie', 'series'));

-- +goose Down
ALTER TABLE public.user_personal_collections
    DROP CONSTRAINT IF EXISTS user_personal_collections_media_filter_check;
ALTER TABLE public.user_personal_collections
    DROP CONSTRAINT IF EXISTS user_personal_collections_watch_filter_check;
ALTER TABLE public.user_personal_collections
    DROP COLUMN IF EXISTS media_filter,
    DROP COLUMN IF EXISTS watch_filter;
