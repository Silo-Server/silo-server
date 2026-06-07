-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS public.episode_libraries (
    episode_id text NOT NULL,
    media_folder_id integer NOT NULL,
    first_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT episode_libraries_pkey PRIMARY KEY (episode_id, media_folder_id),
    CONSTRAINT episode_libraries_episode_id_fkey FOREIGN KEY (episode_id) REFERENCES public.episodes(content_id) ON DELETE CASCADE,
    CONSTRAINT episode_libraries_media_folder_id_fkey FOREIGN KEY (media_folder_id) REFERENCES public.media_folders(id) ON DELETE CASCADE
);

INSERT INTO public.episode_libraries (episode_id, media_folder_id, first_seen_at)
SELECT mf.episode_id, mf.media_folder_id, MIN(mf.created_at) AS first_seen_at
FROM public.media_files mf
JOIN public.episodes e ON e.content_id = mf.episode_id
WHERE mf.episode_id IS NOT NULL
  AND mf.missing_since IS NULL
GROUP BY mf.episode_id, mf.media_folder_id
ON CONFLICT (episode_id, media_folder_id) DO NOTHING;

CREATE INDEX IF NOT EXISTS idx_episode_libraries_folder_episode
ON public.episode_libraries USING btree (media_folder_id, episode_id);

CREATE INDEX IF NOT EXISTS idx_episode_libraries_folder_first_seen
ON public.episode_libraries USING btree (media_folder_id, first_seen_at DESC, episode_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.idx_episode_libraries_folder_first_seen;

DROP INDEX IF EXISTS public.idx_episode_libraries_folder_episode;

DROP TABLE IF EXISTS public.episode_libraries;
-- +goose StatementEnd
