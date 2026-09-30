-- +goose NO TRANSACTION

-- +goose Up
-- Chapter thumbnails used to be stored twice, as a full-size original.webp and
-- a 300px w300.webp. thumbnail_path held the original, and every reader
-- rewrote it to w300 before signing a URL. New thumbnails store only w300.webp
-- and thumbnail_path names it directly.
--
-- This trigger keeps every chapter row on the w300 image: a thumbnail_path
-- naming a legacy original is rewritten to the w300 object beside it, which
-- earlier builds always wrote before saving the original's path. It also
-- catches writes from a node still on an earlier build during a rolling
-- upgrade, and from any copy of the chapters array loaded before this
-- migration, so no row can point at an original again. That is what lets the
-- originals cleanup task delete original.webp objects without stranding a row.
--
-- Each statement commits on its own. CREATE TRIGGER briefly locks
-- media_files against writes; committing it before the backfill keeps that
-- lock from lasting the whole rewrite, which takes only row locks. Every
-- statement is safe to re-run if the migration stops partway.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION media_files_chapter_thumbnails_served_variant() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF jsonb_typeof(NEW.chapters) = 'array' THEN
        NEW.chapters := (
            SELECT jsonb_agg(
                CASE
                    WHEN e->>'thumbnail_path' ~ '^chapter-images/[0-9]+/[0-9]+/original\.webp$'
                    THEN jsonb_set(e, '{thumbnail_path}',
                                   to_jsonb(regexp_replace(e->>'thumbnail_path', '/original\.webp$', '/w300.webp')))
                    ELSE e
                END
                ORDER BY ord
            )
            FROM jsonb_array_elements(NEW.chapters) WITH ORDINALITY AS t(e, ord)
        );
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE OR REPLACE TRIGGER media_files_chapter_thumbnails_served_variant
    BEFORE INSERT OR UPDATE OF chapters ON media_files
    FOR EACH ROW
    WHEN (NEW.chapters::text LIKE '%/original.webp%')
    EXECUTE FUNCTION media_files_chapter_thumbnails_served_variant();

-- Point existing rows at the w300 image by saving them through the trigger.
-- The artwork reconciler checks thumbnail_path, so this also moves its check
-- onto the object clients actually load.
UPDATE media_files
SET chapters = chapters
WHERE chapters::text LIKE '%/original.webp%';

-- +goose Down
-- The rewritten paths stay: earlier builds rewrite /original. to /w300. before
-- serving and leave a w300 path unchanged, so they read them correctly, and
-- pointing rows back at original.webp would reference objects the cleanup
-- task may already have deleted.
DROP TRIGGER IF EXISTS media_files_chapter_thumbnails_served_variant ON media_files;
DROP FUNCTION IF EXISTS media_files_chapter_thumbnails_served_variant();
