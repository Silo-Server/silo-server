-- +goose Up
ALTER TABLE user_profile_onboarding ADD COLUMN revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0);
-- +goose StatementBegin
CREATE FUNCTION bump_onboarding_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.revision := OLD.revision + 1;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER onboarding_revision BEFORE UPDATE ON user_profile_onboarding
FOR EACH ROW EXECUTE FUNCTION bump_onboarding_revision();

-- +goose Down
DROP TRIGGER onboarding_revision ON user_profile_onboarding;
DROP FUNCTION bump_onboarding_revision();
ALTER TABLE user_profile_onboarding DROP COLUMN revision;
