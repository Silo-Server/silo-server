-- +goose Up
ALTER TABLE public.downloads ADD COLUMN status_event_at timestamptz;
-- Preserve the last bridge report as the initial ordering fence.
UPDATE public.downloads SET status_event_at = updated_at
WHERE device_id IS NOT NULL AND status IN ('downloading', 'completed');

-- +goose Down
ALTER TABLE public.downloads DROP COLUMN status_event_at;
