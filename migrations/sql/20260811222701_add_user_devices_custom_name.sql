-- +goose Up
-- The name a profile chose for one of its devices ("Bedroom Apple TV"), so a
-- household with three identical clients can tell them apart. NULL means none
-- is set and clients fall back to the reported device_name; registration keeps
-- updating device_name from request headers and never touches this column.
ALTER TABLE public.user_devices
    ADD COLUMN custom_name text;

-- +goose Down
ALTER TABLE public.user_devices
    DROP COLUMN custom_name;
