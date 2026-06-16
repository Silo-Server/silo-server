-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.user_hidden_recommendations (
    user_id integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id text NOT NULL,
    media_item_id text NOT NULL,
    hidden_at timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT user_hidden_recommendations_pkey PRIMARY KEY (user_id, profile_id, media_item_id)
);

CREATE INDEX idx_user_hidden_recommendations_profile
    ON public.user_hidden_recommendations (user_id, profile_id, hidden_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_user_hidden_recommendations_profile;

DROP TABLE IF EXISTS public.user_hidden_recommendations;
-- +goose StatementEnd
