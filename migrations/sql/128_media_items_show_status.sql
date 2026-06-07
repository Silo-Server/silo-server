-- +goose Up
-- +goose StatementBegin
-- show_status captures the series lifecycle ("returning", "ended", "cancelled",
-- "in_production") sourced from metadata providers. Empty string when unknown
-- or not applicable (movies). Used by the card overlay system to render a
-- Show Status badge on series cards.
ALTER TABLE media_items
  ADD COLUMN show_status TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE media_items
  DROP COLUMN show_status;
-- +goose StatementEnd
