-- +goose Up
-- Fields an admin locked on one season or episode, using the MetadataField
-- numbers media_items.locked_fields uses. Refreshes keep a locked field's value.
ALTER TABLE public.seasons
    ADD COLUMN locked_fields integer[] DEFAULT '{}'::integer[] NOT NULL;
ALTER TABLE public.episodes
    ADD COLUMN locked_fields integer[] DEFAULT '{}'::integer[] NOT NULL;

-- +goose Down
ALTER TABLE public.episodes DROP COLUMN locked_fields;
ALTER TABLE public.seasons DROP COLUMN locked_fields;
