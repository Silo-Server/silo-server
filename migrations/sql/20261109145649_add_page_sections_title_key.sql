-- title_key names the translation catalog message a section's title came
-- from, and title_args fills its placeholders. A key is set only while the
-- title is text the server wrote (a default row, a generated library row);
-- clients then translate the key and fall back to title. Any update that
-- changes the title without also changing title_key clears both, so a
-- rename by any writer, older binaries included, leaves the typed title
-- unkeyed.
--
-- The backfill keys the rows that still hold a default's exact text.

-- +goose Up
ALTER TABLE page_sections
    ADD COLUMN title_key TEXT NOT NULL DEFAULT '',
    ADD COLUMN title_args JSONB;

-- +goose StatementBegin
CREATE FUNCTION page_sections_title_key_clear() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.title_key := '';
    NEW.title_args := NULL;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER page_sections_title_key_clear BEFORE UPDATE ON page_sections
    FOR EACH ROW
    WHEN (NEW.title IS DISTINCT FROM OLD.title AND NEW.title_key = OLD.title_key AND OLD.title_key <> '')
    EXECUTE FUNCTION page_sections_title_key_clear();

UPDATE page_sections ps
SET title_key = d.key
FROM (VALUES
    ('Continue Watching', 'home.section.continueWatching'),
    ('Continue Listening', 'home.section.continueListening'),
    ('Continue Reading', 'home.section.continueReading'),
    ('Trending This Week', 'home.section.trendingThisWeek'),
    ('Recently Added', 'home.section.recentlyAdded'),
    ('Recently Added Movies', 'home.section.recentlyAddedMovies'),
    ('Recently Added TV', 'home.section.recentlyAddedTv'),
    ('Recently Added Audiobooks', 'home.section.recentlyAddedAudiobooks'),
    ('Recently Added Ebooks', 'home.section.recentlyAddedEbooks'),
    ('Recently Added Manga', 'home.section.recentlyAddedManga'),
    ('Recently Released', 'home.section.recentlyReleased'),
    ('Recently Released Movies', 'home.section.recentlyReleasedMovies'),
    ('Recently Released Episodes', 'home.section.recentlyReleasedEpisodes'),
    ('Recently Released Audiobooks', 'home.section.recentlyReleasedAudiobooks'),
    ('Recently Released Ebooks', 'home.section.recentlyReleasedEbooks'),
    ('Recently Released Manga', 'home.section.recentlyReleasedManga'),
    ('Recommended for You', 'home.section.recommendedForYou'),
    ('Because You Watched', 'home.section.becauseYouWatched'),
    ('Returning Shows', 'home.section.returningShows'),
    ('Top Rated', 'home.section.topRated'),
    ('Top Rated Movies', 'home.section.topRatedMovies'),
    ('Top Rated TV', 'home.section.topRatedTv'),
    ('Hidden Gems', 'home.section.hiddenGems'),
    ('Seasonal Picks', 'home.section.seasonalPicks'),
    ('Random Picks', 'home.section.randomPicks'),
    ('Next in Your Series', 'home.section.nextInYourSeries')
) AS d(title, key)
WHERE ps.title = d.title;

UPDATE page_sections ps
SET title_key = g.key,
    title_args = jsonb_build_object('library', mf.name)
FROM media_folders mf,
    (VALUES
        ('Recently Added in ', 'home.section.recentlyAddedIn'),
        ('Recently Released in ', 'home.section.recentlyReleasedIn'),
        ('Recently Released Episodes in ', 'home.section.recentlyReleasedEpisodesIn')
    ) AS g(prefix, key)
WHERE ps.scope = 'home'
  AND ps.library_id IS NULL
  AND ps.config->>'generated_source' = 'home_library_recent'
  AND COALESCE(ps.config->>'generated_library_id', ps.config->>'filter_library_id') = mf.id::text
  AND ps.title = g.prefix || mf.name;

-- +goose Down
DROP TRIGGER page_sections_title_key_clear ON page_sections;
DROP FUNCTION page_sections_title_key_clear();
ALTER TABLE page_sections
    DROP COLUMN title_args,
    DROP COLUMN title_key;
