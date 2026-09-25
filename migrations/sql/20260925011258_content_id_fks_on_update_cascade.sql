-- Restore ON UPDATE CASCADE on every content_id-family foreign key.
--
-- 20260614120000_content_id_online_reid rebuilt each FK referencing
-- media_items, seasons or episodes with ON UPDATE CASCADE so that
-- silo_rename_content_id can move a content_id in place. Tables created
-- afterwards (media_item_aliases, item_videos, media_extras and the
-- audiobook_* tables among them) declared their FKs without it, so renaming
-- an item with rows there fails with a foreign key violation: applying a
-- match or re-anchoring a provider id leaves the item unmatched.
--
-- This repeats that migration's rebuild for any FK still lacking an ON UPDATE
-- clause. pg_get_constraintdef carries the existing ON DELETE clause through.

-- +goose Up
-- +goose StatementBegin
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN
        SELECT con.conname,
               con.conrelid::regclass AS rel,
               pg_get_constraintdef(con.oid) AS condef
        FROM pg_constraint con
        WHERE con.contype = 'f'
          AND con.confrelid IN ('media_items'::regclass, 'seasons'::regclass, 'episodes'::regclass)
          AND position('ON UPDATE' IN pg_get_constraintdef(con.oid)) = 0
    LOOP
        EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', r.rel, r.conname);
        EXECUTE format('ALTER TABLE %s ADD CONSTRAINT %I %s ON UPDATE CASCADE',
                       r.rel, r.conname, r.condef);
    END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
-- The cascades stay. They only fire when silo_rename_content_id moves a
-- content_id, which older binaries also rely on, and this migration cannot
-- tell which constraints it changed from the ones 20260614120000 already had.
SELECT 1;
