-- +goose Up
-- Why a live session's route changes the file (codec, bandwidth, buffering,
-- subtitle burn-in, ...), from the closed playback.AdaptationReasons list.
ALTER TABLE playback_sessions_sync
    ADD COLUMN adaptation_reasons TEXT[];

-- +goose Down
ALTER TABLE playback_sessions_sync
    DROP COLUMN adaptation_reasons;
