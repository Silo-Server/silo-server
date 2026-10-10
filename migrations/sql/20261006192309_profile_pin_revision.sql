-- +goose Up
-- A profile verification token (X-Profile-Token) proves that the caller knew
-- this profile's PIN. It is bound to pin_revision, which advances whenever the
-- profile's pin_hash changes (set, changed or cleared). Editing any other
-- field, or any other profile on the account, leaves it unchanged, so a
-- household parent's token survives editing a child's limits. The account-wide
-- users.access_policy_revision is unchanged and still bumps on the same events
-- as before; it no longer gates profile tokens.
--
-- Tokens minted before this change carry no pin_revision claim and are
-- refused, so each PIN-locked profile enters its PIN once after the upgrade.
--
-- A constant default is a catalog-only change; no row is rewritten.
ALTER TABLE public.user_profiles
    ADD COLUMN IF NOT EXISTS pin_revision bigint NOT NULL DEFAULT 0;

-- The trigger, not the application, advances the revision, so every writer
-- of pin_hash invalidates the old PIN's tokens exactly once: this release's
-- profile store, a node from an older release during a rolling deploy (which
-- writes pin_hash and knows nothing of pin_revision), and manual SQL. A bcrypt
-- hash is salted, so re-setting the same PIN still counts as a change;
-- clearing an already empty PIN does not.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.bump_user_profile_pin_revision() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.pin_revision := OLD.pin_revision + 1;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS user_profiles_pin_revision ON public.user_profiles;
CREATE TRIGGER user_profiles_pin_revision
    BEFORE UPDATE OF pin_hash ON public.user_profiles
    FOR EACH ROW
    WHEN (NEW.pin_hash IS DISTINCT FROM OLD.pin_hash)
    EXECUTE FUNCTION public.bump_user_profile_pin_revision();

-- +goose Down
DROP TRIGGER IF EXISTS user_profiles_pin_revision ON public.user_profiles;
DROP FUNCTION IF EXISTS public.bump_user_profile_pin_revision();

ALTER TABLE public.user_profiles
    DROP COLUMN IF EXISTS pin_revision;
