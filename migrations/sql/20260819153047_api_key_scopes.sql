-- Add optional scopes to API keys.
--
-- An empty array keeps today's behavior: the key acts with the owning user's
-- full role everywhere. A non-empty array turns the key into an allowlist
-- credential — it may only call the routes its scopes name (enforced in the
-- auth middleware), so an integration key for machine-to-machine user
-- management can no longer touch server settings, playback, or any other
-- admin surface if it leaks. Scopes only narrow: they never grant a route the
-- owning user's role could not reach.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.api_keys
    ADD COLUMN scopes text[] NOT NULL DEFAULT '{}'::text[];
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.api_keys
    DROP COLUMN scopes;
-- +goose StatementEnd
