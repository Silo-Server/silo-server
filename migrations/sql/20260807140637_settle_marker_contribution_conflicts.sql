-- +goose Up
-- +goose StatementBegin
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

-- Idempotency is provider-payload scoped rather than local-file scoped. The
-- content hash already includes the resolved external target, segment bounds,
-- and video duration.
CREATE INDEX marker_contributions_provider_hash_terminal_idx
    ON public.marker_contributions (provider, segment_kind, content_hash)
    WHERE status <> 'error';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.marker_contributions_provider_hash_terminal_idx;
-- Settled conflict rows are intentionally retained: reverting them to generic
-- errors would make the server resubmit them on the next scheduled run.
-- +goose StatementEnd
