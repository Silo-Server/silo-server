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
	SubtitleStreamIndex  *compatIntValue `json:"SubtitleStreamIndex,omitempty"`
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
	BeginTransport(sessionID string) error
	EndTransport(sessionID string) error
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

// PlaybackSessionSyncer flushes the in-memory native-session snapshot into the
// shared admin live-session table (playback_sessions_sync). Without it, compat
// session starts and stops only become visible on the periodic reconciler
// tick, leaving ghost rows in the activity dashboard for several seconds.
type PlaybackSessionSyncer interface {
	SyncNow(ctx context.Context) error
}

// PlaybackHandler serves Jellyfin playback negotiation endpoints.
type PlaybackHandler struct {
	cfg                     *config.Config
	content                 ContentService
	codec                   *ResourceIDCodec
	deviceProfiles          *DeviceProfileStore
	playbackStore           CompatPlaybackStore
	sessionMgr              SessionManagerInterface
	fileResolver            FilePathResolver
	storeProvider           userstore.UserStoreProvider
	NodePlanner             nodepool.SessionPlanner
	JWTSecret               string
	profileStaler           profileStaler
	profileRefreshRequester profileRefreshRequester
	FFmpegPath              string
	HWAccel                 string
	TranscodeDir            string
	// tm is the shared transcode-session lifecycle (live map, reconstruct) — the
	// same type the native handler uses, so jellycompat gets the reconstruct cap
	// and node-affinity rule for free. The reconstruction recipe is carried in the
	// compat playback store (PlaybackSession.Recipe), since Jellyfin clients cannot
	// round-trip a native stream token.
	tm            *playback.TranscodeManager
	SubtitleRepo  subtitles.Repository  // optional; enables downloaded subtitles
	S3Client      subtitles.S3Client    // optional; for serving S3 subtitles
	S3Bucket      string                // bucket for subtitle storage
	SettingsRepo  SettingsReader        // optional; reads watched threshold setting
	SessionSyncer PlaybackSessionSyncer // optional; enables immediate session sync to shared admin view
	// RecipeNodeStore hands a remote transcode's reconstruction recipe to the
	// control-plane recipe store (Redis) so a dedicated transcode node that
	// restarts can rebuild ffmpeg from it. The node-hop token is server-minted and
	// could carry the recipe, but it is mutated in place and the client can't be
	// driven to refresh a stale token, so the node reconstructs from this
	// server-authoritative store instead (see internal/noderecipe). Optional
	// (nil disables it — integrated/no-node deployments need no handoff).
	RecipeNodeStore recipeNodePutter
}

// recipeNodePutter persists and removes a remote transcode's reconstruction
// recipe in a control-plane store keyed by upstream session id. *noderecipe.Store
// implements it. Delete is nil-safe and treats a missing key as a no-op success;
// it is called on deliberate teardown so a stopped session cannot be resurrected
// from a leaked recipe.
type recipeNodePutter interface {
	Put(ctx context.Context, sessionID string, card playback.RecipeCard) error
	Delete(ctx context.Context, sessionID string) error
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
	playbackStore CompatPlaybackStore,
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
		tm:             playback.NewTranscodeManager(),
	}
	// Wire the shared transcode manager with closures so it reads the handler's
	// (late-set) JWTSecret lazily, matching the native handler.
	h.tm.JWTSecretFn = func() string { return h.JWTSecret }
	h.tm.Config = func() playback.TranscodeRuntimeConfig {
		return playback.TranscodeRuntimeConfig{
			TranscodeDir: h.TranscodeDir,
			FFmpegPath:   h.FFmpegPath,
			HWAccel:      h.HWAccel,
		}
	}
	if reg, ok := sessionMgr.(interface {
		GetSession(string) (*playback.Session, error)
		RegisterReconstructed(*playback.Session) *playback.Session
		RegisterReconstructedWithLimits(context.Context, *playback.Session) (*playback.Session, error)
	}); ok {
		h.tm.Sessions = reg
	}
	h.tm.OnFFmpegCrash = func(ctx context.Context, sessionID string, dead *playback.TranscodeSession) {
		// ffmpeg crash: drop the dead transcode and stop the upstream native
		// session. The recipe stays in the compat store so a resume reconstructs.
		nodeURL := ""
		if h.sessionMgr != nil {
			if up, err := h.sessionMgr.GetSession(sessionID); err == nil && up != nil {
				nodeURL = up.TranscodeNodeURL
			}
		}
		// Guarded close is the authoritative gate: only tear down if the live entry
		// is still the crashed one. We must NOT stop the upstream session before this
		// check — if a successor reconstructed under the same id between ffmpeg's exit
		// and here, an early StopSession would orphan its ffmpeg (live transcode, no
		// session). Only stop the upstream session when the compare-and-delete matched
		// the dead transcode. The recipe stays in the compat store either way so a
		// resume reconstructs.
		if h.sessionMgr != nil && h.tm.CloseTranscodeSessionIf(sessionID, dead, nodeURL) {
			_ = h.sessionMgr.StopSession(sessionID)
		}
	}
	return h
}

// CleanupOrphanedTranscodes removes stale per-session transcode dirs, sparing
// those whose recipe card still exists. Delegates to the shared manager.
func (h *PlaybackHandler) CleanupOrphanedTranscodes() (int, error) {
	return h.tm.CleanupOrphanedTranscodes()
}

// buildProxyRedirectURL signs a stream token and builds the redirect URL for
// the given proxy node (the planner's pick for this session).
func (h *PlaybackHandler) buildProxyRedirectURL(
	playSessionID string,
	upstreamSessionID string,
	method string,
	file *models.MediaFile,
	source PlaybackMediaSource,
	transcodeNodeURL string,
	seekSeconds float64,
	proxyNode *nodepool.Node,
) (string, error) {
	if proxyNode == nil || h.JWTSecret == "" {
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
		DVProfile:       file.PrimaryDVProfile(),
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
	playSessionID string,
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
	// Bound the dispatch like the native path does (playback.go) — without
	// this, an unreachable transcode node hangs the compat manifest request
	// until the OS gives up on the connection.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
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

	// Mirror the byte-affecting opts sent to the node into a RecipeCard and persist
	// it for restart resilience. The node-hop token is identity-only by design (see
	// internal/noderecipe), and central serves Jellyfin clients that carry no native
	// token of their own, so without a persisted recipe a node or central restart
	// cannot rebuild ffmpeg and segment serves 404.
	opts := playback.TranscodeOpts{
		SessionID:          upstreamSessionID,
		InputPath:          reqBody.InputPath,
		SeekSeconds:        reqBody.SeekSeconds,
		StartSegmentNumber: reqBody.StartSegmentNumber,
		TargetCodecVideo:   reqBody.TargetCodecVideo,
		TargetCodecAudio:   reqBody.TargetCodecAudio,
		SegmentDuration:    reqBody.SegmentDuration,
		HWAccel:            reqBody.HWAccel,
		AudioTrackIndex:    reqBody.AudioTrackIndex,
		TotalDuration:      reqBody.TotalDuration,
	}
	if source.TranscodeAudio {
		opts.TargetCodecVideo = "copy"
	}

	if err := h.persistTranscodeRecipe(ctx, playSessionID, upstreamSessionID, opts); err != nil {
		// Roll back the already-started node ffmpeg so it isn't leaked.
		h.tm.CloseTranscodeSession(upstreamSessionID, transcodeNodeURL)
		return err
	}

	return nil
}

// persistTranscodeRecipe builds the reconstruction recipe from the upstream
// session's identity and persists it for restart resilience. It is shared by the
// local (ensureLocalTranscode) and remote (startRemoteTranscode) transcode paths
// so both stores stay in lock-step.
//
// The recipe is recorded in the compat store in the same Update that marks the
// transcode started — a failed write leaves neither set — and then best-effort
// handed to the node recipe store (Redis) for dedicated transcode nodes. The node
// URL is taken from the upstream session (bound before start on the remote path),
// so it is "" for integrated transcodes and the node-store write is skipped.
// A Jellyfin client carries no native token of its own and the node-hop token is
// deliberately identity-only (see internal/noderecipe), so the persisted recipe is
// the only way a node or central restart can rebuild ffmpeg.
//
// Returns an error only when the compat-store Update fails; the caller owns
// rolling back its (local or remote) transcode in that case. A missing upstream
// session (a start/build race, or no session manager in tests) is logged and the
// live transcode is left serving — only restart resilience is forfeited.
func (h *PlaybackHandler) persistTranscodeRecipe(
	ctx context.Context,
	playSessionID, upstreamSessionID string,
	opts playback.TranscodeOpts,
) error {
	var recipe *playback.RecipeCard
	if h.sessionMgr != nil {
		if upstream, err := h.sessionMgr.GetSession(upstreamSessionID); err == nil && upstream != nil {
			card := playback.NewRecipeCard(upstream.UserID, upstream.ProfileID, upstream.MediaFileID, upstream.TranscodeNodeURL, opts)
			recipe = &card
		}
	}
	if recipe == nil {
		slog.Warn("transcode recipe not persisted: upstream session unavailable",
			"playback_session_id", upstreamSessionID)
	}

	if err := h.playbackStore.Update(playSessionID, func(current *PlaybackSession) error {
		current.TranscodeStarted = true
		current.Recipe = recipe
		return nil
	}); err != nil {
		return fmt.Errorf("update playback session: %w", err)
	}

	// Hand the recipe to the control-plane store (Redis) so a dedicated transcode
	// node that restarts can rebuild ffmpeg from it. Bounded and best effort: a
	// stalled write must not hang the manifest request, and a failed write only
	// forfeits node-restart resilience for this session, never the start.
	if recipe != nil && recipe.TranscodeNodeURL != "" && h.RecipeNodeStore != nil {
		putCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if err := h.RecipeNodeStore.Put(putCtx, upstreamSessionID, *recipe); err != nil {
			slog.Warn("persist node transcode recipe failed", "error", err,
				"playback_session_id", upstreamSessionID, "node", recipe.TranscodeNodeURL)
		}
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
		if req.MediaSourceID != "" && !mediaSourceIDsEqual(source.ID, req.MediaSourceID) {
			continue
		}

		// Resolve the client's subtitle selection against both the
		// embedded/external tracks and any downloaded subtitles before
		// advertising the streams, so the chosen subtitle is marked default and
		// starts with playback (mirrors the audio-selection plumbing).
		var downloaded []subtitles.DownloadedSubtitle
		downloadedKnown := true
		if h.SubtitleRepo != nil {
			var listErr error
			downloaded, listErr = h.SubtitleRepo.ListDownloadedSubtitles(r.Context(), source.Version.FileID)
			if listErr != nil {
				// Don't treat a lookup failure as "no downloaded subtitles": that
				// would silently downgrade a valid downloaded selection to the
				// media default. Resolution falls back to honoring the request.
				downloaded = nil
				downloadedKnown = false
				slog.Warn("jellycompat downloaded subtitle lookup failed",
					"file_id", source.Version.FileID,
					"error", listErr,
				)
			}
		}
		var requestedSubtitleIndex *int
		if req.SubtitleStreamIndex != nil {
			requestedSubtitleIndex = intPtr(int(*req.SubtitleStreamIndex))
		}
		source.SelectedSubtitleStreamIndex = resolveSelectedSubtitleStreamIndex(source.Version, len(downloaded), downloadedKnown, requestedSubtitleIndex, source.DefaultSubtitleStreamIndex)

		sources = append(sources, source)
		dto := h.mediaSourceDTO(routeItemID, playSessionID, session.Token, source)

		// Append downloaded subtitles to the media streams, honoring the selection.
		if len(downloaded) > 0 {
			selectedSubtitleStreamIndex := effectiveCompatSubtitleStreamIndex(source)
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
					IsDefault:              selectedSubtitleStreamIndex != nil && streamIndex == *selectedSubtitleStreamIndex,
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

	audioIndex := preferredAudioStreamIndex(version, profile)
	subtitleIndex := defaultSubtitleStreamIndex(version)
	selectedAudioIndex := audioIndex
	if req.AudioStreamIndex != nil && isValidCompatAudioStreamIndex(version, int(*req.AudioStreamIndex)) {
		selectedAudioIndex = intPtr(int(*req.AudioStreamIndex))
	}

	supportsDirectPlay := enableDirectPlay && profile.SupportsDirectPlayForAudioStream(version, selectedAudioIndex)
	audioSupported := profile.SupportsAudioCodecForDirectStreamForAudioStream(version, selectedAudioIndex)
	videoSupported := profile.SupportsVideoCodecForDirectStreamForAudioStream(version, selectedAudioIndex)
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
		DefaultSubtitleStreamIndex:          effectiveCompatSubtitleStreamIndex(source),
		MediaStreams:                        buildMediaStreamsWithSelection(routeItemID, source.ID, source.Version, selectedAudioStreamIndex, source.SelectedSubtitleStreamIndex, compatToken, playSessionID),
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
	return buildMediaStreamsWithSelection(routeItemID, mediaSourceID, version, nil, nil, "", "")
}

func buildMediaStreamsWithSelection(routeItemID, mediaSourceID string, version catalog.FileVersion, selectedAudioStreamIndex, selectedSubtitleStreamIndex *int, compatToken, playSessionID string) []mediaStreamDTO {
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
			VideoRange:             compatVideoRange(track, version.HDR),
			VideoRangeType:         compatVideoRangeType(track, version.HDR),
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
			DisplayTitle:           audioTrackDisplayTitle(track),
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
		// When the client has made an explicit subtitle selection, only that
		// stream is the default. A negative selection ("subtitles off") matches
		// no stream, which correctly clears every embedded default.
		isDefault := track.Default
		if selectedSubtitleStreamIndex != nil {
			isDefault = streamIndex == *selectedSubtitleStreamIndex
		}
		streams = append(streams, mediaStreamDTO{
			Index:                  streamIndex,
			Type:                   "Subtitle",
			Codec:                  strings.ToLower(track.Codec),
			Language:               track.Language,
			TimeBase:               "1/1000",
			DisplayTitle:           displayTitle,
			Title:                  displayTitle,
			IsDefault:              isDefault,
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

// losslessPassthroughCodecs are audio codecs that require dedicated hardware
// passthrough (AV receiver or decoder chip) and cannot be decoded by
// software-only players like ExoPlayer on most Android TV devices.
var losslessPassthroughCodecs = map[string]bool{
	"truehd": true,
	"mlp":    true,
}

// compatFallbackCodecs are broadly supported audio codecs suitable for
// software decoding. Lower index = higher preference.
var compatFallbackCodecRank = map[string]int{
	"eac3":      1,
	"ac3":       2,
	"dts":       3,
	"aac":       4,
	"flac":      5,
	"opus":      6,
	"vorbis":    7,
	"mp3":       8,
	"pcm_s16le": 9,
	"pcm_s24le": 10,
}

// preferredAudioStreamIndex returns the best audio stream index for the given
// device profile. When the default audio track is a lossless passthrough codec
// (TrueHD, MLP) and the profile does not explicitly list that codec as
// supported, it selects the most compatible fallback track (same language
// preferred). This prevents Android TV and similar clients from receiving a
// TrueHD stream they cannot decode when an AC3/EAC3 fallback is present.
func preferredAudioStreamIndex(version catalog.FileVersion, profile DeviceProfile) *int {
	defaultIdx := defaultAudioStreamIndex(version)
	if defaultIdx == nil {
		return defaultIdx
	}

	defaultTrackIdx := *defaultIdx - len(version.VideoTracks)
	if defaultTrackIdx < 0 || defaultTrackIdx >= len(version.AudioTracks) {
		return defaultIdx
	}
	defaultTrack := version.AudioTracks[defaultTrackIdx]
	if !losslessPassthroughCodecs[normalizeCompatToken(defaultTrack.Codec)] {
		return defaultIdx // not a passthrough codec; keep default
	}

	// Check whether the profile explicitly lists this lossless codec as
	// supported in any DirectPlayProfile. An empty AudioCodec field is a
	// wildcard that many clients use to mean "try anything" — but for
	// lossless passthrough codecs we cannot assume the device can actually
	// decode them, so we do not treat wildcard as explicit support.
	defaultCodec := normalizeCompatToken(defaultTrack.Codec)
	for _, p := range profile.DirectPlayProfiles {
		if !matchesVideoType(p.Type) {
			continue
		}
		if strings.TrimSpace(p.AudioCodec) == "" {
			continue // wildcard — not explicit support for lossless
		}
		for part := range strings.SplitSeq(p.AudioCodec, ",") {
			if normalizeCompatToken(part) == defaultCodec {
				return defaultIdx // profile explicitly supports this lossless codec
			}
		}
	}

	// Profile does not explicitly support this lossless codec. Find the best
	// compatible fallback with the same language as the default track.
	defaultLang := strings.ToLower(strings.TrimSpace(defaultTrack.Language))

	bestIdx := -1
	bestRank := 0

	for i, track := range version.AudioTracks {
		rank, ok := compatFallbackCodecRank[normalizeCompatToken(track.Codec)]
		if !ok {
			continue
		}
		lang := strings.ToLower(strings.TrimSpace(track.Language))
		sameLang := lang == defaultLang
		// Prefer same-language tracks; within each group prefer lower rank number.
		if bestIdx == -1 {
			bestIdx = i
			bestRank = rank
			if sameLang {
				bestRank = -rank // negative signals same-language preference
			}
		} else {
			currentSameLang := bestRank < 0
			if sameLang && !currentSameLang {
				// Upgrade from cross-language to same-language.
				bestIdx = i
				bestRank = -rank
			} else if sameLang == currentSameLang {
				// Same language group — pick lower rank (higher quality).
				effectiveRank := rank
				effectiveBest := bestRank
				if sameLang {
					effectiveRank = -rank
					effectiveBest = bestRank // already negative
				}
				if effectiveRank < effectiveBest {
					bestIdx = i
					bestRank = effectiveRank
				}
			}
		}
	}

	if bestIdx >= 0 {
		idx := len(version.VideoTracks) + bestIdx
		return &idx
	}
	return defaultIdx
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

// isValidCompatSubtitleStreamIndex reports whether streamIndex addresses a
// deliverable subtitle: either a streamable embedded/external track (bitmap
// subs that require burn-in are excluded, matching buildMediaStreams) or one of
// the downloaded subtitles appended after the embedded streams.
func isValidCompatSubtitleStreamIndex(version catalog.FileVersion, downloadedCount, streamIndex int) bool {
	for index, track := range version.SubtitleTracks {
		if !subtitleTrackStreamable(track.Codec, track.External) {
			continue
		}
		if subtitleTrackIndex(version, track, index) == streamIndex {
			return true
		}
	}
	if downloadedCount > 0 {
		base := nextDownloadedSubtitleIndex(version)
		if streamIndex >= base && streamIndex < base+downloadedCount {
			return true
		}
	}
	return false
}

// resolveSelectedSubtitleStreamIndex maps a client-requested subtitle stream
// index onto the selection stored for the session. A nil request keeps the
// media default; a negative request is preserved as an explicit "subtitles off"
// (-1); a valid request is honored; an invalid request falls back to the media
// default.
//
// downloadedKnown reports whether the downloaded-subtitle list was loaded
// successfully. When it is false, an index that does not match an
// embedded/external track is honored rather than downgraded, because it may be a
// downloaded subtitle we could not enumerate — losing the user's choice on a
// transient lookup failure is worse than echoing an index whose stream is
// temporarily absent.
func resolveSelectedSubtitleStreamIndex(version catalog.FileVersion, downloadedCount int, downloadedKnown bool, requested, mediaDefault *int) *int {
	if requested == nil {
		return mediaDefault
	}
	if *requested < 0 {
		return intPtr(-1)
	}
	if isValidCompatSubtitleStreamIndex(version, downloadedCount, *requested) {
		return intPtr(*requested)
	}
	if !downloadedKnown {
		return intPtr(*requested)
	}
	return mediaDefault
}

// effectiveCompatSubtitleStreamIndex returns the subtitle stream index to
// advertise as the default for a source: the explicit selection when present
// (collapsing "subtitles off" to none), otherwise the media default.
func effectiveCompatSubtitleStreamIndex(source PlaybackMediaSource) *int {
	if source.SelectedSubtitleStreamIndex != nil {
		if *source.SelectedSubtitleStreamIndex < 0 {
			return nil
		}
		return intPtr(*source.SelectedSubtitleStreamIndex)
	}
	return source.DefaultSubtitleStreamIndex
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

func audioTrackDisplayTitle(track models.AudioTrack) string {
	lang := compatLanguageName(track.Language)
	codec := audioCodecDisplayName(track.Codec)
	channels := audioChannelsDisplayName(track.Channels)
	title := strings.TrimSpace(codec + " " + channels)
	if lang != "" {
		title = lang + " - " + title
	}
	// Append embedded title only when it adds info beyond codec/channels (e.g. "Commentary").
	embedded := strings.TrimSpace(firstNonEmpty(track.Title, track.EmbeddedTitle))
	if !isGenericAudioLabel(embedded) {
		title += " - " + embedded
	}
	return title
}

func audioCodecDisplayName(codec string) string {
	switch normalizeCompatToken(codec) {
	case "truehd":
		return "TrueHD"
	case "mlp":
		return "MLP"
	case "ac3":
		return "AC3"
	case "eac3":
		return "EAC3"
	case "dts":
		return "DTS"
	case "dtshd", "dtshd_ma":
		return "DTS-HD MA"
	case "aac":
		return "AAC"
	case "mp3":
		return "MP3"
	case "flac":
		return "FLAC"
	case "opus":
		return "Opus"
	case "vorbis":
		return "Vorbis"
	case "pcms16le", "pcms24le", "pcms32le", "pcmf32le":
		return "PCM"
	default:
		return strings.ToUpper(strings.TrimSpace(codec))
	}
}

func audioChannelsDisplayName(channels int) string {
	switch channels {
	case 1:
		return "Mono"
	case 2:
		return "Stereo"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	default:
		if channels > 0 {
			return fmt.Sprintf("%d ch", channels)
		}
		return ""
	}
}

func isGenericAudioLabel(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "stereo", "mono", "5.1", "7.1", "surround", "5.1 surround", "7.1 surround":
		return true
	}
	return false
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
	if value, ok := parseOptionalInt(firstQueryValue(query, "SubtitleStreamIndex")); ok {
		req.SubtitleStreamIndex = compatIntValuePtr(value)
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
