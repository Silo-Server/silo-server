-- Items inside a playlist. library_item_id is NOT FK'd (decoupled to
-- allow future episode support); episode_id defaults to '' (empty) so
-- the unique constraint works without COALESCE.
--
-- position is a sort hint; gaps are allowed (no compaction on remove).

CREATE TABLE IF NOT EXISTS public.abs_playlist_items (
    playlist_id     text NOT NULL REFERENCES public.abs_playlists(id) ON DELETE CASCADE,
    library_item_id text NOT NULL,
    episode_id      text NOT NULL DEFAULT '',
    position        integer NOT NULL,
    added_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (playlist_id, library_item_id, episode_id)
);

CREATE INDEX IF NOT EXISTS abs_playlist_items_playlist_position_idx
    ON public.abs_playlist_items (playlist_id, position);
