-- Prepared (remux/transcode) download artifacts, deduplicated by
-- (media_file_id, format, params_hash). The attempts/lease_*/next_retry_at
-- columns make this table a durable, recoverable job queue so a crash mid-encode
-- cannot strand a download in `preparing`. See
-- docs/superpowers/specs/2026-06-18-offline-sync-mobile-design.md ("Durable artifact queue").

-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.download_artifacts (
    id                text        NOT NULL,
    media_file_id     integer     NOT NULL REFERENCES public.media_files(id) ON DELETE CASCADE,
    format            text        NOT NULL,            -- 'remux' | 'transcode'
    params_hash       text        NOT NULL,            -- sha256 of the encode parameters
    container         text        NOT NULL DEFAULT 'mp4',
    codec_video       text        NOT NULL DEFAULT '',
    codec_audio       text        NOT NULL DEFAULT '',
    resolution        text        NOT NULL DEFAULT '',
    audio_track_index integer     NOT NULL DEFAULT -1,
    target_bitrate_kbps integer   NOT NULL DEFAULT 0,
    output_path       text        NOT NULL DEFAULT '', -- absolute path on the server's artifact volume
    file_size         bigint      NOT NULL DEFAULT 0,
    status            text        NOT NULL DEFAULT 'queued',
    error_message     text        NOT NULL DEFAULT '',
    -- Durable-queue / crash-recovery columns.
    attempts          integer     NOT NULL DEFAULT 0,
    max_attempts      integer     NOT NULL DEFAULT 3,
    lease_owner       text,                              -- worker/node id holding the running lease
    lease_expires_at  timestamptz,                       -- NULL unless status='running'
    next_retry_at     timestamptz,                       -- backoff gate for re-enqueue after a failure
    created_at        timestamptz NOT NULL DEFAULT now(),
    completed_at      timestamptz,
    last_used_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT download_artifacts_pkey PRIMARY KEY (id),
    CONSTRAINT download_artifacts_unique UNIQUE (media_file_id, format, params_hash),
    CONSTRAINT download_artifacts_status_check CHECK (status IN ('queued','running','ready','failed'))
);

CREATE INDEX download_artifacts_lru_idx       ON public.download_artifacts (last_used_at) WHERE status = 'ready';
-- Claimable work: queued rows whose backoff has elapsed, plus running rows whose lease has expired.
CREATE INDEX download_artifacts_claimable_idx ON public.download_artifacts (status, next_retry_at);
CREATE INDEX download_artifacts_lease_idx     ON public.download_artifacts (lease_expires_at) WHERE status = 'running';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.download_artifacts;
-- +goose StatementEnd
