-- +goose Up
-- +goose StatementBegin
-- Discord DM notification channel (docs/superpowers/plans/notifications).
-- Account-level like email: the OAuth-linked Discord identity belongs to the
-- login account (users), not a profile, so one row carries the identity, the
-- user-chosen mode, and the watermark sweep state. The worker advances the
-- watermark only after a successful DM, so a crash or Discord outage re-sends
-- instead of dropping; the watermark resets to now() on link and on enabling
-- so history never floods a fresh opt-in.
--
-- Deliberately no FK to users: notification tables stay FK-free toward
-- account/profile storage (see 20260611100000). Rows for deleted accounts
-- drop out of the recipient join and are inert.
CREATE TABLE public.notification_discord_prefs (
    user_id integer PRIMARY KEY,
    -- OAuth-linked identity; empty until the user completes the link flow.
    discord_user_id text NOT NULL DEFAULT '',
    discord_username text NOT NULL DEFAULT '',
    -- DM channel cache from POST /users/@me/channels (DM channels are
    -- permanent, so the cache never expires; it is only a saved round-trip).
    dm_channel_id text NOT NULL DEFAULT '',
    mode text NOT NULL DEFAULT 'off'
        CHECK (mode IN ('off', 'per_episode', 'daily_digest')),
    watermark_created_at timestamptz NOT NULL DEFAULT now(),
    watermark_id text NOT NULL DEFAULT '',
    last_digest_at timestamptz,
    last_attempt_at timestamptz,
    consecutive_failures integer NOT NULL DEFAULT 0,
    -- Last DM failure surfaced in the settings UI ('' = healthy). Typically
    -- Discord error 50007: the user does not share a server with the bot.
    link_failure text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- One-time state rows for the Discord OAuth linking flow. The state is a
-- 32-byte random value; the atomic DELETE RETURNING on callback makes each
-- state single-use, which is the flow's authentication. Expired rows are
-- reaped by retention.
CREATE TABLE public.notification_discord_link_state (
    state text PRIMARY KEY,
    user_id integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX notification_discord_link_state_expires_idx
    ON public.notification_discord_link_state (expires_at);

-- The Discord sweep reads deliveries by account via
-- notification_deliveries_user_created_idx (created in 20260611201720).
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.notification_discord_link_state;
DROP TABLE IF EXISTS public.notification_discord_prefs;
-- +goose StatementEnd
