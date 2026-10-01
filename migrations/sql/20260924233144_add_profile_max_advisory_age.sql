-- +goose Up
-- A profile's advisory-age limit: titles whose advisory age
-- (media_items.advisory_age, e.g. Common Sense Media's "13+") is above it are
-- hidden from the profile. NULL means no limit. The range matches the advisory
-- ages the host accepts, so every storable limit can take effect.
--
-- Adding a nullable column with no default is a catalog-only change; no row is
-- rewritten.
ALTER TABLE public.user_profiles
    ADD COLUMN IF NOT EXISTS max_advisory_age smallint
    CONSTRAINT user_profiles_max_advisory_age_check CHECK (max_advisory_age BETWEEN 1 AND 21);

-- +goose Down
ALTER TABLE public.user_profiles
    DROP COLUMN IF EXISTS max_advisory_age;
