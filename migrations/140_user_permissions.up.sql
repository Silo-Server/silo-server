ALTER TABLE public.users
    ADD COLUMN IF NOT EXISTS permissions text[] NOT NULL DEFAULT '{}'::text[];

UPDATE public.users
SET permissions = '{}'::text[]
WHERE permissions IS NULL;
