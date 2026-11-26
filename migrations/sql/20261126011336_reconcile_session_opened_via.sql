-- +goose Up
-- Two columns record how a login session was opened: opened_via (password,
-- provider, device_login, app_key or impersonation) and the older
-- via_device_sign_in, which is exactly "opened_via is device_login".
-- opened_via is the one the server reads. via_device_sign_in stays because a
-- server of the previous release names it in every sign-in, so it must keep
-- existing while such a server may run against this database, during a
-- rolling upgrade or after going back a release. The server now writes it
-- from opened_via, and the next migration adds the check that keeps the two
-- from disagreeing.
--
-- This migration is right whichever column the database had first. It only
-- records what is known: a session flagged as a device sign-in gets
-- opened_via; one flagged false, or not flagged, keeps no opened_via, because
-- the flag does not say which other way it was opened.
--
-- The check is a migration of its own so that these updates' row locks are
-- released before it asks for the table: taken in one transaction, the two
-- can deadlock with a server of the previous release that writes one of
-- these rows in between.
UPDATE public.auth_sessions
SET opened_via = 'device_login'
WHERE via_device_sign_in IS TRUE
  AND opened_via IS NULL;

UPDATE public.auth_sessions
SET via_device_sign_in = (opened_via = 'device_login')
WHERE opened_via IS NOT NULL
  AND via_device_sign_in IS DISTINCT FROM (opened_via = 'device_login');

-- +goose Down
-- Nothing to undo here: the values this migration wrote are true of the
-- sessions, and each column's own migration drops it.
SELECT 1;
