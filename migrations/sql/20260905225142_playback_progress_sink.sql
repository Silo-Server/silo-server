-- +goose Up
CREATE TABLE public.playback_progress_sinks (
    user_id integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id text NOT NULL,
    session_id text NOT NULL,
    media_item_id text NOT NULL,
    attempt_id text NOT NULL,
    incarnation text NOT NULL,
    owner_id text NOT NULL,
    epoch bigint NOT NULL CHECK (epoch > 0),
    state text NOT NULL CHECK (state IN ('active', 'stopped')),
    last_sequence bigint NOT NULL CHECK (last_sequence >= 0),
    document jsonb NOT NULL CHECK (jsonb_typeof(document) = 'object' AND octet_length(document::text) <= 262144),
    PRIMARY KEY (user_id, profile_id, session_id)
);

-- +goose Down
DROP TABLE public.playback_progress_sinks;
