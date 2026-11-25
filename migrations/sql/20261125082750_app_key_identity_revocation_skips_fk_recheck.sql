-- +goose Up
-- +goose StatementBegin
-- The trigger that revokes the app keys of a deleted identity also clears
-- their identity_id, and that of the sessions their device keys opened, in
-- the same update. Otherwise the ON DELETE SET NULL actions update those rows
-- a second time; Postgres re-runs the foreign key checks of a row the
-- transaction has already updated, and the user_id check share-locks the
-- account row. Uninstalling a plugin takes no account lock, while a
-- provider's refusal holds the account row and then locks the account's key
-- rows, so the two deadlocked. With the identity cleared here the SET NULL
-- actions find nothing left to change, and deleting an identity locks only
-- the identity, the key rows and their sessions, in that order.
CREATE OR REPLACE FUNCTION public.revoke_app_keys_of_deleted_identity() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    UPDATE public.auth_person_keys
        SET revoked_at = COALESCE(revoked_at, NOW()), identity_id = NULL
        WHERE identity_id = OLD.id;
    WITH revoked AS (
        UPDATE public.auth_device_keys
            SET revoked_at = COALESCE(revoked_at, NOW()), identity_id = NULL
            WHERE identity_id = OLD.id
            RETURNING id
    )
    UPDATE public.auth_sessions
        SET revoked_at = COALESCE(revoked_at, NOW()),
            identity_id = CASE WHEN identity_id = OLD.id THEN NULL ELSE identity_id END
        WHERE device_key_id IN (SELECT id FROM revoked);
    RETURN OLD;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.revoke_app_keys_of_deleted_identity() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    UPDATE public.auth_person_keys SET revoked_at = COALESCE(revoked_at, NOW())
        WHERE identity_id = OLD.id AND revoked_at IS NULL;
    WITH revoked AS (
        UPDATE public.auth_device_keys SET revoked_at = COALESCE(revoked_at, NOW())
            WHERE identity_id = OLD.id AND revoked_at IS NULL
            RETURNING id
    )
    UPDATE public.auth_sessions SET revoked_at = COALESCE(revoked_at, NOW())
        WHERE device_key_id IN (SELECT id FROM revoked) AND revoked_at IS NULL;
    RETURN OLD;
END;
$$;
-- +goose StatementEnd
