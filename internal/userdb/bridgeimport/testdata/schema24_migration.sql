-- Frozen schema23-to24 migration from 78212ad379c7e638283590c749dc65bdfb440eee.
-- Do not follow current migrations.

CREATE TABLE IF NOT EXISTS playback_source_markers (
 user_id INTEGER PRIMARY KEY,
 source_id TEXT NOT NULL,
 selection_generation INTEGER NOT NULL CHECK(selection_generation > 0),
 gate TEXT NOT NULL DEFAULT 'quarantined' CHECK(gate IN ('writable','quarantined','sealed'))
);

PRAGMA user_version = 24;
