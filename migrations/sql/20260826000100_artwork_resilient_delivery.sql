-- +goose Up
ALTER TABLE public.artwork_revision_gc_candidates
    ADD COLUMN missing_at TIMESTAMPTZ,
    ADD COLUMN repair_state TEXT NOT NULL DEFAULT '',
    ADD COLUMN repair_queued_at TIMESTAMPTZ,
    ADD COLUMN protected_loss_at TIMESTAMPTZ;

CREATE INDEX artwork_revision_repair_state_idx
    ON public.artwork_revision_gc_candidates (repair_state, repair_queued_at, id)
    WHERE repair_state <> '';

ALTER TABLE public.artwork_storage_accounting_state
    ADD COLUMN store_health TEXT NOT NULL DEFAULT 'healthy',
    ADD COLUMN health_changed_at TIMESTAMPTZ,
    ADD COLUMN missing_bytes BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN repair_pending_bytes BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN missing_revision_count BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN repairing_revision_count BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN protected_loss_count BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN rebuild_generation TEXT NOT NULL DEFAULT '',
    ADD COLUMN rebuild_surface_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN rebuild_enqueued_at TIMESTAMPTZ;

ALTER TABLE public.metadata_image_cache_jobs
    ADD COLUMN repair_requested BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE public.artwork_storage_alerts (
    id BIGSERIAL PRIMARY KEY,
    kind TEXT NOT NULL,
    surface_name TEXT NOT NULL,
    target_keys TEXT[] NOT NULL DEFAULT '{}',
    image_slot TEXT NOT NULL,
    original_path TEXT NOT NULL DEFAULT '',
    message TEXT NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at TIMESTAMPTZ,
    UNIQUE (kind, surface_name, target_keys, image_slot)
);

CREATE INDEX artwork_storage_alerts_active_idx
    ON public.artwork_storage_alerts (kind, last_seen_at DESC)
    WHERE resolved_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS public.artwork_storage_alerts;

ALTER TABLE public.metadata_image_cache_jobs
    DROP COLUMN IF EXISTS repair_requested;

ALTER TABLE public.artwork_storage_accounting_state
    DROP COLUMN IF EXISTS rebuild_enqueued_at,
    DROP COLUMN IF EXISTS rebuild_surface_name,
    DROP COLUMN IF EXISTS rebuild_generation,
    DROP COLUMN IF EXISTS protected_loss_count,
    DROP COLUMN IF EXISTS repairing_revision_count,
    DROP COLUMN IF EXISTS missing_revision_count,
    DROP COLUMN IF EXISTS repair_pending_bytes,
    DROP COLUMN IF EXISTS missing_bytes,
    DROP COLUMN IF EXISTS health_changed_at,
    DROP COLUMN IF EXISTS store_health;

DROP INDEX IF EXISTS public.artwork_revision_repair_state_idx;

ALTER TABLE public.artwork_revision_gc_candidates
    DROP COLUMN IF EXISTS protected_loss_at,
    DROP COLUMN IF EXISTS repair_queued_at,
    DROP COLUMN IF EXISTS repair_state,
    DROP COLUMN IF EXISTS missing_at;
