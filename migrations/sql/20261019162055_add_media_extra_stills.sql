-- A local extra's 16:9 card image. The scanner copies a sidecar beside the
-- extra's file (<name>-thumb.<ext>, then <name>.<ext>) into the image store.
-- still_path is the cached copy, empty when the extra has none.
-- still_source identifies what produced it: a sidecar's file:// URL carrying
-- its size and modification time, so a scan reads the image again only when
-- it changed.
--
-- The revision GC's reference check covers still_path (artworkSweepSurfaces);
-- the next migration indexes it. The triggers queue a replaced or deleted
-- image for collection, as for item backdrops.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE media_extras
    ADD COLUMN IF NOT EXISTS still_path TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS still_source TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS still_thumbhash TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER media_extras_artwork_revision_gc_update
AFTER UPDATE OF still_path ON public.media_extras
FOR EACH ROW
WHEN (OLD.still_path IS DISTINCT FROM NEW.still_path)
EXECUTE FUNCTION public.queue_displaced_artwork_revision('still_path', 'still');
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER media_extras_artwork_revision_gc_delete
AFTER DELETE ON public.media_extras
FOR EACH ROW EXECUTE FUNCTION public.queue_displaced_artwork_revision('still_path', 'still');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS media_extras_artwork_revision_gc_delete ON public.media_extras;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TRIGGER IF EXISTS media_extras_artwork_revision_gc_update ON public.media_extras;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE media_extras
    DROP COLUMN IF EXISTS still_thumbhash,
    DROP COLUMN IF EXISTS still_source,
    DROP COLUMN IF EXISTS still_path;
-- +goose StatementEnd
