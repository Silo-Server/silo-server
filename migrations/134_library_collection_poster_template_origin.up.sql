ALTER TABLE library_collections
    ADD COLUMN IF NOT EXISTS poster_from_template BOOLEAN NOT NULL DEFAULT FALSE;
