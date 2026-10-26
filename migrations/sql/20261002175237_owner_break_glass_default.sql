-- +goose Up
-- The server Owner is a break-glass account by default: it keeps local
-- password sign-in when auth.local_password_login is off and when it links a
-- sign-in provider, so a provider outage cannot lock it out. The Owner may
-- clear the flag on its own account. New Owners get it from first-run setup
-- and ownership transfer; this marks the existing one.
UPDATE users SET break_glass = true WHERE is_owner AND role = 'admin';

-- +goose Down
-- The previous value is not recorded, and the flag is valid either way: the
-- Owner keeps it until it clears it.
SELECT 1;
