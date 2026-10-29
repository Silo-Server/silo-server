-- +goose Up
-- Each account's most recent activity, kept current by the activity log
-- consumer. The admin account list read it as MAX(timestamp) over the whole
-- activity_log, which grows with every request and took seconds per page.
-- It lives outside users so recording activity never bumps admin_revision,
-- the users row's edit validator.
CREATE TABLE public.user_last_activity (
    user_id        bigint PRIMARY KEY REFERENCES public.users(id) ON DELETE CASCADE,
    last_active_at timestamp with time zone NOT NULL
);

INSERT INTO public.user_last_activity (user_id, last_active_at)
SELECT a.user_id, MAX(a."timestamp")
FROM public.activity_log a
JOIN public.users u ON u.id = a.user_id
GROUP BY a.user_id;

-- +goose Down
DROP TABLE public.user_last_activity;
