-- +goose Up
-- media_items has many columns that the Go model (models.MediaItem) declares as
-- non-pointer string/int fields, yet migration 001 left them nullable. Every
-- scanner — catalog item_repo, catalog browse, jellycompat, sections, and the
-- catalog API handler — scans these straight into the non-pointer fields, so a
-- NULL row panics with "cannot scan NULL into *string" (or *int). item_repo
-- papers over a subset (poster/backdrop/logo/metadata paths) with COALESCE in
-- its SELECT list, but the other four scanners read the same columns raw and
-- crash; sort_title/original_title/etc. are not coalesced anywhere.
--
-- No writer stores a meaningful NULL: every insert/upsert passes the Go field
-- ('' or 0 at worst), and all sort/filter SQL already collapses NULL and ''
-- (COALESCE(NULLIF(BTRIM(sort_title), ''), title); "poster_path IS NULL OR
-- poster_path = ''"; etc.). The only deliberate NULLs are poster_thumbhash /
-- backdrop_thumbhash written via NULLIF($, '') in item_repo; those writers are
-- updated in the same change to store '' directly. NULL rows otherwise arise
-- only from raw SQL inserts and legacy data.
--
-- Enforce the invariant the code already assumes so the whole class of NULL-scan
-- crash is fixed in one place rather than scattering COALESCE across every
-- current and future scanner. This mirrors what later migrations already did for
-- original_language, show_status, default_metadata_language, and the *_source_path
-- columns (all NOT NULL DEFAULT '').

-- Backfill existing NULLs to the type-appropriate empty value.
UPDATE media_items SET
    sort_title         = COALESCE(sort_title, ''),
    original_title     = COALESCE(original_title, ''),
    content_rating     = COALESCE(content_rating, ''),
    overview           = COALESCE(overview, ''),
    tagline            = COALESCE(tagline, ''),
    imdb_id            = COALESCE(imdb_id, ''),
    tmdb_id            = COALESCE(tmdb_id, ''),
    tvdb_id            = COALESCE(tvdb_id, ''),
    poster_path        = COALESCE(poster_path, ''),
    poster_thumbhash   = COALESCE(poster_thumbhash, ''),
    backdrop_path      = COALESCE(backdrop_path, ''),
    backdrop_thumbhash = COALESCE(backdrop_thumbhash, ''),
    logo_path          = COALESCE(logo_path, ''),
    metadata_s3_path   = COALESCE(metadata_s3_path, ''),
    metadata_etag      = COALESCE(metadata_etag, '')
WHERE sort_title IS NULL OR original_title IS NULL OR content_rating IS NULL
   OR overview IS NULL OR tagline IS NULL OR imdb_id IS NULL OR tmdb_id IS NULL
   OR tvdb_id IS NULL OR poster_path IS NULL OR poster_thumbhash IS NULL
   OR backdrop_path IS NULL OR backdrop_thumbhash IS NULL OR logo_path IS NULL
   OR metadata_s3_path IS NULL OR metadata_etag IS NULL;

UPDATE media_items SET year = 0 WHERE year IS NULL;
UPDATE media_items SET runtime = 0 WHERE runtime IS NULL;

-- Establish defaults and NOT NULL atomically (single ACCESS EXCLUSIVE lock).
ALTER TABLE media_items
    ALTER COLUMN sort_title         SET DEFAULT '', ALTER COLUMN sort_title         SET NOT NULL,
    ALTER COLUMN original_title     SET DEFAULT '', ALTER COLUMN original_title     SET NOT NULL,
    ALTER COLUMN content_rating     SET DEFAULT '', ALTER COLUMN content_rating     SET NOT NULL,
    ALTER COLUMN overview           SET DEFAULT '', ALTER COLUMN overview           SET NOT NULL,
    ALTER COLUMN tagline            SET DEFAULT '', ALTER COLUMN tagline            SET NOT NULL,
    ALTER COLUMN imdb_id            SET DEFAULT '', ALTER COLUMN imdb_id            SET NOT NULL,
    ALTER COLUMN tmdb_id            SET DEFAULT '', ALTER COLUMN tmdb_id            SET NOT NULL,
    ALTER COLUMN tvdb_id            SET DEFAULT '', ALTER COLUMN tvdb_id            SET NOT NULL,
    ALTER COLUMN poster_path        SET DEFAULT '', ALTER COLUMN poster_path        SET NOT NULL,
    ALTER COLUMN poster_thumbhash   SET DEFAULT '', ALTER COLUMN poster_thumbhash   SET NOT NULL,
    ALTER COLUMN backdrop_path      SET DEFAULT '', ALTER COLUMN backdrop_path      SET NOT NULL,
    ALTER COLUMN backdrop_thumbhash SET DEFAULT '', ALTER COLUMN backdrop_thumbhash SET NOT NULL,
    ALTER COLUMN logo_path          SET DEFAULT '', ALTER COLUMN logo_path          SET NOT NULL,
    ALTER COLUMN metadata_s3_path   SET DEFAULT '', ALTER COLUMN metadata_s3_path   SET NOT NULL,
    ALTER COLUMN metadata_etag      SET DEFAULT '', ALTER COLUMN metadata_etag      SET NOT NULL,
    ALTER COLUMN year               SET DEFAULT 0,  ALTER COLUMN year               SET NOT NULL,
    ALTER COLUMN runtime            SET DEFAULT 0,  ALTER COLUMN runtime            SET NOT NULL;

-- +goose Down
ALTER TABLE media_items
    ALTER COLUMN sort_title         DROP NOT NULL, ALTER COLUMN sort_title         DROP DEFAULT,
    ALTER COLUMN original_title     DROP NOT NULL, ALTER COLUMN original_title     DROP DEFAULT,
    ALTER COLUMN content_rating     DROP NOT NULL, ALTER COLUMN content_rating     DROP DEFAULT,
    ALTER COLUMN overview           DROP NOT NULL, ALTER COLUMN overview           DROP DEFAULT,
    ALTER COLUMN tagline            DROP NOT NULL, ALTER COLUMN tagline            DROP DEFAULT,
    ALTER COLUMN imdb_id            DROP NOT NULL, ALTER COLUMN imdb_id            DROP DEFAULT,
    ALTER COLUMN tmdb_id            DROP NOT NULL, ALTER COLUMN tmdb_id            DROP DEFAULT,
    ALTER COLUMN tvdb_id            DROP NOT NULL, ALTER COLUMN tvdb_id            DROP DEFAULT,
    ALTER COLUMN poster_path        DROP NOT NULL, ALTER COLUMN poster_path        DROP DEFAULT,
    ALTER COLUMN poster_thumbhash   DROP NOT NULL, ALTER COLUMN poster_thumbhash   DROP DEFAULT,
    ALTER COLUMN backdrop_path      DROP NOT NULL, ALTER COLUMN backdrop_path      DROP DEFAULT,
    ALTER COLUMN backdrop_thumbhash DROP NOT NULL, ALTER COLUMN backdrop_thumbhash DROP DEFAULT,
    ALTER COLUMN logo_path          DROP NOT NULL, ALTER COLUMN logo_path          DROP DEFAULT,
    ALTER COLUMN metadata_s3_path   DROP NOT NULL, ALTER COLUMN metadata_s3_path   DROP DEFAULT,
    ALTER COLUMN metadata_etag      DROP NOT NULL, ALTER COLUMN metadata_etag      DROP DEFAULT,
    ALTER COLUMN year               DROP NOT NULL, ALTER COLUMN year               DROP DEFAULT,
    ALTER COLUMN runtime            DROP NOT NULL, ALTER COLUMN runtime            DROP DEFAULT;
