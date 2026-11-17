-- +goose Up
-- +goose StatementBegin
-- One row per watchlist title entry that promotion moved onto the library
-- watchlist, written in the promotion's transaction. The watchlist arrival
-- notice reads it to find profiles whose entry was promoted between the item's
-- arrival and the notification fanout. The library watchlist itself can live
-- in a per-user SQLite store the fanout cannot query, and user_watchlist has
-- no index by item.
--
-- media_item_id is the catalog item the entry moved onto. Rows are short-lived:
-- notification retention deletes them once promoted_at passes the fanout
-- staleness horizon, after which they can no longer match. Profiles may live
-- in the SQLite user store, so there is no profile foreign key; deleting a
-- profile removes its rows explicitly, as for user_watchlist_titles.
CREATE TABLE public.watchlist_title_promotions (
    user_id integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id text NOT NULL,
    media_item_id text NOT NULL,
    -- The title entry's added_at, which the library entry also keeps.
    title_added_at timestamptz NOT NULL,
    -- Every TMDB ID the title held when it was promoted. The catalog item may
    -- not carry a former one, and a request made under it must still be found.
    title_tmdb_ids integer[] NOT NULL DEFAULT '{}',
    promoted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, profile_id, media_item_id)
);
CREATE INDEX idx_watchlist_title_promotions_item
    ON public.watchlist_title_promotions (media_item_id);
CREATE INDEX idx_watchlist_title_promotions_promoted_at
    ON public.watchlist_title_promotions (promoted_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.watchlist_title_promotions;
-- +goose StatementEnd
