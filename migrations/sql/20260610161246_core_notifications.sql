-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.notifications (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id       integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id    text NULL,
    category      text NOT NULL CHECK (category IN ('request','content','announcement','system','admin')),
    type          text NOT NULL,
    title         text NOT NULL,
    body          text NOT NULL DEFAULT '',
    link          text NULL,
    item_id       text NULL,
    source_event  text NULL,
    dedup_ref     text NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    read_at       timestamptz NULL,
    dismissed_at  timestamptz NULL,
    expires_at    timestamptz NULL,
    CONSTRAINT notifications_profile_fkey FOREIGN KEY (user_id, profile_id) REFERENCES public.user_profiles(user_id, id) ON DELETE CASCADE
);

CREATE INDEX notifications_inbox_idx
    ON public.notifications (user_id, created_at DESC)
    WHERE dismissed_at IS NULL;

CREATE UNIQUE INDEX notifications_dedup_idx
    ON public.notifications (user_id, type, dedup_ref)
    WHERE dedup_ref IS NOT NULL;

CREATE TABLE public.notification_preferences (
    user_id   integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    category  text NOT NULL CHECK (category IN ('request','content','system','admin','content_digest')),
    enabled   boolean NOT NULL,
    PRIMARY KEY (user_id, category)
);

CREATE TABLE public.announcements (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title       text NOT NULL,
    body        text NOT NULL DEFAULT '',
    audience    jsonb NOT NULL,
    created_by  integer NULL REFERENCES public.users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE public.announcements;
DROP TABLE public.notification_preferences;
DROP TABLE public.notifications;
-- +goose StatementEnd
