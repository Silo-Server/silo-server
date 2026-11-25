-- +goose Up
-- A server collection can hide, from each viewer, the titles that viewer's
-- profile has watched. Members stay shared; the filter applies when a profile
-- reads the collection.
ALTER TABLE public.library_collections
    ADD COLUMN IF NOT EXISTS hide_watched boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE public.library_collections
    DROP COLUMN IF EXISTS hide_watched;
