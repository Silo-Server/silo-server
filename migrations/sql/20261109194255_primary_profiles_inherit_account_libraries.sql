-- +goose Up
-- The primary profile now inherits its account's libraries and hides the
-- ones it doesn't want (ui.disabled_library_ids) instead of being limited to
-- some of them; access.ProfileLibraryLimit ignores a limit stored on it.
-- Clear the limits primary profiles still carry, so the profile editors show
-- what the server applies and a client resending the stored value isn't
-- refused. Their recommendation rows were built under the old limit, so mark
-- their taste profiles stale for the stale sweep to rebuild, as an account
-- scope change does.
UPDATE user_taste_profiles t
SET stale_at = now()
FROM user_profiles p
WHERE t.user_id = p.user_id
  AND t.profile_id = p.id
  AND p.is_primary
  AND (p.library_restrictions_enabled
       OR EXISTS (SELECT 1 FROM user_profile_allowed_libraries a
                  WHERE a.user_id = p.user_id AND a.profile_id = p.id));

DELETE FROM user_profile_allowed_libraries a
USING user_profiles p
WHERE a.user_id = p.user_id
  AND a.profile_id = p.id
  AND p.is_primary;

UPDATE user_profiles
SET library_restrictions_enabled = false
WHERE is_primary AND library_restrictions_enabled;

-- +goose Down
-- Nothing to undo: the cleared limits no longer applied to primary profiles,
-- and the server reads a primary profile the same way with or without them.
SELECT 1;
