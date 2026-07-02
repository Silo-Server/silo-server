-- Reshape the downloads table for downloads v2 (offline sync for mobile).
--
-- One device-aware, format-aware downloads table carries both lifecycles:
--   * device_id IS NULL  -> ephemeral / account-level web download (today's behavior)
--   * device_id set       -> managed device-library entry (Phase 1+)
-- Downloads ship default-off with little/no production data, so widening the
-- table in place is safe. See docs/superpowers/specs/2026-06-18-offline-sync-mobile-design.md.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.downloads
    ADD COLUMN profile_id  text,                         -- NULL for ephemeral/web rows
    ADD COLUMN device_id   text,                         -- NULL = ephemeral; set = managed device entry
    ADD COLUMN format      text NOT NULL DEFAULT 'original', -- internal delivery format
    ADD COLUMN quality     text NOT NULL DEFAULT 'original', -- client-requested quality preset
    ADD COLUMN effective_quality text NOT NULL DEFAULT 'original',
    ADD COLUMN target_bitrate_kbps integer NOT NULL DEFAULT 0,
    ADD COLUMN revision    integer NOT NULL DEFAULT 1,
    ADD COLUMN artifact_id text;                         -- set for remux/transcode once prepared

ALTER TABLE public.downloads
    ADD CONSTRAINT downloads_format_check CHECK (format IN ('original','remux','transcode'));
ALTER TABLE public.downloads
    ADD CONSTRAINT downloads_quality_check CHECK (quality IN ('original','20mbps','10mbps','5mbps','2mbps','1mbps'));
ALTER TABLE public.downloads
    ADD CONSTRAINT downloads_effective_quality_check CHECK (effective_quality IN ('original','20mbps','10mbps','5mbps','2mbps','1mbps'));
ALTER TABLE public.downloads
    ADD CONSTRAINT downloads_target_bitrate_check CHECK (target_bitrate_kbps >= 0);
ALTER TABLE public.downloads
    ADD CONSTRAINT downloads_revision_check CHECK (revision >= 1);

-- Widen the status enum to cover the managed-entry lifecycle alongside the existing serve states.
ALTER TABLE public.downloads DROP CONSTRAINT downloads_status_check;
ALTER TABLE public.downloads ADD  CONSTRAINT downloads_status_check
    CHECK (status IN ('queued','downloading','completed','failed','cancelled',  -- existing
                      'preparing','ready','revoked'));                          -- managed entries

-- One managed entry per (user, profile, device, content, episode). Ephemeral rows (NULL device_id)
-- are exempt via the partial index. COALESCE(episode_id,'') collapses movies (NULL episode_id) to
-- one managed entry per (device, content) without NULL-comparison surprises.
CREATE UNIQUE INDEX downloads_device_entry_uidx
    ON public.downloads (user_id, profile_id, device_id, content_id, COALESCE(episode_id, ''))
    WHERE device_id IS NOT NULL;

-- No separate (user_id, profile_id, device_id) index: downloads_device_entry_uidx
-- already serves that prefix under the same partial predicate.
CREATE INDEX downloads_artifact_idx ON public.downloads (artifact_id) WHERE artifact_id IS NOT NULL;

-- Composite FK to user_devices. MATCH SIMPLE (the default) skips the check whenever any column is NULL,
-- so ephemeral rows (NULL device_id) are not constrained while managed entries are.
ALTER TABLE public.downloads
    ADD CONSTRAINT downloads_device_fkey
    FOREIGN KEY (user_id, profile_id, device_id)
    REFERENCES public.user_devices(user_id, profile_id, device_id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.downloads DROP CONSTRAINT IF EXISTS downloads_device_fkey;
DROP INDEX IF EXISTS downloads_artifact_idx;
DROP INDEX IF EXISTS downloads_device_entry_uidx;
ALTER TABLE public.downloads DROP CONSTRAINT IF EXISTS downloads_revision_check;
ALTER TABLE public.downloads DROP CONSTRAINT IF EXISTS downloads_target_bitrate_check;
ALTER TABLE public.downloads DROP CONSTRAINT IF EXISTS downloads_effective_quality_check;
ALTER TABLE public.downloads DROP CONSTRAINT IF EXISTS downloads_quality_check;
ALTER TABLE public.downloads DROP CONSTRAINT IF EXISTS downloads_format_check;
ALTER TABLE public.downloads DROP CONSTRAINT IF EXISTS downloads_status_check;
-- Rollback must not fail validation: collapse managed-lifecycle rows to the
-- legacy enum before re-narrowing the CHECK (data loss is expected on a
-- reshape Down; a failing Down is not).
UPDATE public.downloads SET status = 'cancelled' WHERE status IN ('preparing','ready','revoked');
ALTER TABLE public.downloads ADD  CONSTRAINT downloads_status_check
    CHECK (status IN ('queued','downloading','completed','failed','cancelled'));
ALTER TABLE public.downloads
    DROP COLUMN artifact_id,
    DROP COLUMN revision,
    DROP COLUMN target_bitrate_kbps,
    DROP COLUMN effective_quality,
    DROP COLUMN quality,
    DROP COLUMN format,
    DROP COLUMN device_id,
    DROP COLUMN profile_id;
-- +goose StatementEnd
