-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.user_collection_sort_preferences
    DROP CONSTRAINT user_collection_sort_preferences_kind_check;
ALTER TABLE public.user_collection_sort_preferences
    ADD CONSTRAINT user_collection_sort_preferences_kind_check
    CHECK (collection_kind IN ('library', 'user', 'watchlist', 'favorites'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM public.user_collection_sort_preferences
WHERE collection_kind IN ('watchlist', 'favorites');
ALTER TABLE public.user_collection_sort_preferences
    DROP CONSTRAINT user_collection_sort_preferences_kind_check;
ALTER TABLE public.user_collection_sort_preferences
    ADD CONSTRAINT user_collection_sort_preferences_kind_check
    CHECK (collection_kind IN ('library', 'user'));
-- +goose StatementEnd
