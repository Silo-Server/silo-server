-- +goose Up
-- +goose StatementBegin
-- Adds the combined account-channel mode: per-episode sends all day plus a
-- daily digest recapping everything since the previous digest. Applies to
-- both account-level channels (email, Discord DMs).
ALTER TABLE public.notification_email_prefs
    DROP CONSTRAINT notification_email_prefs_mode_check;
ALTER TABLE public.notification_email_prefs
    ADD CONSTRAINT notification_email_prefs_mode_check
    CHECK (mode IN ('off', 'per_episode', 'daily_digest', 'per_episode_and_digest'));

ALTER TABLE public.notification_discord_prefs
    DROP CONSTRAINT notification_discord_prefs_mode_check;
ALTER TABLE public.notification_discord_prefs
    ADD CONSTRAINT notification_discord_prefs_mode_check
    CHECK (mode IN ('off', 'per_episode', 'daily_digest', 'per_episode_and_digest'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Coerce combined-mode rows to per_episode before restoring the narrower
-- constraint, so the rollback cannot fail on existing data.
UPDATE public.notification_email_prefs
    SET mode = 'per_episode' WHERE mode = 'per_episode_and_digest';
UPDATE public.notification_discord_prefs
    SET mode = 'per_episode' WHERE mode = 'per_episode_and_digest';

ALTER TABLE public.notification_email_prefs
    DROP CONSTRAINT notification_email_prefs_mode_check;
ALTER TABLE public.notification_email_prefs
    ADD CONSTRAINT notification_email_prefs_mode_check
    CHECK (mode IN ('off', 'per_episode', 'daily_digest'));

ALTER TABLE public.notification_discord_prefs
    DROP CONSTRAINT notification_discord_prefs_mode_check;
ALTER TABLE public.notification_discord_prefs
    ADD CONSTRAINT notification_discord_prefs_mode_check
    CHECK (mode IN ('off', 'per_episode', 'daily_digest'));
-- +goose StatementEnd
