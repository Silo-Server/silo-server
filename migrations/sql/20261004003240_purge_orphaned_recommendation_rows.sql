-- +goose Up
-- Deleting a profile left its ratings, taste vector, taste clusters and cached
-- recommendation rows behind. None of these tables has a foreign key to
-- user_profiles (profiles may live outside Postgres), so the rows outlived the
-- profile: the nightly job kept rebuilding the deleted profile's taste, and its
-- ratings kept feeding other accounts' Similar Users rows. Profile deletion
-- now purges them; this removes the rows earlier deletions left.
--
-- Only on the Postgres user store, where user_profiles holds every profile.
-- With the SQLite user store, profiles live in per-user SQLite files and
-- user_profiles does not list them, so an orphan cannot be told from a live
-- profile here and nothing is deleted. As a further guard, an account with no
-- row at all in user_profiles is skipped. Global recommendation rows
-- (user_id NULL) belong to no profile and stay.
-- +goose StatementBegin
DO $$
BEGIN
    IF COALESCE((SELECT value FROM public.server_settings WHERE key = 'userdb.backend'), 'postgres') <> 'postgres' THEN
        RETURN;
    END IF;

    DELETE FROM public.user_ratings t
    WHERE EXISTS (SELECT 1 FROM public.user_profiles p WHERE p.user_id = t.user_id)
      AND NOT EXISTS (SELECT 1 FROM public.user_profiles p WHERE p.user_id = t.user_id AND p.id = t.profile_id);

    DELETE FROM public.user_taste_profiles t
    WHERE EXISTS (SELECT 1 FROM public.user_profiles p WHERE p.user_id = t.user_id)
      AND NOT EXISTS (SELECT 1 FROM public.user_profiles p WHERE p.user_id = t.user_id AND p.id = t.profile_id);

    DELETE FROM public.user_taste_clusters t
    WHERE EXISTS (SELECT 1 FROM public.user_profiles p WHERE p.user_id = t.user_id)
      AND NOT EXISTS (SELECT 1 FROM public.user_profiles p WHERE p.user_id = t.user_id AND p.id = t.profile_id);

    DELETE FROM public.recommendation_cache t
    WHERE t.user_id IS NOT NULL
      AND EXISTS (SELECT 1 FROM public.user_profiles p WHERE p.user_id = t.user_id)
      AND NOT EXISTS (SELECT 1 FROM public.user_profiles p WHERE p.user_id = t.user_id AND p.id = t.profile_id);
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- The deleted rows belonged to profiles that no longer exist; there is
-- nothing to restore.
SELECT 1;
