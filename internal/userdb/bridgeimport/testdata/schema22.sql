-- Frozen schema22 constructor DDL; preserve independently of current migrations.
-- Captured from sqlite_schema after NewUserDB at d8c2876519b3ed65110c72c4afb088ccda8df6ac.
CREATE TABLE audio_preferences (
    profile_id TEXT NOT NULL,
    series_id TEXT NOT NULL,
    audio_track_index INT,
    audio_language TEXT,
    audio_track_signature TEXT NOT NULL DEFAULT '{}',
    updated_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, series_id)
);
CREATE TABLE collection_sort_preferences (
    profile_id TEXT NOT NULL,
    collection_kind TEXT NOT NULL,
    collection_id TEXT NOT NULL,
    sort_field TEXT NOT NULL DEFAULT '',
    sort_order TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, collection_kind, collection_id),
    CHECK (collection_kind IN ('library', 'user', 'watchlist', 'favorites')),
    CHECK (sort_order IN ('', 'asc', 'desc'))
);
CREATE TABLE downloads (
    id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    media_file_id INTEGER NOT NULL,
    quality TEXT,
    transcoded BOOLEAN DEFAULT false,
    file_size INTEGER,
    expires_at TEXT,
    downloaded_at TEXT
);
CREATE TABLE favorites (
    profile_id TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    added_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, media_item_id)
);
CREATE TABLE hidden_history_items (
    profile_id TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    hidden_before TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, media_item_id)
);
CREATE TABLE home_item_dismissals (
    profile_id TEXT NOT NULL,
    surface TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    series_id TEXT,
    progress_updated_at TEXT,
    dismissed_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, surface, media_item_id)
);
CREATE TABLE jellycompat_displayprefs (
    prefs_id   TEXT NOT NULL,
    client     TEXT NOT NULL,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (prefs_id, client)
);
CREATE TABLE library_playback_preferences (
    profile_id TEXT NOT NULL,
    library_id INTEGER NOT NULL,
    audio_language TEXT,
    subtitle_language TEXT,
    subtitle_mode TEXT,
    show_forced_subtitles BOOLEAN,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, library_id)
);
CREATE TABLE personal_collection_items (
    collection_id TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    position INTEGER,
    added_at TEXT NOT NULL,
    PRIMARY KEY (collection_id, media_item_id)
);
CREATE TABLE personal_collection_order_revision (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 revision INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE personal_collection_profiles (
    collection_id TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    PRIMARY KEY (collection_id, profile_id)
);
CREATE TABLE personal_collection_revisions (
    collection_id TEXT PRIMARY KEY,
    revision INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE personal_collections (
    id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL,
    creator_profile_id TEXT NOT NULL,
    name TEXT NOT NULL,
    collection_type TEXT NOT NULL DEFAULT 'manual',
    is_shared BOOLEAN DEFAULT false,
    query_definition TEXT NOT NULL DEFAULT '{}',
    sort_config TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE playback_sessions (
    session_id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL,
    media_file_id INTEGER NOT NULL,
    play_method TEXT NOT NULL,
    position_seconds REAL DEFAULT 0,
    is_paused BOOLEAN DEFAULT false,
    started_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE profile_allowed_libraries (
    profile_id TEXT NOT NULL,
    library_id INTEGER NOT NULL,
    PRIMARY KEY (profile_id, library_id)
);
CREATE TABLE profile_onboarding (
    profile_id TEXT NOT NULL,
    tour_id TEXT NOT NULL,
    last_step TEXT NOT NULL DEFAULT '',
    completed_at TEXT,
    skipped_at TEXT,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, tour_id)
);
CREATE TABLE profile_section_overrides (
    id                TEXT    PRIMARY KEY,
    profile_id        TEXT    NOT NULL,
    scope             TEXT    NOT NULL CHECK (scope IN ('home', 'library')),
    library_id        TEXT,
    section_id        TEXT,
    position          INTEGER,
    hidden            INTEGER NOT NULL DEFAULT 0,
    removed           INTEGER NOT NULL DEFAULT 0,
    section_type      TEXT,
    title             TEXT,
    featured          INTEGER,
    item_limit        INTEGER,
    config            TEXT,
    is_user_added     INTEGER NOT NULL DEFAULT 0,
    user_section_type TEXT,
    user_config       TEXT,
    user_title        TEXT,
    created_at        TEXT    NOT NULL,
    updated_at        TEXT    NOT NULL
);
CREATE TABLE profiles (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    avatar TEXT,
    pin_hash TEXT,
    is_child BOOLEAN DEFAULT false,
    is_primary BOOLEAN NOT NULL DEFAULT false,
    max_content_rating TEXT,
    quality_preference TEXT DEFAULT '1080p',
    language TEXT DEFAULT 'en',
    subtitle_language TEXT,
    subtitle_mode TEXT DEFAULT 'auto',
    auto_skip_intro BOOLEAN DEFAULT false,
    auto_skip_credits BOOLEAN DEFAULT false,
    auto_skip_recap BOOLEAN DEFAULT false,
    auto_play_next_preview BOOLEAN DEFAULT false,
    show_forced_subtitles BOOLEAN NOT NULL DEFAULT true,
    library_restrictions_enabled BOOLEAN DEFAULT false,
    max_playback_quality TEXT DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE series_playback_preferences (
    profile_id TEXT NOT NULL,
    series_id TEXT NOT NULL,
    resolution TEXT,
    hdr BOOLEAN NOT NULL DEFAULT false,
    codec_video TEXT,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, series_id)
);
CREATE TABLE subtitle_preferences (
    profile_id TEXT NOT NULL,
    series_id TEXT NOT NULL,
    subtitle_language TEXT,
    subtitle_track_index INT,
    external_subtitle_path TEXT,
    subtitle_mode TEXT,
    subtitle_track_signature TEXT NOT NULL DEFAULT '{}',
    show_forced_subtitles BOOLEAN,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, series_id)
);
CREATE TABLE user_device_settings (
    profile_id TEXT NOT NULL,
    device_id TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    device_name TEXT NOT NULL DEFAULT '',
    device_platform TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, device_id, key)
);
CREATE TABLE user_devices (
    profile_id TEXT NOT NULL,
    device_id TEXT NOT NULL,
    device_name TEXT NOT NULL DEFAULT '',
    device_platform TEXT NOT NULL DEFAULT '',
    last_seen_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, device_id)
);
CREATE TABLE user_setting_migration_rejects (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    source_table TEXT NOT NULL,
    source_key   TEXT NOT NULL,
    identity     TEXT NOT NULL CHECK (json_valid(identity)),
    value        TEXT,
    reason       TEXT NOT NULL,
    recorded_at  TEXT NOT NULL
);
CREATE TABLE user_setting_mutations (
    mutation_id  TEXT PRIMARY KEY,
    request_hash TEXT NOT NULL,
    result       TEXT NOT NULL CHECK (json_valid(result)),
    created_at   TEXT NOT NULL,
    expires_at   TEXT NOT NULL
);
CREATE TABLE user_setting_values (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    key         TEXT NOT NULL,
    scope       TEXT NOT NULL,
    profile_id  TEXT,
    client_family TEXT,
    device_id   TEXT,
    library_id  INTEGER,
    series_id   TEXT,
    value       TEXT NOT NULL CHECK (json_valid(value)),
    revision    INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    CHECK (scope IN ('account', 'profile', 'profile_client', 'profile_device', 'profile_library', 'profile_series')),
    CHECK (client_family IS NULL OR client_family IN ('tv', 'mobile', 'tablet', 'desktop', 'web')),
    CHECK (
      (scope = 'account' AND profile_id IS NULL AND client_family IS NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NULL) OR
      (scope = 'profile' AND profile_id IS NOT NULL AND client_family IS NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NULL) OR
      (scope = 'profile_client' AND profile_id IS NOT NULL AND client_family IS NOT NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NULL) OR
      (scope = 'profile_device' AND profile_id IS NOT NULL AND client_family IS NULL AND device_id IS NOT NULL AND library_id IS NULL AND series_id IS NULL) OR
      (scope = 'profile_library' AND profile_id IS NOT NULL AND client_family IS NULL AND device_id IS NULL AND library_id IS NOT NULL AND series_id IS NULL) OR
      (scope = 'profile_series' AND profile_id IS NOT NULL AND client_family IS NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NOT NULL)
    )
);
CREATE TABLE user_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE watch_history (
    id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    watched_at TEXT NOT NULL,
    duration_seconds REAL,
    completed BOOLEAN DEFAULT false,
    source TEXT NOT NULL DEFAULT 'legacy',
    watch_identity TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE watch_progress (
    profile_id TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    position_seconds REAL NOT NULL,
    duration_seconds REAL NOT NULL,
    completed BOOLEAN DEFAULT false,
    updated_at TEXT NOT NULL,
    event_at TEXT,
    synced_seq INTEGER,
    last_file_id INTEGER,
    last_resolution TEXT,
    last_hdr BOOLEAN,
    last_codec_video TEXT,
    last_edition_key TEXT,
    PRIMARY KEY (profile_id, media_item_id)
);
CREATE TABLE watchlist (
    profile_id TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    added_at TEXT NOT NULL,
    sort_index INTEGER,
    PRIMARY KEY (profile_id, media_item_id)
);
CREATE INDEX idx_hidden_history_items_lookup
    ON hidden_history_items(profile_id, hidden_before);
CREATE INDEX idx_home_item_dismissals_lookup
    ON home_item_dismissals(profile_id, surface);
CREATE INDEX idx_personal_collection_profiles_lookup
    ON personal_collection_profiles(profile_id, collection_id);
CREATE INDEX idx_profile_allowed_libraries_lookup
    ON profile_allowed_libraries(profile_id);
CREATE INDEX idx_profile_section_overrides_lookup
    ON profile_section_overrides(profile_id, scope, library_id);
CREATE INDEX idx_watch_history_item_witness
    ON watch_history (profile_id, media_item_id, watched_at DESC, id DESC);
CREATE INDEX personal_collection_items_continuation_idx
    ON personal_collection_items (collection_id, position, media_item_id);
CREATE INDEX user_devices_household_recency_idx ON user_devices (last_seen_at DESC, profile_id, device_id);
CREATE INDEX user_devices_profile_recency_idx ON user_devices (profile_id, last_seen_at DESC, device_id);
CREATE INDEX user_setting_migration_rejects_source_idx
    ON user_setting_migration_rejects (source_table);
CREATE INDEX user_setting_mutations_expiry_idx
    ON user_setting_mutations (expires_at);
CREATE UNIQUE INDEX user_setting_values_account_uq
    ON user_setting_values (key) WHERE scope = 'account';
CREATE INDEX user_setting_values_library_idx
    ON user_setting_values (profile_id, library_id);
CREATE UNIQUE INDEX user_setting_values_profile_client_uq
			ON user_setting_values (profile_id, client_family, key) WHERE scope = 'profile_client';
CREATE UNIQUE INDEX user_setting_values_profile_device_uq
    ON user_setting_values (profile_id, device_id, key) WHERE scope = 'profile_device';
CREATE UNIQUE INDEX user_setting_values_profile_library_uq
    ON user_setting_values (profile_id, library_id, key) WHERE scope = 'profile_library';
CREATE UNIQUE INDEX user_setting_values_profile_series_uq
    ON user_setting_values (profile_id, series_id, key) WHERE scope = 'profile_series';
CREATE UNIQUE INDEX user_setting_values_profile_uq
    ON user_setting_values (profile_id, key) WHERE scope = 'profile';
CREATE INDEX user_setting_values_resolution_idx
    ON user_setting_values (profile_id, key, scope);
CREATE INDEX user_setting_values_series_idx
    ON user_setting_values (profile_id, series_id);
CREATE INDEX watch_progress_synced_idx ON watch_progress (profile_id, synced_seq);
CREATE TRIGGER personal_collection_items_position_insert AFTER INSERT ON personal_collection_items
WHEN NEW.position IS NULL
BEGIN
    UPDATE personal_collection_items SET position = 0 WHERE collection_id = NEW.collection_id AND media_item_id = NEW.media_item_id;
END;
CREATE TRIGGER personal_collection_items_position_update AFTER UPDATE OF position ON personal_collection_items
WHEN NEW.position IS NULL
BEGIN
    UPDATE personal_collection_items SET position = 0 WHERE collection_id = NEW.collection_id AND media_item_id = NEW.media_item_id;
END;
CREATE TRIGGER personal_collection_items_revision_delete AFTER DELETE ON personal_collection_items
BEGIN
    INSERT INTO personal_collection_revisions (collection_id, revision) VALUES (OLD.collection_id, 1)
    ON CONFLICT (collection_id) DO UPDATE SET revision = revision + 1;
END;
CREATE TRIGGER personal_collection_items_revision_insert AFTER INSERT ON personal_collection_items
BEGIN
    INSERT INTO personal_collection_revisions (collection_id, revision) VALUES (NEW.collection_id, 1)
    ON CONFLICT (collection_id) DO UPDATE SET revision = revision + 1;
END;
CREATE TRIGGER personal_collection_items_revision_update AFTER UPDATE ON personal_collection_items
BEGIN
    INSERT INTO personal_collection_revisions (collection_id, revision) VALUES (OLD.collection_id, 1)
    ON CONFLICT (collection_id) DO UPDATE SET revision = revision + 1;
    INSERT INTO personal_collection_revisions (collection_id, revision) VALUES (NEW.collection_id, 1)
    ON CONFLICT (collection_id) DO UPDATE SET revision = revision + 1;
END;
CREATE TRIGGER personal_collection_profiles_order_revision_delete AFTER DELETE ON personal_collection_profiles
BEGIN
 UPDATE personal_collection_order_revision SET revision=revision+1 WHERE singleton=1;
END;
CREATE TRIGGER personal_collection_profiles_order_revision_insert AFTER INSERT ON personal_collection_profiles
BEGIN
 UPDATE personal_collection_order_revision SET revision=revision+1 WHERE singleton=1;
END;
CREATE TRIGGER personal_collection_profiles_order_revision_update AFTER UPDATE ON personal_collection_profiles
BEGIN
 UPDATE personal_collection_order_revision SET revision=revision+1 WHERE singleton=1;
END;
CREATE TRIGGER personal_collection_profiles_revision_delete AFTER DELETE ON personal_collection_profiles
BEGIN
    INSERT INTO personal_collection_revisions (collection_id, revision) VALUES (OLD.collection_id, 1)
    ON CONFLICT (collection_id) DO UPDATE SET revision = revision + 1;
END;
CREATE TRIGGER personal_collection_profiles_revision_insert AFTER INSERT ON personal_collection_profiles
BEGIN
    INSERT INTO personal_collection_revisions (collection_id, revision) VALUES (NEW.collection_id, 1)
    ON CONFLICT (collection_id) DO UPDATE SET revision = revision + 1;
END;
CREATE TRIGGER personal_collection_profiles_revision_update AFTER UPDATE ON personal_collection_profiles
BEGIN
    INSERT INTO personal_collection_revisions (collection_id, revision) VALUES (OLD.collection_id, 1)
    ON CONFLICT (collection_id) DO UPDATE SET revision = revision + 1;
    INSERT INTO personal_collection_revisions (collection_id, revision) VALUES (NEW.collection_id, 1)
    ON CONFLICT (collection_id) DO UPDATE SET revision = revision + 1;
END;
CREATE TRIGGER personal_collections_order_revision_delete AFTER DELETE ON personal_collections
BEGIN
 UPDATE personal_collection_order_revision SET revision=revision+1 WHERE singleton=1;
END;
CREATE TRIGGER personal_collections_order_revision_insert AFTER INSERT ON personal_collections
BEGIN
 UPDATE personal_collection_order_revision SET revision=revision+1 WHERE singleton=1;
END;
CREATE TRIGGER personal_collections_order_revision_update AFTER UPDATE ON personal_collections
BEGIN
 UPDATE personal_collection_order_revision SET revision=revision+1 WHERE singleton=1;
END;
CREATE TRIGGER personal_collections_revision_delete AFTER DELETE ON personal_collections
BEGIN
    INSERT INTO personal_collection_revisions (collection_id, revision) VALUES (OLD.id, 1)
    ON CONFLICT (collection_id) DO UPDATE SET revision = revision + 1;
END;
CREATE TRIGGER personal_collections_revision_insert AFTER INSERT ON personal_collections
BEGIN
    INSERT INTO personal_collection_revisions (collection_id, revision) VALUES (NEW.id, 1)
    ON CONFLICT (collection_id) DO UPDATE SET revision = revision + 1;
END;
CREATE TRIGGER personal_collections_revision_update AFTER UPDATE ON personal_collections
BEGIN
    INSERT INTO personal_collection_revisions (collection_id, revision) VALUES (OLD.id, 1)
    ON CONFLICT (collection_id) DO UPDATE SET revision = revision + 1;
    INSERT INTO personal_collection_revisions (collection_id, revision) VALUES (NEW.id, 1)
    ON CONFLICT (collection_id) DO UPDATE SET revision = revision + 1;
END;
CREATE TRIGGER watch_progress_stamp_ins AFTER INSERT ON watch_progress
BEGIN
    UPDATE watch_progress
    SET synced_seq = (SELECT COALESCE(MAX(synced_seq), 0) + 1 FROM watch_progress),
        event_at = COALESCE(event_at, updated_at)
    WHERE rowid = NEW.rowid;
END;
CREATE TRIGGER watch_progress_stamp_upd AFTER UPDATE ON watch_progress
BEGIN
    UPDATE watch_progress
    SET synced_seq = (SELECT COALESCE(MAX(synced_seq), 0) + 1 FROM watch_progress),
        event_at = CASE
            WHEN NEW.event_at IS NULL THEN NEW.updated_at
            WHEN NEW.event_at IS OLD.event_at AND NEW.updated_at IS NOT OLD.updated_at THEN NEW.updated_at
            ELSE NEW.event_at
        END
    WHERE rowid = NEW.rowid;
END;
PRAGMA user_version=22;

-- Initial singleton inserted by the schema22 constructor.
INSERT INTO personal_collection_order_revision VALUES(1,1);
