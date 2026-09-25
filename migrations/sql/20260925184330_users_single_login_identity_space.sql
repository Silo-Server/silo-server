-- +goose Up
-- Sign-in and password reset resolve a typed identifier against the username
-- column first, then the email column (auth.LookupLogin), so one account's
-- username must never be another account's email. users_username_key and
-- users_email_key only enforce uniqueness within each column.
--
-- user_login_identifiers holds every account's username and email in one
-- unique column, so its primary key closes the gap across the two. A unique
-- index sees concurrent uncommitted claims at every isolation level, which a
-- trigger-side existence check cannot. The violation is an ordinary
-- unique_violation, which callers already map to "username or email taken".
CREATE TABLE user_login_identifiers (
    identifier citext PRIMARY KEY,
    user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX user_login_identifiers_user_id_idx ON user_login_identifiers (user_id);

-- Rows written before this migration can already collide. The lower account
-- id keeps the shared identifier; the other account keeps its value in users
-- but does not own it here, and stays editable (see the trigger below).
INSERT INTO user_login_identifiers (identifier, user_id)
SELECT identifier, user_id FROM (
    SELECT username AS identifier, id AS user_id FROM users WHERE username IS NOT NULL
    UNION ALL
    SELECT email, id FROM users WHERE email IS NOT NULL
) AS claimed
ORDER BY user_id
ON CONFLICT (identifier) DO NOTHING;

-- +goose StatementBegin
-- Only values a write sets or changes are claimed, so an existing collision
-- does not block unrelated edits to the account that lost the backfill.
CREATE FUNCTION users_sync_login_identifiers() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    username_changed boolean := TG_OP = 'INSERT' OR NEW.username IS DISTINCT FROM OLD.username;
    email_changed boolean := TG_OP = 'INSERT' OR NEW.email IS DISTINCT FROM OLD.email;
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        -- Release what this account no longer holds. An account from an older
        -- collision may still hold a released identifier; hand it over so the
        -- identifier stays claimed and the collision cannot spread.
        WITH released AS (
            DELETE FROM user_login_identifiers
            WHERE user_id = OLD.id
              AND (TG_OP = 'DELETE' OR (identifier IS DISTINCT FROM NEW.username
                                        AND identifier IS DISTINCT FROM NEW.email))
            RETURNING identifier
        )
        INSERT INTO user_login_identifiers (identifier, user_id)
        SELECT DISTINCT ON (released.identifier) released.identifier, holder.id
        FROM released
        JOIN users holder ON holder.id <> OLD.id
            AND (holder.username = released.identifier OR holder.email = released.identifier)
        ORDER BY released.identifier, holder.id
        ON CONFLICT (identifier) DO NOTHING;
    END IF;
    IF TG_OP = 'DELETE' THEN
        -- BEFORE DELETE, so the hand-over runs before the foreign key's
        -- cascade removes the rows; returning OLD lets the delete proceed.
        RETURN OLD;
    END IF;

    INSERT INTO user_login_identifiers (identifier, user_id)
    SELECT DISTINCT claimed.identifier, NEW.id
    FROM unnest(ARRAY[
        CASE WHEN username_changed THEN NEW.username END,
        CASE WHEN email_changed THEN NEW.email END
    ]) AS claimed(identifier)
    WHERE claimed.identifier IS NOT NULL
      AND NOT EXISTS (
          SELECT 1 FROM user_login_identifiers owned
          WHERE owned.identifier = claimed.identifier AND owned.user_id = NEW.id
      );
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER users_login_identifiers AFTER INSERT OR UPDATE OF username, email ON users
FOR EACH ROW EXECUTE FUNCTION users_sync_login_identifiers();
CREATE TRIGGER users_release_login_identifiers BEFORE DELETE ON users
FOR EACH ROW EXECUTE FUNCTION users_sync_login_identifiers();

-- +goose Down
DROP TRIGGER users_release_login_identifiers ON users;
DROP TRIGGER users_login_identifiers ON users;
DROP FUNCTION users_sync_login_identifiers();
DROP TABLE user_login_identifiers;
