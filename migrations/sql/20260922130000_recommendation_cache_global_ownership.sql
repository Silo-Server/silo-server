-- +goose Up
-- Global (non-personalized) recommendation cache rows are owned by no account.
-- 20260912231023_user_fk_integrity added recommendation_cache_user_id_fkey ->
-- users(id) ON DELETE CASCADE, and its orphan sweep deleted the global rows the
-- worker wrote under the sentinel user_id = 0 (no users row has id 0). Every
-- subsequent global cache write then failed the foreign key (SQLSTATE 23503),
-- so popular / recently-added / top-rated / genre sections lost their cache.
--
-- Represent "owned by no account" as user_id IS NULL instead of the sentinel 0.
-- A NULL foreign-key column is exempt from the reference check, so global rows
-- persist while personalized rows keep their account integrity and ON DELETE
-- CASCADE cleanup. The foreign key itself is intentionally left in place.
--
-- The primary key included user_id, so it cannot stay NOT NULL. Replace it with
-- a UNIQUE INDEX using NULLS NOT DISTINCT (PostgreSQL 15+, and this deployment
-- requires 18) so a single NULL-owned identity still collides on upsert.
ALTER TABLE public.recommendation_cache DROP CONSTRAINT recommendation_cache_pkey;
ALTER TABLE public.recommendation_cache ALTER COLUMN user_id DROP NOT NULL;
UPDATE public.recommendation_cache SET user_id = NULL WHERE user_id = 0;
CREATE UNIQUE INDEX recommendation_cache_identity_key
    ON public.recommendation_cache (user_id, profile_id, rec_type, source_item_id) NULLS NOT DISTINCT;

-- +goose Down
-- Restore the NOT NULL primary key. Global rows cannot return to the sentinel
-- user_id = 0 while the account foreign key exists, so drop them; they are a
-- cache the worker rebuilds (after rollback its global writes fail again, the
-- pre-fix behavior).
DELETE FROM public.recommendation_cache WHERE user_id IS NULL;
DROP INDEX IF EXISTS recommendation_cache_identity_key;
ALTER TABLE public.recommendation_cache ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE public.recommendation_cache
    ADD CONSTRAINT recommendation_cache_pkey PRIMARY KEY (user_id, profile_id, rec_type, source_item_id);
