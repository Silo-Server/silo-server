-- +goose Up
-- +goose StatementBegin
-- The transcription quota counts a user's recent transcription jobs on every
-- enqueue and every quota lookup (player translate modal). Job rows are never
-- deleted, so without an index that count degrades to a full sequential scan
-- as job history grows.
CREATE INDEX subtitle_ai_jobs_quota_idx
    ON public.subtitle_ai_jobs (requested_by, created_at)
    WHERE kind IN ('transcribe', 'transcribe_translate');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS subtitle_ai_jobs_quota_idx;
-- +goose StatementEnd
