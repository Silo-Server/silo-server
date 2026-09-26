-- +goose Up
ALTER TABLE users ADD COLUMN admin_revision BIGINT NOT NULL DEFAULT 1;
-- +goose StatementBegin
CREATE FUNCTION bump_user_admin_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.admin_revision := OLD.admin_revision + 1;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER users_admin_revision BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION bump_user_admin_revision();

-- +goose Down
DROP TRIGGER users_admin_revision ON users;
DROP FUNCTION bump_user_admin_revision();
ALTER TABLE users DROP COLUMN admin_revision;
