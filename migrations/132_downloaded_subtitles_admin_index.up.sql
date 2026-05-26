CREATE INDEX IF NOT EXISTS idx_downloaded_subtitles_created
    ON downloaded_subtitles (created_at DESC);

CREATE INDEX IF NOT EXISTS idx_downloaded_subtitles_provider_created
    ON downloaded_subtitles (provider, created_at DESC);
