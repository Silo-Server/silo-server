package complexv22

import "time"

var Capabilities = []string{"branding.v1", "sessions.snapshot.v2", "sessions.terminate.v1", "users.identity.v1"}

type SystemCapabilitiesResponse struct {
	APIVersion   string   `json:"api_version"`
	Capabilities []string `json:"capabilities"`
}
type BrandingResponse struct {
	ServerName string  `json:"server_name"`
	LogoURL    *string `json:"logo_url"`
	LogoETag   *string `json:"logo_etag"`
}
type SnapshotSession struct {
	SessionID                string    `json:"session_id"`
	SessionGeneration        string    `json:"session_generation"`
	UserID                   int       `json:"user_id"`
	Username                 string    `json:"username"`
	ProfileID                string    `json:"profile_id"`
	ProfileName              string    `json:"profile_name,omitempty"`
	MediaFileID              int       `json:"media_file_id"`
	RequestedMediaFileID     int       `json:"requested_media_file_id"`
	ContentID                string    `json:"content_id,omitempty"`
	MediaTitle               string    `json:"media_title"`
	MediaType                string    `json:"media_type"`
	SeriesName               string    `json:"series_name,omitempty"`
	EpisodeName              string    `json:"episode_name,omitempty"`
	SeasonNumber             *int      `json:"season_number,omitempty"`
	EpisodeNumber            *int      `json:"episode_number,omitempty"`
	PosterURL                string    `json:"poster_url,omitempty"`
	PlayMethod               string    `json:"play_method"`
	ReportingNode            string    `json:"reporting_node"`
	NodeDisplayName          string    `json:"node_display_name,omitempty"`
	FileDuration             *int      `json:"file_duration"`
	StartedAt                time.Time `json:"started_at"`
	UpdatedAt                time.Time `json:"updated_at"`
	PositionSeconds          float64   `json:"position_seconds"`
	IsPaused                 bool      `json:"is_paused"`
	State                    string    `json:"state"`
	IsTranscoded             bool      `json:"is_transcoded"`
	HasPlaybackControl       bool      `json:"has_playback_control"`
	ClientIP                 string    `json:"client_ip,omitempty"`
	ClientName               string    `json:"client_name,omitempty"`
	ClientVersion            string    `json:"client_version,omitempty"`
	ClientLabel              string    `json:"client_label,omitempty"`
	ClientUserAgent          string    `json:"client_user_agent,omitempty"`
	AudioTrackIndex          int       `json:"audio_track_index"`
	TranscodeAudio           bool      `json:"transcode_audio"`
	StreamBitrateKbps        *int      `json:"stream_bitrate_kbps"`
	TranscodeNodeURL         string    `json:"-"`
	TargetResolution         string    `json:"target_resolution,omitempty"`
	TargetVideoCodec         string    `json:"target_video_codec,omitempty"`
	TargetAudioCodec         string    `json:"target_audio_codec,omitempty"`
	TargetBitrateKbps        *int      `json:"target_bitrate_kbps"`
	TranscodeHWAccel         string    `json:"transcode_hw_accel,omitempty"`
	SourceContainer          string    `json:"source_container,omitempty"`
	SourceBitrateKbps        *int      `json:"source_bitrate_kbps"`
	SourceVideoCodec         string    `json:"source_video_codec,omitempty"`
	SourceVideoResolution    string    `json:"source_video_resolution,omitempty"`
	SourceAudioCodec         string    `json:"source_audio_codec,omitempty"`
	SourceAudioChannels      *int      `json:"source_audio_channels"`
	SourceAudioLanguage      string    `json:"source_audio_language,omitempty"`
	SourceAudioTitle         string    `json:"source_audio_title,omitempty"`
	SourceAudioLayout        string    `json:"source_audio_layout,omitempty"`
	RequestedVideoCodec      string    `json:"requested_video_codec,omitempty"`
	RequestedVideoResolution string    `json:"requested_video_resolution,omitempty"`
	VideoDecision            string    `json:"video_decision,omitempty"`
	AudioDecision            string    `json:"audio_decision,omitempty"`
	EffectivePlayMethod      string    `json:"effective_play_method,omitempty"`
	IsJellyfinClient         bool      `json:"is_jellyfin_client,omitempty"`
	CompatOrigin             bool      `json:"-"`
}
type SessionSnapshotResponse struct {
	SnapshotID       string            `json:"snapshot_id"`
	GeneratedAt      time.Time         `json:"generated_at"`
	Complete         bool              `json:"complete"`
	IncompleteReason string            `json:"incomplete_reason,omitempty"`
	Sessions         []SnapshotSession `json:"sessions"`
}
type TerminateRequest struct {
	SessionGeneration string `json:"session_generation"`
	SnapshotID        string `json:"snapshot_id"`
	ReasonCode        string `json:"reason_code"`
	IdempotencyKey    string `json:"idempotency_key"`
}
type TerminateResponse struct {
	CommandID string `json:"command_id"`
	Status    string `json:"status"`
}
