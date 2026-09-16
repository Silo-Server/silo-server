-- +goose Up
-- +goose StatementBegin
-- Provider artwork caching defaults to on now that local storage exists, but
-- migration 035 still seeds metadata.cache_images=false on every new database
-- before the wizard runs, so a fresh install never saw the new default. A
-- database with no accounts has had no administrator to choose; flip only
-- those. Anything with a user keeps whatever value it has.
UPDATE server_settings
SET value = 'true'
WHERE key = 'metadata.cache_images'
  AND value = 'false'
  AND NOT EXISTS (SELECT 1 FROM users);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Intentionally empty: the flipped row is indistinguishable from an
-- administrator's choice once the wizard has run.
-- +goose StatementEnd
