-- +goose Up
-- +goose StatementBegin
-- Per-profile sort override for a collection's browse view. A collection's own
-- sort_config supplies the default order its creator chose; a row here records
-- that a viewer changed it, and wins over that default for this profile only.
--
-- sort_field = '' is meaningful: it records "this viewer explicitly chose the
-- collection's own source order", which must survive even when the creator
-- later sets a default sort. Absence of a row means "no choice made yet".
--
-- collection_kind separates the two id spaces ('library' = admin/server-wide
-- collections, 'user' = personal ones); ids are unique within a kind but not
-- across them.
CREATE TABLE IF NOT EXISTS public.user_collection_sort_preferences (
    user_id INTEGER NOT NULL,
    profile_id TEXT NOT NULL,
    collection_kind TEXT NOT NULL,
    collection_id TEXT NOT NULL,
    sort_field TEXT NOT NULL DEFAULT '',
    sort_order TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, profile_id, collection_kind, collection_id),
    CONSTRAINT user_collection_sort_preferences_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT user_collection_sort_preferences_profile_fkey
        FOREIGN KEY (user_id, profile_id)
        REFERENCES user_profiles(user_id, id) ON DELETE CASCADE,
    CONSTRAINT user_collection_sort_preferences_kind_check
        CHECK (collection_kind IN ('library', 'user')),
    CONSTRAINT user_collection_sort_preferences_order_check
        CHECK (sort_order IN ('', 'asc', 'desc'))
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.user_collection_sort_preferences;
-- +goose StatementEnd
