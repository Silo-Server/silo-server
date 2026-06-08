-- +goose Up
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_item_people_content_kind_person
ON public.item_people USING btree (content_id, kind, person_id);

CREATE INDEX IF NOT EXISTS idx_user_watch_history_profile_item_completed
ON public.user_watch_history USING btree (user_id, profile_id, media_item_id, watched_at DESC)
WHERE completed = TRUE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.idx_user_watch_history_profile_item_completed;

DROP INDEX IF EXISTS public.idx_item_people_content_kind_person;
-- +goose StatementEnd
