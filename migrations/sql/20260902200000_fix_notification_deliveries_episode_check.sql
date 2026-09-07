-- +goose Up
-- notification_deliveries has carried two mutually exclusive rules since
-- 20260611100000_profile_release_notifications.sql created it:
--
--   release_event_id text REFERENCES release_events(id) ON DELETE SET NULL
--   CHECK (type <> 'episode.available' OR (release_event_id IS NOT NULL AND ...))
--
-- Deleting a processed release_event sets release_event_id to NULL on its
-- deliveries, which the CHECK then rejects, so the DELETE aborts. Retention
-- has therefore never been able to prune release events: the nightly
-- notifications_retention task fails on
--
--   prune release events: ERROR: new row for relation "notification_deliveries"
--   violates check constraint "notification_deliveries_episode_fields_check"
--
-- and every later step in RunRetention is skipped with it. Observed in
-- production with 148,299 release_events, all processed, none ever pruned.
--
-- The original migration's own comment says which rule is the mistake:
--
--   "release_event_id is nullable: operational types (e.g.
--    webhook.auto_disabled) have no release event, and retention pruning of
--    old release_events must not delete inbox rows."
--
-- So ON DELETE SET NULL is the intent and the CHECK contradicts it. Drop only
-- the release_event_id clause. library_id, series_id and episode_id stay
-- required: they are stable identifiers, they are what makes an
-- episode.available row renderable after its event has aged out, and nothing
-- nulls them.
ALTER TABLE public.notification_deliveries
    DROP CONSTRAINT notification_deliveries_episode_fields_check;

ALTER TABLE public.notification_deliveries
    ADD CONSTRAINT notification_deliveries_episode_fields_check CHECK (
        type <> 'episode.available'
        OR (library_id IS NOT NULL
            AND series_id IS NOT NULL
            AND episode_id IS NOT NULL)
    );

-- +goose Down
ALTER TABLE public.notification_deliveries
    DROP CONSTRAINT notification_deliveries_episode_fields_check;

ALTER TABLE public.notification_deliveries
    ADD CONSTRAINT notification_deliveries_episode_fields_check CHECK (
        type <> 'episode.available'
        OR (release_event_id IS NOT NULL
            AND library_id IS NOT NULL
            AND series_id IS NOT NULL
            AND episode_id IS NOT NULL)
    );
