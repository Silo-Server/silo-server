-- +goose Up
-- +goose StatementBegin
CREATE INDEX user_watchlist_media_item_idx ON public.user_watchlist (media_item_id);
CREATE INDEX user_favorites_media_item_idx ON public.user_favorites (media_item_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX public.user_favorites_media_item_idx;
DROP INDEX public.user_watchlist_media_item_idx;
-- +goose StatementEnd
