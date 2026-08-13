package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// playbackSessionRow represents a live playback row from playback_sessions_sync
// enriched with user and media information. play_method is the current transport
// method for the active stream; component-level behavior is exposed separately
// via video_decision and audio_decision.
type playbackSessionRow struct {
	SessionID                string    `json:"session_id"`
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
	HasPlaybackControl       bool      `json:"has_playback_control"`
	ClientIP                 string    `json:"client_ip,omitempty"`
	ClientName               string    `json:"client_name,omitempty"`
	ClientVersion            string    `json:"client_version,omitempty"`
	ClientBuild              string    `json:"client_build,omitempty"`
	ClientChannel            string    `json:"client_channel,omitempty"`
	ClientLabel              string    `json:"client_label,omitempty"`
	ClientLabelFull          string    `json:"client_label_full,omitempty"`
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

// playbackSessionsCapabilitiesResponse advertises the additive fields of the
// live admin session payload so independently deployed clients (Android,
// Apple) can feature-detect them. Both fields are omitempty on the wire, so
// absence on a row is otherwise indistinguishable from an older server.
type playbackSessionsCapabilitiesResponse struct {
	// EffectivePlayMethod reports that rows carry effective_play_method.
	EffectivePlayMethod bool `json:"effective_play_method"`
	// EffectivePlayMethodValues is the closed bucket vocabulary a supported
	// server emits (absent field = unknown).
	EffectivePlayMethodValues []string `json:"effective_play_method_values"`
	// IsJellyfinClient reports that rows carry is_jellyfin_client.
	IsJellyfinClient bool `json:"is_jellyfin_client"`
	// ClientBuild reports that rows carry client_build (and the exact-version
	// client_label_full derived from it).
	ClientBuild bool `json:"client_build"`
	// ClientChannel reports that rows carry client_channel.
	ClientChannel bool `json:"client_channel"`
}

// HandleGetSessionsCapabilities exposes additive feature support for the live
// admin session payload (GET /admin/sessions/capabilities).
func (h *AdminHandler) HandleGetSessionsCapabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, playbackSessionsCapabilitiesResponse{
		EffectivePlayMethod:       true,
		EffectivePlayMethodValues: []string{"direct", "remux", "transcode", "audio"},
		IsJellyfinClient:          true,
		ClientBuild:               true,
		ClientChannel:             true,
	})
}

// PlaybackSessionsQuery scopes live session listing.
type PlaybackSessionsQuery struct {
	// UserID, when positive, limits results to sessions owned by that account.
	UserID int
}

type playbackSessionsReader interface {
	Load(ctx context.Context, r *http.Request, query PlaybackSessionsQuery) ([]playbackSessionRow, error)
}

func resolvePlaybackSessionsLoader(
	loader *PlaybackSessionsLoader,
	pool *pgxpool.Pool,
	storeProv userstore.UserStoreProvider,
	detailSvc *catalog.DetailService,
) (*PlaybackSessionsLoader, error) {
	if loader != nil {
		return loader, nil
	}
	if pool == nil {
		return nil, errors.New("database not configured")
	}
	return NewPlaybackSessionsLoader(pool, storeProv, detailSvc), nil
}

// PlaybackSessionsLoader reads enriched rows from playback_sessions_sync.
type PlaybackSessionsLoader struct {
	pool      *pgxpool.Pool
	storeProv userstore.UserStoreProvider
	DetailSvc *catalog.DetailService
}

func NewPlaybackSessionsLoader(
	pool *pgxpool.Pool,
	storeProv userstore.UserStoreProvider,
	detailSvc *catalog.DetailService,
) *PlaybackSessionsLoader {
	return &PlaybackSessionsLoader{
		pool:      pool,
		storeProv: storeProv,
		DetailSvc: detailSvc,
	}
}

func (l *PlaybackSessionsLoader) Load(
	ctx context.Context,
	r *http.Request,
	query PlaybackSessionsQuery,
) ([]playbackSessionRow, error) {
	if l == nil || l.pool == nil {
		return nil, errors.New("database not configured")
	}

	sql := `
		SELECT
			s.session_id,
			s.user_id,
			COALESCE(u.username, ''),
			COALESCE(s.profile_id, ''),
			s.media_file_id,
			COALESCE(s.requested_media_file_id, s.media_file_id, 0),
			COALESCE(mf.episode_id, mf.content_id, ''),
			COALESCE(mi.title, ''),
			COALESCE(mi.type, ''),
			COALESCE(series_mi.title, ''),
			COALESCE(e.title, ''),
			e.season_number,
			e.episode_number,
			COALESCE(CASE WHEN e.series_id IS NOT NULL THEN series_mi.poster_path ELSE mi.poster_path END, ''),
			s.play_method,
			s.reporting_node,
			COALESCE(remote_node.name, ''),
			mf.duration,
			s.started_at,
			s.updated_at,
			COALESCE(s.position_seconds, 0),
			COALESCE(s.is_paused, FALSE),
			COALESCE(s.has_websocket, FALSE),
			COALESCE(HOST(s.client_ip), ''),
			COALESCE(s.client_name, ''),
			COALESCE(s.client_version, ''),
			COALESCE(s.client_build, ''),
			COALESCE(s.client_channel, ''),
			COALESCE(s.client_user_agent, ''),
			COALESCE(s.audio_track_index, 0),
			COALESCE(s.transcode_audio, FALSE),
			s.stream_bitrate_kbps,
			COALESCE(s.transcode_node_url, ''),
			COALESCE(s.target_resolution, ''),
			COALESCE(s.target_video_codec, ''),
			COALESCE(s.target_audio_codec, ''),
			s.target_bitrate_kbps,
			COALESCE(s.transcode_hw_accel, ''),
			COALESCE(mf.container, ''),
			mf.bitrate,
			COALESCE(mf.codec_video, ''),
			COALESCE(mf.resolution, ''),
			COALESCE(mf.codec_audio, ''),
			mf.audio_channels,
			COALESCE(mf.audio_tracks::text, '[]'),
			COALESCE(requested_mf.codec_video, ''),
			COALESCE(requested_mf.resolution, ''),
			COALESCE(s.compat_origin, FALSE)
		 FROM playback_sessions_sync s
		 LEFT JOIN users u ON u.id = s.user_id
		 LEFT JOIN media_files mf ON mf.id = s.media_file_id
		 LEFT JOIN media_files requested_mf ON requested_mf.id = COALESCE(s.requested_media_file_id, s.media_file_id)
		 LEFT JOIN media_items mi ON mi.content_id = mf.content_id
		 LEFT JOIN episodes e ON e.content_id = mf.episode_id
		 LEFT JOIN media_items series_mi ON series_mi.content_id = e.series_id
		 LEFT JOIN stream_nodes remote_node ON remote_node.url = s.transcode_node_url`

	var args []any
	if query.UserID > 0 {
		sql += " WHERE s.user_id = $1"
		args = append(args, query.UserID)
	}
	sql += " ORDER BY s.started_at DESC LIMIT 200"

	rows, err := l.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("querying playback sessions: %w", err)
	}
	defer rows.Close()

	sessions := make([]playbackSessionRow, 0)
	for rows.Next() {
		var s playbackSessionRow
		var posterPath string
		var streamBitrateKbps *int
		var targetBitrateKbps *int
		var sourceBitrateKbps *int
		var sourceAudioChannels *int
		var audioTracksJSON []byte
		if err := rows.Scan(
			&s.SessionID, &s.UserID, &s.Username, &s.ProfileID, &s.MediaFileID, &s.RequestedMediaFileID, &s.ContentID,
			&s.MediaTitle, &s.MediaType, &s.SeriesName, &s.EpisodeName, &s.SeasonNumber, &s.EpisodeNumber,
			&posterPath,
			&s.PlayMethod, &s.ReportingNode, &s.NodeDisplayName, &s.FileDuration, &s.StartedAt, &s.UpdatedAt,
			&s.PositionSeconds, &s.IsPaused, &s.HasPlaybackControl, &s.ClientIP, &s.ClientName, &s.ClientVersion,
			&s.ClientBuild, &s.ClientChannel,
			&s.ClientUserAgent, &s.AudioTrackIndex, &s.TranscodeAudio, &streamBitrateKbps,
			&s.TranscodeNodeURL, &s.TargetResolution, &s.TargetVideoCodec, &s.TargetAudioCodec, &targetBitrateKbps,
			&s.TranscodeHWAccel, &s.SourceContainer, &sourceBitrateKbps, &s.SourceVideoCodec, &s.SourceVideoResolution,
			&s.SourceAudioCodec, &sourceAudioChannels, &audioTracksJSON, &s.RequestedVideoCodec, &s.RequestedVideoResolution,
			&s.CompatOrigin,
		); err != nil {
			return nil, fmt.Errorf("scanning playback session: %w", err)
		}
		s.PosterURL = l.presignPosterURL(r, posterPath)
		s.StreamBitrateKbps = streamBitrateKbps
		s.TargetBitrateKbps = targetBitrateKbps
		s.SourceBitrateKbps = sourceBitrateKbps
		s.SourceAudioChannels = sourceAudioChannels
		s.ClientLabel = playbackClientDisplayName(s.ClientName, s.ClientVersion, s.ClientUserAgent)
		// Only worth the bytes when it says more than the compact label — which
		// it does not for any client without a build or a non-release channel,
		// i.e. most of a 200-row page. Clients read client_label when
		// client_label_full is absent, so omitting the duplicate costs nothing.
		if full := playbackClientFullDisplayName(s.ClientName, s.ClientVersion, s.ClientBuild, s.ClientChannel, s.ClientUserAgent); full != s.ClientLabel {
			s.ClientLabelFull = full
		}
		enrichPlaybackSessionRow(&s, audioTracksJSON)
		sessions = append(sessions, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	l.populateProfileNames(ctx, sessions)

	return sessions, nil
}

func (l *PlaybackSessionsLoader) presignPosterURL(r *http.Request, path string) string {
	if l != nil && l.DetailSvc != nil {
		return l.DetailSvc.PresignURL(r.Context(), cardThumbnailPath(path), "card")
	}
	return ""
}

func enrichPlaybackSessionRow(row *playbackSessionRow, audioTracksJSON []byte) {
	if row == nil {
		return
	}

	row.VideoDecision, row.AudioDecision = sessionComponentDecision(row.PlayMethod, row.TranscodeAudio, row.TargetVideoCodec)
	row.EffectivePlayMethod = effectivePlayMethod(row.VideoDecision, row.AudioDecision)
	row.IsJellyfinClient = row.CompatOrigin || isJellyfinEcosystemClient(row.ClientName, row.ClientUserAgent)

	var audioTracks []models.AudioTrack
	if len(audioTracksJSON) > 0 {
		_ = json.Unmarshal(audioTracksJSON, &audioTracks)
	}
	if track := pickAdminAudioTrack(audioTracks, row.AudioTrackIndex); track != nil {
		if codec := strings.TrimSpace(track.Codec); codec != "" {
			row.SourceAudioCodec = codec
		}
		if track.Channels > 0 {
			channels := track.Channels
			row.SourceAudioChannels = &channels
		}
		row.SourceAudioLanguage = strings.TrimSpace(track.Language)
		row.SourceAudioTitle = firstNonEmptyValue(track.Title, track.EmbeddedTitle)
		row.SourceAudioLayout = strings.TrimSpace(track.Layout)
	}

	if row.StreamBitrateKbps == nil {
		switch {
		case row.TargetBitrateKbps != nil:
			row.StreamBitrateKbps = row.TargetBitrateKbps
		case row.SourceBitrateKbps != nil:
			row.StreamBitrateKbps = row.SourceBitrateKbps
		}
	}

	if row.AudioDecision == "transcode" && strings.TrimSpace(row.TargetAudioCodec) == "" {
		row.TargetAudioCodec = "aac"
	}
	if row.VideoDecision == "transcode" && strings.TrimSpace(row.TargetVideoCodec) == "" {
		row.TargetVideoCodec = "h264"
	}
	if row.VideoDecision == "transcode" && strings.TrimSpace(row.TargetResolution) == "" {
		row.TargetResolution = row.SourceVideoResolution
	}
	if strings.TrimSpace(row.NodeDisplayName) == "" {
		if strings.TrimSpace(row.TranscodeNodeURL) != "" {
			row.NodeDisplayName = "Remote transcode"
		} else {
			row.NodeDisplayName = "Local server"
		}
	}
}

func pickAdminAudioTrack(tracks []models.AudioTrack, index int) *models.AudioTrack {
	if len(tracks) == 0 {
		return nil
	}
	if index >= 0 && index < len(tracks) {
		track := tracks[index]
		return &track
	}
	for _, track := range tracks {
		if track.Default {
			cp := track
			return &cp
		}
	}
	track := tracks[0]
	return &track
}

func sessionComponentDecision(playMethod string, transcodeAudio bool, targetVideoCodec string) (string, string) {
	switch strings.TrimSpace(playMethod) {
	case "direct":
		return "direct", "direct"
	case "remux":
		if transcodeAudio {
			return "remux", "transcode"
		}
		return "remux", "remux"
	case "transcode":
		videoDec := "transcode"
		if strings.EqualFold(strings.TrimSpace(targetVideoCodec), "copy") {
			videoDec = "remux"
		}
		audioDec := "transcode"
		if !transcodeAudio {
			audioDec = "remux"
		}
		return videoDec, audioDec
	default:
		return "", ""
	}
}

// effectivePlayMethod reduces the per-stream decisions to the single bucket
// the admin activity views aggregate on. Raw play_method is misleading there:
// an HLS repackage reports play_method "transcode" with a copied video stream,
// and an audio-only re-encode reports "remux" — the decisions carry what
// actually costs CPU.
//   - video re-encoded        -> "transcode"
//   - only audio re-encoded   -> "audio"
//   - streams only repackaged -> "remux"
//   - nothing touched         -> "direct"
//
// Returns "" when the decisions are unknown (empty or unrecognized
// play_method, e.g. a stale row from an older node), so consumers can
// distinguish "unknown" from a definite bucket instead of inventing one.
func effectivePlayMethod(videoDecision, audioDecision string) string {
	switch {
	case videoDecision == "" && audioDecision == "":
		return ""
	case videoDecision == "transcode":
		return "transcode"
	case audioDecision == "transcode":
		return "audio"
	case videoDecision == "direct" && audioDecision == "direct":
		return "direct"
	default:
		return "remux"
	}
}

// jellyfinClientTokens identifies Jellyfin-ecosystem clients — sessions served
// through the Jellyfin compatibility surface — by client name (parsed from the
// MediaBrowser auth header) or user agent. This is the single source of truth
// behind is_jellyfin_client, which the admin UIs surface as the "JF" pill;
// keep it in step with the display-labeling rules in
// playbackClientDisplayName so a client that gets a label also gets the flag.
// Kodi and mpv reach Silo only through the Jellyfin compat surface today
// (jellyfin-kodi/JellyCon, jellyfin-mpv-shim). Generic browser tokens are
// deliberately excluded: the native web player shares those user agents.
var jellyfinClientTokens = []string{
	"jellyfin",
	"findroid",
	"streamyfin",
	"swiftfin",
	"jellycon",
	"wholphin",
	"fladder",
	"vidhub",
	"senplayer",
	"infuse",
	"delfin",
	"finamp",
	"kodi",
	"mpv",
}

// isJellyfinEcosystemClient reports whether the session's client metadata
// matches a known Jellyfin-ecosystem client. This heuristic remains a fallback
// for rows created before compat-origin identity was persisted.
func isJellyfinEcosystemClient(clientName, userAgent string) bool {
	for _, value := range []string{clientName, userAgent} {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		for _, token := range jellyfinClientTokens {
			if strings.Contains(value, token) {
				return true
			}
		}
	}
	return false
}

func firstNonEmptyValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// playbackClientFullDisplayName renders the client's exact identity — version,
// opaque build, and non-default channel — for surfaces that can afford the
// width (expanded session details, tooltips). The name-and-version half is
// whatever the compact label resolved, so a client identified only by its user
// agent still gets its build named: a client that bothered to report a build
// has earned having it displayed, whether or not it also named itself.
func playbackClientFullDisplayName(name, version, build, channel, userAgent string) string {
	label := playbackClientDisplayName(name, version, userAgent)
	if label == "" {
		return ""
	}

	build = strings.TrimSpace(build)
	channel = strings.TrimSpace(channel)
	// "release" is the assumed channel, so naming it adds width without adding
	// information; every other channel is worth calling out.
	if strings.EqualFold(channel, "release") {
		channel = ""
	}
	var qualifiers []string
	if build != "" {
		qualifiers = append(qualifiers, "build "+build)
	}
	if channel != "" {
		qualifiers = append(qualifiers, channel)
	}
	if len(qualifiers) > 0 {
		label += " (" + strings.Join(qualifiers, ", ") + ")"
	}
	return label
}

// playbackClientDisplayName renders the compact label the session lists show.
// A client that reports its own name keeps its version verbatim — truncating
// "1.0.0" to "1" hid the only field that identifies the running build. Labels
// derived from a user agent stay shortened, where a full browser version
// ("120.0.6099.109") is noise.
func playbackClientDisplayName(name, version, userAgent string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		if version = strings.TrimSpace(version); version != "" {
			return name + " " + version
		}
		return name
	}

	userAgent = strings.TrimSpace(userAgent)
	if userAgent == "" {
		return ""
	}

	rules := []struct {
		label         string
		tokens        []string
		versionTokens []string
	}{
		{label: "Infuse", tokens: []string{"infuse"}, versionTokens: []string{"infuse-direct", "infuse"}},
		{label: "Findroid", tokens: []string{"findroid"}, versionTokens: []string{"findroid"}},
		{label: "Streamyfin", tokens: []string{"streamyfin"}, versionTokens: []string{"streamyfin"}},
		{label: "Swiftfin", tokens: []string{"swiftfin"}, versionTokens: []string{"swiftfin"}},
		{label: "Jellyfin", tokens: []string{"jellyfin"}, versionTokens: []string{"jellyfin"}},
		{label: "JellyCon", tokens: []string{"jellycon"}, versionTokens: []string{"jellycon"}},
		{label: "Wholphin", tokens: []string{"wholphin"}, versionTokens: []string{"wholphin"}},
		{label: "Fladder", tokens: []string{"fladder"}, versionTokens: []string{"fladder"}},
		{label: "VidHub", tokens: []string{"vidhub"}, versionTokens: []string{"vidhub"}},
		{label: "SenPlayer", tokens: []string{"senplayer"}, versionTokens: []string{"senplayer"}},
		{label: "Kodi", tokens: []string{"kodi"}, versionTokens: []string{"kodi"}},
		{label: "MPV", tokens: []string{"mpv"}, versionTokens: []string{"mpv"}},
		{label: "Edge", tokens: []string{"edg/"}, versionTokens: []string{"edg"}},
		{label: "Opera", tokens: []string{"opr/", "opera"}, versionTokens: []string{"opr", "opera"}},
		{label: "Firefox", tokens: []string{"firefox/", "fxios/"}, versionTokens: []string{"firefox", "fxios"}},
		{label: "Chrome", tokens: []string{"chrome/", "crios/"}, versionTokens: []string{"chrome", "crios"}},
		{label: "Safari", tokens: []string{"safari/"}, versionTokens: []string{"version"}},
	}
	lower := strings.ToLower(userAgent)
	for _, rule := range rules {
		if !containsAny(lower, rule.tokens) {
			continue
		}
		if version := firstProductVersion(userAgent, rule.versionTokens); version != "" {
			return rule.label + " " + version
		}
		return rule.label
	}

	switch {
	case strings.Contains(lower, "applecoremedia"):
		return "Apple player"
	case strings.Contains(lower, "okhttp"):
		return "Android client"
	case strings.Contains(lower, "dart/"):
		return "Flutter client"
	case strings.Contains(lower, "go-http-client"):
		return "Go client"
	case strings.Contains(lower, "curl/"):
		return "curl"
	case strings.Contains(lower, "python-requests"):
		return "Python requests"
	default:
		if label := androidDeviceLabel(userAgent); label != "" {
			return label
		}
		return firstUserAgentProduct(userAgent)
	}
}

// knownAndroidDeviceLabels maps Android / Fire OS build model codes to friendly
// product names for the admin session view. Keys are uppercased so the lookup is
// case-insensitive. This only affects the displayed label — the session still
// stores the raw model code in its user agent.
var knownAndroidDeviceLabels = map[string]string{
	"AFTKRT":            "Fire TV Stick 4K Max",
	"AFTMM":             "Fire TV Stick 4K",
	"AFTKM":             "Fire TV Stick 4K (2nd Gen)",
	"AFTKA":             "Fire TV Stick 4K Max (1st Gen)",
	"AFTSSS":            "Fire TV Stick (3rd Gen)",
	"AFTSS":             "Fire TV Stick Lite (1st Gen)",
	"AFTT":              "Fire TV Stick (2nd Gen)",
	"AFTB":              "Fire TV (1st Gen)",
	"AFTS":              "Fire TV (2nd Gen)",
	"AFTN":              "Fire TV (3rd Gen)",
	"AFTR":              "Fire TV Cube (2nd Gen)",
	"AFTA":              "Fire TV Cube (1st Gen)",
	"SHIELD ANDROID TV": "NVIDIA Shield",
}

// androidDeviceLabel derives a friendly device label from a bare Android / Fire OS
// user agent of the form "... (Linux; U; Android <ver>; <MODEL> Build/<build>)".
// The model is the whole segment between the last ';' and 'Build/', so multi-word
// models ("Pixel 7", "SHIELD Android TV") survive intact. Known model codes map to
// a product name; anything else falls back to "Android · <MODEL>" rather than the
// uninformative "Dalvik". Returns "" when no model can be parsed.
func androidDeviceLabel(userAgent string) string {
	buildIndex := strings.Index(userAgent, "Build/")
	if buildIndex < 0 {
		return ""
	}

	prefix := userAgent[:buildIndex]
	separator := strings.LastIndex(prefix, ";")
	if separator < 0 {
		return ""
	}
	hasAndroidPlatform := false
	for _, segment := range strings.Split(prefix[:separator], ";") {
		segment = strings.TrimSpace(strings.Trim(segment, "()"))
		if strings.HasPrefix(strings.ToLower(segment), "android ") {
			hasAndroidPlatform = true
			break
		}
	}
	if !hasAndroidPlatform {
		return ""
	}

	model := prefix[separator+1:]
	model = strings.TrimSpace(strings.Trim(strings.TrimSpace(model), "();"))
	if model == "" {
		return ""
	}
	if label, ok := knownAndroidDeviceLabels[strings.ToUpper(model)]; ok {
		return label
	}
	return "Android · " + model
}

func containsAny(value string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func firstProductVersion(userAgent string, tokens []string) string {
	for _, field := range strings.Fields(userAgent) {
		name, version, ok := strings.Cut(field, "/")
		if !ok {
			continue
		}
		name = strings.TrimSpace(strings.ToLower(name))
		for _, token := range tokens {
			if name == strings.TrimSpace(strings.ToLower(token)) {
				return shortPlaybackClientVersion(version)
			}
		}
	}
	return ""
}

func shortPlaybackClientVersion(version string) string {
	version = strings.Trim(strings.TrimSpace(version), `";),`)
	if version == "" {
		return ""
	}
	version = strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || r == '.' {
			return r
		}
		return -1
	}, version)
	parts := strings.Split(version, ".")
	filtered := parts[:0]
	for _, part := range parts {
		if part != "" {
			filtered = append(filtered, part)
		}
	}
	if len(filtered) == 0 {
		return ""
	}
	if len(filtered) > 2 {
		filtered = filtered[:2]
	}
	if len(filtered) == 2 && filtered[1] == "0" {
		filtered = filtered[:1]
	}
	return strings.Join(filtered, ".")
}

func firstUserAgentProduct(userAgent string) string {
	for _, field := range strings.Fields(userAgent) {
		name, _, ok := strings.Cut(field, "/")
		if ok {
			name = strings.Trim(name, `";(),`)
			if name != "" && !strings.EqualFold(name, "Mozilla") {
				return name
			}
		}
	}
	return "Unknown client"
}

func (l *PlaybackSessionsLoader) populateProfileNames(ctx context.Context, sessions []playbackSessionRow) {
	if l == nil || l.storeProv == nil || len(sessions) == 0 {
		return
	}

	profileNamesByUser := make(map[int]map[string]string)
	for i := range sessions {
		if strings.TrimSpace(sessions[i].ProfileID) == "" {
			continue
		}
		if _, ok := profileNamesByUser[sessions[i].UserID]; ok {
			continue
		}
		store, err := l.storeProv.ForUser(ctx, sessions[i].UserID)
		if err != nil || store == nil {
			continue
		}
		profiles, err := store.ListProfiles(ctx)
		if err != nil {
			continue
		}
		names := make(map[string]string, len(profiles))
		for _, profile := range profiles {
			if trimmed := strings.TrimSpace(profile.Name); trimmed != "" {
				names[profile.ID] = trimmed
			}
		}
		profileNamesByUser[sessions[i].UserID] = names
	}

	for i := range sessions {
		names := profileNamesByUser[sessions[i].UserID]
		if names == nil {
			continue
		}
		sessions[i].ProfileName = names[sessions[i].ProfileID]
	}
}
