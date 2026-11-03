-- +goose Up
-- Filename parsers before #1313 stored ranges for names that are not
-- multi-episode files, such as an absolute number after the episode
-- ("S02E01 - 013 - Title" became episodes 1-13). A multi-episode file now
-- serves every episode of its range, so clear the parsed ranges and let the
-- next scan, which compares them with the current parser, write them again.
-- Ranges an import supplied are kept.
UPDATE public.media_files
SET multi_episode_start = NULL,
    multi_episode_end = NULL
WHERE multi_episode_end > multi_episode_start
  AND edition_source <> 'import';

-- +goose Down
-- The cleared ranges come back with the next scan.
SELECT 1;
