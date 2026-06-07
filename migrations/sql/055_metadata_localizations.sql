-- +goose Up
-- +goose StatementBegin
ALTER TABLE media_items
  ADD COLUMN default_metadata_language text NOT NULL DEFAULT 'en';

ALTER TABLE seasons
  ADD COLUMN default_metadata_language text NOT NULL DEFAULT 'en';

ALTER TABLE episodes
  ADD COLUMN default_metadata_language text NOT NULL DEFAULT 'en';

CREATE TABLE media_item_localizations (
  content_id text NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE,
  language text NOT NULL,
  title text,
  sort_title text,
  overview text,
  tagline text,
  poster_path text,
  poster_thumbhash text,
  backdrop_path text,
  backdrop_thumbhash text,
  logo_path text,
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  updated_at timestamp with time zone NOT NULL DEFAULT now(),
  PRIMARY KEY (content_id, language)
);

CREATE TABLE season_localizations (
  season_content_id text NOT NULL REFERENCES seasons(content_id) ON DELETE CASCADE,
  language text NOT NULL,
  title text,
  overview text,
  poster_path text,
  poster_thumbhash text,
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  updated_at timestamp with time zone NOT NULL DEFAULT now(),
  PRIMARY KEY (season_content_id, language)
);

CREATE TABLE episode_localizations (
  episode_content_id text NOT NULL REFERENCES episodes(content_id) ON DELETE CASCADE,
  language text NOT NULL,
  title text,
  overview text,
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  updated_at timestamp with time zone NOT NULL DEFAULT now(),
  PRIMARY KEY (episode_content_id, language)
);

CREATE INDEX idx_media_item_localizations_language ON media_item_localizations(language);
CREATE INDEX idx_season_localizations_language ON season_localizations(language);
CREATE INDEX idx_episode_localizations_language ON episode_localizations(language);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_episode_localizations_language;
DROP INDEX IF EXISTS idx_season_localizations_language;
DROP INDEX IF EXISTS idx_media_item_localizations_language;

DROP TABLE IF EXISTS episode_localizations;
DROP TABLE IF EXISTS season_localizations;
DROP TABLE IF EXISTS media_item_localizations;

ALTER TABLE episodes
  DROP COLUMN IF EXISTS default_metadata_language;

ALTER TABLE seasons
  DROP COLUMN IF EXISTS default_metadata_language;

ALTER TABLE media_items
  DROP COLUMN IF EXISTS default_metadata_language;
-- +goose StatementEnd
