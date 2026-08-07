-- +goose NO TRANSACTION

-- +goose Up
ALTER TABLE public.marker_contributions
    ADD COLUMN IF NOT EXISTS claim_active boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS claim_token uuid;

-- Legacy marker-provider plugins reported upstream HTTP conflicts as generic
-- errors. These conflicts are terminal for an unchanged contribution payload,
-- so settle existing rows before the next daily task can retry them.
UPDATE public.marker_contributions
SET status = 'conflict',
    http_status = 409,
    updated_at = now()
WHERE status = 'error'
  AND (
      http_status = 409
      OR error LIKE '%submit HTTP 409:%'
  );

-- Retain every audit row while choosing one active claim for each historical
-- provider payload. Error rows stay inactive so a later attempt can retry.
UPDATE public.marker_contributions
SET claim_active = false
WHERE status = 'error';

WITH ranked AS (
    SELECT id,
           ROW_NUMBER() OVER (
               PARTITION BY provider, segment_kind, content_hash
               ORDER BY claim_active DESC, updated_at DESC, submitted_at DESC, id
           ) AS row_number
    FROM public.marker_contributions
    WHERE status <> 'error'
)
UPDATE public.marker_contributions AS contribution
SET claim_active = false
FROM ranked
WHERE contribution.id = ranked.id
  AND ranked.row_number > 1;

WITH ranked AS (
    SELECT id,
           ROW_NUMBER() OVER (
               PARTITION BY provider, segment_kind, content_hash
               ORDER BY claim_active DESC, updated_at DESC, submitted_at DESC, id
           ) AS row_number
    FROM public.marker_contributions
    WHERE status <> 'error'
)
UPDATE public.marker_contributions AS contribution
SET claim_active = true
FROM ranked
WHERE contribution.id = ranked.id
  AND ranked.row_number = 1
  AND NOT contribution.claim_active;

-- Remove an INVALID remnant before retrying an interrupted concurrent build.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'marker_contributions_provider_hash_active_uidx'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.marker_contributions_provider_hash_active_uidx;
    END IF;
END;
$$;
-- +goose StatementEnd

-- The active claim is provider-payload scoped rather than local-file scoped.
-- Build the global uniqueness guarantee without blocking contribution writes.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS marker_contributions_provider_hash_active_uidx
    ON public.marker_contributions (provider, segment_kind, content_hash)
    WHERE claim_active;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.marker_contributions_provider_hash_active_uidx;
ALTER TABLE public.marker_contributions
    DROP COLUMN IF EXISTS claim_token,
    DROP COLUMN IF EXISTS claim_active;
-- Settled conflict rows are intentionally retained: reverting them to generic
-- errors would make the server resubmit them on the next scheduled run.
