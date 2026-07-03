-- Series download monitoring (auto-download) for downloads v2.
--
-- A download subscription is a device-scoped, explicit opt-in to keep a series
-- downloaded on one device. The client triggers a sync (on open / background
-- refresh) and the server registers the in-scope, not-yet-downloaded episodes —
-- idempotent via the downloads managed-entry unique index. It is intentionally
-- separate from the device-less, derived profile_series_interest used by
-- notifications. See docs/superpowers/specs/2026-06-18-offline-sync-mobile-design.md.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.download_subscriptions (
    id                text        NOT NULL,
    user_id           integer     NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id        text        NOT NULL,
    device_id         text        NOT NULL,
    series_id         text        NOT NULL,                 -- content_id of the series media_item
    mode              text        NOT NULL,                 -- all | future | latest_season | specific_seasons
    season_numbers    integer[]   NOT NULL DEFAULT '{}',    -- the monitored seasons when mode='specific_seasons'
    target_season     integer,                              -- the latest season at subscribe time when mode='latest_season'
    delete_watched    boolean     NOT NULL DEFAULT false,   -- client-enforced: delete episodes once watched
    max_storage_bytes bigint      NOT NULL DEFAULT 0,       -- 0 = unlimited; client-enforced, server soft-gates auto-registration
    active            boolean     NOT NULL DEFAULT true,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT download_subscriptions_pkey PRIMARY KEY (id),
    CONSTRAINT download_subscriptions_mode_check
        CHECK (mode IN ('all', 'future', 'latest_season', 'specific_seasons')),
    -- One subscription per (user, profile, device, series). Household profiles share a
    -- user_id, so profile_id is part of the key; the device makes it device-scoped.
    CONSTRAINT download_subscriptions_entry_uidx
        UNIQUE (user_id, profile_id, device_id, series_id),
    -- Composite FK to user_devices (all three columns NOT NULL, so it always enforces).
    CONSTRAINT download_subscriptions_device_fkey
        FOREIGN KEY (user_id, profile_id, device_id)
        REFERENCES public.user_devices(user_id, profile_id, device_id) ON DELETE CASCADE
);

-- No separate device-listing index: the UNIQUE constraint's index on
-- (user_id, profile_id, device_id, series_id) already serves that prefix.
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.download_subscriptions;
-- +goose StatementEnd
