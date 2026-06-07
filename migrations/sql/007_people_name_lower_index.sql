-- +goose Up
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_people_name_lower ON public.people USING btree (LOWER(name));
-- +goose StatementEnd
