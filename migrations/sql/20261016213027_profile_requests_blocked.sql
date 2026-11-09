-- +goose Up
-- A household manager can keep one profile out of requests and request
-- discovery while the account, and its other profiles, keep them. The column
-- records a block rather than an allow so a constant false default leaves
-- every existing profile as it is; a constant default is a catalog-only
-- change and rewrites no row.
ALTER TABLE public.user_profiles
    ADD COLUMN IF NOT EXISTS requests_blocked boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE public.user_profiles
    DROP COLUMN IF EXISTS requests_blocked;
