-- Make user login identifiers case-insensitive. The citext column type compares
-- case-insensitively, so the existing users_username_key / users_email_key unique
-- indexes are rebuilt as case-insensitive and WHERE username = $1 / email = $1
-- lookups match regardless of case. Original casing is preserved for display.
CREATE EXTENSION IF NOT EXISTS citext;

ALTER TABLE public.users
    ALTER COLUMN username TYPE citext USING username::citext,
    ALTER COLUMN email TYPE citext USING email::citext;
