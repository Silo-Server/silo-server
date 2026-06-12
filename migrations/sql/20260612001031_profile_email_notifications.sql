-- +goose Up
-- +goose StatementBegin
-- Re-keys the email notification channel from login accounts to profiles.
-- Each profile now owns its mode, dispatch watermark, and verified
-- destination address. There is deliberately no fallback to the account
-- email (it would funnel every profile's mail to the account holder), so
-- old account-level opt-ins are NOT carried over: a profile receives
-- nothing until its own address is verified and the mode re-enabled.
-- user_id is denormalized (no FK, no join to profile storage): profiles may
-- live in per-user SQLite stores, so Postgres never joins notification
-- tables against profile tables (see 20260611100000).
DROP TABLE public.notification_email_prefs;

CREATE TABLE public.notification_email_prefs (
    profile_id text PRIMARY KEY,
    user_id integer NOT NULL,
    mode text NOT NULL DEFAULT 'off'
        CHECK (mode IN ('off', 'per_episode', 'daily_digest', 'per_episode_and_digest')),
    -- Verified destination address; '' = fall back to users.email.
    custom_email text NOT NULL DEFAULT '',
    -- In-flight address verification: the candidate address and the SHA-256
    -- hex of its single-use token. Cleared on success or replacement.
    pending_email text NOT NULL DEFAULT '',
    pending_token_hash text NOT NULL DEFAULT '',
    pending_expires_at timestamptz,
    -- Verification-send rate limiting: minimum gap since the last send, plus
    -- a daily cap counted within pending_last_sent_at's UTC day.
    pending_last_sent_at timestamptz,
    verify_sends_today integer NOT NULL DEFAULT 0,
    -- Capability token embedded in every email's unsubscribe link; only
    -- powers "set this profile's mode to off", so stored plaintext. Minted by
    -- the send path right before the first email that embeds it.
    unsubscribe_token text NOT NULL DEFAULT '',
    watermark_created_at timestamptz NOT NULL DEFAULT now(),
    watermark_id text NOT NULL DEFAULT '',
    last_digest_at timestamptz,
    last_attempt_at timestamptz,
    consecutive_failures integer NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX notification_email_prefs_user_idx
    ON public.notification_email_prefs (user_id);
CREATE INDEX notification_email_prefs_pending_token_idx
    ON public.notification_email_prefs (pending_token_hash)
    WHERE pending_token_hash <> '';
CREATE INDEX notification_email_prefs_unsubscribe_token_idx
    ON public.notification_email_prefs (unsubscribe_token)
    WHERE unsubscribe_token <> '';
-- One verified destination per profile, globally unique: an address may not
-- serve two profiles. Backstops the application-level checks against
-- concurrent verifications.
CREATE UNIQUE INDEX notification_email_prefs_custom_email_key
    ON public.notification_email_prefs (lower(custom_email))
    WHERE custom_email <> '';

-- The per-profile sweep reads deliveries by profile in watermark order.
CREATE INDEX notification_deliveries_profile_created_idx
    ON public.notification_deliveries (profile_id, created_at, id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.notification_deliveries_profile_created_idx;

-- Rollback restores the account-keyed schema empty: per-profile state
-- (verified addresses, watermarks) has no account-level representation, so
-- accounts re-opt-in. Symmetric with the up migration, which also carries
-- nothing over.
DROP TABLE public.notification_email_prefs;

CREATE TABLE public.notification_email_prefs (
    user_id integer PRIMARY KEY,
    mode text NOT NULL DEFAULT 'off'
        CHECK (mode IN ('off', 'per_episode', 'daily_digest', 'per_episode_and_digest')),
    watermark_created_at timestamptz NOT NULL DEFAULT now(),
    watermark_id text NOT NULL DEFAULT '',
    last_digest_at timestamptz,
    last_attempt_at timestamptz,
    consecutive_failures integer NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now()
);
-- +goose StatementEnd
