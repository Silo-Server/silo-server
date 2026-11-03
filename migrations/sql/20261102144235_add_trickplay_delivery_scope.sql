-- +goose Up
-- Confirmation that clients can fetch the published revision's sheets
-- through an external (public or token-authenticated) read endpoint, which
-- can lag behind a storage write. delivery_scope names the endpoint
-- configuration every sheet was fetched through; readers hand out direct
-- endpoint URLs only for a revision confirmed under the current one, and the
-- signed server route otherwise. delivery_checked_at spaces attempts.
ALTER TABLE public.media_file_trickplay
    ADD COLUMN delivery_scope text,
    ADD COLUMN delivery_checked_at timestamptz;

-- +goose Down
ALTER TABLE public.media_file_trickplay
    DROP COLUMN delivery_checked_at,
    DROP COLUMN delivery_scope;
