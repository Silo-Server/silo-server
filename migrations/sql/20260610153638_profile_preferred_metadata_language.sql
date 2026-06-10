-- +goose Up
-- +goose StatementBegin
-- Per-profile metadata (presentation) language. Empty = inherit the library's
-- metadata_language. When set, catalog serving prefers this language's
-- localizations, and the on-view AI translation flow targets it.
ALTER TABLE user_profiles
    ADD COLUMN preferred_metadata_language text NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE user_profiles DROP COLUMN preferred_metadata_language;
-- +goose StatementEnd
