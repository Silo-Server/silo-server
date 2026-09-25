-- +goose Up
-- Sign-in and password reset resolve a typed identifier against the username
-- column first, then the email column (auth.LookupLogin), so one account's
-- username must never be another account's email. users_username_key and
-- users_email_key only enforce uniqueness within each column; this trigger
-- closes the gap across them. Only values a write sets or changes are checked,
-- so rows that already collide stay editable. The violation is reported as
-- unique_violation, which callers already map to "username or email taken".
-- +goose StatementBegin
CREATE FUNCTION users_enforce_login_identity_space() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    username_changed boolean := TG_OP = 'INSERT' OR NEW.username IS DISTINCT FROM OLD.username;
    email_changed boolean := TG_OP = 'INSERT' OR NEW.email IS DISTINCT FROM OLD.email;
    lock_key bigint;
BEGIN
    IF NOT username_changed AND NOT email_changed THEN
        RETURN NEW;
    END IF;

    -- Serialize writers claiming the same identifier, so two concurrent
    -- transactions cannot each miss the other's uncommitted row. Keys are
    -- taken in sorted order to avoid deadlocks between two-identifier writes.
    FOR lock_key IN
        SELECT DISTINCT hashtextextended('users-login-identifier:' || lower(identifier::text), 0)
        FROM unnest(ARRAY[
            CASE WHEN username_changed THEN NEW.username END,
            CASE WHEN email_changed THEN NEW.email END
        ]) AS identifier
        WHERE identifier IS NOT NULL
        ORDER BY 1
    LOOP
        PERFORM pg_advisory_xact_lock(lock_key);
    END LOOP;

    IF username_changed AND NEW.username IS NOT NULL AND EXISTS (
        SELECT 1 FROM users WHERE email = NEW.username AND id <> NEW.id
    ) THEN
        RAISE EXCEPTION 'username is already another account''s email'
            USING ERRCODE = 'unique_violation', TABLE = 'users', CONSTRAINT = 'users_login_identity_space';
    END IF;
    IF email_changed AND NEW.email IS NOT NULL AND EXISTS (
        SELECT 1 FROM users WHERE username = NEW.email AND id <> NEW.id
    ) THEN
        RAISE EXCEPTION 'email is already another account''s username'
            USING ERRCODE = 'unique_violation', TABLE = 'users', CONSTRAINT = 'users_login_identity_space';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER users_login_identity_space BEFORE INSERT OR UPDATE OF username, email ON users
FOR EACH ROW EXECUTE FUNCTION users_enforce_login_identity_space();

-- +goose Down
DROP TRIGGER users_login_identity_space ON users;
DROP FUNCTION users_enforce_login_identity_space();
