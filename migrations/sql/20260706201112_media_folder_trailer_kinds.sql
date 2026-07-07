-- +goose Up
-- +goose StatementBegin
-- Per-library policy for which remote video kinds are fetched/stored during
-- metadata match/refresh. Explicit allow-list: kinds absent from the array are
-- filtered before persisting. Defaults to every kind (the shared vocabulary in
-- internal/models/extras.go); an empty array disables remote videos entirely.
ALTER TABLE media_folders
    ADD COLUMN IF NOT EXISTS trailer_kinds TEXT[] NOT NULL
    DEFAULT ARRAY['trailer','teaser','featurette','clip','behind_the_scenes','bloopers','other']::text[];
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE media_folders DROP COLUMN IF EXISTS trailer_kinds;
-- +goose StatementEnd
