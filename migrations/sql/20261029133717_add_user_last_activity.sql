-- +goose Up
-- Each account's most recent activity, kept current by the activity log
-- consumer. The admin account list read it as MAX(timestamp) over the whole
-- activity_log, which grows with every request and took seconds per page.
-- It lives outside users so recording activity never bumps admin_revision,
-- the users row's edit validator. A later migration backfills it, so the
-- foreign key's lock on users is released before that scan of activity_log.
CREATE TABLE public.user_last_activity (
    user_id        bigint PRIMARY KEY REFERENCES public.users(id) ON DELETE CASCADE,
    last_active_at timestamp with time zone NOT NULL
);

-- +goose Down
DROP TABLE public.user_last_activity;
