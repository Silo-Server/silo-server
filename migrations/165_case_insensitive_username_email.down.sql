-- Revert username/email to case-sensitive text. The citext extension is left
-- installed: dropping it is unnecessary and would fail if any other object
-- ever depends on it.
ALTER TABLE public.users
    ALTER COLUMN username TYPE text USING username::text,
    ALTER COLUMN email TYPE text USING email::text;
