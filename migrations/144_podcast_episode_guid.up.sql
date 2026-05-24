-- Podcast-specific episode metadata. RSS episodes are identified by an
-- opaque GUID from the feed (not a sequential episode number), and they
-- carry a remote audio URL rather than a local file path.
--
-- podcast_guid  — the RSS <guid> value; unique per show so the feed
--                 refresher can upsert without duplicating.
-- podcast_audio_url — the remote enclosure URL for streaming / download.
--
-- Both columns are NULL for non-podcast episodes (movies/TV/audiobooks).

ALTER TABLE public.episodes
    ADD COLUMN IF NOT EXISTS podcast_guid      text,
    ADD COLUMN IF NOT EXISTS podcast_audio_url text;

-- Unique constraint: one row per (series_id, podcast_guid) so the feed
-- refresher can ON CONFLICT (series_id, podcast_guid) DO UPDATE.
CREATE UNIQUE INDEX IF NOT EXISTS idx_episodes_podcast_guid
    ON public.episodes (series_id, podcast_guid)
    WHERE podcast_guid IS NOT NULL;
