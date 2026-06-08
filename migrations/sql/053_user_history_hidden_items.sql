-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.user_history_hidden_items (
    user_id integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id text NOT NULL,
    media_item_id text NOT NULL,
    hidden_before timestamp with time zone NOT NULL DEFAULT now(),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT user_history_hidden_items_pkey PRIMARY KEY (user_id, profile_id, media_item_id)
);

CREATE INDEX idx_user_history_hidden_items_profile
    ON public.user_history_hidden_items (user_id, profile_id, hidden_before DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_user_history_hidden_items_profile;

DROP TABLE IF EXISTS public.user_history_hidden_items;
-- +goose StatementEnd
