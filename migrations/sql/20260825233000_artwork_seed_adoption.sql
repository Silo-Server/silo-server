-- +goose Up
ALTER TABLE public.artwork_revision_gc_candidates
    DROP CONSTRAINT IF EXISTS artwork_revision_inventory_source_class_check,
    ADD COLUMN seed_imported_at timestamptz,
    ADD COLUMN seed_expires_at timestamptz,
    ADD CONSTRAINT artwork_revision_inventory_source_class_check CHECK (
        source_class IN ('provider', 'plugin', 'library_sidecar', 'embedded', 'generated', 'upload', 'bundled', 'seed', 'unknown')
    ) NOT VALID;

ALTER TABLE public.artwork_storage_accounting_state
	ADD COLUMN seed_bytes bigint NOT NULL DEFAULT 0,
	ADD COLUMN expired_seed_bytes bigint NOT NULL DEFAULT 0,
    ADD COLUMN seed_revisions bigint NOT NULL DEFAULT 0,
    ADD COLUMN adoption_index_bytes bigint NOT NULL DEFAULT 0,
    ADD COLUMN adoption_index_objects bigint NOT NULL DEFAULT 0,
    ADD COLUMN branding_bytes bigint NOT NULL DEFAULT 0,
    ADD COLUMN branding_objects bigint NOT NULL DEFAULT 0,
    ADD COLUMN legacy_upload_bytes bigint NOT NULL DEFAULT 0,
    ADD COLUMN legacy_upload_objects bigint NOT NULL DEFAULT 0,
    ADD COLUMN last_seed_import_at timestamptz;

DROP INDEX IF EXISTS public.admin_jobs_active_artwork_storage_idx;
CREATE UNIQUE INDEX admin_jobs_active_artwork_storage_idx
    ON public.admin_jobs ((TRUE))
    WHERE status IN ('queued', 'running')
      AND job_type IN ('artwork_storage_refresh', 'artwork_storage_purge', 'artwork_storage_import');

CREATE INDEX artwork_revision_seed_expiry_idx
    ON public.artwork_revision_gc_candidates (seed_expires_at, id)
    WHERE source_class = 'seed' AND tombstoned_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS public.artwork_revision_seed_expiry_idx;
DROP INDEX IF EXISTS public.admin_jobs_active_artwork_storage_idx;
CREATE UNIQUE INDEX admin_jobs_active_artwork_storage_idx
    ON public.admin_jobs ((TRUE))
    WHERE status IN ('queued', 'running')
      AND job_type IN ('artwork_storage_refresh', 'artwork_storage_purge');

ALTER TABLE public.artwork_storage_accounting_state
    DROP COLUMN IF EXISTS last_seed_import_at,
    DROP COLUMN IF EXISTS legacy_upload_objects,
    DROP COLUMN IF EXISTS legacy_upload_bytes,
    DROP COLUMN IF EXISTS branding_objects,
    DROP COLUMN IF EXISTS branding_bytes,
    DROP COLUMN IF EXISTS adoption_index_objects,
    DROP COLUMN IF EXISTS adoption_index_bytes,
    DROP COLUMN IF EXISTS seed_revisions,
    DROP COLUMN IF EXISTS expired_seed_bytes,
    DROP COLUMN IF EXISTS seed_bytes;

ALTER TABLE public.artwork_revision_gc_candidates
    DROP CONSTRAINT IF EXISTS artwork_revision_inventory_source_class_check,
    DROP COLUMN IF EXISTS seed_expires_at,
    DROP COLUMN IF EXISTS seed_imported_at,
    ADD CONSTRAINT artwork_revision_inventory_source_class_check CHECK (
        source_class IN ('provider', 'plugin', 'library_sidecar', 'embedded', 'generated', 'upload', 'bundled', 'unknown')
    ) NOT VALID;
