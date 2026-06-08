-- +goose Up
-- +goose StatementBegin
-- Operator-editable display label for an autoscan source. Empty string = unset;
-- the admin UI falls back to connection name / plugin display_name / capability.
ALTER TABLE public.autoscan_sources ADD COLUMN label text NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.autoscan_sources DROP COLUMN label;
-- +goose StatementEnd
