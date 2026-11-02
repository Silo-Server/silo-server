-- +goose Up
-- +goose StatementBegin

-- Series a profile muted: it gets no new-episode notification for them,
-- whatever its interest reasons, until it unmutes. A mute is independent of
-- the interest rows, which are recomputed and pruned, so it survives the
-- series leaving and rejoining the profile's lists. There is no profile or
-- media_items foreign key: shared tables do not reference profile rows, and
-- content IDs are deterministic, so a mute still applies to a series that is
-- rescanned. PurgeProfile removes a deleted profile's rows explicitly. A
-- profile is identified by account and profile ID together, as in
-- user_profiles: legacy profile IDs such as "default" repeat across accounts.
CREATE TABLE public.profile_series_notification_mutes (
    user_id integer NOT NULL
        REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id text NOT NULL,
    series_id text NOT NULL,
    muted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, profile_id, series_id)
);

-- Fanout anti-joins a series' candidate recipients against its mutes.
CREATE INDEX profile_series_notification_mutes_series_idx
    ON public.profile_series_notification_mutes (series_id, user_id, profile_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.profile_series_notification_mutes;
-- +goose StatementEnd
