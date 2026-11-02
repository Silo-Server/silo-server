-- Rewrite every stored ui.card_presentation caption to "title_metadata".
--
-- Settings manifest revision 24 retires the caption choice. People hid card
-- titles and then could not tell what a movie or series was, so every card
-- now shows its title and details. "title" and "artwork" stay valid so an
-- older client can still write them, but an older client also renders them,
-- so the stored values are reset rather than left to hide titles there.
-- poster_size is kept as stored.
--
-- The revision moves so a client holding the old row sees a conflict and
-- re-reads it. updated_at is left alone: the admin device surfaces sort and
-- label devices by it, and this is not a change anyone made to a device.

-- +goose Up
-- +goose StatementBegin
UPDATE public.user_setting_values
SET value = jsonb_set(value, '{caption}', '"title_metadata"'::jsonb),
    revision = revision + 1
WHERE key = 'ui.card_presentation'
  AND value->>'caption' <> 'title_metadata';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Deliberately empty: nothing records which rows held another caption, and
-- current clients ignore the field.
SELECT 1;
-- +goose StatementEnd
