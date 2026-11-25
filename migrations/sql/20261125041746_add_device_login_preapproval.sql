-- +goose Up
-- A device sign-in approved in advance from the account's own session: the
-- row is inserted already approved, bound to the device's fingerprint, and
-- waits up to 30 days for the device to poll it. preapproved_at marks such a
-- row; client_purpose stays device_login so the existing poll redeems it.
-- The existing index on approved_by_user_id serves the per-account list and
-- cap.
-- preapproved_session_id is the login session that approved it. Revoking
-- that one session withdraws the sign-ins it approved in advance and has not
-- yet been redeemed, so a session someone else was using cannot leave a
-- sign-in behind, nor hide one by logging itself out.
ALTER TABLE device_login_requests
    ADD COLUMN preapproved_at TIMESTAMPTZ,
    ADD COLUMN preapproved_session_id TEXT REFERENCES auth_sessions(id) ON DELETE SET NULL,
    ADD CONSTRAINT device_login_requests_preapproved_bound CHECK (
        preapproved_at IS NULL
        OR (client_purpose = 'device_login' AND temporary = FALSE AND binding_hash IS NOT NULL)
    );

CREATE INDEX idx_device_login_requests_preapproved_session_id
    ON device_login_requests (preapproved_session_id) WHERE preapproved_session_id IS NOT NULL;

-- The withdrawal is a trigger, not application code, so it holds for every
-- write that revokes a login session: logout, the session list, an
-- administrator, a revoked app key, the account-wide revocations, and a node
-- from an older release that knows nothing of pre-approval. A session that
-- only expires is not revoked and leaves its sign-ins, which expire within
-- the same 30 days.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.withdraw_session_device_preapprovals() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE public.device_login_requests
    SET status = 'denied', denied_at = NOW(), updated_at = NOW()
    WHERE preapproved_session_id = NEW.id AND preapproved_at IS NOT NULL AND status = 'approved';
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS auth_sessions_withdraw_preapprovals ON public.auth_sessions;
CREATE TRIGGER auth_sessions_withdraw_preapprovals
    AFTER UPDATE OF revoked_at ON public.auth_sessions
    FOR EACH ROW
    WHEN (OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL)
    EXECUTE FUNCTION public.withdraw_session_device_preapprovals();

-- +goose Down
DROP TRIGGER IF EXISTS auth_sessions_withdraw_preapprovals ON public.auth_sessions;
DROP FUNCTION IF EXISTS public.withdraw_session_device_preapprovals();
-- A pre-approved row left behind would read as an ordinary approval good for
-- up to 30 days, which the account could no longer list or withdraw.
DELETE FROM device_login_requests WHERE preapproved_at IS NOT NULL;
ALTER TABLE device_login_requests
    DROP CONSTRAINT IF EXISTS device_login_requests_preapproved_bound,
    DROP COLUMN IF EXISTS preapproved_session_id,
    DROP COLUMN IF EXISTS preapproved_at;
