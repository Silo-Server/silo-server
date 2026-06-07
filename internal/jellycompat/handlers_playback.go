package jellycompat

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type playbackInfoRequest struct {
	UserID               string          `json:"UserId"`
	MediaSourceID        string          `json:"MediaSourceId"`
	AudioStreamIndex     *compatIntValue `json:"AudioStreamIndex,omitempty"`
	StartTimeTicks       int64           `json:"StartTimeTicks"`
	EnableDirectPlay     *bool           `json:"EnableDirectPlay"`
	EnableDirectStream   *bool           `json:"EnableDirectStream"`
	EnableTranscoding    *bool           `json:"EnableTranscoding"`
	AllowVideoStreamCopy *bool           `json:"AllowVideoStreamCopy"`
	AllowAudioStreamCopy *bool           `json:"AllowAudioStreamCopy"`
	DeviceProfile        json.RawMessage `json:"DeviceProfile"`
}

var compatLanguageNames = map[string]string{
	"ar": "Arabic", "ara": "Arabic",
	"bg": "Bulgarian", "bul": "Bulgarian",
	"bn": "Bengali", "ben": "Bengali",
	"cs": "Czech", "ces": "Czech", "cze": "Czech",
	"da": "Danish", "dan": "Danish",
	"de": "German", "deu": "German", "ger": "German",
	"el": "Greek", "ell": "Greek", "gre": "Greek",
	"en": "English", "eng": "English",
	"es": "Spanish", "spa": "Spanish",
	"fa": "Persian", "fas": "Persian", "per": "Persian",
	"fi": "Finnish", "fin": "Finnish",
	"fr": "French", "fra": "French", "fre": "French",
	"he": "Hebrew", "heb": "Hebrew",
	"hi": "Hindi", "hin": "Hindi",
	"hr": "Croatian", "hrv": "Croatian",
	"hu": "Hungarian", "hun": "Hungarian",
	"id": "Indonesian", "ind": "Indonesian",
	"it": "Italian", "ita": "Italian",
	"ja": "Japanese", "jpn": "Japanese",
	"ko": "Korean", "kor": "Korean",
	"ms": "Malay", "may": "Malay", "msa": "Malay",
	"nl": "Dutch", "dut": "Dutch", "nld": "Dutch",
	"no": "Norwegian", "nor": "Norwegian",
	"pl": "Polish", "pol": "Polish",
	"pt": "Portuguese", "por": "Portuguese",
	"ro": "Romanian", "ron": "Romanian", "rum": "Romanian",
	"ru": "Russian", "rus": "Russian",
	"sk": "Slovak", "slk": "Slovak", "slo": "Slovak",
	"sl": "Slovenian", "slv": "Slovenian",
	"sv": "Swedish", "swe": "Swedish",
	"ta": "Tamil", "tam": "Tamil",
	"te": "Telugu", "tel": "Telugu",
	"th": "Thai", "tha": "Thai",
	"tr": "Turkish", "tur": "Turkish",
	"uk": "Ukrainian", "ukr": "Ukrainian",
	"vi": "Vietnamese", "vie": "Vietnamese",
	"zh": "Chinese", "chi": "Chinese", "zho": "Chinese",
}

// SessionManagerInterface matches the playback session manager's API.
type SessionManagerInterface interface {
	StartSession(userID int, profileID string, fileID int, method playback.PlayMethod, transcodeAudio bool) (*playback.Session, error)
	UpdateProgress(sessionID string, position float64, isPaused bool) error
	UpdateAudioTrack(sessionID string, audioTrackIndex int, method playback.PlayMethod) error
	StopSession(sessionID string) error
	GetSession(sessionID string) (*playback.Session, error)
	SetTranscodeNodeURL(sessionID, url string) error
}

type sessionStarterContext interface {
	StartSessionWithContext(ctx context.Context, userID int, profileID string, fileID int, method playback.PlayMethod, transcodeAudio bool) (*playback.Session, error)
}

// FilePathResolver looks up media files by ID.
type FilePathResolver interface {
	GetByID(ctx context.Context, id int) (*models.MediaFile, error)
}

// SettingsReader reads server settings by key.
type SettingsReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// PlaybackHandler serves Jellyfin playback negotiation endpoints.
type PlaybackHandler struct {
	cfg                     *config.Config
	content                 ContentService
	codec                   *ResourceIDCodec
	deviceProfiles          *DeviceProfileStore
	playbackStore           *PlaybackSessionStore
	sessionMgr              SessionManagerInterface
	fileResolver            FilePathResolver
	storeProvider           userstore.UserStoreProvider
	ProxyPool               *nodepool.ProxyPool
	TranscodePool           *nodepool.TranscodePool
	JWTSecret               string
	profileStaler           profileStaler
	profileRefreshRequester profileRefreshRequester
	FFmpegPath              string
	HWAccel                 string
	TranscodeDir            string
	transcodeMu             sync.RWMutex
	transcodes              map[string]*playback.TranscodeSession
	SubtitleRepo            subtitles.Repository // optional; enables downloaded subtitles
	S3Client                subtitles.S3Client   // optional; for serving S3 subtitles
	S3Bucket                string               // bucket for subtitle storage
	SettingsRepo            SettingsReader       // optional; reads watched threshold setting
}

// playbackThresholds reads the playback.watched_threshold and
// playback.min_resume_threshold settings. Zero values mean "use defaults".
func (h *PlaybackHandler) playbackThresholds(ctx context.Context) userstore.ProgressThresholds {
	if h.SettingsRepo == nil {
		return userstore.ProgressThresholds{}
	}
	var t userstore.ProgressThresholds
	if v, _ := h.SettingsRepo.Get(ctx, "playback.watched_threshold"); v != "" {
		if pct, err := strconv.Atoi(v); err == nil && pct > 0 {
			t.WatchedPct = pct
		}
	}
	if v, _ := h.SettingsRepo.Get(ctx, "playback.min_resume_threshold"); v != "" {
		if pct, err := strconv.Atoi(v); err == nil && pct > 0 {
			t.MinResumePct = pct
		}
	}
	return t
}

var errTranscode4KDisallowed = errors.New("4k video transcode disallowed by server settings")

// allow4KVideoTranscode reads the allow_4k_transcode server setting,
// defaulting to deny like the native playback handler.
func (h *PlaybackHandler) allow4KVideoTranscode(ctx context.Context) bool {
	if h.SettingsRepo == nil {
		return false
	}
	v, _ := h.SettingsRepo.Get(ctx, "allow_4k_transcode")
	return v == "true"
}

func is4KResolution(res string) bool {
	return access.CompareQuality(res, "2160p") >= 0
}

// NewPlaybackHandler creates a playback handler.
func NewPlaybackHandler(
	cfg *config.Config,
	content ContentService,
	codec *ResourceIDCodec,
	deviceProfiles *DeviceProfileStore,
	playbackStore *PlaybackSessionStore,
	sessionMgr SessionManagerInterface,
	fileResolver FilePathResolver,
	storeProvider userstore.UserStoreProvider,
) *PlaybackHandler {
	transcodeDir := filepath.Join(os.TempDir(), "silo-transcode")
	ffmpegPath := ""
	hwAccel := ""
	if cfg != nil {
		if cfg.Playback.TranscodeDir != "" {
			transcodeDir = cfg.Playback.TranscodeDir
		}
		ffmpegPath = cfg.Playback.FFmpegPath
		hwAccel = cfg.Playback.HWAccel
	}

	h := &PlaybackHandler{
		cfg:            cfg,
		content:        content,
		codec:          codec,
		deviceProfiles: deviceProfiles,
		playbackStore:  playbackStore,
		sessionMgr:     sessionMgr,
		fileResolver:   fileResolver,
		storeProvider:  storeProvider,
		FFmpegPath:     ffmpegPath,
		HWAccel:        hwAccel,
		TranscodeDir:   transcodeDir,
		transcodes:     make(map[string]*playback.TranscodeSession),
	}
	if cleaned, err := playback.CleanupOrphanedTranscodeDirs(h.TranscodeDir, nil); err != nil {
		slog.Warn("jellycompat transcode cleanup failed", "dir", h.TranscodeDir, "error", err)
	} else if cleaned > 0 {
		slog.Info("jellycompat transcode cleanup removed orphaned dirs", "dir", h.TranscodeDir, "count", cleaned)
	}
	return h
}

func (h *PlaybackHandler) pickTranscodeNode(currentURL string) *nodepool.Node {
	if h.TranscodePool == nil {
		return nil
	}
	best := h.TranscodePool.Acquire()
	if best == nil {
		return nil
	}
	if currentURL == "" || best.URL == currentURL {
		return best
	}
	current := h.TranscodePool.FindByURL(currentURL)
	if current != nil && current.Healthy && current.Enabled &&
		best.ActiveJobs+2 <= current.ActiveJobs {
		return best
	}
	if current != nil && current.Healthy && current.Enabled {
		return current
	}
	return best
}

func (h *PlaybackHandler) buildProxyRedirectURL(
	playSessionID string,
	upstreamSessionID string,
	method string,
	file *models.MediaFile,
	source PlaybackMediaSource,
	transcodeNodeURL string,
	seekSeconds float64,
) (string, error) {
	if h.ProxyPool == nil || h.JWTSecret == "" {
		return "", fmt.Errorf("proxy transport unavailable")
	}
	proxyNode := h.ProxyPool.Pick()
	if proxyNode == nil {
		return "", fmt.Errorf("proxy transport unavailable")
	}

	audioTrackIndex := 0
	if resolvedAudioTrackIndex, ok := compatAudioTrackIndex(source); ok {
		audioTrackIndex = resolvedAudioTrackIndex
	}

	claims := streamtoken.Claims{
		SessionID:       upstreamSessionID,
		MediaPath:       file.FilePath,
		PlayMethod:      method,
		TranscodeAudio:  source.TranscodeAudio,
		AudioTrackIndex: audioTrackIndex,
		TranscodeNode:   transcodeNodeURL,
	}
	token, err := streamtoken.Sign(claims, h.JWTSecret, 24*time.Hour)
	if err != nil {
		return "", err
	}

	switch method {
	case string(playback.PlayDirect):
		return proxyNode.URL + "/stream/direct/" + token, nil
	case string(playback.PlayRemux):
		redirectURL := proxyNode.URL + "/stream/remux/" + token
		if seekSeconds > 0 {
			redirectURL += "?seek=" + strconv.FormatFloat(seekSeconds, 'f', -1, 64)
		}
		return redirectURL, nil
	case string(playback.PlayTranscode):
		return proxyNode.URL + "/stream/transcode/" + token + "/master.m3u8", nil
	default:
		return "", fmt.Errorf("unsupported proxy method %q", method)
	}
}

// clampSeekSeconds caps a client-supplied seek position to the longest
// negotiated source duration. Clients sometimes pass StartTimeTicks values
// that exceed the source runtime (e.g. when a stale resume position is read
// from a Played item). Letting that through makes ffmpeg seek past EOF and
// stalls the HLS pipeline (1+s init.mp4 latency, dead segments after init).
func clampSeekSeconds(seekSeconds float64, sources []PlaybackMediaSource) float64 {
	if seekSeconds <= 0 {
		return 0
	}
	var maxDuration float64
	for _, s := range sources {
		if d := float64(s.Version.Duration); d > maxDuration {
			maxDuration = d
		}
	}
	if maxDuration > 0 && seekSeconds > maxDuration {
		return maxDuration
	}
	return seekSeconds
}

func (h *PlaybackHandler) startRemoteTranscode(
	ctx context.Context,
	upstreamSessionID string,
	source PlaybackMediaSource,
	file *models.MediaFile,
	initialSeekSeconds float64,
	transcodeNodeURL string,
) error {
	if !source.TranscodeAudio && is4KResolution(source.Version.Resolution) && !h.allow4KVideoTranscode(ctx) {
		return errTranscode4KDisallowed
	}
	if d := float64(source.Version.Duration); d > 0 && initialSeekSeconds > d {
		initialSeekSeconds = d
	}
	if initialSeekSeconds < 0 {
		initialSeekSeconds = 0
	}
	segmentDuration := h.compatSegmentDuration()
	startSegmentNumber := 0
	if initialSeekSeconds > 0 && segmentDuration > 0 {
		startSegmentNumber = int(initialSeekSeconds / float64(segmentDuration))
	}

	reqBody := transcodenode.TranscodeStartRequest{
		SessionID:          upstreamSessionID,
		InputPath:          file.FilePath,
		SeekSeconds:        initialSeekSeconds,
		StartSegmentNumber: startSegmentNumber,
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
		SegmentDuration:    segmentDuration,
		HWAccel:            h.HWAccel,
		AudioTrackIndex:    compatAudioTrackIndexOrDefault(source),
		TotalDuration:      float64(source.Version.Duration),
	}
	if source.TranscodeAudio {
		reqBody.TargetCodecVideo = "copy"
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal transcode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, transcodeNodeURL+"/transcode/start", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("build transcode request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+h.JWTSecret)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("remote transcode start failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("remote transcode start rejected: status %d", resp.StatusCode)
	}
	return nil
}

// HandleCapabilitiesFull stores the client device profile reported by Jellyfin apps.
func (h *PlaybackHandler) HandleCapabilitiesFull(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	profile, err := decodeDeviceProfile(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", "Invalid capabilities payload")
		return
	}
	if profile.HasData() {
		h.deviceProfiles.Put(session.Token, profile)
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleBitrateTest returns a small binary payload for clients that probe bandwidth.
func (h *PlaybackHandler) HandleBitrateTest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(make([]byte, 1024*1024))
}

// HandlePlaybackInfo negotiates media sources for a Jellyfin item.
func (h *PlaybackHandler) HandlePlaybackInfo(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	contentID, err := decodeItemID(h.codec, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}

	req, profile, err := h.parsePlaybackRequest(r, session.Token)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", "Invalid playback request")
		return
	}
	if req.UserID != "" && req.UserID != session.PseudoUserID.String() {
		writeError(w, http.StatusNotFound, "NotFound", "User not found")
		return
	}

	detail, err := h.content.GetItemDetail(r.Context(), session, contentID, nil)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	if len(detail.Versions) == 0 {
		writeError(w, http.StatusBadRequest, "PlaybackUnavailable", "Item does not have playable media")
		return
	}

	routeItemID := h.codec.EncodeStringID(EncodedIDItem, detail.ContentID)
	playSessionID := h.codec.EncodeStringID(EncodedIDPlaySession, uuidNewString())
	sources := make([]PlaybackMediaSource, 0, len(detail.Versions))
	sourceDTOs := make([]mediaSourceDTO, 0, len(detail.Versions))

	allow4KTranscode := h.allow4KVideoTranscode(r.Context())
	for _, version := range detail.Versions {
		source := h.buildPlaybackSource(routeItemID, playSessionID, version, profile, req, allow4KTranscode)
		if req.MediaSourceID != "" && source.ID != req.MediaSourceID {
			continue
		}
		sources = append(sources, source)
		dto := h.mediaSourceDTO(routeItemID, playSessionID, session.Token, source)
		// Append downloaded subtitles to the media streams.
		if h.SubtitleRepo != nil {
			downloaded, _ := h.SubtitleRepo.ListDownloadedSubtitles(r.Context(), source.Version.FileID)
			baseIndex := nextDownloadedSubtitleIndex(source.Version)
			for i, dl := range downloaded {
				streamIndex := baseIndex + i
				format := subtitleRouteFormat(string(dl.Format))
				displayTitle := downloadedSubtitleDisplayTitle(dl)
				dto.MediaStreams = append(dto.MediaStreams, mediaStreamDTO{
					Index:                  streamIndex,
					Type:                   "Subtitle",
					Codec:                  string(dl.Format),
					Language:               dl.Language,
					DisplayTitle:           displayTitle,
					Title:                  displayTitle,
					IsDefault:              false,
					IsExternal:             true,
					IsForced:               false,
					IsHearingImpaired:      dl.HearingImpaired,
					IsTextSubtitleStream:   true,
					SupportsExternalStream: true,
					DeliveryURL:            subtitleDeliveryURL(routeItemID, source.ID, streamIndex, format, session.Token, playSessionID),
					DeliveryMethod:         "External",
					Path:                   downloadedSubtitlePath(source.Version, dl),
					IsExternalURL:          boolPtr(false),
				})
			}
		}
		sourceDTOs = append(sourceDTOs, dto)
	}

	if len(sourceDTOs) == 0 {
		writeError(w, http.StatusNotFound, "NotFound", "Media source not found")
		return
	}

	h.playbackStore.Put(PlaybackSession{
		ID:                 playSessionID,
		CompatToken:        session.Token,
		ItemID:             detail.ContentID,
		RouteItemID:        routeItemID,
		UserID:             session.PseudoUserID.String(),
		InitialSeekSeconds: clampSeekSeconds(float64(req.StartTimeTicks)/10_000_000, sources),
		MediaSources:       sources,
	})

	writeJSON(w, http.StatusOK, playbackInfoResponseDTO{
		PlaySessionID: playSessionID,
		MediaSources:  sourceDTOs,
	})
}

func (h *PlaybackHandler) parsePlaybackRequest(r *http.Request, compatToken string) (playbackInfoRequest, DeviceProfile, error) {
	var req playbackInfoRequest
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return req, DeviceProfile{}, err
	}
	if len(strings.TrimSpace(string(body))) != 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return req, DeviceProfile{}, err
		}
	}
	applyPlaybackQueryOverrides(&req, r.URL.Query())

	profile := DeviceProfile{}
	if len(req.DeviceProfile) > 0 {
		if err := json.Unmarshal(req.DeviceProfile, &profile); err != nil {
			return req, DeviceProfile{}, err
		}
		if profile.HasData() {
			h.deviceProfiles.Put(compatToken, profile)
		}
	}
	if !profile.HasData() {
		if stored, ok := h.deviceProfiles.Get(compatToken); ok {
			profile = stored
		} else {
			profile = DefaultDeviceProfile()
		}
	}

	return req, profile, nil
}

func (h *PlaybackHandler) buildPlaybackSource(
	routeItemID, playSessionID string,
	version catalog.FileVersion,
	profile DeviceProfile,
	req playbackInfoRequest,
	allow4KTranscode bool,
) PlaybackMediaSource {
	sourceID := h.codec.EncodeIntID(EncodedIDMediaSource, int64(version.FileID))
	enableDirectPlay := boolDefault(req.EnableDirectPlay, true)
	enableDirectStream := boolDefault(req.EnableDirectStream, true)
	allowVideoCopy := boolDefault(req.AllowVideoStreamCopy, true)
	allowAudioCopy := boolDefault(req.AllowAudioStreamCopy, true)

	supportsDirectPlay := enableDirectPlay && profile.SupportsDirectPlay(version)
	audioSupported := profile.SupportsAudioCodecForDirectStream(version)
	videoSupported := profile.SupportsVideoCodecForDirectStream(version)
	transcodeAudio := enableDirectStream && allowVideoCopy && videoSupported && !audioSupported
	supportsDirectStream := !transcodeAudio &&
		enableDirectStream &&
		allowVideoCopy &&
		videoSupported &&
		allowAudioCopy &&
		audioSupported
	supportsTranscoding := boolDefault(req.EnableTranscoding, true) && profile.SupportsTranscoding(version)
	// Don't offer full video encodes of 4K sources when allow_4k_transcode is
	// off. Audio-only transcodes (transcodeAudio) stream-copy the video and
	// stay available.
	if supportsTranscoding && !transcodeAudio && !allow4KTranscode && is4KResolution(version.Resolution) {
		supportsTranscoding = false
	}

	audioIndex := defaultAudioStreamIndex(version)
	subtitleIndex := defaultSubtitleStreamIndex(version)
	selectedAudioIndex := audioIndex
	if req.AudioStreamIndex != nil && isValidCompatAudioStreamIndex(version, int(*req.AudioStreamIndex)) {
		selectedAudioIndex = intPtr(int(*req.AudioStreamIndex))
	}

	return PlaybackMediaSource{
		ID:                         sourceID,
		FileID:                     version.FileID,
		Version:                    version,
		SupportsDirectPlay:         supportsDirectPlay,
		SupportsDirectStream:       supportsDirectStream,
		SupportsTranscoding:        supportsTranscoding,
		TranscodeAudio:             transcodeAudio,
		DefaultAudioStreamIndex:    audioIndex,
		SelectedAudioStreamIndex:   selectedAudioIndex,
		DefaultSubtitleStreamIndex: subtitleIndex,
		ETag:                       mediaSourceETag(version),
	}
}

func (h *PlaybackHandler) mediaSourceDTO(routeItemID, playSessionID, compatToken string, source PlaybackMediaSource) mediaSourceDTO {
	selectedAudioStreamIndex := effectiveCompatAudioStreamIndex(source)
	dto := mediaSourceDTO{
		Protocol:                            "File",
		ID:                                  source.ID,
		Path:                                compatMediaPath(source.Version),
		Type:                                "Default",
		Container:                           strings.ToLower(source.Version.Container),
		Size:                                source.Version.FileSize,
		Name:                                mediaSourceName(source.Version),
		IsRemote:                            false,
		ETag:                                source.ETag,
		RunTimeTicks:                        secondsToTicks(float64(source.Version.Duration)),
		ReadAtNativeFramerate:               false,
		IgnoreDts:                           false,
		IgnoreIndex:                         false,
		GenPtsInput:                         false,
		SupportsTranscoding:                 source.SupportsTranscoding,
		SupportsDirectStream:                source.SupportsDirectStream,
		SupportsDirectPlay:                  source.SupportsDirectPlay,
		IsInfiniteStream:                    false,
		UseMostCompatibleTranscodingProfile: false,
		RequiresOpening:                     false,
		RequiresClosing:                     false,
		RequiresLooping:                     false,
		SupportsProbing:                     true,
		VideoType:                           "VideoFile",
		HasSegments:                         false,
		Formats:                             []string{strings.ToLower(source.Version.Container)},
		RequiredHTTPHeaders:                 map[string]string{},
		MediaAttachments:                    []map[string]any{},
		Bitrate:                             source.Version.Bitrate * 1000,
		DefaultAudioStreamIndex:             selectedAudioStreamIndex,
		DefaultSubtitleStreamIndex:          source.DefaultSubtitleStreamIndex,
		MediaStreams:                        buildMediaStreamsWithSelection(routeItemID, source.ID, source.Version, selectedAudioStreamIndex, compatToken, playSessionID),
	}
	if source.SupportsDirectPlay || source.SupportsDirectStream {
		dto.DirectStreamURL = fmt.Sprintf(
			"/Videos/%s/stream?static=true&mediaSourceId=%s&api_key=%s&PlaySessionId=%s",
			routeItemID,
			url.QueryEscape(source.ID),
			url.QueryEscape(compatToken),
			url.QueryEscape(playSessionID),
		)
	}
	dto.TranscodingSubProtocol = "hls"
	if source.SupportsTranscoding {
		dto.TranscodingURL = fmt.Sprintf("/Videos/%s/master.m3u8?PlaySessionId=%s&MediaSourceId=%s", routeItemID, playSessionID, source.ID)
		if source.TranscodeAudio {
			dto.TranscodingContainer = "mp4"
		} else {
			dto.TranscodingContainer = "ts"
		}
	}
	return dto
}

func buildMediaStreams(routeItemID, mediaSourceID string, version catalog.FileVersion) []mediaStreamDTO {
	return buildMediaStreamsWithSelection(routeItemID, mediaSourceID, version, nil, "", "")
}

func buildMediaStreamsWithSelection(routeItemID, mediaSourceID string, version catalog.FileVersion, selectedAudioStreamIndex *int, compatToken, playSessionID string) []mediaStreamDTO {
	streams := make([]mediaStreamDTO, 0, len(version.VideoTracks)+len(version.AudioTracks)+len(version.SubtitleTracks))
	effectiveAudioStreamIndex := selectedAudioStreamIndex
	if effectiveAudioStreamIndex != nil && !isValidCompatAudioStreamIndex(version, *effectiveAudioStreamIndex) {
		effectiveAudioStreamIndex = nil
	}

	for index, track := range version.VideoTracks {
		bitrate := track.Bitrate
		if bitrate == 0 && version.Bitrate > 0 {
			bitrate = version.Bitrate * 1000
		}
		streams = append(streams, mediaStreamDTO{
			Index:                  index,
			Type:                   "Video",
			Codec:                  strings.ToLower(track.Codec),
			TimeBase:               "1/1000",
			DisplayTitle:           firstNonEmpty(track.Title, track.Codec),
			Title:                  firstNonEmpty(track.Title, track.Codec),
			IsDefault:              index == 0,
			IsExternal:             false,
			IsForced:               false,
			IsHearingImpaired:      false,
			IsTextSubtitleStream:   false,
			SupportsExternalStream: false,
			IsInterlaced:           track.Interlaced,
			IsAVC:                  strings.EqualFold(track.Codec, "h264"),
			IsAnamorphic:           false,
			NalLengthSize:          "4",
			BitDepth:               track.BitDepth,
			RefFrames:              track.ReferenceFrames,
			Profile:                track.Profile,
			Level:                  track.Level,
			AspectRatio:            track.AspectRatio,
			VideoRange:             firstNonEmpty(track.VideoRange, "Unknown"),
			VideoRangeType:         firstNonEmpty(track.VideoRange, "Unknown"),
			ColorPrimaries:         track.ColorPrimaries,
			ColorSpace:             track.ColorSpace,
			ColorTransfer:          track.ColorTransfer,
			PixelFormat:            track.PixelFormat,
			AudioSpatialFormat:     "None",
			AverageFrameRate:       parseCompatFrameRate(track.FrameRate),
			RealFrameRate:          parseCompatFrameRate(track.FrameRate),
			ReferenceFrameRate:     parseCompatFrameRate(track.FrameRate),
			Height:                 track.Height,
			Width:                  track.Width,
			BitRate:                bitrate,
		})
	}

	for index, track := range version.AudioTracks {
		streamIndex := len(version.VideoTracks) + index
		isDefault := track.Default || (index == 0 && !anyDefaultAudioTrack(version.AudioTracks))
		if effectiveAudioStreamIndex != nil {
			isDefault = streamIndex == *effectiveAudioStreamIndex
		}
		streams = append(streams, mediaStreamDTO{
			Index:                  streamIndex,
			Type:                   "Audio",
			Codec:                  strings.ToLower(track.Codec),
			Language:               track.Language,
			TimeBase:               "1/1000",
			DisplayTitle:           firstNonEmpty(track.Title, track.EmbeddedTitle, track.Language, track.Codec),
			Title:                  firstNonEmpty(track.Title, track.EmbeddedTitle),
			IsDefault:              isDefault,
			IsExternal:             false,
			IsForced:               false,
			IsHearingImpaired:      false,
			IsTextSubtitleStream:   false,
			SupportsExternalStream: false,
			AudioSpatialFormat:     "None",
			Channels:               track.Channels,
			BitRate:                track.Bitrate,
		})
	}

	for index, track := range version.SubtitleTracks {
		if !subtitleTrackStreamable(track.Codec, track.External) {
			continue
		}
		streamIndex := subtitleTrackIndex(version, track, index)
		format := subtitleRouteFormat(track.Codec)
		displayTitle := compatSubtitleDisplayTitle(track)
		streams = append(streams, mediaStreamDTO{
			Index:                  streamIndex,
			Type:                   "Subtitle",
			Codec:                  strings.ToLower(track.Codec),
			Language:               track.Language,
			TimeBase:               "1/1000",
			DisplayTitle:           displayTitle,
			Title:                  displayTitle,
			IsDefault:              track.Default,
			IsExternal:             track.External,
			IsForced:               track.Forced,
			IsHearingImpaired:      track.HearingImpaired,
			IsTextSubtitleStream:   true,
			SupportsExternalStream: track.External,
			AudioSpatialFormat:     "None",
			DeliveryURL:            subtitleDeliveryURL(routeItemID, mediaSourceID, streamIndex, format, compatToken, playSessionID),
			DeliveryMethod:         subtitleDeliveryMethod(track.External),
			Path:                   subtitlePath(track, routeItemID, mediaSourceID, streamIndex, format),
			IsExternalURL:          subtitleExternalURL(track),
		})
	}

	return streams
}

func mediaSourceETag(version catalog.FileVersion) string {
	sum := sha1.Sum(fmt.Appendf(nil, "%d:%s:%s:%d", version.FileID, version.Container, version.CodecVideo, version.Bitrate))
	return hex.EncodeToString(sum[:8])
}

func defaultAudioStreamIndex(version catalog.FileVersion) *int {
	if len(version.AudioTracks) == 0 {
		return nil
	}
	for index, track := range version.AudioTracks {
		if track.Default {
			value := len(version.VideoTracks) + index
			return &value
		}
	}
	value := len(version.VideoTracks)
	return &value
}

func effectiveCompatAudioStreamIndex(source PlaybackMediaSource) *int {
	if source.SelectedAudioStreamIndex != nil && isValidCompatAudioStreamIndex(source.Version, *source.SelectedAudioStreamIndex) {
		return intPtr(*source.SelectedAudioStreamIndex)
	}
	if source.DefaultAudioStreamIndex != nil && isValidCompatAudioStreamIndex(source.Version, *source.DefaultAudioStreamIndex) {
		return intPtr(*source.DefaultAudioStreamIndex)
	}
	return nil
}

func compatAudioTrackIndex(source PlaybackMediaSource) (int, bool) {
	streamIndex := effectiveCompatAudioStreamIndex(source)
	if streamIndex == nil {
		return 0, false
	}
	audioTrackIndex := *streamIndex - len(source.Version.VideoTracks)
	if audioTrackIndex < 0 || audioTrackIndex >= len(source.Version.AudioTracks) {
		return 0, false
	}
	return audioTrackIndex, true
}

func compatAudioTrackIndexOrDefault(source PlaybackMediaSource) int {
	if audioTrackIndex, ok := compatAudioTrackIndex(source); ok {
		return audioTrackIndex
	}
	return 0
}

func isValidCompatAudioStreamIndex(version catalog.FileVersion, streamIndex int) bool {
	audioStart := len(version.VideoTracks)
	audioEnd := audioStart + len(version.AudioTracks)
	return streamIndex >= audioStart && streamIndex < audioEnd
}

func defaultSubtitleStreamIndex(version catalog.FileVersion) *int {
	if len(version.SubtitleTracks) == 0 {
		return nil
	}
	for index, track := range version.SubtitleTracks {
		if !subtitleTrackStreamable(track.Codec, track.External) {
			continue
		}
		if track.Default {
			value := subtitleTrackIndex(version, track, index)
			return &value
		}
	}
	return nil
}

func subtitleTrackIndex(version catalog.FileVersion, track catalog.VersionSubtitleTrack, fallback int) int {
	if track.Index > 0 {
		return track.Index
	}
	return len(version.VideoTracks) + len(version.AudioTracks) + fallback
}

func nextDownloadedSubtitleIndex(version catalog.FileVersion) int {
	maxIndex := len(version.VideoTracks) + len(version.AudioTracks) - 1
	for index, track := range version.SubtitleTracks {
		streamIndex := subtitleTrackIndex(version, track, index)
		if streamIndex > maxIndex {
			maxIndex = streamIndex
		}
	}
	return maxIndex + 1
}

func subtitleTrackStreamable(codec string, external bool) bool {
	return external || !playback.NeedsBurnIn(codec)
}

func anyDefaultAudioTrack(tracks []models.AudioTrack) bool {
	for _, track := range tracks {
		if track.Default {
			return true
		}
	}
	return false
}

func parseCompatFrameRate(raw string) float64 {
	if strings.TrimSpace(raw) == "" {
		return 0
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err == nil {
		return value
	}
	if strings.Contains(raw, "/") {
		parts := strings.SplitN(raw, "/", 2)
		if len(parts) == 2 {
			num, numErr := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
			den, denErr := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
			if numErr == nil && denErr == nil && den != 0 {
				return num / den
			}
		}
	}
	return 0
}

func compatSubtitleDisplayTitle(track catalog.VersionSubtitleTrack) string {
	base := compatLanguageName(track.Language)
	tags := make([]string, 0, 4)
	if variant := subtitleVariantLabel(track.Codec, track.Language, track.Title, track.EmbeddedTitle, track.FileName); variant != "" {
		tags = append(tags, variant)
	}
	if track.Forced {
		tags = append(tags, "Forced")
	}
	if track.HearingImpaired || subtitleHasSDH(track.Title, track.EmbeddedTitle, track.FileName) {
		tags = append(tags, "SDH")
	}
	if track.External {
		tags = append(tags, "External")
	} else {
		tags = append(tags, "Embedded")
	}
	tags = append(tags, subtitleFormatLabel(track.Codec))
	return formatSubtitleLabel(base, tags...)
}

func downloadedSubtitleDisplayTitle(sub subtitles.DownloadedSubtitle) string {
	base := compatLanguageName(sub.Language)
	tags := []string{"Downloaded"}
	if sub.HearingImpaired {
		tags = append(tags, "SDH")
	}
	tags = append(tags, subtitleFormatLabel(string(sub.Format)))
	if provider := subtitleProviderLabel(sub.Provider); provider != "" {
		tags = append(tags, provider)
	}
	return formatSubtitleLabel(base, tags...)
}

func compatLanguageName(code string) string {
	trimmed := strings.TrimSpace(code)
	if trimmed == "" {
		return "Unknown"
	}
	normalized := strings.ToLower(trimmed)
	if name, ok := compatLanguageNames[normalized]; ok {
		return name
	}
	if len(normalized) > 3 {
		return titleCaseWords(trimmed)
	}
	return strings.ToUpper(normalized)
}

func subtitleFormatLabel(codec string) string {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "", "unknown":
		return "Subtitle"
	case "srt", "subrip":
		return "SRT"
	case "ass":
		return "ASS"
	case "ssa":
		return "SSA"
	case "vtt", "webvtt":
		return "VTT"
	case "mov_text":
		return "MOV_TEXT"
	case "hdmv_pgs_subtitle", "pgs":
		return "PGS"
	case "dvd_subtitle":
		return "DVD_SUB"
	case "dvb_subtitle":
		return "DVB_SUB"
	default:
		return strings.ToUpper(strings.TrimSpace(codec))
	}
}

func subtitleVariantLabel(codec, language string, values ...string) string {
	for _, value := range values {
		candidate := strings.TrimSpace(value)
		if candidate == "" {
			continue
		}
		if subtitleHasSDH(candidate) || subtitleTitleLooksGeneric(candidate, codec, language) || subtitleTitleLooksFilename(candidate) {
			continue
		}
		return titleCaseWords(candidate)
	}
	return ""
}

func subtitleHasSDH(values ...string) bool {
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		switch {
		case normalized == "sdh",
			normalized == "cc",
			normalized == "hi",
			normalized == "hearing impaired",
			normalized == "hearing-impaired",
			normalized == "closed captions",
			normalized == "closed caption",
			strings.Contains(normalized, " sdh"),
			strings.Contains(normalized, "cc "),
			strings.Contains(normalized, "closed caption"),
			strings.Contains(normalized, "hearing impaired"):
			return true
		}
	}
	return false
}

func subtitleTitleLooksGeneric(title, codec, language string) bool {
	normalized := strings.ToLower(strings.TrimSpace(title))
	if normalized == "" {
		return true
	}
	if normalized == strings.ToLower(strings.TrimSpace(codec)) || normalized == strings.ToLower(subtitleFormatLabel(codec)) {
		return true
	}
	if normalized == strings.ToLower(strings.TrimSpace(language)) || normalized == strings.ToLower(compatLanguageName(language)) {
		return true
	}
	switch normalized {
	case "subtitle", "subtitles", "text", "subrip", "mov_text", "unknown":
		return true
	default:
		return false
	}
}

func subtitleTitleLooksFilename(title string) bool {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return false
	}
	if filepath.Ext(trimmed) != "" {
		return true
	}
	return strings.ContainsAny(trimmed, "/\\[]")
}

func subtitleProviderLabel(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "":
		return ""
	case "opensubtitles":
		return "OpenSubtitles"
	case "subsource":
		return "SubSource"
	case "subdl":
		return "SubDL"
	default:
		return titleCaseWords(provider)
	}
}

func formatSubtitleLabel(base string, tags ...string) string {
	unique := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		trimmed := strings.TrimSpace(tag)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, trimmed)
	}
	if base == "" {
		base = "Unknown"
	}
	if len(unique) == 0 {
		return base
	}
	return fmt.Sprintf("%s (%s)", base, strings.Join(unique, ", "))
}

func titleCaseWords(value string) string {
	parts := strings.FieldsFunc(strings.TrimSpace(value), func(r rune) bool {
		return r == ' ' || r == '_' || r == '-'
	})
	if len(parts) == 0 {
		return ""
	}
	for i, part := range parts {
		if part == "" {
			continue
		}
		upper := strings.ToUpper(part)
		if part == upper && len(part) <= 5 {
			parts[i] = upper
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
	}
	return strings.Join(parts, " ")
}

func subtitleDeliveryMethod(external bool) string {
	if external {
		return "External"
	}
	return "Embed"
}

func downloadedSubtitlePath(version catalog.FileVersion, sub subtitles.DownloadedSubtitle) string {
	name := strings.TrimSpace(downloadedSubtitleDisplayTitle(sub))
	if name == "" {
		name = mediaSourceName(version)
	}
	if name == "" {
		name = "subtitle"
	}
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, "\\", "-")
	filename := name + "." + subtitleRouteFormat(string(sub.Format))
	return filepath.ToSlash(filepath.Join("/silo/subtitles", filename))
}

func subtitleDeliveryURL(routeItemID, mediaSourceID string, streamIndex int, format, compatToken, playSessionID string) string {
	base := fmt.Sprintf("/Videos/%s/%s/Subtitles/%d/stream.%s", routeItemID, mediaSourceID, streamIndex, format)
	query := url.Values{}
	if compatToken != "" {
		query.Set("api_key", compatToken)
	}
	if playSessionID != "" {
		query.Set("PlaySessionId", playSessionID)
	}
	if encoded := query.Encode(); encoded != "" {
		return base + "?" + encoded
	}
	return base
}

func subtitlePath(track catalog.VersionSubtitleTrack, routeItemID, mediaSourceID string, streamIndex int, format string) string {
	if !track.External {
		return ""
	}
	return fmt.Sprintf("/Videos/%s/%s/Subtitles/%d/stream.%s", routeItemID, mediaSourceID, streamIndex, format)
}

func subtitleExternalURL(track catalog.VersionSubtitleTrack) *bool {
	if !track.External {
		return nil
	}
	return boolPtr(false)
}

func subtitleRouteFormat(codec string) string {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "ass", "ssa":
		return "ass"
	case "vtt", "webvtt":
		return "vtt"
	case "srt", "subrip":
		return "srt"
	default:
		return "vtt"
	}
}

func boolDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func boolPtr(value bool) *bool {
	return &value
}

func applyPlaybackQueryOverrides(req *playbackInfoRequest, query url.Values) {
	if value := firstQueryValue(query, "UserId"); value != "" {
		req.UserID = value
	}
	if value := firstQueryValue(query, "MediaSourceId"); value != "" {
		req.MediaSourceID = value
	}
	if value, ok := parseOptionalInt(firstQueryValue(query, "AudioStreamIndex")); ok {
		req.AudioStreamIndex = compatIntValuePtr(value)
	}
	if value, ok := parseOptionalBool(firstQueryValue(query, "EnableDirectPlay")); ok {
		req.EnableDirectPlay = &value
	}
	if value, ok := parseOptionalBool(firstQueryValue(query, "EnableDirectStream")); ok {
		req.EnableDirectStream = &value
	}
	if value, ok := parseOptionalBool(firstQueryValue(query, "EnableTranscoding")); ok {
		req.EnableTranscoding = &value
	}
	if value, ok := parseOptionalBool(firstQueryValue(query, "AllowVideoStreamCopy")); ok {
		req.AllowVideoStreamCopy = &value
	}
	if value, ok := parseOptionalBool(firstQueryValue(query, "AllowAudioStreamCopy")); ok {
		req.AllowAudioStreamCopy = &value
	}
}

func firstQueryValue(values url.Values, key string) string {
	for currentKey, entries := range values {
		if strings.EqualFold(currentKey, key) && len(entries) > 0 {
			return strings.TrimSpace(entries[0])
		}
	}
	return ""
}

func parseOptionalBool(raw string) (bool, bool) {
	if strings.TrimSpace(raw) == "" {
		return false, false
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, false
	}
	return value, true
}

func parseOptionalInt(raw string) (int, bool) {
	if strings.TrimSpace(raw) == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return value, true
}

type compatIntValue int

func (v *compatIntValue) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil
	}

	var number int
	if err := json.Unmarshal(data, &number); err == nil {
		*v = compatIntValue(number)
		return nil
	}

	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	*v = compatIntValue(parsed)
	return nil
}

func compatIntValuePtr(value int) *compatIntValue {
	v := compatIntValue(value)
	return &v
}

func (h *PlaybackHandler) playbackUnavailable(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrSessionNotFound):
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Authentication failed")
	default:
		writeCompatUpstreamError(w, err)
	}
}
