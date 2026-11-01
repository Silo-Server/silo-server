-- +goose Up
-- +goose StatementBegin
-- Email alerts: editable templates and theme, recipients that need no Silo
-- profile, and per-recipient instant, daily and weekly sends.

-- One row: the email design and delivery settings as JSON (templates,
-- theme, custom variables, schedule, delivery limits, self-service).
CREATE TABLE public.notification_email_alert_settings (
    id smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    settings jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Addresses an administrator adds. They receive everything new in the
-- chosen libraries and types, and manage themselves through the token link
-- in every email.
CREATE TABLE public.notification_email_recipients (
    id text PRIMARY KEY,
    name text NOT NULL DEFAULT '',
    email text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    mode text NOT NULL CHECK (mode IN ('instant', 'instant_daily', 'instant_weekly', 'daily', 'weekly')),
    types text[] NOT NULL DEFAULT '{}',
    library_ids integer[] NOT NULL DEFAULT '{}',
    max_age_hours integer NOT NULL DEFAULT 0,
    manage_token text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX notification_email_recipients_email_key
    ON public.notification_email_recipients (lower(email));

-- Profiles can pair instant emails with a weekly digest.
ALTER TABLE public.notification_email_prefs
    DROP CONSTRAINT notification_email_prefs_mode_check;
ALTER TABLE public.notification_email_prefs
    ADD CONSTRAINT notification_email_prefs_mode_check
    CHECK (mode IN ('off', 'per_episode', 'daily_digest', 'per_episode_and_digest', 'weekly_digest', 'per_episode_and_weekly_digest'));

-- A profile's own email gains the recipient choices: what to send (shows it
-- follows, or everything new it can open), which types and libraries, and
-- how recent counts as new.
ALTER TABLE public.notification_email_prefs
    ADD COLUMN content text NOT NULL DEFAULT 'followed' CHECK (content IN ('followed', 'all')),
    ADD COLUMN types text[] NOT NULL DEFAULT '{}',
    ADD COLUMN library_ids integer[] NOT NULL DEFAULT '{}',
    ADD COLUMN max_age_hours integer NOT NULL DEFAULT 0;

-- Send state per recipient and leg (instant, daily, weekly). recipient_key
-- is 'r:<recipient id>' or 'p:<user id>:<profile id>'. The watermark is the
-- (created_at, id) cursor of the last item considered, over release_events
-- or notification_deliveries depending on what the recipient receives.
CREATE TABLE public.notification_email_alert_state (
    recipient_key text NOT NULL,
    leg text NOT NULL CHECK (leg IN ('instant', 'daily', 'weekly')),
    watermark_created_at timestamptz NOT NULL,
    watermark_id text NOT NULL DEFAULT '',
    last_run_at timestamptz,
    last_sent_at timestamptz,
    last_instant_at timestamptz,
    day text NOT NULL DEFAULT '',
    day_count integer NOT NULL DEFAULT 0,
    failures integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz,
    PRIMARY KEY (recipient_key, leg)
);

CREATE TABLE public.notification_email_alert_log (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    sent_at timestamptz NOT NULL DEFAULT now(),
    recipient_key text NOT NULL,
    email text NOT NULL,
    leg text NOT NULL,
    items integer NOT NULL DEFAULT 0,
    subject text NOT NULL DEFAULT '',
    error text NOT NULL DEFAULT ''
);
CREATE INDEX notification_email_alert_log_sent_idx
    ON public.notification_email_alert_log (sent_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE public.notification_email_alert_log;
DROP TABLE public.notification_email_alert_state;
UPDATE public.notification_email_prefs SET mode = 'per_episode_and_digest'
    WHERE mode = 'per_episode_and_weekly_digest';
ALTER TABLE public.notification_email_prefs
    DROP CONSTRAINT notification_email_prefs_mode_check;
ALTER TABLE public.notification_email_prefs
    ADD CONSTRAINT notification_email_prefs_mode_check
    CHECK (mode IN ('off', 'per_episode', 'daily_digest', 'per_episode_and_digest', 'weekly_digest'));
ALTER TABLE public.notification_email_prefs
    DROP COLUMN content,
    DROP COLUMN types,
    DROP COLUMN library_ids,
    DROP COLUMN max_age_hours;
DROP TABLE public.notification_email_recipients;
DROP TABLE public.notification_email_alert_settings;
-- +goose StatementEnd
