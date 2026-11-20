-- +goose NO TRANSACTION

-- +goose Up
-- A book's work-level identifiers (openlibrary_work, hardcover) name the book
-- an edition belongs to, so every edition of the book holds the same value.
-- media_item_provider_ids lets one item of a type hold a value, so they move
-- to their own table, where a row is a claim that the item belongs to that
-- work and many items can claim one work. source is the metadata capability
-- that made the claim; rows moved here have none. See
-- docs/architecture/audiobook-metadata.md, "Work ids".
CREATE TABLE IF NOT EXISTS public.media_item_work_ids (
    content_id TEXT COLLATE "C" NOT NULL
        REFERENCES public.media_items(content_id) ON UPDATE CASCADE ON DELETE CASCADE,
    provider TEXT NOT NULL CHECK (provider IN ('openlibrary_work', 'hardcover')),
    provider_id TEXT NOT NULL CHECK (provider_id <> ''),
    evidence TEXT NOT NULL DEFAULT 'unknown'
        CHECK (evidence IN ('isbn', 'title_author', 'unknown')),
    source TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (content_id, provider)
);

CREATE INDEX IF NOT EXISTS idx_media_item_work_ids_provider_id
    ON public.media_item_work_ids (provider, provider_id);

-- Forbid the keys on book rows before moving them, so a writer still running
-- an older build fails instead of putting one back after the move. NOT VALID
-- holds the table's ACCESS EXCLUSIVE lock only to record the constraint; the
-- VALIDATE below scans under SHARE UPDATE EXCLUSIVE, which lets writes through.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'public.media_item_provider_ids'::regclass
          AND conname = 'media_item_provider_ids_book_work_ids_check'
    ) THEN
        ALTER TABLE public.media_item_provider_ids
            ADD CONSTRAINT media_item_provider_ids_book_work_ids_check
            CHECK (
                item_type NOT IN ('audiobook', 'ebook')
                OR lower(btrim(provider)) NOT IN ('openlibrary_work', 'hardcover')
            ) NOT VALID;
    END IF;
END;
$$;
-- +goose StatementEnd

-- One statement, so the rows leave media_item_provider_ids only together with
-- their copy. Keys are stored folded (Hardcover is hardcover); when two rows of
-- one item fold to one key the most recently updated wins. A claim already
-- written by a newer build stays.
WITH moved AS (
    DELETE FROM public.media_item_provider_ids
    WHERE item_type IN ('audiobook', 'ebook')
      AND lower(btrim(provider)) IN ('openlibrary_work', 'hardcover')
    RETURNING content_id, lower(btrim(provider)) AS provider, btrim(provider_id) AS provider_id,
              created_at, updated_at
),
kept AS (
    SELECT DISTINCT ON (content_id, provider)
           content_id, provider, provider_id, created_at, updated_at
    FROM moved
    WHERE provider_id <> ''
    ORDER BY content_id, provider, updated_at DESC, provider_id
)
INSERT INTO public.media_item_work_ids (content_id, provider, provider_id, evidence, source, created_at, updated_at)
SELECT content_id, provider, provider_id, 'unknown', '', created_at, updated_at
FROM kept
ON CONFLICT (content_id, provider) DO NOTHING;

ALTER TABLE public.media_item_provider_ids
    VALIDATE CONSTRAINT media_item_provider_ids_book_work_ids_check;

-- +goose Down
ALTER TABLE public.media_item_provider_ids
    DROP CONSTRAINT IF EXISTS media_item_provider_ids_book_work_ids_check;

-- Put back what the old layout can hold: one item per work value and type.
-- The other claims are lost with the table.
INSERT INTO public.media_item_provider_ids (content_id, item_type, provider, provider_id, created_at, updated_at)
SELECT w.content_id, mi.type, w.provider, w.provider_id, w.created_at, w.updated_at
FROM public.media_item_work_ids w
JOIN public.media_items mi ON mi.content_id = w.content_id
ORDER BY w.content_id, w.provider
ON CONFLICT DO NOTHING;

DROP TABLE IF EXISTS public.media_item_work_ids;
