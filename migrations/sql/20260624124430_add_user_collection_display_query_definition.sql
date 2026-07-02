-- +goose Up
-- Personal-collection display filtering is stored as a catalog QueryDefinition
-- fragment (filter-only: match + groups, with type / watched rules), not as
-- bespoke enum columns. This migration is intentionally separate from
-- 20260623184858 because that timestamp may already be applied on live
-- databases with the legacy watch_filter / media_filter DDL.
ALTER TABLE public.user_personal_collections
    ADD COLUMN IF NOT EXISTS display_query_definition jsonb;

-- +goose Down
ALTER TABLE public.user_personal_collections
    DROP COLUMN IF EXISTS display_query_definition;
