-- +goose NO TRANSACTION

-- +goose Up
-- A profile can require an advisory age: with its advisory-age limit set, a
-- title with no advisory age is hidden too, so the profile sees only titles an
-- advisory service has rated at or under the limit. Without the option a
-- missing advisory age passes and the content-rating ceiling alone decides.
--
-- NOT NULL DEFAULT false keeps every existing profile on today's behavior.
-- A constant default is a catalog-only change; no row is rewritten.
--
-- The file runs without a wrapping transaction so the indexes below build
-- concurrently while the catalog keeps serving reads. Every statement is safe
-- to re-run after a partial failure.
ALTER TABLE public.user_profiles
    ADD COLUMN IF NOT EXISTS require_advisory_age boolean NOT NULL DEFAULT false;

-- 20260924233146_episode_catalog_advisory_age.sql added no index, because the
-- lenient limit, "advisory_age IS NULL OR advisory_age <= $n", matches nearly
-- every row. The strict limit, "advisory_age IS NOT NULL AND advisory_age <=
-- $n", matches only titles that have an age, and it is at its most selective
-- right after a library enables the provider, when almost no title has one.
-- Then a browse sorted by title walks the whole sort index for matches that
-- barely exist and falls back to a full scan. Measured on 150,000 titles with
-- 5 carrying an age: 46 ms with a parallel sequential scan, 5 ms with the
-- partial index below. Once coverage is dense the planner prefers the
-- (type, sort_title) index and filters, and ignores this one (about 1 ms
-- either way). The indexes hold only rows that have an age, so they stay
-- small, and the lenient predicate cannot use them and does not need to.
--
-- A failed concurrent build leaves an invalid index, so drop before building.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_advisory_age;
CREATE INDEX CONCURRENTLY idx_media_items_advisory_age
ON public.media_items USING btree (advisory_age)
WHERE advisory_age IS NOT NULL;

-- Episode listings always scope to one library folder, so the folder leads.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_episode_catalog_entries_advisory_age;
CREATE INDEX CONCURRENTLY idx_episode_catalog_entries_advisory_age
ON public.episode_catalog_entries USING btree (media_folder_id, advisory_age)
WHERE advisory_age IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_episode_catalog_entries_advisory_age;

DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_advisory_age;

ALTER TABLE public.user_profiles
    DROP COLUMN IF EXISTS require_advisory_age;
