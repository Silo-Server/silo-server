-- +goose Up
-- Fill user_last_activity from the retained activity log. This runs apart
-- from the table's creation so the scan holds no lock that blocks writes to
-- users. A newer value already recorded is kept; Down leaves the rows,
-- which the previous migration's Down drops with the table.
INSERT INTO public.user_last_activity (user_id, last_active_at)
SELECT a.user_id, MAX(a."timestamp")
FROM public.activity_log a
JOIN public.users u ON u.id = a.user_id
GROUP BY a.user_id
ON CONFLICT (user_id) DO UPDATE SET last_active_at = EXCLUDED.last_active_at
WHERE user_last_activity.last_active_at < EXCLUDED.last_active_at;

-- +goose Down
SELECT 1;
