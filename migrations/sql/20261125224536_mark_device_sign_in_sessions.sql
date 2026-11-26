-- +goose Up
-- Sessions whose device request is still stored were opened by device sign-in.
-- Requests are deleted about a day after they expire, so an older device
-- session keeps NULL and is judged by its recorded platform. This runs apart
-- from the column add, so the add's lock is not held while rows are updated.
-- Rows already marked are left alone, so running it again writes nothing.
UPDATE public.auth_sessions s
SET via_device_sign_in = TRUE
FROM public.device_login_requests d
WHERE d.auth_session_id = s.id
  AND s.via_device_sign_in IS DISTINCT FROM TRUE;

-- +goose Down
-- Nothing to undo here: the previous migration's Down drops the column with
-- every value this one set.
SELECT 1;
