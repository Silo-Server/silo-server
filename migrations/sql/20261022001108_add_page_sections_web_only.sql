-- web_only keeps a hero row on the web app alone. While the row is its page's
-- first featured row, the one the web shows as the banner, only a caller that
-- declares the web client family (X-Silo-Client-Family: web) receives it. A
-- later featured row reaches every client as a regular row, and the flag does
-- nothing while the row is not featured.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE page_sections
    ADD COLUMN IF NOT EXISTS web_only BOOLEAN NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE page_sections
    DROP COLUMN IF EXISTS web_only;
-- +goose StatementEnd
