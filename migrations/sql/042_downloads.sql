-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.downloads (
    id             text        NOT NULL,
    user_id        integer     NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    media_file_id  integer     NOT NULL REFERENCES media_files(id) ON DELETE CASCADE,
    content_id     text        NOT NULL,
    episode_id     text,
    batch_id       text,
    kind           text        NOT NULL DEFAULT 'queued',
    status         text        NOT NULL DEFAULT 'queued',
    file_size      bigint      NOT NULL DEFAULT 0,
    bytes_sent     bigint      NOT NULL DEFAULT 0,
    error_message  text        NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    completed_at   timestamptz,
    CONSTRAINT downloads_pkey PRIMARY KEY (id),
    CONSTRAINT downloads_kind_check   CHECK (kind   IN ('queued', 'direct')),
    CONSTRAINT downloads_status_check CHECK (status IN ('queued', 'downloading', 'completed', 'failed', 'cancelled'))
);

CREATE INDEX downloads_user_id_idx         ON public.downloads (user_id);
CREATE INDEX downloads_user_status_idx     ON public.downloads (user_id, status);
CREATE INDEX downloads_user_created_at_idx ON public.downloads (user_id, created_at DESC);
CREATE INDEX downloads_batch_id_idx        ON public.downloads (batch_id) WHERE batch_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.downloads;
-- +goose StatementEnd
