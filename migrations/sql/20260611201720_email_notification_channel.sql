-- +goose Up
-- +goose StatementBegin
-- Email notification channel (docs/superpowers/plans/notifications/06,
-- item 3). Email addresses live on login accounts (users), not profiles, so
-- the mode and dispatch state are account-level. The email worker sweeps
-- notification_deliveries per user and advances the watermark only after a
-- successful SMTP send, so a crash or SMTP outage re-sends instead of
-- dropping; the watermark is initialized to now() whenever the channel is
-- enabled so history never floods a fresh opt-in.
CREATE TABLE public.notification_email_prefs (
    user_id integer PRIMARY KEY,
    mode text NOT NULL DEFAULT 'off'
        CHECK (mode IN ('off', 'per_episode', 'daily_digest')),
    watermark_created_at timestamptz NOT NULL DEFAULT now(),
    watermark_id text NOT NULL DEFAULT '',
    last_digest_at timestamptz,
    last_attempt_at timestamptz,
    consecutive_failures integer NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now()
);
-- Deliberately no FK to users: notification tables stay FK-free toward
-- account/profile storage (see 20260611100000). Rows for deleted accounts
-- drop out of the recipient join and are inert.

-- The email sweep reads deliveries by account, not profile.
CREATE INDEX notification_deliveries_user_created_idx
    ON public.notification_deliveries (user_id, created_at, id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.notification_deliveries_user_created_idx;
DROP TABLE IF EXISTS public.notification_email_prefs;
-- +goose StatementEnd
