-- +goose Up
-- Drain existing writers before seeding the order boundary. Reads may continue.
LOCK TABLE notification_deliveries IN SHARE ROW EXCLUSIVE MODE;

-- Retain this boundary after delivery retention/deletion so old checkpoints
-- cannot move ahead of a newly recreated inbox, even if the clock moves back.
CREATE TABLE notification_inbox_clocks (
    profile_id text PRIMARY KEY,
    last_created_at timestamptz NOT NULL DEFAULT '-infinity'
);
INSERT INTO notification_inbox_clocks(profile_id, last_created_at)
SELECT profile_id, max(created_at) FROM notification_deliveries GROUP BY profile_id;

-- +goose StatementBegin
CREATE FUNCTION assign_notification_inbox_time() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO notification_inbox_clocks(profile_id, last_created_at)
    VALUES (NEW.profile_id, clock_timestamp())
    ON CONFLICT (profile_id) DO UPDATE
        SET last_created_at = GREATEST(clock_timestamp(), notification_inbox_clocks.last_created_at + interval '1 microsecond')
    RETURNING last_created_at INTO NEW.created_at;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER notification_deliveries_inbox_time BEFORE INSERT ON notification_deliveries
FOR EACH ROW EXECUTE FUNCTION assign_notification_inbox_time();

-- +goose Down
DROP TRIGGER notification_deliveries_inbox_time ON notification_deliveries;
DROP FUNCTION assign_notification_inbox_time();
DROP TABLE notification_inbox_clocks;
