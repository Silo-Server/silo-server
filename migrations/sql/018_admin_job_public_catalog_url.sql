-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.admin_jobs
  ADD COLUMN public_url text DEFAULT ''::text NOT NULL,
  ADD COLUMN published_at timestamptz;
-- +goose StatementEnd
