-- +goose Up
-- A taste profile with no positive signal keeps its row (signal counts, stale
-- mark) but has no taste vector: a vector built only from disliked or abandoned
-- titles points away from them and recommends their opposites. NULL marks that
-- state. Dropping NOT NULL is a catalog-only change; the HNSW index skips NULL
-- rows.
ALTER TABLE public.user_taste_profiles
    ALTER COLUMN embedding DROP NOT NULL;

-- +goose Down
-- Rows without a vector cannot satisfy NOT NULL; the next refresh rebuilds
-- them.
DELETE FROM public.user_taste_profiles WHERE embedding IS NULL;
ALTER TABLE public.user_taste_profiles
    ALTER COLUMN embedding SET NOT NULL;
