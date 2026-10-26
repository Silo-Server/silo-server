-- +goose NO TRANSACTION
-- +goose Up
-- A profile id is only unique within its account: every account from before
-- profiles has a profile named 'default'. Episode notification state was keyed
-- by profile id alone, so two accounts' 'default' profiles shared one series
-- interest row (whichever account wrote it kept it, and only that account was
-- notified), one set of notification switches, and one delivery per episode,
-- and each saw the other's inbox. Key all of it by account and profile.
--
-- CONCURRENTLY keeps these tables writable while the indexes build; it cannot
-- run inside a transaction, hence NO TRANSACTION. Each step can run again
-- after an interrupted attempt: an interrupted concurrent build leaves an
-- invalid index that IF NOT EXISTS would keep, so those are dropped first, and
-- the key swaps check whether they already happened.
-- +goose StatementBegin
DO $$
DECLARE
    idx text;
BEGIN
    FOR idx IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND NOT i.indisvalid
          AND c.relname IN (
              'profile_series_interest_account_pkey',
              'notification_deliveries_account_profile_event_key',
              'notification_deliveries_account_profile_episode_key',
              'notification_deliveries_account_profile_created_idx',
              'notification_deliveries_account_unread_idx')
    LOOP
        EXECUTE format('DROP INDEX public.%I', idx);
    END LOOP;
END;
$$;
-- +goose StatementEnd

-- profile_series_interest: rows already carry user_id, and the wider key holds
-- every existing row. The primary key leads with (user_id, profile_id), which
-- serves the per-profile reads profile_series_interest_profile_idx did.
-- Already swapped on a rerun: the index then exists under the constraint's
-- name, and the stray copy built here is dropped below.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS profile_series_interest_account_pkey
    ON public.profile_series_interest (user_id, profile_id, library_id, series_id);

-- +goose StatementBegin
DO $$
BEGIN
    IF pg_get_constraintdef((SELECT oid FROM pg_constraint
            WHERE conrelid = 'public.profile_series_interest'::regclass
              AND conname = 'profile_series_interest_pkey'))
        <> 'PRIMARY KEY (user_id, profile_id, library_id, series_id)' THEN
        ALTER TABLE public.profile_series_interest
            DROP CONSTRAINT profile_series_interest_pkey,
            ADD CONSTRAINT profile_series_interest_pkey
                PRIMARY KEY USING INDEX profile_series_interest_account_pkey;
    END IF;
END;
$$;
-- +goose StatementEnd

DROP INDEX CONCURRENTLY IF EXISTS public.profile_series_interest_account_pkey;
DROP INDEX CONCURRENTLY IF EXISTS public.profile_series_interest_profile_idx;

-- notification_deliveries: the episode at-most-once keys and the inbox read
-- indexes. The release delivery path inserts with a bare ON CONFLICT DO
-- NOTHING, so it honours whichever keys exist while they are swapped. One
-- (user_id, profile_id, created_at, id) index replaces both
-- notification_deliveries_inbox_idx and
-- notification_deliveries_profile_created_idx; newest-first pages scan it
-- backwards.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS notification_deliveries_account_profile_event_key
    ON public.notification_deliveries (user_id, profile_id, release_event_id)
    WHERE release_event_id IS NOT NULL;
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS notification_deliveries_account_profile_episode_key
    ON public.notification_deliveries (user_id, profile_id, episode_id)
    WHERE type = 'episode.available';
CREATE INDEX CONCURRENTLY IF NOT EXISTS notification_deliveries_account_profile_created_idx
    ON public.notification_deliveries (user_id, profile_id, created_at, id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS notification_deliveries_account_unread_idx
    ON public.notification_deliveries (user_id, profile_id, read_at, created_at DESC, id DESC);

DROP INDEX CONCURRENTLY IF EXISTS public.notification_deliveries_profile_event_key;
DROP INDEX CONCURRENTLY IF EXISTS public.notification_deliveries_profile_episode_key;
DROP INDEX CONCURRENTLY IF EXISTS public.notification_deliveries_inbox_idx;
DROP INDEX CONCURRENTLY IF EXISTS public.notification_deliveries_profile_created_idx;
DROP INDEX CONCURRENTLY IF EXISTS public.notification_deliveries_unread_idx;

-- notification_preferences gains its account. A row shared by two accounts'
-- profiles with the same id is copied to each of them, so every profile keeps
-- the switches it has today. The accounts that have a profile with the row's
-- id come from user_profiles and, for profiles stored outside Postgres, from
-- the account-keyed notification tables. A row no account can be found for
-- belongs to a deleted profile and is dropped. The table holds at most one
-- row per profile, so this rewrites it in one statement.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'notification_preferences'
          AND column_name = 'user_id'
    ) THEN
        LOCK TABLE public.notification_preferences IN ACCESS EXCLUSIVE MODE;
        ALTER TABLE public.notification_preferences
            DROP CONSTRAINT notification_preferences_pkey,
            ADD COLUMN user_id integer;
        INSERT INTO public.notification_preferences
            (user_id, profile_id, enabled, notify_favorites, notify_watchlist,
             notify_continue_watching, notify_next_up, updated_at)
        SELECT owner.user_id, np.profile_id, np.enabled, np.notify_favorites, np.notify_watchlist,
               np.notify_continue_watching, np.notify_next_up, np.updated_at
        FROM public.notification_preferences np
        JOIN (
            SELECT user_id, id AS profile_id FROM public.user_profiles
            UNION SELECT user_id, profile_id FROM public.profile_series_interest
            UNION SELECT user_id, profile_id FROM public.notification_deliveries
            UNION SELECT user_id, profile_id FROM public.notification_email_prefs
        ) owner ON owner.profile_id = np.profile_id
        JOIN public.users u ON u.id = owner.user_id
        WHERE np.user_id IS NULL;
        DELETE FROM public.notification_preferences WHERE user_id IS NULL;
        ALTER TABLE public.notification_preferences
            ALTER COLUMN user_id SET NOT NULL,
            ADD CONSTRAINT notification_preferences_pkey PRIMARY KEY (user_id, profile_id),
            ADD CONSTRAINT notification_preferences_user_id_fkey FOREIGN KEY (user_id)
                REFERENCES public.users(id) ON DELETE CASCADE;
    END IF;
END;
$$;
-- +goose StatementEnd

-- Interest rows written before this migration hold one account's state for
-- both accounts' profiles. Restart the interest rebuild from the first
-- profile so its next run, at startup, writes the missing account's rows.
DELETE FROM public.notification_backfill_state WHERE task = 'interest_rebuild';

-- +goose Down
-- The profile-only keys cannot hold two accounts' rows for one profile id.
-- Refuse instead of choosing which account's notification state to delete.
-- Stop the servers that write notifications first: a row written after this
-- check would fail the unique builds below.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM public.profile_series_interest
               GROUP BY profile_id, library_id, series_id HAVING count(*) > 1)
       OR EXISTS (SELECT 1 FROM public.notification_preferences
                  GROUP BY profile_id HAVING count(*) > 1)
       OR EXISTS (SELECT 1 FROM public.notification_deliveries
                  WHERE release_event_id IS NOT NULL
                  GROUP BY profile_id, release_event_id HAVING count(*) > 1)
       OR EXISTS (SELECT 1 FROM public.notification_deliveries
                  WHERE type = 'episode.available'
                  GROUP BY profile_id, episode_id HAVING count(*) > 1) THEN
        RAISE EXCEPTION 'notification state for profiles with the same id on different accounts cannot be keyed by profile id alone';
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
    idx text;
BEGIN
    FOR idx IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND NOT i.indisvalid
          AND c.relname IN (
              'profile_series_interest_profile_pkey',
              'profile_series_interest_profile_idx',
              'notification_deliveries_profile_event_key',
              'notification_deliveries_profile_episode_key',
              'notification_deliveries_inbox_idx',
              'notification_deliveries_profile_created_idx',
              'notification_deliveries_unread_idx')
    LOOP
        EXECUTE format('DROP INDEX public.%I', idx);
    END LOOP;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'notification_preferences'
          AND column_name = 'user_id'
    ) THEN
        ALTER TABLE public.notification_preferences
            DROP CONSTRAINT notification_preferences_user_id_fkey,
            DROP CONSTRAINT notification_preferences_pkey,
            DROP COLUMN user_id,
            ADD CONSTRAINT notification_preferences_pkey PRIMARY KEY (profile_id);
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS notification_deliveries_profile_event_key
    ON public.notification_deliveries (profile_id, release_event_id)
    WHERE release_event_id IS NOT NULL;
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS notification_deliveries_profile_episode_key
    ON public.notification_deliveries (profile_id, episode_id)
    WHERE type = 'episode.available';
CREATE INDEX CONCURRENTLY IF NOT EXISTS notification_deliveries_inbox_idx
    ON public.notification_deliveries (profile_id, created_at DESC);
CREATE INDEX CONCURRENTLY IF NOT EXISTS notification_deliveries_profile_created_idx
    ON public.notification_deliveries (profile_id, created_at, id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS notification_deliveries_unread_idx
    ON public.notification_deliveries (profile_id, read_at, created_at DESC);
DROP INDEX CONCURRENTLY IF EXISTS public.notification_deliveries_account_profile_event_key;
DROP INDEX CONCURRENTLY IF EXISTS public.notification_deliveries_account_profile_episode_key;
DROP INDEX CONCURRENTLY IF EXISTS public.notification_deliveries_account_profile_created_idx;
DROP INDEX CONCURRENTLY IF EXISTS public.notification_deliveries_account_unread_idx;

CREATE INDEX CONCURRENTLY IF NOT EXISTS profile_series_interest_profile_idx
    ON public.profile_series_interest (profile_id, updated_at DESC);
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS profile_series_interest_profile_pkey
    ON public.profile_series_interest (profile_id, library_id, series_id);
-- +goose StatementBegin
DO $$
BEGIN
    IF pg_get_constraintdef((SELECT oid FROM pg_constraint
            WHERE conrelid = 'public.profile_series_interest'::regclass
              AND conname = 'profile_series_interest_pkey'))
        <> 'PRIMARY KEY (profile_id, library_id, series_id)' THEN
        ALTER TABLE public.profile_series_interest
            DROP CONSTRAINT profile_series_interest_pkey,
            ADD CONSTRAINT profile_series_interest_pkey
                PRIMARY KEY USING INDEX profile_series_interest_profile_pkey;
    END IF;
END;
$$;
-- +goose StatementEnd
DROP INDEX CONCURRENTLY IF EXISTS public.profile_series_interest_profile_pkey;
