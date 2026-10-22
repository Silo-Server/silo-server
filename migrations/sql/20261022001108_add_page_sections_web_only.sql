-- web_only keeps a hero row on the web app alone. While the row is featured,
-- only a caller that declares the web client family (X-Silo-Client-Family:
-- web) receives it; native apps and every other client never see the row.
-- The flag does nothing while the row is not featured.

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
