-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.media_items
ADD COLUMN original_language TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd
