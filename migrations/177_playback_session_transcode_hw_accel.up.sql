ALTER TABLE playback_sessions_sync
    ADD COLUMN IF NOT EXISTS transcode_hw_accel text;
