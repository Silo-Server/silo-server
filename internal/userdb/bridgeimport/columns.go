package bridgeimport

// ColumnKind is the explicit wire-to-storage conversion used by the importer.
type ColumnKind uint8

const (
	textColumn ColumnKind = iota
	integerColumn
	booleanColumn
	realColumn
	instantColumn
	instantTextColumn
	jsonColumn
)

// SourceColumn describes the exact supported schema-22 source representation.
type SourceColumn struct {
	Name       string
	SQLiteType string
	Kind       ColumnKind
	NotNull    bool
	PrimaryKey int
}

const (
	sourceProfileID           = "profile_id"
	sourceText                = "TEXT"
	sourceInteger             = "INTEGER"
	sourceBoolean             = "BOOLEAN"
	sourceReal                = "REAL"
	sourceSeriesId            = "series_id"
	sourceUpdatedAt           = "updated_at"
	sourceCollectionId        = "collection_id"
	sourceMediaItemId         = "media_item_id"
	sourceAddedAt             = "added_at"
	sourceValue               = "value"
	sourceLibraryId           = "library_id"
	sourceSubtitleLanguage    = "subtitle_language"
	sourceSubtitleMode        = "subtitle_mode"
	sourceShowForcedSubtitles = "show_forced_subtitles"
	sourceRevision            = "revision"
	sourceCreatedAt           = "created_at"
	sourceDeviceId            = "device_id"
	sourceKey                 = "key"
)

// sourceColumns deliberately lists every column; unknown state cannot be silently discarded.
var sourceColumns = map[string][]SourceColumn{
	"audio_preferences": {
		{sourceProfileID, sourceText, textColumn, true, 1},
		{sourceSeriesId, sourceText, textColumn, true, 2},
		{"audio_track_index", "INT", integerColumn, false, 0},
		{"audio_language", sourceText, textColumn, false, 0},
		{"audio_track_signature", sourceText, jsonColumn, true, 0},
		{sourceUpdatedAt, sourceText, instantColumn, true, 0},
	},
	"collection_sort_preferences": {
		{sourceProfileID, sourceText, textColumn, true, 1},
		{"collection_kind", sourceText, textColumn, true, 2},
		{sourceCollectionId, sourceText, textColumn, true, 3},
		{"sort_field", sourceText, textColumn, true, 0},
		{"sort_order", sourceText, textColumn, true, 0},
		{sourceUpdatedAt, sourceText, instantColumn, true, 0},
	},
	"downloads": {
		{"id", sourceText, textColumn, false, 1},
		{sourceProfileID, sourceText, textColumn, true, 0},
		{sourceMediaItemId, sourceText, textColumn, true, 0},
		{"media_file_id", sourceInteger, integerColumn, true, 0},
		{"quality", sourceText, textColumn, false, 0},
		{"transcoded", sourceBoolean, booleanColumn, false, 0},
		{"file_size", sourceInteger, integerColumn, false, 0},
		{"expires_at", sourceText, instantColumn, false, 0},
		{"downloaded_at", sourceText, instantColumn, false, 0},
	},
	"favorites": {
		{sourceProfileID, sourceText, textColumn, true, 1},
		{sourceMediaItemId, sourceText, textColumn, true, 2},
		{sourceAddedAt, sourceText, instantColumn, true, 0},
	},
	"hidden_history_items": {
		{sourceProfileID, sourceText, textColumn, true, 1},
		{sourceMediaItemId, sourceText, textColumn, true, 2},
		{"hidden_before", sourceText, instantColumn, true, 0},
		{sourceUpdatedAt, sourceText, instantColumn, true, 0},
	},
	"home_item_dismissals": {
		{sourceProfileID, sourceText, textColumn, true, 1},
		{"surface", sourceText, textColumn, true, 2},
		{sourceMediaItemId, sourceText, textColumn, true, 3},
		{sourceSeriesId, sourceText, textColumn, false, 0},
		{"progress_updated_at", sourceText, instantColumn, false, 0},
		{"dismissed_at", sourceText, instantColumn, true, 0},
	},
	"jellycompat_displayprefs": {
		{"prefs_id", sourceText, textColumn, true, 1},
		{"client", sourceText, textColumn, true, 2},
		{sourceValue, sourceText, textColumn, true, 0},
		{sourceUpdatedAt, sourceText, instantColumn, true, 0},
	},
	"library_playback_preferences": {
		{sourceProfileID, sourceText, textColumn, true, 1},
		{sourceLibraryId, sourceInteger, integerColumn, true, 2},
		{"audio_language", sourceText, textColumn, false, 0},
		{sourceSubtitleLanguage, sourceText, textColumn, false, 0},
		{sourceSubtitleMode, sourceText, textColumn, false, 0},
		{sourceShowForcedSubtitles, sourceBoolean, booleanColumn, false, 0},
		{sourceUpdatedAt, sourceText, instantColumn, true, 0},
	},
	"personal_collection_items": {
		{sourceCollectionId, sourceText, textColumn, true, 1},
		{sourceMediaItemId, sourceText, textColumn, true, 2},
		{"position", sourceInteger, integerColumn, false, 0},
		{sourceAddedAt, sourceText, instantColumn, true, 0},
	},
	sourceOrderRevision: {
		{sourceSingleton, sourceInteger, integerColumn, false, 1},
		{sourceRevision, sourceInteger, integerColumn, true, 0},
	},
	"personal_collection_profiles": {
		{sourceCollectionId, sourceText, textColumn, true, 1},
		{sourceProfileID, sourceText, textColumn, true, 2},
	},
	sourceCollectionRevisions: {
		{sourceCollectionId, sourceText, textColumn, false, 1},
		{sourceRevision, sourceInteger, integerColumn, true, 0},
	},
	"personal_collections": {
		{"id", sourceText, textColumn, false, 1},
		{sourceProfileID, sourceText, textColumn, true, 0},
		{"creator_profile_id", sourceText, textColumn, true, 0},
		{"name", sourceText, textColumn, true, 0},
		{"collection_type", sourceText, textColumn, true, 0},
		{"is_shared", sourceBoolean, booleanColumn, false, 0},
		{"query_definition", sourceText, jsonColumn, true, 0},
		{"sort_config", sourceText, jsonColumn, true, 0},
		{sourceCreatedAt, sourceText, instantColumn, true, 0},
		{sourceUpdatedAt, sourceText, instantColumn, true, 0},
	},
	"playback_sessions": {
		{"session_id", sourceText, textColumn, false, 1},
		{sourceProfileID, sourceText, textColumn, true, 0},
		{"media_file_id", sourceInteger, integerColumn, true, 0},
		{"play_method", sourceText, textColumn, true, 0},
		{"position_seconds", sourceReal, realColumn, false, 0},
		{"is_paused", sourceBoolean, booleanColumn, false, 0},
		{"started_at", sourceText, instantColumn, true, 0},
		{sourceUpdatedAt, sourceText, instantColumn, true, 0},
	},
	"profile_allowed_libraries": {
		{sourceProfileID, sourceText, textColumn, true, 1},
		{sourceLibraryId, sourceInteger, integerColumn, true, 2},
	},
	"profile_onboarding": {
		{sourceProfileID, sourceText, textColumn, true, 1},
		{"tour_id", sourceText, textColumn, true, 2},
		{"last_step", sourceText, textColumn, true, 0},
		{"completed_at", sourceText, instantTextColumn, false, 0},
		{"skipped_at", sourceText, instantTextColumn, false, 0},
		{sourceUpdatedAt, sourceText, instantTextColumn, true, 0},
	},
	sourceSectionOverrides: {
		{"id", sourceText, textColumn, false, 1},
		{sourceProfileID, sourceText, textColumn, true, 0},
		{"scope", sourceText, textColumn, true, 0},
		{sourceLibraryId, sourceText, textColumn, false, 0},
		{"section_id", sourceText, textColumn, false, 0},
		{"position", sourceInteger, integerColumn, false, 0},
		{"hidden", sourceInteger, integerColumn, true, 0},
		{"removed", sourceInteger, integerColumn, true, 0},
		{"section_type", sourceText, textColumn, false, 0},
		{"title", sourceText, textColumn, false, 0},
		{"featured", sourceInteger, integerColumn, false, 0},
		{"item_limit", sourceInteger, integerColumn, false, 0},
		{"config", sourceText, textColumn, false, 0},
		{"is_user_added", sourceInteger, integerColumn, true, 0},
		{"user_section_type", sourceText, textColumn, false, 0},
		{"user_config", sourceText, textColumn, false, 0},
		{"user_title", sourceText, textColumn, false, 0},
		{sourceCreatedAt, sourceText, instantColumn, true, 0},
		{sourceUpdatedAt, sourceText, instantColumn, true, 0},
	},
	"profiles": {
		{"id", sourceText, textColumn, false, 1},
		{"name", sourceText, textColumn, true, 0},
		{"avatar", sourceText, textColumn, false, 0},
		{"pin_hash", sourceText, textColumn, false, 0},
		{"is_child", sourceBoolean, booleanColumn, false, 0},
		{"is_primary", sourceBoolean, booleanColumn, true, 0},
		{"max_content_rating", sourceText, textColumn, false, 0},
		{"quality_preference", sourceText, textColumn, false, 0},
		{"language", sourceText, textColumn, false, 0},
		{sourceSubtitleLanguage, sourceText, textColumn, false, 0},
		{sourceSubtitleMode, sourceText, textColumn, false, 0},
		{"auto_skip_intro", sourceBoolean, booleanColumn, false, 0},
		{"auto_skip_credits", sourceBoolean, booleanColumn, false, 0},
		{"auto_skip_recap", sourceBoolean, booleanColumn, false, 0},
		{"auto_play_next_preview", sourceBoolean, booleanColumn, false, 0},
		{sourceShowForcedSubtitles, sourceBoolean, booleanColumn, true, 0},
		{"library_restrictions_enabled", sourceBoolean, booleanColumn, false, 0},
		{"max_playback_quality", sourceText, textColumn, false, 0},
		{sourceCreatedAt, sourceText, instantColumn, true, 0},
		{sourceUpdatedAt, sourceText, instantColumn, true, 0},
	},
	"series_playback_preferences": {
		{sourceProfileID, sourceText, textColumn, true, 1},
		{sourceSeriesId, sourceText, textColumn, true, 2},
		{"resolution", sourceText, textColumn, false, 0},
		{"hdr", sourceBoolean, booleanColumn, true, 0},
		{"codec_video", sourceText, textColumn, false, 0},
		{sourceUpdatedAt, sourceText, instantColumn, true, 0},
	},
	"subtitle_preferences": {
		{sourceProfileID, sourceText, textColumn, true, 1},
		{sourceSeriesId, sourceText, textColumn, true, 2},
		{sourceSubtitleLanguage, sourceText, textColumn, false, 0},
		{"subtitle_track_index", "INT", integerColumn, false, 0},
		{"external_subtitle_path", sourceText, textColumn, false, 0},
		{sourceSubtitleMode, sourceText, textColumn, false, 0},
		{"subtitle_track_signature", sourceText, jsonColumn, true, 0},
		{sourceShowForcedSubtitles, sourceBoolean, booleanColumn, false, 0},
		{sourceUpdatedAt, sourceText, instantColumn, true, 0},
	},
	"user_device_settings": {
		{sourceProfileID, sourceText, textColumn, true, 1},
		{sourceDeviceId, sourceText, textColumn, true, 2},
		{sourceKey, sourceText, textColumn, true, 3},
		{sourceValue, sourceText, textColumn, true, 0},
		{"device_name", sourceText, textColumn, true, 0},
		{"device_platform", sourceText, textColumn, true, 0},
		{sourceUpdatedAt, sourceText, instantColumn, true, 0},
	},
	"user_devices": {
		{sourceProfileID, sourceText, textColumn, true, 1},
		{sourceDeviceId, sourceText, textColumn, true, 2},
		{"device_name", sourceText, textColumn, true, 0},
		{"device_platform", sourceText, textColumn, true, 0},
		{"last_seen_at", sourceText, instantColumn, true, 0},
	},
	"user_setting_migration_rejects": {
		{"id", sourceInteger, integerColumn, false, 1},
		{"source_table", sourceText, textColumn, true, 0},
		{"source_key", sourceText, textColumn, true, 0},
		{"identity", sourceText, jsonColumn, true, 0},
		{sourceValue, sourceText, textColumn, false, 0},
		{"reason", sourceText, textColumn, true, 0},
		{"recorded_at", sourceText, instantColumn, true, 0},
	},
	"user_setting_mutations": {
		{"mutation_id", sourceText, textColumn, false, 1},
		{"request_hash", sourceText, textColumn, true, 0},
		{"result", sourceText, jsonColumn, true, 0},
		{sourceCreatedAt, sourceText, instantColumn, true, 0},
		{"expires_at", sourceText, instantColumn, true, 0},
	},
	"user_setting_values": {
		{"id", sourceInteger, integerColumn, false, 1},
		{sourceKey, sourceText, textColumn, true, 0},
		{"scope", sourceText, textColumn, true, 0},
		{sourceProfileID, sourceText, textColumn, false, 0},
		{"client_family", sourceText, textColumn, false, 0},
		{sourceDeviceId, sourceText, textColumn, false, 0},
		{sourceLibraryId, sourceInteger, integerColumn, false, 0},
		{sourceSeriesId, sourceText, textColumn, false, 0},
		{sourceValue, sourceText, jsonColumn, true, 0},
		{sourceRevision, sourceInteger, integerColumn, true, 0},
		{sourceCreatedAt, sourceText, instantColumn, true, 0},
		{sourceUpdatedAt, sourceText, instantColumn, true, 0},
	},
	"user_settings": {
		{sourceKey, sourceText, textColumn, false, 1},
		{sourceValue, sourceText, textColumn, true, 0},
	},
	"watch_history": {
		{"id", sourceText, textColumn, false, 1},
		{sourceProfileID, sourceText, textColumn, true, 0},
		{sourceMediaItemId, sourceText, textColumn, true, 0},
		{"watched_at", sourceText, instantColumn, true, 0},
		{"duration_seconds", sourceReal, realColumn, false, 0},
		{"completed", sourceBoolean, booleanColumn, false, 0},
		{"source", sourceText, textColumn, true, 0},
		{"watch_identity", sourceText, jsonColumn, true, 0},
	},
	"watch_progress": {
		{sourceProfileID, sourceText, textColumn, true, 1},
		{sourceMediaItemId, sourceText, textColumn, true, 2},
		{"position_seconds", sourceReal, realColumn, true, 0},
		{"duration_seconds", sourceReal, realColumn, true, 0},
		{"completed", sourceBoolean, booleanColumn, false, 0},
		{sourceUpdatedAt, sourceText, instantColumn, true, 0},
		{"event_at", sourceText, instantColumn, false, 0},
		{"synced_seq", sourceInteger, integerColumn, false, 0},
		{"last_file_id", sourceInteger, integerColumn, false, 0},
		{"last_resolution", sourceText, textColumn, false, 0},
		{"last_hdr", sourceBoolean, booleanColumn, false, 0},
		{"last_codec_video", sourceText, textColumn, false, 0},
		{"last_edition_key", sourceText, textColumn, false, 0},
	},
	"watchlist": {
		{sourceProfileID, sourceText, textColumn, true, 1},
		{sourceMediaItemId, sourceText, textColumn, true, 2},
		{sourceAddedAt, sourceText, instantColumn, true, 0},
		{"sort_index", sourceInteger, integerColumn, false, 0},
	},
}
