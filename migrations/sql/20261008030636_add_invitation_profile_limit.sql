-- +goose Up
ALTER TABLE public.invitations
    ADD COLUMN max_profiles integer
        CONSTRAINT invitations_max_profiles_check CHECK (max_profiles >= 1);

-- +goose Down
-- Serialize with issuance and acceptance before deciding whether the persisted
-- invitation limits can be discarded. Already accepted user limits remain.
LOCK TABLE public.invitations IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM public.invitations
        WHERE max_profiles IS NOT NULL AND accepted_at IS NULL
          AND revoked_at IS NULL AND expires_at > clock_timestamp()
    ) THEN
        RAISE EXCEPTION 'revoke or accept profile-limited invitations before rolling back this migration';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE public.invitations DROP COLUMN max_profiles;
