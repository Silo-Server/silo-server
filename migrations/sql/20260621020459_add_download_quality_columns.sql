-- +goose Up
-- +goose StatementBegin
-- Corrective migration for dev/branch databases that had already applied the
-- Downloads V2 table migrations before quality/revision/bitrate columns were
-- added to those migration files. Fresh databases may already have these columns
-- from the base migrations, so every change here is idempotent.

ALTER TABLE public.downloads
    ADD COLUMN IF NOT EXISTS quality text NOT NULL DEFAULT 'original',
    ADD COLUMN IF NOT EXISTS effective_quality text NOT NULL DEFAULT 'original',
    ADD COLUMN IF NOT EXISTS target_bitrate_kbps integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS revision integer NOT NULL DEFAULT 1;

UPDATE public.downloads
SET quality = COALESCE(NULLIF(quality, ''), 'original'),
    effective_quality = COALESCE(NULLIF(effective_quality, ''), COALESCE(NULLIF(quality, ''), 'original')),
    target_bitrate_kbps = COALESCE(target_bitrate_kbps, 0),
    revision = GREATEST(COALESCE(revision, 1), 1);

ALTER TABLE public.downloads
    ALTER COLUMN quality SET DEFAULT 'original',
    ALTER COLUMN quality SET NOT NULL,
    ALTER COLUMN effective_quality SET DEFAULT 'original',
    ALTER COLUMN effective_quality SET NOT NULL,
    ALTER COLUMN target_bitrate_kbps SET DEFAULT 0,
    ALTER COLUMN target_bitrate_kbps SET NOT NULL,
    ALTER COLUMN revision SET DEFAULT 1,
    ALTER COLUMN revision SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'public.downloads'::regclass
          AND conname = 'downloads_quality_check'
    ) THEN
        ALTER TABLE public.downloads
            ADD CONSTRAINT downloads_quality_check
            CHECK (quality IN ('original','20mbps','10mbps','5mbps','2mbps','1mbps'));
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'public.downloads'::regclass
          AND conname = 'downloads_effective_quality_check'
    ) THEN
        ALTER TABLE public.downloads
            ADD CONSTRAINT downloads_effective_quality_check
            CHECK (effective_quality IN ('original','20mbps','10mbps','5mbps','2mbps','1mbps'));
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'public.downloads'::regclass
          AND conname = 'downloads_target_bitrate_check'
    ) THEN
        ALTER TABLE public.downloads
            ADD CONSTRAINT downloads_target_bitrate_check
            CHECK (target_bitrate_kbps >= 0);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'public.downloads'::regclass
          AND conname = 'downloads_revision_check'
    ) THEN
        ALTER TABLE public.downloads
            ADD CONSTRAINT downloads_revision_check
            CHECK (revision >= 1);
    END IF;
END $$;

ALTER TABLE public.download_artifacts
    ADD COLUMN IF NOT EXISTS target_bitrate_kbps integer NOT NULL DEFAULT 0;

UPDATE public.download_artifacts
SET target_bitrate_kbps = COALESCE(target_bitrate_kbps, 0);

ALTER TABLE public.download_artifacts
    ALTER COLUMN target_bitrate_kbps SET DEFAULT 0,
    ALTER COLUMN target_bitrate_kbps SET NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Intentionally no-op: on fresh databases these columns are introduced by the
-- earlier Downloads V2 migrations, while on already-migrated dev databases this
-- migration only reconciles drift caused by editing an applied migration.
SELECT 1;
-- +goose StatementEnd
