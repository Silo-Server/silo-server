-- +goose Up
-- Trickplay: seek-bar preview sprite sheets, generated per media file for
-- libraries that opt in (off by default). See internal/trickplay and
-- docs/architecture/trickplay.md.
ALTER TABLE public.media_folders
    ADD COLUMN trickplay_enabled boolean NOT NULL DEFAULT false;

-- One row per media file of an opted-in library. The row is both the work
-- queue entry (state, available_at, the lease) and the published manifest
-- (revision and the sheet geometry). A regeneration keeps serving the
-- published revision until the new one publishes.
CREATE TABLE public.media_file_trickplay (
    media_file_id bigint PRIMARY KEY REFERENCES public.media_files(id) ON DELETE CASCADE,
    state text NOT NULL DEFAULT 'pending',
    available_at timestamptz NOT NULL DEFAULT now(),
    -- The newest generation algorithm that has touched the row. A server
    -- claims and invalidates only rows at or below its own version, so
    -- mixed versions during a rolling upgrade do not undo each other's work.
    recipe_version integer NOT NULL,
    lease_owner text,
    lease_expires_at timestamptz,
    -- The revision a running generation uploads under, set just before its
    -- first upload; its prefix is queued for deletion if it never publishes.
    work_revision bigint,
    failure_count integer NOT NULL DEFAULT 0,
    last_error text NOT NULL DEFAULT '',
    -- The file a claim started from, which an unusable row keeps so that a
    -- replaced file is tried again.
    source_size bigint,
    source_hash text,
    source_duration bigint,
    -- The published sheets: trickplay/<media_file_id>/<revision>/ in the
    -- assets store named by store_identity, made from the file identity in
    -- published_* with the settings in published_recipe.
    revision bigint,
    published_recipe text,
    published_size bigint,
    published_hash text,
    published_duration bigint,
    store_identity text,
    width integer,
    height integer,
    tile_columns integer,
    tile_rows integer,
    interval_ms integer,
    thumbnail_count integer,
    sheet_count integer,
    bandwidth integer,
    sheet_bytes bigint,
    decoder text,
    filled integer,
    generated_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT media_file_trickplay_state_check
        CHECK (state IN ('pending', 'running', 'ready', 'unusable')),
    CONSTRAINT media_file_trickplay_lease_check
        CHECK ((state = 'running') = (lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL)),
    CONSTRAINT media_file_trickplay_work_revision_check
        CHECK (work_revision IS NULL OR (state = 'running' AND work_revision > 0)),
    CONSTRAINT media_file_trickplay_manifest_check
        CHECK (revision IS NULL OR (revision > 0 AND published_recipe IS NOT NULL AND store_identity IS NOT NULL
            AND width > 0 AND height > 0 AND tile_columns > 0 AND tile_rows > 0 AND interval_ms > 0
            AND thumbnail_count > 0 AND sheet_count > 0)),
    CONSTRAINT media_file_trickplay_failure_count_check CHECK (failure_count >= 0)
);

CREATE INDEX media_file_trickplay_claim_idx
    ON public.media_file_trickplay (available_at, media_file_id)
    WHERE state = 'pending';

CREATE INDEX media_file_trickplay_lease_idx
    ON public.media_file_trickplay (lease_expires_at)
    WHERE state = 'running';

-- Sheets are queued for deletion under their revision's prefix.
ALTER TABLE public.blob_gc_queue DROP CONSTRAINT blob_gc_queue_prefix_check;
ALTER TABLE public.blob_gc_queue ADD CONSTRAINT blob_gc_queue_prefix_check
    CHECK (prefix ~ '^chapter-images/[1-9][0-9]*/$' OR prefix ~ '^trickplay/[1-9][0-9]*/[1-9][0-9]*/$');

-- A deleted row, whether its library opted out or its media file was
-- deleted (the cascade), leaves nothing referencing its revisions. Go code
-- queues the revisions a row displaces while it lives.
-- +goose StatementBegin
CREATE FUNCTION public.queue_deleted_trickplay_revisions()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO public.blob_gc_queue (prefix, not_before)
    SELECT 'trickplay/' || old_rows.media_file_id || '/' || revisions.revision || '/', now() + interval '48 hours'
    FROM old_rows
    CROSS JOIN LATERAL (VALUES (old_rows.revision), (old_rows.work_revision)) AS revisions(revision)
    WHERE revisions.revision IS NOT NULL
    ON CONFLICT (prefix) DO NOTHING;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER media_file_trickplay_queue_blob_gc
    AFTER DELETE ON public.media_file_trickplay
    REFERENCING OLD TABLE AS old_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION public.queue_deleted_trickplay_revisions();

-- +goose Down
DROP TRIGGER IF EXISTS media_file_trickplay_queue_blob_gc ON public.media_file_trickplay;
DROP FUNCTION IF EXISTS public.queue_deleted_trickplay_revisions();
DELETE FROM public.blob_gc_queue WHERE prefix LIKE 'trickplay/%';
ALTER TABLE public.blob_gc_queue DROP CONSTRAINT blob_gc_queue_prefix_check;
ALTER TABLE public.blob_gc_queue ADD CONSTRAINT blob_gc_queue_prefix_check
    CHECK (prefix ~ '^chapter-images/[1-9][0-9]*/$');
DROP TABLE IF EXISTS public.media_file_trickplay;
ALTER TABLE public.media_folders DROP COLUMN IF EXISTS trickplay_enabled;
