-- +goose Up
ALTER TABLE public.artwork_revision_gc_candidates
    ADD COLUMN object_sizes_bytes bigint[] NOT NULL DEFAULT '{}',
    ADD COLUMN object_content_types text[] NOT NULL DEFAULT '{}',
    ADD COLUMN total_physical_bytes bigint NOT NULL DEFAULT 0,
    ADD COLUMN source_class text NOT NULL DEFAULT 'unknown',
    ADD COLUMN store_generation text NOT NULL DEFAULT '',
    ADD COLUMN inventory_complete boolean NOT NULL DEFAULT FALSE,
    ADD COLUMN last_reference_check_at timestamptz,
    ADD COLUMN last_verified_at timestamptz,
    ADD COLUMN deletion_started_at timestamptz,
    ADD COLUMN tombstoned_at timestamptz;

ALTER TABLE public.artwork_revision_gc_candidates
    ADD COLUMN lifecycle_state text GENERATED ALWAYS AS (
        CASE
            WHEN tombstoned_at IS NOT NULL THEN 'tombstoned'
            WHEN deleted_at IS NOT NULL THEN 'deleted'
            WHEN deletion_started_at IS NOT NULL THEN 'deleting'
            WHEN next_attempt_at IS NULL THEN 'parked'
            ELSE 'pending_gc'
        END
    ) STORED;

ALTER TABLE public.artwork_revision_gc_candidates
    ADD CONSTRAINT artwork_revision_inventory_sizes_check CHECK (
        total_physical_bytes >= 0
        AND (
            NOT inventory_complete
            OR (
                cardinality(object_keys) > 0
                AND cardinality(object_keys) = cardinality(object_sizes_bytes)
                AND cardinality(object_keys) = cardinality(object_content_types)
            )
        )
    ) NOT VALID,
    ADD CONSTRAINT artwork_revision_inventory_object_sizes_check CHECK (
        0 <= ALL(object_sizes_bytes)
    ) NOT VALID,
    ADD CONSTRAINT artwork_revision_inventory_source_class_check CHECK (
        source_class IN ('provider', 'plugin', 'library_sidecar', 'embedded', 'generated', 'upload', 'bundled', 'unknown')
    ) NOT VALID;

CREATE INDEX artwork_revision_inventory_live_bytes_idx
    ON public.artwork_revision_gc_candidates (lifecycle_state, inventory_complete, id)
    INCLUDE (total_physical_bytes, image_type, source_class, store_generation);

CREATE INDEX artwork_revision_inventory_dormant_recheck_idx
    ON public.artwork_revision_gc_candidates (updated_at, id)
    WHERE next_attempt_at IS NULL AND tombstoned_at IS NULL;

CREATE INDEX artwork_revision_inventory_original_live_idx
    ON public.artwork_revision_gc_candidates (original_path)
    WHERE lifecycle_state <> 'tombstoned';

CREATE TABLE public.artwork_storage_accounting_state (
    singleton boolean PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    snapshot_at timestamptz,
    inventory_complete boolean NOT NULL DEFAULT FALSE,
    known_revisions bigint NOT NULL DEFAULT 0,
    missing_revisions bigint NOT NULL DEFAULT 0,
    missing_objects bigint NOT NULL DEFAULT 0,
    orphan_objects bigint NOT NULL DEFAULT 0,
    failure_count bigint NOT NULL DEFAULT 0,
    coverage_limited boolean NOT NULL DEFAULT FALSE,
    coverage_limit_reason text NOT NULL DEFAULT '',
    last_error text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT NOW()
);

INSERT INTO public.artwork_storage_accounting_state (singleton) VALUES (TRUE);

CREATE TABLE public.artwork_legacy_prefix_gc_candidates (
    prefix text PRIMARY KEY,
    not_before timestamptz NOT NULL DEFAULT (NOW() + interval '24 hours'),
    attempt_count integer NOT NULL DEFAULT 0,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CHECK (BTRIM(prefix) <> '' AND prefix NOT LIKE 'artwork/v1/%')
);

ALTER TABLE public.admin_jobs
    ADD COLUMN dry_run boolean NOT NULL DEFAULT FALSE,
    ADD COLUMN checkpoint jsonb NOT NULL DEFAULT '{}'::jsonb;

CREATE UNIQUE INDEX admin_jobs_active_artwork_storage_idx
    ON public.admin_jobs ((TRUE))
    WHERE status IN ('queued', 'running')
      AND job_type IN ('artwork_storage_refresh', 'artwork_storage_purge');

-- +goose Down
DROP INDEX IF EXISTS public.admin_jobs_active_artwork_storage_idx;
ALTER TABLE public.admin_jobs
    DROP COLUMN IF EXISTS checkpoint,
    DROP COLUMN IF EXISTS dry_run;

DROP TABLE IF EXISTS public.artwork_storage_accounting_state;
DROP TABLE IF EXISTS public.artwork_legacy_prefix_gc_candidates;
DROP INDEX IF EXISTS public.artwork_revision_inventory_original_live_idx;
DROP INDEX IF EXISTS public.artwork_revision_inventory_dormant_recheck_idx;
DROP INDEX IF EXISTS public.artwork_revision_inventory_live_bytes_idx;

ALTER TABLE public.artwork_revision_gc_candidates
    DROP CONSTRAINT IF EXISTS artwork_revision_inventory_source_class_check,
    DROP CONSTRAINT IF EXISTS artwork_revision_inventory_object_sizes_check,
    DROP CONSTRAINT IF EXISTS artwork_revision_inventory_sizes_check,
    DROP COLUMN IF EXISTS lifecycle_state,
    DROP COLUMN IF EXISTS tombstoned_at,
    DROP COLUMN IF EXISTS deletion_started_at,
    DROP COLUMN IF EXISTS last_verified_at,
    DROP COLUMN IF EXISTS last_reference_check_at,
    DROP COLUMN IF EXISTS inventory_complete,
    DROP COLUMN IF EXISTS store_generation,
    DROP COLUMN IF EXISTS source_class,
    DROP COLUMN IF EXISTS total_physical_bytes,
    DROP COLUMN IF EXISTS object_content_types,
    DROP COLUMN IF EXISTS object_sizes_bytes;
