-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.activity_log
    ADD COLUMN IF NOT EXISTS playback_session_id text;

CREATE INDEX IF NOT EXISTS idx_activity_log_playback_session_id
    ON public.activity_log USING btree (playback_session_id, "timestamp" DESC);

ALTER TABLE public.operational_logs
    ADD COLUMN IF NOT EXISTS playback_session_id text;

CREATE INDEX IF NOT EXISTS idx_operational_logs_playback_session_id
    ON public.operational_logs USING btree (playback_session_id, "timestamp" DESC);
-- +goose StatementEnd
