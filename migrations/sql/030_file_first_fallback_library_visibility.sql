-- +goose Up
-- +goose StatementBegin
ALTER TABLE media_items
  ADD COLUMN episode_metadata_incomplete BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN episode_metadata_last_checked_at TIMESTAMPTZ;

ALTER TABLE seasons
  ADD COLUMN metadata_source TEXT NOT NULL DEFAULT 'provider';

ALTER TABLE episodes
  ADD COLUMN metadata_source TEXT NOT NULL DEFAULT 'provider';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE episodes
  DROP COLUMN metadata_source;

ALTER TABLE seasons
  DROP COLUMN metadata_source;

ALTER TABLE media_items
  DROP COLUMN episode_metadata_last_checked_at,
  DROP COLUMN episode_metadata_incomplete;
-- +goose StatementEnd
