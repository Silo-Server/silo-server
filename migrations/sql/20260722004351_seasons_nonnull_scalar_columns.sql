-- +goose Up
UPDATE seasons SET
    title             = COALESCE(title, ''),
    overview          = COALESCE(overview, ''),
    poster_path       = COALESCE(poster_path, ''),
    poster_thumbhash  = COALESCE(poster_thumbhash, ''),
    metadata_s3_path  = COALESCE(metadata_s3_path, ''),
    metadata_etag     = COALESCE(metadata_etag, ''),
    metadata_source   = COALESCE(metadata_source, '')
WHERE title IS NULL OR overview IS NULL OR poster_path IS NULL
   OR poster_thumbhash IS NULL OR metadata_s3_path IS NULL
   OR metadata_etag IS NULL OR metadata_source IS NULL;

ALTER TABLE seasons
    ALTER COLUMN title            SET DEFAULT '', ALTER COLUMN title            SET NOT NULL,
    ALTER COLUMN overview         SET DEFAULT '', ALTER COLUMN overview         SET NOT NULL,
    ALTER COLUMN poster_path      SET DEFAULT '', ALTER COLUMN poster_path      SET NOT NULL,
    ALTER COLUMN poster_thumbhash SET DEFAULT '', ALTER COLUMN poster_thumbhash SET NOT NULL,
    ALTER COLUMN metadata_s3_path SET DEFAULT '', ALTER COLUMN metadata_s3_path SET NOT NULL,
    ALTER COLUMN metadata_etag    SET DEFAULT '', ALTER COLUMN metadata_etag    SET NOT NULL,
    ALTER COLUMN metadata_source  SET DEFAULT '', ALTER COLUMN metadata_source  SET NOT NULL;

-- +goose Down
ALTER TABLE seasons
    ALTER COLUMN title            DROP NOT NULL, ALTER COLUMN title            DROP DEFAULT,
    ALTER COLUMN overview         DROP NOT NULL, ALTER COLUMN overview         DROP DEFAULT,
    ALTER COLUMN poster_path      DROP NOT NULL, ALTER COLUMN poster_path      DROP DEFAULT,
    ALTER COLUMN poster_thumbhash DROP NOT NULL, ALTER COLUMN poster_thumbhash DROP DEFAULT,
    ALTER COLUMN metadata_s3_path DROP NOT NULL, ALTER COLUMN metadata_s3_path DROP DEFAULT,
    ALTER COLUMN metadata_etag    DROP NOT NULL, ALTER COLUMN metadata_etag    DROP DEFAULT,
    ALTER COLUMN metadata_source  DROP NOT NULL, ALTER COLUMN metadata_source  DROP DEFAULT;
