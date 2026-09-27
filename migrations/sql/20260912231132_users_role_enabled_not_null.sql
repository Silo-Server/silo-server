-- +goose Up
-- Repository scans require string/bool values. Refuse ambiguous legacy account
-- state instead of assigning a role or enabling an account during upgrade.
-- Operators must choose explicit values and retry. All DDL is transactional.
-- Keep enabled DEFAULT true; role deliberately has no default.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM public.users WHERE role IS NULL OR enabled IS NULL) THEN
        RAISE EXCEPTION 'schema hygiene: users.role or users.enabled contains NULL'
            USING HINT = 'Review affected accounts and set explicit role/enabled values, then retry; no account values have been changed.';
    END IF;
END;
$$;
-- +goose StatementEnd
ALTER TABLE public.users ALTER COLUMN role SET NOT NULL;
ALTER TABLE public.users ALTER COLUMN enabled SET NOT NULL;

-- +goose Down
ALTER TABLE public.users ALTER COLUMN enabled DROP NOT NULL;
ALTER TABLE public.users ALTER COLUMN role DROP NOT NULL;
