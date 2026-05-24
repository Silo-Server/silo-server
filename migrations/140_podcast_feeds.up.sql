-- RSS-feed metadata for subscribed podcasts. One row per podcast
-- media_items row; the feed refresher polls feed_url every
-- refresh_interval_seconds and upserts new episodes into the existing
-- episodes table.

CREATE TABLE IF NOT EXISTS public.podcast_feeds (
    media_item_id              text PRIMARY KEY
                               REFERENCES public.media_items(content_id) ON DELETE CASCADE,
    feed_url                   text NOT NULL,
    etag                       text,
    last_modified              text,
    last_refreshed_at          timestamp with time zone,
    last_refresh_error         text,
    refresh_interval_seconds   integer NOT NULL DEFAULT 600,
    created_at                 timestamp with time zone NOT NULL DEFAULT now(),
    updated_at                 timestamp with time zone NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_podcast_feeds_feed_url
    ON public.podcast_feeds (feed_url);

CREATE INDEX IF NOT EXISTS idx_podcast_feeds_due_for_refresh
    ON public.podcast_feeds (last_refreshed_at);
