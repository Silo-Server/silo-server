-- +goose Up
-- +goose StatementBegin
-- The trigger that revokes the app keys of a deleted identity must leave
-- nothing for a later statement in the same transaction to update again.
-- When a transaction updates a row twice, Postgres re-checks the row's
-- foreign keys on the second update; for a row that names an account that
-- takes a key-share lock on the users row. Uninstalling a plugin takes no
-- account lock, while a provider's refusal, a key sign-in or a password reset
-- holds the account row and then waits on these rows, so a second update here
-- deadlocks with them.
--
-- A key session may have approved a device sign-in in advance. Ending the
-- session fires auth_sessions_withdraw_preapprovals, which denies that
-- device_login_requests row, and the identity's ON DELETE SET NULL on
-- approved_identity_id then updated it a second time. So the trigger denies
-- those pre-approvals itself, and clears their approved_identity_id, in one
-- statement before it ends the sessions: the withdraw trigger then finds
-- nothing still approved and SET NULL nothing still naming the identity.
--
-- Order: key rows, then the pre-approvals of their sessions, then the
-- sessions. Every row is updated once, with no foreign key column changed to
-- a non-NULL value, so no statement here reads the users row.
CREATE OR REPLACE FUNCTION public.revoke_app_keys_of_deleted_identity() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    revoked uuid[];
    ending text[];
BEGIN
    UPDATE public.auth_person_keys
        SET revoked_at = COALESCE(revoked_at, NOW()), identity_id = NULL
        WHERE identity_id = OLD.id;
    WITH r AS (
        UPDATE public.auth_device_keys
            SET revoked_at = COALESCE(revoked_at, NOW()), identity_id = NULL
            WHERE identity_id = OLD.id
            RETURNING id
    )
    SELECT array_agg(id) INTO revoked FROM r;
    IF revoked IS NULL THEN
        RETURN OLD;
    END IF;
    SELECT array_agg(id) INTO ending FROM public.auth_sessions WHERE device_key_id = ANY (revoked);
    IF ending IS NULL THEN
        RETURN OLD;
    END IF;
    UPDATE public.device_login_requests
        SET status = CASE WHEN preapproved_at IS NOT NULL AND status = 'approved' THEN 'denied' ELSE status END,
            denied_at = CASE WHEN preapproved_at IS NOT NULL AND status = 'approved' THEN NOW() ELSE denied_at END,
            approved_identity_id = CASE WHEN approved_identity_id = OLD.id THEN NULL ELSE approved_identity_id END,
            updated_at = NOW()
        WHERE preapproved_session_id = ANY (ending)
            AND ((preapproved_at IS NOT NULL AND status = 'approved') OR approved_identity_id = OLD.id);
    UPDATE public.auth_sessions
        SET revoked_at = COALESCE(revoked_at, NOW()),
            identity_id = CASE WHEN identity_id = OLD.id THEN NULL ELSE identity_id END
        WHERE id = ANY (ending);
    RETURN OLD;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
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
