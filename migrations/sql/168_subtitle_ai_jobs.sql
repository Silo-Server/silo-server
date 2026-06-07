-- +goose Up
-- +goose StatementBegin
-- AI subtitle jobs: on-demand translation (and, later, Whisper ASR generation)
-- of subtitle tracks. Each row is one user-triggered job. The worker runs the
-- configured OpenAI-compatible engine and stores the result as an ordinary
-- downloaded_subtitles row, so the generated track is served to every client
-- through the existing subtitle pipeline with no client changes.
CREATE TABLE public.subtitle_ai_jobs (
    id                 bigserial   PRIMARY KEY,
    media_file_id      integer     NOT NULL,
    kind               text        NOT NULL,                   -- 'translate' (future: 'transcribe', 'transcribe_translate')
    source_index       integer     NOT NULL DEFAULT -1,        -- combined player subtitle index of the source track
    source_language    text        NOT NULL DEFAULT '',
    target_language    text        NOT NULL,
    engine             text        NOT NULL DEFAULT 'openai',
    model              text        NOT NULL DEFAULT '',        -- snapshot of the model used, for provenance
    status             text        NOT NULL DEFAULT 'pending', -- pending|running|completed|failed|cancelled
    progress           double precision NOT NULL DEFAULT 0,    -- 0..1
    progress_message   text        NOT NULL DEFAULT '',
    result_subtitle_id integer,                                -- downloaded_subtitles.id on success
    error_message      text        NOT NULL DEFAULT '',
    idempotency_key    text        NOT NULL,
    requested_by       integer,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    heartbeat_at       timestamptz NOT NULL DEFAULT now()
);

-- Prevent duplicate in-flight jobs for the same work. Completed/failed/cancelled
-- rows do not block a re-run, so a user can retry after a failure.
CREATE UNIQUE INDEX subtitle_ai_jobs_active_idempotency_idx
    ON public.subtitle_ai_jobs (idempotency_key)
    WHERE status IN ('pending', 'running');

-- Listing recent jobs for a media file (player "Translate" panel, admin views).
CREATE INDEX subtitle_ai_jobs_media_file_idx
    ON public.subtitle_ai_jobs (media_file_id, created_at DESC);

-- Startup recovery scan for jobs left running by a crashed process.
CREATE INDEX subtitle_ai_jobs_status_idx
    ON public.subtitle_ai_jobs (status) WHERE status IN ('pending', 'running');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.subtitle_ai_jobs;
-- +goose StatementEnd
