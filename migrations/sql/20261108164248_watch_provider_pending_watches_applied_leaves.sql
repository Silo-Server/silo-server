-- +goose Up
-- A whole-series or whole-season watched marker stays kept after it matches:
-- it covers episodes a library may only gain later. applied_leaves is how
-- many local episodes it covered when last applied, so a later sync applies it
-- again only once it covers more.
ALTER TABLE public.watch_provider_pending_watches
    ADD COLUMN applied_leaves integer NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE public.watch_provider_pending_watches
    DROP COLUMN IF EXISTS applied_leaves;
