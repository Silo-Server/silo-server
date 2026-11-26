-- +goose Up
-- Ending a login session no longer writes device_login_requests. A device
-- sign-in approved in advance is honored at collection only while the
-- session that approved it is live (not revoked, not expired), and the lists
-- show only those, so the trigger that withdrew them when the session was
-- revoked goes. Ending a session then touches session rows only.
-- No row is rewritten: requests the trigger already withdrew stay withdrawn,
-- and a request still approved in advance whose approving session has ended
-- is refused when collected and removed by the expired-request cleanup.
DROP TRIGGER IF EXISTS auth_sessions_withdraw_preapprovals ON public.auth_sessions;
DROP FUNCTION IF EXISTS public.withdraw_session_device_preapprovals();

-- +goose Down
-- Puts the trigger back as 20261125041746_add_device_login_preapproval made
-- it, then withdraws, as the trigger would have, every request still
-- approved in advance whose approving session ended while the trigger was
-- gone (missing, revoked or expired): the earlier build honors any approved
-- request for up to 30 days and does not read the approver's state.
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
UPDATE public.device_login_requests r
SET status = 'denied', denied_at = NOW(), updated_at = NOW()
WHERE r.status = 'approved' AND r.preapproved_at IS NOT NULL
    AND NOT EXISTS (
        SELECT 1 FROM public.auth_sessions s
        WHERE s.id = r.preapproved_session_id AND s.revoked_at IS NULL AND s.expires_at > NOW()
    );
