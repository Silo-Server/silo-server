-- +goose Up
-- A hero clip is a derived asset: one short clip of a title's trailer, played
-- muted behind the home hero's backdrop. The row publishes the object the clip
-- lives at, which is what lets the weekly orphan sweep tell a live clip from
-- one that a recipe change superseded.
CREATE TABLE IF NOT EXISTS public.hero_clips (
    content_id  text        PRIMARY KEY REFERENCES public.media_items (content_id) ON DELETE CASCADE,
    clip_path   text        NOT NULL,
    recipe      text        NOT NULL,
    source      text        NOT NULL DEFAULT '',
    source_key  text        NOT NULL DEFAULT '',
    duration_ms integer     NOT NULL DEFAULT 0,
    width       integer     NOT NULL DEFAULT 0,
    height      integer     NOT NULL DEFAULT 0,
    bytes       bigint      NOT NULL DEFAULT 0,
    state       text        NOT NULL DEFAULT 'ready' CHECK (state IN ('ready', 'failed')),
    error       text        NOT NULL DEFAULT '',
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- A failed clip is retried after a pause, so the generator reads by state.
CREATE INDEX IF NOT EXISTS idx_hero_clips_state ON public.hero_clips (state, updated_at);

-- The queue admits every prefix a namespace may hand it, and the sweep queues a
-- hero clip it finds unreferenced, so its pattern has to be admitted here.
ALTER TABLE public.blob_gc_queue DROP CONSTRAINT blob_gc_queue_prefix_check;
ALTER TABLE public.blob_gc_queue ADD CONSTRAINT blob_gc_queue_prefix_check CHECK (
    prefix ~ '^chapter-images/[1-9][0-9]*/$'
    OR prefix ~ '^chapter-images/[1-9][0-9]*/((0|[1-9][0-9]*)(-[a-f0-9]{64})?|marker-[a-f0-9]{64}-[a-f0-9]{64})/w[1-9][0-9]*\.webp$'
    OR prefix ~ '^trickplay/[1-9][0-9]*/[1-9][0-9]*/$'
    OR prefix ~ '^hero-clips/[a-f0-9]{32}/v[1-9][0-9]*-w[1-9][0-9]*-crf[1-9][0-9]*-a[1-9][0-9]*k\.mp4$');

-- +goose Down
-- Queued hero clip prefixes have to go before the constraint that admitted them
-- is restored, or restoring it would fail on the rows still holding one.
DELETE FROM public.blob_gc_queue WHERE prefix LIKE 'hero-clips/%';
DROP INDEX IF EXISTS public.idx_hero_clips_state;
DROP TABLE IF EXISTS public.hero_clips;
ALTER TABLE public.blob_gc_queue DROP CONSTRAINT blob_gc_queue_prefix_check;
ALTER TABLE public.blob_gc_queue ADD CONSTRAINT blob_gc_queue_prefix_check CHECK (
    prefix ~ '^chapter-images/[1-9][0-9]*/$'
    OR prefix ~ '^chapter-images/[1-9][0-9]*/((0|[1-9][0-9]*)(-[a-f0-9]{64})?|marker-[a-f0-9]{64}-[a-f0-9]{64})/w[1-9][0-9]*\.webp$'
    OR prefix ~ '^trickplay/[1-9][0-9]*/[1-9][0-9]*/$');

