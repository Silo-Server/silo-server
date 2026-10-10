-- +goose Up
-- Expiry clears an artifact's live locator (origin_node_id and the rest) so
-- remote cleanup never mistakes the old node file for a live one. The node it
-- was on is kept here so the prepared-file inventory can still say where an
-- expired file was. NULL on a row that never expired, and on the server.
ALTER TABLE public.download_artifacts ADD COLUMN expired_from_node_id integer;

-- +goose Down
ALTER TABLE public.download_artifacts DROP COLUMN expired_from_node_id;
