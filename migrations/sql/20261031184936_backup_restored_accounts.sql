-- +goose Up
-- Accounts and profiles a selective restore created, by the backup's server
-- id and the source account and profile ids. Matching consults them first,
-- so running the same restore again maps each source to what the earlier run
-- created instead of creating it twice: a restored account or profile gets a
-- new id, and on the same server the source id no longer finds it. Rows go
-- with the account or profile they name.
CREATE TABLE public.backup_restored_accounts (
    source_server_id text NOT NULL,
    source_account_id bigint NOT NULL,
    user_id bigint NOT NULL REFERENCES public.users (id) ON DELETE CASCADE,
    restored_at timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (source_server_id, source_account_id)
);

CREATE INDEX backup_restored_accounts_user_idx ON public.backup_restored_accounts (user_id);

CREATE TABLE public.backup_restored_profiles (
    source_server_id text NOT NULL,
    source_account_id bigint NOT NULL,
    source_profile_id text NOT NULL,
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    restored_at timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (source_server_id, source_account_id, source_profile_id),
    FOREIGN KEY (user_id, profile_id) REFERENCES public.user_profiles (user_id, id) ON DELETE CASCADE
);

CREATE INDEX backup_restored_profiles_profile_idx ON public.backup_restored_profiles (user_id, profile_id);

-- +goose Down
DROP TABLE IF EXISTS public.backup_restored_profiles;
DROP TABLE IF EXISTS public.backup_restored_accounts;
