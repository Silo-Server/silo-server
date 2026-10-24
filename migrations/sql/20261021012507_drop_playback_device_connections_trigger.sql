-- The API deletes a device's playback_connections rows once the deletion of
-- its last registration commits, reading the account's registry in whichever
-- user store holds it. This trigger saw only Postgres user_devices: with the
-- SQLite user store those rows are download mirrors, so a profile delete could
-- drop the observations of a device another profile still registers. Its
-- unlocked check also let two concurrent deletes of a device's last two
-- registrations both skip the cleanup.

-- +goose Up
DROP TRIGGER delete_playback_device_connections ON user_devices;
DROP FUNCTION delete_playback_device_connections();

-- +goose Down
-- +goose StatementBegin
CREATE FUNCTION delete_playback_device_connections() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM user_devices WHERE user_id=OLD.user_id AND device_id=OLD.device_id) THEN
        DELETE FROM playback_connections WHERE user_id=OLD.user_id AND device_id=OLD.device_id;
    END IF;
    RETURN OLD;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER delete_playback_device_connections AFTER DELETE ON user_devices
FOR EACH ROW EXECUTE FUNCTION delete_playback_device_connections();
