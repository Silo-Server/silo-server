-- +goose Up
-- Sign-in and password reset resolve a typed identifier against the username
-- column first, then the email column (auth.LookupLogin), so one account's
-- username must never be another account's email. users_username_key and
-- users_email_key only enforce uniqueness within each column.
--
-- user_login_identifiers records every account's username and email in one
-- column. The partial unique index lets only one account hold an identifier,
-- and it sees concurrent uncommitted claims at every isolation level, which a
-- trigger-side existence check cannot. The violation is an ordinary
-- unique_violation, which callers already map to "username or email taken".
--
-- Accounts that already shared an identifier before this migration keep a
-- legacy_duplicate row for it: the lowest account id holds the identifier,
-- the others stay editable, and the next one takes it over when the holder
-- releases it, so the old collision cannot spread to a new account.
-- Block writes to users (reads continue) until the triggers exist, so an
-- account written by a server still running the previous release cannot land
-- between the backfill and the triggers without identifier rows.
LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE;

CREATE TABLE user_login_identifiers (
    identifier citext NOT NULL,
    user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    legacy_duplicate boolean NOT NULL DEFAULT false,
    PRIMARY KEY (identifier, user_id)
);
CREATE UNIQUE INDEX user_login_identifiers_holder_key ON user_login_identifiers (identifier)
WHERE NOT legacy_duplicate;
CREATE INDEX user_login_identifiers_user_id_idx ON user_login_identifiers (user_id);

INSERT INTO user_login_identifiers (identifier, user_id, legacy_duplicate)
SELECT identifier, user_id, row_number() OVER (PARTITION BY identifier ORDER BY user_id) > 1
FROM (
    SELECT username AS identifier, id AS user_id FROM users WHERE username IS NOT NULL
    UNION
    SELECT email, id FROM users WHERE email IS NOT NULL
) AS held;

-- +goose StatementBegin
CREATE FUNCTION users_sync_login_identifiers() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    old_identifiers citext[] := CASE WHEN TG_OP = 'INSERT' THEN '{}' ELSE ARRAY[OLD.username, OLD.email] END;
    new_identifiers citext[] := CASE WHEN TG_OP = 'DELETE' THEN '{}' ELSE ARRAY[NEW.username, NEW.email] END;
    released citext[];
    lock_key bigint;
BEGIN
    -- Every change to an identifier's rows happens under this lock, taken for
    -- old and new identifiers in one global order, so writers exchanging
    -- identifiers wait for each other instead of deadlocking.
    FOR lock_key IN
        SELECT DISTINCT hashtextextended('user-login-identifier:' || lower(identifier::text), 0)
        FROM unnest(old_identifiers || new_identifiers) AS identifier
        WHERE identifier IS NOT NULL
        ORDER BY 1
    LOOP
        PERFORM pg_advisory_xact_lock(lock_key);
    END LOOP;

    IF TG_OP <> 'INSERT' THEN
        -- Release what this account no longer uses.
        WITH dropped AS (
            DELETE FROM user_login_identifiers
            WHERE user_id = OLD.id
              AND (TG_OP = 'DELETE' OR (identifier IS DISTINCT FROM NEW.username
                                        AND identifier IS DISTINCT FROM NEW.email))
            RETURNING identifier, legacy_duplicate
        )
        SELECT array_agg(identifier) INTO released FROM dropped WHERE NOT legacy_duplicate;

        -- Pass a released identifier to the next account that still shares it
        -- from before the migration.
        UPDATE user_login_identifiers heir SET legacy_duplicate = false
        FROM (
            SELECT DISTINCT ON (identifier) identifier, user_id
            FROM user_login_identifiers
            WHERE identifier = ANY (released) AND legacy_duplicate
            ORDER BY identifier, user_id
        ) next_holder
        WHERE heir.identifier = next_holder.identifier AND heir.user_id = next_holder.user_id;
    END IF;
    IF TG_OP = 'DELETE' THEN
        -- BEFORE DELETE, so the hand-over runs before the foreign key's
        -- cascade removes the rows; returning OLD lets the delete proceed.
        RETURN OLD;
    END IF;

    INSERT INTO user_login_identifiers (identifier, user_id)
    SELECT DISTINCT claimed.identifier, NEW.id
    FROM unnest(new_identifiers) AS claimed(identifier)
    WHERE claimed.identifier IS NOT NULL
      AND NOT EXISTS (
          SELECT 1 FROM user_login_identifiers held
          WHERE held.identifier = claimed.identifier AND held.user_id = NEW.id
      )
    ORDER BY claimed.identifier;
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
