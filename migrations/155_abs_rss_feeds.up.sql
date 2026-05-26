-- Audiobookshelf-style RSS podcast feeds. Each row exposes one
-- audiobook (library_item_id) as a public RSS XML feed reachable at
-- /feed/{slug}.xml — slug is the unguessable capability token.
-- closed_at is NULL while the feed is active; closing soft-deletes
-- so re-opening creates a new row with a new slug.

CREATE TABLE IF NOT EXISTS public.abs_rss_feeds (
    id              text PRIMARY KEY,
    user_id         integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id      uuid,
    library_item_id text NOT NULL REFERENCES public.media_items(content_id) ON DELETE CASCADE,
    slug            text NOT NULL,
    minified        boolean NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    closed_at       timestamptz
);

CREATE UNIQUE INDEX IF NOT EXISTS abs_rss_feeds_slug_uniq
    ON public.abs_rss_feeds (slug);

CREATE INDEX IF NOT EXISTS abs_rss_feeds_user_profile_idx
    ON public.abs_rss_feeds (
        user_id,
        COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
    );
