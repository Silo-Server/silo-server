-- +goose Up
-- +goose StatementBegin
-- A profile's Not interested marks: movies and series it asked recommendations
-- to leave out. The per-user SQLite store keeps the same rows in its
-- not_interested table.
CREATE TABLE public.user_not_interested (
    user_id integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id text NOT NULL,
    media_item_id text NOT NULL,
    added_at timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT user_not_interested_pkey PRIMARY KEY (user_id, profile_id, media_item_id)
);

CREATE INDEX idx_user_not_interested_profile
    ON public.user_not_interested (user_id, profile_id, added_at DESC, media_item_id DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_user_not_interested_profile;

DROP TABLE IF EXISTS public.user_not_interested;
-- +goose StatementEnd
