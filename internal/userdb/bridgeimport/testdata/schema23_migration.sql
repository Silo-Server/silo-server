-- Frozen schema22-to23 migration from 46df415bf. Do not follow current migrations.

CREATE TABLE IF NOT EXISTS playback_progress_sinks (
    profile_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    attempt_id TEXT NOT NULL,
    incarnation TEXT NOT NULL,
    owner_id TEXT NOT NULL,
    epoch INTEGER NOT NULL CHECK (epoch > 0),
    state TEXT NOT NULL CHECK (state IN ('active', 'stopped')),
    last_sequence INTEGER NOT NULL CHECK (last_sequence >= 0),
    document TEXT NOT NULL,
    PRIMARY KEY (profile_id, session_id)
);

PRAGMA user_version = 23;
