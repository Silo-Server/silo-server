-- +goose Up
-- +goose StatementBegin
-- A profile's email chooses its sends independently: instant emails, a
-- daily digest and a weekly digest, in any combination. mode stays as the
-- closest single value for older clients.
ALTER TABLE public.notification_email_prefs
    ADD COLUMN send_instant boolean NOT NULL DEFAULT false,
    ADD COLUMN send_daily boolean NOT NULL DEFAULT false,
    ADD COLUMN send_weekly boolean NOT NULL DEFAULT false;
UPDATE public.notification_email_prefs SET
    send_instant = mode IN ('per_episode', 'per_episode_and_digest', 'per_episode_and_weekly_digest'),
    send_daily = mode IN ('daily_digest', 'per_episode_and_digest'),
    send_weekly = mode IN ('weekly_digest', 'per_episode_and_weekly_digest');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.notification_email_prefs
    DROP COLUMN send_instant,
    DROP COLUMN send_daily,
    DROP COLUMN send_weekly;
-- +goose StatementEnd
