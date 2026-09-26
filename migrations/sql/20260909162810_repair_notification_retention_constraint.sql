-- +goose Up
-- Event retention clears the optional FK while the durable inbox keeps the
-- episode identity snapshot. The snapshot remains required for episode rows.
ALTER TABLE public.notification_deliveries
    DROP CONSTRAINT notification_deliveries_episode_fields_check,
    ADD CONSTRAINT notification_deliveries_episode_fields_check CHECK (
        type <> 'episode.available'
        OR (library_id IS NOT NULL AND series_id IS NOT NULL AND episode_id IS NOT NULL)
    );

-- +goose Down
-- PostgreSQL rejects this rollback atomically if retention has detached inbox
-- rows. Do not delete durable notifications to restore the old constraint.
ALTER TABLE public.notification_deliveries
    DROP CONSTRAINT notification_deliveries_episode_fields_check,
    ADD CONSTRAINT notification_deliveries_episode_fields_check CHECK (
        type <> 'episode.available'
        OR (release_event_id IS NOT NULL AND library_id IS NOT NULL
            AND series_id IS NOT NULL AND episode_id IS NOT NULL)
    );
