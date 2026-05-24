DROP INDEX IF EXISTS public.idx_episodes_podcast_guid;

ALTER TABLE public.episodes
    DROP COLUMN IF EXISTS podcast_guid,
    DROP COLUMN IF EXISTS podcast_audio_url;
