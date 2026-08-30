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
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

// Tests override this to exercise indeterminate remote-start failures without
// waiting through the production cold-probe budget.
var compatRemoteTranscodeStartTimeout time.Duration

const (
	compatRemoteNodeProbeFallbackTimeout = 2 * time.Minute
	compatToneMapNegotiationTimeout      = 5 * time.Second
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

// compatCapabilitySessionPlanner restricts session placement with a lock-safe
// node predicate when Jellyfin-compatible playback needs tone mapping.
type compatCapabilitySessionPlanner interface {
	PlanSessionWith(sessionID, currentTranscodeURL string, needsTranscode bool, estBitrateKbps int, eligible func(*nodepool.Node) bool) nodepool.Plan
}

// compatTranscodeNodeEnumerator lists the enabled transcode pool for capability
// discovery before a Jellyfin-compatible session is placed.
type compatTranscodeNodeEnumerator interface {
	TranscodeNodeURLs() []string
}

// compatProxyNodeEnumerator lists enabled proxy nodes so a proxy-executed
// remux can be restricted to binaries advertising its frozen recipe version.
type compatProxyNodeEnumerator interface {
	ProxyNodeURLs() []string
}

type compatSessionReservationReleaser interface {
	ReleaseSession(sessionID string)
}

// compatTranscodeNodeHealth reports whether a pooled transcode node is
// currently healthy and enabled. Remote-start adoption is gated on it so a
// recipe another API server published is only adopted while its node still
// serves; *nodepool.Planner implements it.
type compatTranscodeNodeHealth interface {
	TranscodeNodeHealthy(nodeURL string) bool
}

// compatTranscodeNodeLookup resolves the pooled record behind a transcode node
// URL, which carries that node's own acceleration override. Optional, like the
// enumerators above: without it dispatch falls back to the cluster-wide
// acceleration setting. *nodepool.Planner implements it.
type compatTranscodeNodeLookup interface {
	TranscodeNodeByURL(nodeURL string) (*nodepool.Node, bool)
}

// compatProxyNodeLookup resolves the pooled record behind a proxy node URL, so
// capability-budget pricing can read a proxy's stored report and overrides —
// the transcode lookup above answers nothing for a proxy URL, and pricing
// proxies from the cluster policy alone undersizes a sweep whose slowest
// member is a cold proxy. Optional for the same reason. *nodepool.Planner
// implements it.
type compatProxyNodeLookup interface {
	ProxyNodeByURL(nodeURL string) (*nodepool.Node, bool)
}

// transcodeStreamDetailsSetter is implemented by the native SessionManager.
// Optional (like sessionStarterContext) so lightweight test fakes don't have
// to; without it the session keeps transport-level defaults only.
type transcodeStreamDetailsSetter interface {
	SetTranscodeStreamDetails(sessionID, targetVideoCodec, targetAudioCodec string, transcodeAudio bool, hwAccel string, toneMapMode tonemap.Mode) error
}

// recordTranscodeStreamDetails mirrors the encode decisions of a started
// transcode onto the upstream session. StartSession only records the transport
// method (play_method "transcode", transcodeAudio false), so without this an
// audio-only re-encode — video copied — syncs to session sync and the admin
// activity views as a full video transcode. Shared by the local
// (ensureTranscodeSession) and remote (startRemoteTranscode) paths.
func (h *PlaybackHandler) recordTranscodeStreamDetails(ctx context.Context, upstreamSessionID string, opts playback.TranscodeOpts) {
	setter, ok := h.sessionMgr.(transcodeStreamDetailsSetter)
	if !ok {
		return
	}
	transcodeAudio := playback.TranscodesAudio(opts.TargetCodecAudio)
	if err := setter.SetTranscodeStreamDetails(upstreamSessionID, opts.TargetCodecVideo, opts.TargetCodecAudio, transcodeAudio, opts.HWAccel, opts.ToneMapMode); err != nil {
		slog.WarnContext(ctx, "record transcode stream details failed", "component", "jellycompat",
			"error", err, "playback_session_id", upstreamSessionID)
		return
	}
	// The "compat_start" sync inside ensureUpstreamPlayback already flushed
	// this session with transport-level defaults, so flush again now that the
	// real encode decisions are on it — otherwise the admin view shows a
	// video-copy stream as a full video transcode until the next reconciler
	// tick.
	h.syncSessionsNow(ctx, "compat_transcode_details")
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

// PlaybackWatchScrobbler forwards a playback lifecycle to connected watch
// providers. The watchsync service implements this interface.
type PlaybackWatchScrobbler interface {
	ScrobbleStart(ctx context.Context, event watchsync.ScrobbleEvent) error
	ScrobblePause(ctx context.Context, event watchsync.ScrobbleEvent) error
	ScrobbleStop(ctx context.Context, event watchsync.ScrobbleEvent) error
}

// PlaybackWatchStopConfirmer is implemented by watch-sync services that can
// wait for a terminal stop to be accepted by every provider. Jellycompat uses
// it before deleting its durable terminal record.
type PlaybackWatchStopConfirmer interface {
	ScrobbleStopConfirmed(ctx context.Context, event watchsync.ScrobbleEvent) error
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
	tm                     *playback.TranscodeManager
	SubtitleRepo           subtitles.Repository  // optional; enables downloaded subtitles
	S3Client               subtitles.S3Client    // optional; for serving S3 subtitles
	S3Bucket               string                // bucket for subtitle storage
	SettingsRepo           SettingsReader        // optional; reads watched threshold setting
	SessionSyncer          PlaybackSessionSyncer // optional; enables immediate session sync to shared admin view
	WatchScrobbler         PlaybackWatchScrobbler
	StableIdentityResolver watchsync.ScrobbleIdentityResolver
	terminalFallbackDelay  time.Duration
	// RecipeNodeStore hands a remote transcode's reconstruction recipe to the
	// control-plane recipe store (Redis) so a dedicated transcode node that
	// restarts can rebuild ffmpeg from it. The node-hop token is server-minted and
	// could carry the recipe, but it is mutated in place and the client can't be
	// driven to refresh a stale token, so the node reconstructs from this
	// server-authoritative store instead (see internal/noderecipe). Optional
	// (nil disables it — integrated/no-node deployments need no handoff).
	RecipeNodeStore          recipeNodePutter
	compatToneMapProbe       func(context.Context, string, string, string) (tonemap.Capabilities, error)
	compatAudioRegistryMu    sync.Mutex
	compatAudioRegistry      *playback.TransformationRegistryV3
	compatAudioRegistryPath  string
	compatAudioRegistryProbe func(context.Context, string, tonemap.Capabilities) (*playback.TransformationRegistryV3, error)
	// compatLocalTranscodeReady is a test seam invoked after manifest readiness
	// and before lifecycle-locked publication. Production leaves it nil.
	compatLocalTranscodeReady func(*playback.TranscodeSession)
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
var errHDRTranscodeUnsupported = errors.New("HDR video transcode requires an enabled validated tone-map executor")
var errToneMapCapabilityUnavailable = errors.New("tone-map capability discovery is temporarily unavailable")
var errRemoteSoftwareToneMapStartFailed = errors.New("remote software tone-map start failed")
var errRemoteStartAdoptedLocal = errors.New("remote start superseded by local transcode")
var errRemoteStartAdoptedRemote = errors.New("remote start adopted an already-published remote transcode")

type remoteStartAdoptedRemoteError struct {
	nodeURL string
}

func (e *remoteStartAdoptedRemoteError) Error() string { return errRemoteStartAdoptedRemote.Error() }
func (e *remoteStartAdoptedRemoteError) Unwrap() error { return errRemoteStartAdoptedRemote }

// compatToneMapRecipe freezes the safe source classification and executor facts
// that Jellyfin clients cannot round-trip in their protocol.
type compatToneMapRecipe struct {
	policy              tonemap.Policy
	mode                tonemap.Mode
	sourceKind          tonemap.SourceKind
	filter              string
	recipeVersion       string
	hwAccel             string
	preflightRequired   bool
	sourceRevision      tonemap.SourceRevision
	dvConfigPresent     bool
	dvBLCompatIDPresent bool
	dvBLPresent         bool
	dvRPUPresent        bool
}

// downgradeToSoftwareToneMap replaces a failed hardware selection with a
// validated software executor only when the frozen policy permits the change.
func downgradeToSoftwareToneMap(
	policy tonemap.Policy,
	mode *tonemap.Mode,
	filter, hwAccel *string,
	kind tonemap.SourceKind,
	capabilities tonemap.Capabilities,
) bool {
	if mode == nil || filter == nil || hwAccel == nil || *mode != tonemap.ModeHardware ||
		!policy.Allows(tonemap.ModeSoftware) || !capabilities.Supports(tonemap.ModeSoftware, kind) {
		return false
	}
	*mode = tonemap.ModeSoftware
	*filter = capabilities.FilterFor(tonemap.ModeSoftware, kind)
	*hwAccel = playback.HWAccelNone
	return true
}

// requireCompatToneMapMode narrows an already validated recipe without
// widening policy. Software failover must stay software even when the next
// executor also advertises hardware, or it can repeat the failure class that
// triggered the replan.
func requireCompatToneMapMode(recipe *compatToneMapRecipe, capabilities tonemap.Capabilities, required tonemap.Mode) error {
	if required == "" {
		return nil
	}
	if recipe == nil || recipe.mode == "" {
		return errHDRTranscodeUnsupported
	}
	if recipe.mode == required {
		return nil
	}
	if required == tonemap.ModeSoftware && downgradeToSoftwareToneMap(
		recipe.policy, &recipe.mode, &recipe.filter, &recipe.hwAccel,
		recipe.sourceKind, capabilities,
	) {
		return nil
	}
	return errHDRTranscodeUnsupported
}

// apply copies a non-empty compatibility recipe into transcode options without
// disturbing ordinary SDR or source-preserving requests.
func (r compatToneMapRecipe) apply(opts *playback.TranscodeOpts) {
	if opts == nil || r.mode == "" {
		return
	}
	opts.ToneMapPolicy = r.policy
	opts.ToneMapMode = r.mode
	opts.ToneMapSourceKind = r.sourceKind
	opts.ToneMapFilter = r.filter
	opts.ToneMapRecipeVersion = r.recipeVersion
	opts.ToneMapPreflightRequired = r.preflightRequired
	opts.ToneMapSourceRevision = r.sourceRevision
	opts.ToneMapDVConfigPresent = r.dvConfigPresent
	opts.ToneMapDVBLCompatIDPresent = r.dvBLCompatIDPresent
	opts.ToneMapDVBLPresent = r.dvBLPresent
	opts.ToneMapDVRPUPresent = r.dvRPUPresent
	opts.HWAccel = r.hwAccel
}

// toneMapPolicy reads the independent hardware and software settings used by
// Jellyfin-compatible transcode negotiation.
func (h *PlaybackHandler) toneMapPolicy(ctx context.Context) tonemap.Policy {
	policy, _ := h.toneMapPolicyResult(ctx)
	return policy
}

func (h *PlaybackHandler) toneMapPolicyResult(ctx context.Context) (tonemap.Policy, error) {
	if h.SettingsRepo == nil {
		return tonemap.PolicyNone, nil
	}
	hardware, err := h.SettingsRepo.Get(ctx, config.PlaybackTranscodeHardwareToneMapSettingKey)
	if err != nil {
		return tonemap.PolicyNone, fmt.Errorf("load hardware tone-map setting: %w", err)
	}
	software, err := h.SettingsRepo.Get(ctx, config.PlaybackTranscodeSoftwareToneMapSettingKey)
	if err != nil {
		return tonemap.PolicyNone, fmt.Errorf("load software tone-map setting: %w", err)
	}
	return tonemap.NewPolicy(strings.EqualFold(hardware, "true"), strings.EqualFold(software, "true")), nil
}

// resolveCompatToneMapRecipe classifies an HDR source and freezes the preferred
// enabled executor from the supplied validated capability inventory.
func (h *PlaybackHandler) resolveCompatToneMapRecipe(ctx context.Context, file *models.MediaFile, capabilities tonemap.Capabilities) (compatToneMapRecipe, error) {
	policy, err := h.toneMapPolicyResult(ctx)
	if err != nil {
		return compatToneMapRecipe{}, fmt.Errorf("%w: %w", errToneMapCapabilityUnavailable, err)
	}
	return resolveCompatToneMapRecipeWithPolicy(file, capabilities, policy)
}

func resolveCompatToneMapRecipeWithPolicy(file *models.MediaFile, capabilities tonemap.Capabilities, policy tonemap.Policy) (compatToneMapRecipe, error) {
	metadata := tonemap.MetadataForFile(file)
	if metadata.DynamicRange == "" || metadata.DynamicRange == playback.DynamicRangeSDRV3 {
		return compatToneMapRecipe{}, nil
	}
	resolution := tonemap.ResolveSource(metadata)
	kind := resolution.Kind
	mode := capabilities.PreferredMode(policy, kind)
	if kind == "" || mode == "" {
		return compatToneMapRecipe{}, errHDRTranscodeUnsupported
	}
	hwAccel := playback.HWAccelNone
	if mode == tonemap.ModeHardware {
		hwAccel = capabilities.BackendFor(mode, kind)
	}
	return compatToneMapRecipe{
		policy: policy, mode: mode, sourceKind: kind,
		filter:            capabilities.FilterFor(mode, kind),
		recipeVersion:     playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		hwAccel:           hwAccel,
		preflightRequired: resolution.PreflightRequired,
		sourceRevision:    tonemap.RevisionForFile(file),
		dvConfigPresent:   metadata.DVConfigPresent, dvBLCompatIDPresent: metadata.DVBLCompatIDPresent,
		dvBLPresent: metadata.DVBLPresent, dvRPUPresent: metadata.DVRPUPresent,
	}, nil
}

// localToneMapCapabilities probes the API host's live FFmpeg backend and device.
func (h *PlaybackHandler) localToneMapCapabilities(ctx context.Context) (tonemap.Capabilities, error) {
	hwDevice := ""
	if h.cfg != nil {
		hwDevice = h.cfg.Playback.HWDevice
	}
	backend := playback.ResolveHWAccelWithFFmpegContext(ctx, h.HWAccel, h.FFmpegPath, hwDevice)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	probe := tonemap.Probe
	if h.compatToneMapProbe != nil {
		probe = h.compatToneMapProbe
	}
	return probe(ctx, playback.ResolveFFmpegPath(h.FFmpegPath), backend, hwDevice)
}

// remoteToneMapCapabilities retrieves one transcode node's authenticated,
// smoke-tested executor inventory under a bounded request deadline.
func (h *PlaybackHandler) remoteToneMapCapabilities(ctx context.Context, nodeURL string) (tonemap.Capabilities, error) {
	info, err := h.remoteToneMapCapabilityInfo(ctx, nodeURL)
	return info.ToneMapCapabilities, err
}

func (h *PlaybackHandler) remoteToneMapCapabilityInfo(ctx context.Context, nodeURL string) (playback.HWAccelInfo, error) {
	info, status, err := transcodenode.FetchHWCapabilities(ctx, http.DefaultClient, nodeURL, h.JWTSecret)
	if err != nil {
		return playback.HWAccelInfo{}, err
	}
	if status != http.StatusOK {
		return playback.HWAccelInfo{}, fmt.Errorf("transcode node returned %d", status)
	}
	return info, nil
}

// compatAudioBoostNodeURLs returns only nodes that advertise the exact
// audio-to-AAC recipe which understands SourceAudioChannels. Older nodes can
// still transcode ordinary audio, but sending them a surround-to-stereo recipe
// would silently lose the level correction because they ignore the new field.
func (h *PlaybackHandler) compatAudioBoostNodeURLs(ctx context.Context, timeout time.Duration) (map[string]struct{}, error) {
	enumerator, ok := h.NodePlanner.(compatTranscodeNodeEnumerator)
	if !ok {
		return map[string]struct{}{}, nil
	}
	return h.compatAudioBoostNodeURLsFor(ctx, enumerator.TranscodeNodeURLs(), timeout)
}

func (h *PlaybackHandler) compatAudioBoostProxyNodeURLs(ctx context.Context, timeout time.Duration) (map[string]struct{}, error) {
	enumerator, ok := h.NodePlanner.(compatProxyNodeEnumerator)
	if !ok {
		return map[string]struct{}{}, nil
	}
	return h.compatAudioBoostNodeURLsFor(ctx, enumerator.ProxyNodeURLs(), timeout)
}

func (h *PlaybackHandler) compatAudioBoostNodeURLsFor(ctx context.Context, nodeURLs []string, timeout time.Duration) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type capabilityResult struct {
		info playback.HWAccelInfo
		err  error
	}
	results := make([]capabilityResult, len(nodeURLs))
	var wg sync.WaitGroup
	for i, nodeURL := range nodeURLs {
		wg.Add(1)
		go func(i int, nodeURL string) {
			defer wg.Done()
			results[i].info, results[i].err = h.remoteToneMapCapabilityInfo(fetchCtx, nodeURL)
		}(i, nodeURL)
	}
	wg.Wait()

	var probeErr error
	for i, capability := range results {
		if capability.err != nil {
			probeErr = errors.Join(probeErr, capability.err)
			continue
		}
		if compatSupportsAudioBoost(capability.info.Transformations) {
			result[strings.TrimRight(nodeURLs[i], "/")] = struct{}{}
		}
	}
	return result, probeErr
}

// planCompatProxySession keeps surround-to-stereo remux execution on proxies
// that advertise audio_to_aac v2. A missing inventory or selector deliberately
// returns no proxy so HandleVideoStream uses its integrated fallback.
func (h *PlaybackHandler) planCompatProxySession(ctx context.Context, sessionID string, bitrateKbps int, requiresAudioBoost bool) nodepool.Plan {
	if h.NodePlanner == nil {
		return nodepool.Plan{}
	}
	if !requiresAudioBoost {
		return h.NodePlanner.PlanSession(sessionID, "", false, bitrateKbps)
	}
	selector, selectable := h.NodePlanner.(compatCapabilitySessionPlanner)
	_, enumerable := h.NodePlanner.(compatProxyNodeEnumerator)
	if !selectable || !enumerable {
		return nodepool.Plan{}
	}
	capable, _ := h.compatAudioBoostProxyNodeURLs(ctx, h.toneMapCapabilityTimeout())
	return selector.PlanSessionWith(sessionID, "", false, bitrateKbps, func(node *nodepool.Node) bool {
		if node == nil {
			return false
		}
		_, supported := capable[strings.TrimRight(node.URL, "/")]
		return supported
	})
}

func compatSupportsAudioBoost(transformations []playback.TransformationV3) bool {
	for _, transformation := range transformations {
		if strings.EqualFold(strings.TrimSpace(transformation.Name), playback.TransformationAudioToAACV3) &&
			strings.EqualFold(strings.TrimSpace(transformation.Executor), playback.ExecutorServerV3) &&
			strings.TrimSpace(transformation.RecipeVersion) == playback.TransformationAudioToAACRecipeVersionV3 {
			return true
		}
	}
	return false
}

// toneMapCapabilityTimeout bounds one capability sweep. Every caller wraps a
// single deadline around concurrent per-node fetches (plus the local probe),
// so the budget has to cover the slowest node in the fan-out, not a typical
// one: each pooled node is priced the way the v3 and download paths price a
// cold read — ColdCapabilityRequestTimeout over its stored report and its
// effective override — and the sweep takes the maximum, with the fixed
// fallback as the floor for the local probe and for nodes this process cannot
// resolve. Without the derivation, a node with enough configured devices to
// out-price two minutes was canceled while still inside its own advertised
// probe budget, and HDR or audio-boost planning excluded it for no fault.
func (h *PlaybackHandler) toneMapCapabilityTimeout() time.Duration {
	hwDevice := ""
	if h.cfg != nil {
		hwDevice = h.cfg.Playback.HWDevice
	}
	// The local probe runs under the same deadline, priced by the cluster
	// policy; this is also the whole answer when no pool is reachable.
	budget := playback.ColdCapabilityRequestTimeout(nil, h.HWAccel, hwDevice, compatRemoteNodeProbeFallbackTimeout)

	// A URL its lookup cannot resolve — a record that left the pool, or a
	// planner without the lookup at all — prices as a nil node: cluster policy
	// over the fallback, which is the pre-derivation behavior.
	price := func(node *nodepool.Node) {
		cold := playback.ColdCapabilityRequestTimeout(
			node.StoredCapabilities(),
			node.EffectiveHWAccel(h.HWAccel),
			node.EffectiveHWDevice(hwDevice),
			compatRemoteNodeProbeFallbackTimeout,
		)
		if cold > budget {
			budget = cold
		}
	}
	if enumerator, ok := h.NodePlanner.(compatTranscodeNodeEnumerator); ok {
		lookup, canLookup := h.NodePlanner.(compatTranscodeNodeLookup)
		for _, nodeURL := range enumerator.TranscodeNodeURLs() {
			var node *nodepool.Node
			if canLookup {
				node, _ = lookup.TranscodeNodeByURL(nodeURL)
			}
			price(node)
		}
	}
	// Proxy nodes answer the audio-boost recipe sweep under this same
	// deadline, resolved through their own pool: the transcode lookup answers
	// nothing for a proxy URL, and a cold proxy whose report or override
	// out-prices the fallback would otherwise be canceled inside its own
	// budget and dropped from planning.
	if enumerator, ok := h.NodePlanner.(compatProxyNodeEnumerator); ok {
		lookup, canLookup := h.NodePlanner.(compatProxyNodeLookup)
		for _, nodeURL := range enumerator.ProxyNodeURLs() {
			var node *nodepool.Node
			if canLookup {
				node, _ = lookup.ProxyNodeByURL(nodeURL)
			}
			price(node)
		}
	}
	return budget
}

func (h *PlaybackHandler) remoteTranscodeStartTimeout(request transcodenode.TranscodeStartRequest, nodeProbeTimeoutMillis int64) time.Duration {
	if compatRemoteTranscodeStartTimeout > 0 {
		return compatRemoteTranscodeStartTimeout
	}
	if request.ToneMapMode == "" {
		return 20 * time.Second
	}
	timeout := playback.NormalizeProbeRequestTimeout(nodeProbeTimeoutMillis, h.toneMapCapabilityTimeout()) + playback.ManifestStartupTimeout
	if request.ToneMapPreflightRequired {
		timeout += tonemap.SourcePreflightTimeout(request.TotalDuration)
	}
	if request.RequireReady {
		timeout += transcodenode.TranscodeStartReadinessTimeout
	}
	return timeout
}

// availableCompatToneMapCapabilities returns the union visible to media-source
// negotiation, including local execution only when fallback is allowed.
func (h *PlaybackHandler) availableCompatToneMapCapabilities(ctx context.Context, timeout time.Duration) (tonemap.Capabilities, error) {
	capabilities, _, err := h.compatToneMapCapabilityInventory(ctx, timeout)
	return capabilities, err
}

// compatToneMapCapabilityInventory returns both a planning union and per-node
// records so heterogeneous pools can be placed without losing executor identity.
func (h *PlaybackHandler) compatToneMapCapabilityInventory(ctx context.Context, timeout time.Duration) (tonemap.Capabilities, map[string]tonemap.Capabilities, error) {
	capabilities := make(tonemap.Capabilities, 0, 4)
	byNode := make(map[string]tonemap.Capabilities)
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type capabilityResult struct {
		capabilities tonemap.Capabilities
		err          error
	}
	localAllowed := h.NodePlanner == nil || nodepool.LocalTranscodeFallbackAllowed(ctx, h.SettingsRepo)
	var localResult capabilityResult
	var localWG sync.WaitGroup
	if localAllowed {
		localWG.Add(1)
		go func() {
			defer localWG.Done()
			localResult.capabilities, localResult.err = h.localToneMapCapabilities(fetchCtx)
		}()
	}

	var remoteResults []capabilityResult
	var nodeURLs []string
	if enumerator, ok := h.NodePlanner.(compatTranscodeNodeEnumerator); ok {
		nodeURLs = enumerator.TranscodeNodeURLs()
		remoteResults = make([]capabilityResult, len(nodeURLs))
		var wg sync.WaitGroup
		for i, nodeURL := range nodeURLs {
			wg.Add(1)
			go func(i int, nodeURL string) {
				defer wg.Done()
				remoteResults[i].capabilities, remoteResults[i].err = h.remoteToneMapCapabilities(fetchCtx, nodeURL)
			}(i, nodeURL)
		}
		wg.Wait()
	}
	localWG.Wait()

	var probeErr error
	for i, result := range remoteResults {
		if result.err != nil {
			probeErr = errors.Join(probeErr, result.err)
			continue
		}
		byNode[strings.TrimRight(nodeURLs[i], "/")] = result.capabilities
		capabilities = append(capabilities, result.capabilities...)
	}
	if localAllowed {
		if localResult.err != nil {
			probeErr = errors.Join(probeErr, localResult.err)
		} else {
			capabilities = append(capabilities, localResult.capabilities...)
		}
	}
	return capabilities, byNode, probeErr
}

// applyCompatToneMapAvailability suppresses unsafe HDR video transcodes from a
// media source when no enabled validated executor can run them.
func (h *PlaybackHandler) applyCompatToneMapAvailability(ctx context.Context, source PlaybackMediaSource, capabilities tonemap.Capabilities) PlaybackMediaSource {
	return applyCompatToneMapAvailabilityWithPolicy(source, capabilities, h.toneMapPolicy(ctx))
}

func applyCompatToneMapAvailabilityWithPolicy(source PlaybackMediaSource, capabilities tonemap.Capabilities, policy tonemap.Policy) PlaybackMediaSource {
	if !source.SupportsTranscoding || compatHLSCopiesVideo(source) {
		return source
	}
	file := &models.MediaFile{
		ID:          source.Version.FileID,
		HDR:         source.Version.HDR,
		VideoTracks: source.Version.VideoTracks,
	}
	metadata := tonemap.MetadataForFile(file)
	if metadata.DynamicRange == "" || metadata.DynamicRange == playback.DynamicRangeSDRV3 {
		return source
	}
	if _, err := resolveCompatToneMapRecipeWithPolicy(file, capabilities, policy); err != nil {
		source.SupportsTranscoding = false
	}
	return source
}

// compatVersionRequiresToneMap reports whether transcoding the catalog version's
// video would require an HDR-to-SDR executor.
func compatVersionRequiresToneMap(version catalog.FileVersion) bool {
	metadata := tonemap.MetadataForFile(&models.MediaFile{HDR: version.HDR, VideoTracks: version.VideoTracks})
	return metadata.DynamicRange != "" && metadata.DynamicRange != playback.DynamicRangeSDRV3
}

// planCompatTranscodeSession restricts an HDR video transcode to nodes that
// support the preferred policy mode and resolved source kind.
func (h *PlaybackHandler) planCompatTranscodeSession(ctx context.Context, session *playback.Session, file *models.MediaFile, bitrateKbps int, videoTranscode bool, sourceAudioChannels int) (nodepool.Plan, error) {
	if h.NodePlanner == nil || session == nil {
		return nodepool.Plan{}, nil
	}
	metadata := tonemap.MetadataForFile(file)
	requiresToneMap := videoTranscode && metadata.DynamicRange != "" && metadata.DynamicRange != playback.DynamicRangeSDRV3
	requiresAudioBoost := sourceAudioChannels > 2
	if !requiresToneMap && !requiresAudioBoost {
		return h.NodePlanner.PlanSession(session.ID, session.TranscodeNodeURL, true, bitrateKbps), nil
	}
	selector, selectable := h.NodePlanner.(compatCapabilitySessionPlanner)
	_, enumerable := h.NodePlanner.(compatTranscodeNodeEnumerator)
	if !selectable || !enumerable {
		if requiresAudioBoost {
			// A planner that cannot expose and filter its candidates cannot prove a
			// mixed-version node understands the byte-affecting recipe. Leave the
			// work local instead of silently producing the legacy low-level mix.
			return nodepool.Plan{}, nil
		}
		return h.NodePlanner.PlanSession(session.ID, session.TranscodeNodeURL, true, bitrateKbps), nil
	}
	audioBoostNodes := map[string]struct{}{}
	if requiresAudioBoost {
		audioBoostNodes, _ = h.compatAudioBoostNodeURLs(ctx, h.toneMapCapabilityTimeout())
	}
	audioEligible := func(node *nodepool.Node) bool {
		if node == nil {
			return false
		}
		if !requiresAudioBoost {
			return true
		}
		_, ok := audioBoostNodes[strings.TrimRight(node.URL, "/")]
		return ok
	}
	if !requiresToneMap {
		return selector.PlanSessionWith(session.ID, session.TranscodeNodeURL, true, bitrateKbps, audioEligible), nil
	}
	policy, err := h.toneMapPolicyResult(ctx)
	if err != nil {
		return nodepool.Plan{}, fmt.Errorf("%w: %w", errToneMapCapabilityUnavailable, err)
	}
	kind := tonemap.ResolveSource(metadata).Kind
	if kind == "" || policy == tonemap.PolicyNone {
		return nodepool.Plan{}, errHDRTranscodeUnsupported
	}
	available, nodeCapabilities, capabilityErr := h.compatToneMapCapabilityInventory(ctx, h.toneMapCapabilityTimeout())
	preferredMode := available.PreferredMode(policy, kind)
	if preferredMode == "" {
		if capabilityErr != nil {
			return nodepool.Plan{}, fmt.Errorf("%w: %w", errToneMapCapabilityUnavailable, capabilityErr)
		}
		return nodepool.Plan{}, errHDRTranscodeUnsupported
	}
	modes := []tonemap.Mode{preferredMode}
	if preferredMode == tonemap.ModeHardware && policy.Allows(tonemap.ModeSoftware) && available.Supports(tonemap.ModeSoftware, kind) {
		modes = append(modes, tonemap.ModeSoftware)
	}
	for _, mode := range modes {
		plan := selector.PlanSessionWith(session.ID, session.TranscodeNodeURL, true, bitrateKbps, func(node *nodepool.Node) bool {
			return audioEligible(node) && nodeCapabilities[strings.TrimRight(node.URL, "/")].Supports(mode, kind)
		})
		if plan.TranscodeNode != nil {
			return plan, nil
		}
	}
	return nodepool.Plan{}, nil
}

// planCompatSoftwareToneMapSession replans a failed hardware-then-software
// start onto a different node with a validated software executor. Failed node
// URLs are excluded so retries make forward progress instead of following
// session affinity back to an executor that already rejected the recipe.
func (h *PlaybackHandler) planCompatSoftwareToneMapSession(
	ctx context.Context,
	session *playback.Session,
	file *models.MediaFile,
	bitrateKbps int,
	sourceAudioChannels int,
	excluded map[string]struct{},
) (nodepool.Plan, error) {
	if h.NodePlanner == nil || session == nil {
		return nodepool.Plan{}, nil
	}
	policy, err := h.toneMapPolicyResult(ctx)
	if err != nil {
		return nodepool.Plan{}, fmt.Errorf("%w: %w", errToneMapCapabilityUnavailable, err)
	}
	kind := tonemap.ResolveSource(tonemap.MetadataForFile(file)).Kind
	if kind == "" || !policy.Allows(tonemap.ModeSoftware) {
		return nodepool.Plan{}, errHDRTranscodeUnsupported
	}
	selector, selectable := h.NodePlanner.(compatCapabilitySessionPlanner)
	_, enumerable := h.NodePlanner.(compatTranscodeNodeEnumerator)
	if !selectable || !enumerable {
		return nodepool.Plan{}, nil
	}
	audioBoostNodes := map[string]struct{}{}
	if sourceAudioChannels > 2 {
		audioBoostNodes, _ = h.compatAudioBoostNodeURLs(ctx, h.toneMapCapabilityTimeout())
	}
	available, nodeCapabilities, capabilityErr := h.compatToneMapCapabilityInventory(ctx, h.toneMapCapabilityTimeout())
	if !available.Supports(tonemap.ModeSoftware, kind) && capabilityErr != nil {
		return nodepool.Plan{}, fmt.Errorf("%w: %w", errToneMapCapabilityUnavailable, capabilityErr)
	}
	return selector.PlanSessionWith(session.ID, session.TranscodeNodeURL, true, bitrateKbps, func(node *nodepool.Node) bool {
		if node == nil {
			return false
		}
		nodeURL := strings.TrimRight(node.URL, "/")
		if _, failed := excluded[nodeURL]; failed {
			return false
		}
		if sourceAudioChannels > 2 {
			if _, supported := audioBoostNodes[nodeURL]; !supported {
				return false
			}
		}
		return nodeCapabilities[nodeURL].Supports(tonemap.ModeSoftware, kind)
	}), nil
}

func (h *PlaybackHandler) releaseCompatSessionReservation(sessionID string) {
	if releaser, ok := h.NodePlanner.(compatSessionReservationReleaser); ok {
		releaser.ReleaseSession(sessionID)
	}
}

// allow4KVideoTranscode reads the allow_4k_transcode server setting,
// defaulting to deny like the native playback handler.
func (h *PlaybackHandler) allow4KVideoTranscode(ctx context.Context) bool {
	if h.SettingsRepo == nil {
		return false
	}
	v, _ := h.SettingsRepo.Get(ctx, config.Allow4KTranscodeSettingKey)
	return v == "true"
}

func is4KResolution(res string) bool {
	return access.CompareQuality(res, "2160p") >= 0
}

// compatVideoToolboxToneMapBitrateKbps chooses a resolution-aware bitrate for
// Jellyfin-compatible VideoToolbox tone maps. Those requests intentionally
// preserve source dimensions, so leaving the bitrate unset would make the
// encoder use its 1080p fallback even for 4K sources.
func compatVideoToolboxToneMapBitrateKbps(version catalog.FileVersion, recipe compatToneMapRecipe) int {
	if recipe.mode != tonemap.ModeHardware || recipe.hwAccel != tonemap.BackendVideoToolbox {
		return 0
	}

	height := 0
	for _, track := range version.VideoTracks {
		if track.Height > 0 {
			height = track.Height
			break
		}
	}
	if height == 0 {
		resolution := strings.ToLower(strings.TrimSpace(version.Resolution))
		switch resolution {
		case "8k":
			height = 4320
		case "4k", "uhd":
			height = 2160
		default:
			height, _ = strconv.Atoi(strings.TrimSuffix(resolution, "p"))
		}
	}

	switch {
	case height >= 2160:
		return 20_000
	case height >= 1080:
		return 6_000
	case height >= 720:
		return 2_000
	case height > 0:
		return 1_500
	case version.Bitrate > 0:
		return version.Bitrate
	default:
		return 0
	}
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
		var upstreamSession *playback.Session
		if h.sessionMgr != nil {
			if up, err := h.sessionMgr.GetSession(sessionID); err == nil && up != nil {
				upstreamSession = up
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
			if h.playbackStore != nil {
				if playSession, ok := h.playbackStore.FindByUpstreamSessionID(sessionID); ok {
					h.dispatchCompatScrobble(ctx, compatScrobblePause, playSession, upstreamSession, nil)
				}
			}
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
	compatSession *Session,
	createdAt time.Time,
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

	sourceAudioChannels := 0
	if method == string(playback.PlayTranscode) {
		sourceAudioChannels = compatHLSRecipeSourceAudioChannels(source)
	} else if method == string(playback.PlayRemux) && source.TranscodeAudio {
		sourceAudioChannels = compatSourceAudioChannels(source)
	}
	targetAudioCodec := compatTargetAudioCodec
	if method == string(playback.PlayTranscode) && !compatHLSTranscodesAudio(source) {
		targetAudioCodec = compatCopyCodec
	}
	claims := streamtoken.Claims{
		SessionID:           upstreamSessionID,
		MediaPath:           file.FilePath,
		PlayMethod:          method,
		TranscodeAudio:      source.TranscodeAudio,
		TargetCodecAudio:    targetAudioCodec,
		AudioTrackIndex:     audioTrackIndex,
		SourceAudioChannels: sourceAudioChannels,
		AudioOnly:           file.IsAudioOnly(),
		TranscodeNode:       transcodeNodeURL,
		DVProfile:           file.PrimaryDVProfile(),
	}
	if playback.IsAudioToAACStereoDownmixV3(claims.SourceAudioChannels, claims.TargetCodecAudio, claims.TargetAudioChannels) {
		// Compatibility AAC output is stereo by default. Freeze that effective
		// value so the proxy's versioned route can validate the whole downmix
		// shape before starting FFmpeg.
		claims.TargetAudioChannels = 2
		switch method {
		case string(playback.PlayTranscode):
			claims.PlayMethod = streamtoken.PlayMethodAudioDownmixTranscode
		case string(playback.PlayRemux):
			claims.PlayMethod = streamtoken.PlayMethodAudioDownmixRemux
		}
	} else {
		// SourceAudioChannels is meaningful only inside the versioned recipe.
		// Keep ordinary tokens safe for mixed-generation proxy fleets.
		claims.SourceAudioChannels = 0
	}
	if claims.SourceAudioChannels == 0 && method == string(playback.PlayTranscode) && !compatHLSCopiesVideo(source) && compatVersionRequiresToneMap(source.Version) {
		// Older binaries do not understand the frozen tone-map claims. Give them
		// a method they reject rather than let a proxy serve HDR bytes the plan
		// promised as tone-mapped SDR.
		claims.PlayMethod = streamtoken.PlayMethodToneMapTranscode
	}
	if compatSession != nil {
		claims.UserID = compatSession.StreamAppUserID
		claims.ProfileID = compatSession.ProfileID
		claims.MediaFileID = source.FileID
	}
	if !createdAt.IsZero() {
		claims.OriginalStartedAtUnixNano = createdAt.UnixNano()
	}
	token, err := streamtoken.Sign(claims, h.JWTSecret, 24*time.Hour)
	if err != nil {
		return "", err
	}

	switch method {
	case string(playback.PlayDirect):
		return nodepool.NodeEndpoint(proxyNode.ClientURL(), "/stream/direct/"+token), nil
	case string(playback.PlayRemux):
		remuxPath := "/stream/remux/"
		if claims.PlayMethod == streamtoken.PlayMethodAudioDownmixRemux {
			remuxPath = "/stream/remux/audio-v2/"
		}
		redirectURL := nodepool.NodeEndpoint(proxyNode.ClientURL(), remuxPath+token)
		if seekSeconds > 0 {
			redirectURL += "?seek=" + strconv.FormatFloat(seekSeconds, 'f', -1, 64)
		}
		return redirectURL, nil
	case string(playback.PlayTranscode):
		return nodepool.NodeEndpoint(proxyNode.ClientURL(),
			"/stream/transcode/"+token+"/master.m3u8?"+playback.SourceTimelineQueryParam+"=1"), nil
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

// remoteDispatchHWAccel picks the acceleration backend to name in a start
// request to one node: that node's own hw_accel_override when it carries one,
// and otherwise the cluster-wide setting this host runs under. A node under an
// override resolves to its own answer regardless, so naming it keeps the
// request and what runs in agreement. The cluster value passes through
// untouched otherwise — "auto" included, because the node honors a named
// backend verbatim and must be left to resolve it against live hardware. This
// mirrors the v1 dispatch path in internal/api/handlers/playback_v3.go.
func (h *PlaybackHandler) remoteDispatchHWAccel(nodeURL string) string {
	lookup, ok := h.NodePlanner.(compatTranscodeNodeLookup)
	if !ok {
		return h.HWAccel
	}
	node, found := lookup.TranscodeNodeByURL(nodeURL)
	if !found {
		return h.HWAccel
	}
	return node.EffectiveHWAccel(h.HWAccel)
}

// startRemoteTranscode submits a frozen compatibility recipe to a selected node.
func (h *PlaybackHandler) startRemoteTranscode(
	ctx context.Context,
	playSessionID string,
	upstreamSessionID string,
	source PlaybackMediaSource,
	file *models.MediaFile,
	initialSeekSeconds float64,
	transcodeNodeURL string,
) error {
	return h.startRemoteTranscodeWithToneMapMode(
		ctx, playSessionID, upstreamSessionID, source, file,
		initialSeekSeconds, transcodeNodeURL, "",
	)
}

func (h *PlaybackHandler) startRemoteTranscodeWithToneMapMode(
	ctx context.Context,
	playSessionID string,
	upstreamSessionID string,
	source PlaybackMediaSource,
	file *models.MediaFile,
	initialSeekSeconds float64,
	transcodeNodeURL string,
	requiredToneMapMode tonemap.Mode,
) error {
	// Remote contenders for one upstream session single-flight across route
	// binding, node start, and durable publication. This uses a dedicated key,
	// so a local software fallback can still proceed and win through the normal
	// session lifecycle fence while a node request is slow.
	if h.tm != nil {
		remoteStartUnlock := h.tm.LockSessionLifecycle("compat-remote-start\x00" + upstreamSessionID)
		defer remoteStartUnlock()
		if h.tm.GetTranscodeSession(upstreamSessionID) != nil {
			return errRemoteStartAdoptedLocal
		}
	}
	if h.playbackStore != nil {
		expectedSourceAudioChannels := compatHLSRecipeSourceAudioChannels(source)
		expectedAudioTrackIndex := compatAudioTrackIndexOrDefault(source)
		if current, ok := h.playbackStore.Get(playSessionID); ok && current.TranscodeStarted && current.Recipe != nil && current.Recipe.TranscodeNodeURL != "" &&
			current.Recipe.MediaFileID == source.FileID &&
			current.Recipe.SourceAudioChannels == expectedSourceAudioChannels &&
			current.Recipe.AudioTrackIndex == expectedAudioTrackIndex {
			if upstream, sessionErr := h.sessionMgr.GetSession(upstreamSessionID); sessionErr == nil && upstream != nil &&
				strings.TrimRight(upstream.TranscodeNodeURL, "/") == strings.TrimRight(current.Recipe.TranscodeNodeURL, "/") {
				return &remoteStartAdoptedRemoteError{nodeURL: current.Recipe.TranscodeNodeURL}
			}
		}
	}
	if sourceAudioChannels := compatHLSRecipeSourceAudioChannels(source); sourceAudioChannels > 0 {
		capabilityCtx, cancelCapabilityFetch := context.WithTimeout(ctx, h.toneMapCapabilityTimeout())
		info, capabilityErr := h.remoteToneMapCapabilityInfo(capabilityCtx, transcodeNodeURL)
		cancelCapabilityFetch()
		if capabilityErr != nil {
			return fmt.Errorf("load transcode node audio recipe capabilities: %w", capabilityErr)
		}
		if !compatSupportsAudioBoost(info.Transformations) {
			return fmt.Errorf("transcode node does not support %s recipe %s", playback.TransformationAudioToAACV3, playback.TransformationAudioToAACRecipeVersionV3)
		}
	}
	if h.sessionMgr != nil {
		if err := h.sessionMgr.SetTranscodeNodeURL(upstreamSessionID, transcodeNodeURL); err != nil {
			return fmt.Errorf("bind transcode node: %w", err)
		}
	}
	if !compatHLSCopiesVideo(source) && is4KResolution(source.Version.Resolution) && !h.allow4KVideoTranscode(ctx) {
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
	sourceVideoCodec, sourceVideoProfile, sourceVideoBitDepth := playback.SourceVideoTranscodeFacts(file)
	toneMapRecipe := compatToneMapRecipe{}
	var toneMapCapabilities tonemap.Capabilities
	nodeProbeTimeoutMillis := int64(0)
	if !compatHLSCopiesVideo(source) {
		metadata := tonemap.MetadataForFile(file)
		var requiredPolicy tonemap.Policy
		if requiredToneMapMode != "" {
			var policyErr error
			requiredPolicy, policyErr = h.toneMapPolicyResult(ctx)
			if policyErr != nil {
				return fmt.Errorf("%w: %w", errToneMapCapabilityUnavailable, policyErr)
			}
			if !requiredPolicy.Allows(requiredToneMapMode) {
				return errHDRTranscodeUnsupported
			}
		}
		if metadata.DynamicRange != "" && metadata.DynamicRange != playback.DynamicRangeSDRV3 {
			var capabilityErr error
			capabilityCtx, cancelCapabilityFetch := context.WithTimeout(ctx, h.toneMapCapabilityTimeout())
			var info playback.HWAccelInfo
			info, capabilityErr = h.remoteToneMapCapabilityInfo(capabilityCtx, transcodeNodeURL)
			toneMapCapabilities = info.ToneMapCapabilities
			nodeProbeTimeoutMillis = info.ProbeRequestTimeoutMillis
			cancelCapabilityFetch()
			if capabilityErr != nil {
				err := fmt.Errorf("load transcode node tone-map capabilities: %w", capabilityErr)
				if requiredToneMapMode == tonemap.ModeSoftware && ctx.Err() == nil {
					return fmt.Errorf("%w: %w", errRemoteSoftwareToneMapStartFailed, err)
				}
				return err
			}
		}
		var toneMapErr error
		if requiredToneMapMode != "" {
			toneMapRecipe, toneMapErr = resolveCompatToneMapRecipeWithPolicy(file, toneMapCapabilities, requiredPolicy)
		} else {
			toneMapRecipe, toneMapErr = h.resolveCompatToneMapRecipe(ctx, file, toneMapCapabilities)
		}
		if toneMapErr != nil {
			kind := tonemap.ResolveSource(metadata).Kind
			if requiredToneMapMode == tonemap.ModeSoftware && kind != "" && !toneMapCapabilities.Supports(tonemap.ModeSoftware, kind) {
				return fmt.Errorf("%w: %w", errRemoteSoftwareToneMapStartFailed, toneMapErr)
			}
			return toneMapErr
		}
		if toneMapErr = requireCompatToneMapMode(&toneMapRecipe, toneMapCapabilities, requiredToneMapMode); toneMapErr != nil {
			if requiredToneMapMode == tonemap.ModeSoftware && !toneMapCapabilities.Supports(tonemap.ModeSoftware, toneMapRecipe.sourceKind) {
				return fmt.Errorf("%w: %w", errRemoteSoftwareToneMapStartFailed, toneMapErr)
			}
			return toneMapErr
		}
	}

	reqBody := transcodenode.TranscodeStartRequest{
		SessionID:           upstreamSessionID,
		InputPath:           file.FilePath,
		SourceVideoCodec:    sourceVideoCodec,
		SourceVideoProfile:  sourceVideoProfile,
		SourceVideoBitDepth: sourceVideoBitDepth,
		SeekSeconds:         initialSeekSeconds,
		StartSegmentNumber:  startSegmentNumber,
		TargetCodecVideo:    compatTargetVideoCodec,
		TargetCodecAudio:    compatTargetAudioCodec,
		SegmentDuration:     segmentDuration,
		HWAccel:             h.remoteDispatchHWAccel(transcodeNodeURL),
		AudioTrackIndex:     compatAudioTrackIndexOrDefault(source),
		SourceAudioChannels: compatHLSRecipeSourceAudioChannels(source),
		TotalDuration:       float64(source.Version.Duration),
		RequireReady:        toneMapRecipe.mode != "",
	}
	if playback.IsAudioToAACStereoDownmixV3(reqBody.SourceAudioChannels, reqBody.TargetCodecAudio, reqBody.TargetAudioChannels) {
		reqBody.AudioRecipeVersion = playback.TransformationAudioToAACRecipeVersionV3
		reqBody.TargetAudioChannels = 2
		reqBody.RequireReady = true
	} else {
		reqBody.SourceAudioChannels = 0
	}
	if toneMapRecipe.mode != "" {
		reqBody.ToneMapPolicy = toneMapRecipe.policy
		reqBody.ToneMapMode = toneMapRecipe.mode
		reqBody.ToneMapSourceKind = toneMapRecipe.sourceKind
		reqBody.ToneMapRecipeVersion = toneMapRecipe.recipeVersion
		reqBody.ToneMapPreflightRequired = toneMapRecipe.preflightRequired
		reqBody.ToneMapSourceRevision = toneMapRecipe.sourceRevision
		reqBody.ToneMapDVConfigPresent = toneMapRecipe.dvConfigPresent
		reqBody.ToneMapDVBLCompatIDPresent = toneMapRecipe.dvBLCompatIDPresent
		reqBody.ToneMapDVBLPresent = toneMapRecipe.dvBLPresent
		reqBody.ToneMapDVRPUPresent = toneMapRecipe.dvRPUPresent
		reqBody.HWAccel = toneMapRecipe.hwAccel
	}
	autoVideoToolboxBitrate := compatVideoToolboxToneMapBitrateKbps(source.Version, toneMapRecipe)
	if autoVideoToolboxBitrate > 0 {
		reqBody.TargetBitrateKbps = autoVideoToolboxBitrate
	}
	if compatHLSCopiesVideo(source) {
		reqBody.TargetCodecVideo = compatCopyCodec
		reqBody.VideoSampleEntry = playback.VideoSampleEntryForDVCopy(file.PrimaryDVProfile())
	}
	if !compatHLSTranscodesAudio(source) {
		reqBody.TargetCodecAudio = compatCopyCodec
	}

	dispatch := func(request transcodenode.TranscodeStartRequest) (transcodenode.TranscodeStartResponse, int, bool, error) {
		body, err := json.Marshal(request)
		if err != nil {
			return transcodenode.TranscodeStartResponse{}, 0, false, fmt.Errorf("marshal transcode request: %w", err)
		}
		requestCtx, cancel := context.WithTimeout(ctx, h.remoteTranscodeStartTimeout(request, nodeProbeTimeoutMillis))
		defer cancel()
		httpReq, err := http.NewRequestWithContext(requestCtx, http.MethodPost, nodepool.NodeEndpoint(transcodeNodeURL, "/transcode/start"), strings.NewReader(string(body)))
		if err != nil {
			return transcodenode.TranscodeStartResponse{}, 0, false, fmt.Errorf("build transcode request: %w", logredact.SanitizeURLError(err))
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+h.JWTSecret)
		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			return transcodenode.TranscodeStartResponse{}, 0, true, fmt.Errorf("remote transcode start failed: %w", logredact.SanitizeURLError(err))
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusAccepted {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			if request.ToneMapMode != "" {
				if validationErr := transcodenode.ToneMapExecutionErrorForResponse(
					resp.StatusCode,
					resp.Header.Get(transcodenode.ToneMapExecutionErrorHeader),
				); validationErr != nil {
					return transcodenode.TranscodeStartResponse{}, resp.StatusCode, false, validationErr
				}
			}
			return transcodenode.TranscodeStartResponse{}, resp.StatusCode, false, nil
		}
		// Older nodes returned an empty 202 response. Accept that response for
		// ordinary transcodes, while decoding current-node execution facts when
		// present. Tone-mapped recipes still require a confirmed mode below.
		var result transcodenode.TranscodeStartResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			if errors.Is(err, io.EOF) && request.ToneMapMode == "" {
				return transcodenode.TranscodeStartResponse{}, resp.StatusCode, false, nil
			}
			return transcodenode.TranscodeStartResponse{}, resp.StatusCode, true, err
		}
		return result, resp.StatusCode, false, nil
	}
	nodeResponse, status, cleanupRequired, err := dispatch(reqBody)
	initialValidationErr := error(nil)
	if isCompatToneMapExecutionError(err) {
		initialValidationErr = err
		err = nil
	}
	if err == nil && status == http.StatusAccepted && reqBody.ToneMapMode != "" && nodeResponse.ToneMapMode != reqBody.ToneMapMode {
		err = errors.New("remote transcode node did not confirm tone-map mode")
		cleanupRequired = true
	}
	initialStatus := status
	initialErr := err
	validationErr := initialValidationErr
	retryWithSoftware := false
	if err != nil {
		if cleanupRequired {
			h.tm.StopRemoteTranscode(upstreamSessionID, transcodeNodeURL)
		}
		if cleanupRequired && ctx.Err() == nil {
			retryWithSoftware = downgradeToSoftwareToneMap(
				toneMapRecipe.policy, &toneMapRecipe.mode, &toneMapRecipe.filter, &toneMapRecipe.hwAccel,
				toneMapRecipe.sourceKind, toneMapCapabilities,
			)
		}
		if !retryWithSoftware {
			return err
		}
	} else if status == http.StatusUnprocessableEntity || status == http.StatusNotImplemented || errors.Is(initialValidationErr, playback.ErrToneMapExecutorUnavailable) {
		retryWithSoftware = downgradeToSoftwareToneMap(
			toneMapRecipe.policy, &toneMapRecipe.mode, &toneMapRecipe.filter, &toneMapRecipe.hwAccel,
			toneMapRecipe.sourceKind, toneMapCapabilities,
		)
	}
	if retryWithSoftware {
		reqBody.ToneMapMode = toneMapRecipe.mode
		reqBody.HWAccel = toneMapRecipe.hwAccel
		if autoVideoToolboxBitrate > 0 {
			reqBody.TargetBitrateKbps = 0
		}
		nodeResponse, status, cleanupRequired, err = dispatch(reqBody)
		validationErr = nil
		if isCompatToneMapExecutionError(err) {
			validationErr = err
			err = nil
		}
		if err != nil {
			if cleanupRequired {
				h.tm.StopRemoteTranscode(upstreamSessionID, transcodeNodeURL)
			}
			if initialErr != nil {
				return fmt.Errorf("%w: remote hardware tone-map start failed: %w; software retry failed: %w", errRemoteSoftwareToneMapStartFailed, initialErr, err)
			}
			return fmt.Errorf("%w: %w", errRemoteSoftwareToneMapStartFailed, err)
		}
	}
	if status != http.StatusAccepted {
		var startErr error
		if retryWithSoftware {
			if initialErr != nil {
				startErr = fmt.Errorf("remote hardware tone-map start failed: %w; software tone-map retry status %d", initialErr, status)
			} else {
				startErr = fmt.Errorf("remote transcode start rejected: initial status %d; software tone-map retry status %d", initialStatus, status)
			}
		} else {
			startErr = fmt.Errorf("remote transcode start rejected: status %d", status)
		}
		if retryWithSoftware && initialValidationErr != nil && validationErr != nil {
			validationErr = errors.Join(initialValidationErr, validationErr)
		}
		if validationErr != nil {
			startErr = errors.Join(validationErr, startErr)
		}
		if reqBody.ToneMapMode == tonemap.ModeSoftware || validationErr != nil {
			return fmt.Errorf("%w: %w", errRemoteSoftwareToneMapStartFailed, startErr)
		}
		return startErr
	}
	if err := transcodenode.ValidateAudioRecipeAttestation(reqBody, nodeResponse); err != nil {
		h.tm.StopRemoteTranscode(upstreamSessionID, transcodeNodeURL)
		return err
	}
	if reqBody.ToneMapMode != "" && nodeResponse.ToneMapMode != reqBody.ToneMapMode {
		h.tm.StopRemoteTranscode(upstreamSessionID, transcodeNodeURL)
		err := errors.New("remote transcode node did not confirm tone-map mode")
		if reqBody.ToneMapMode == tonemap.ModeSoftware {
			return fmt.Errorf("%w: %w", errRemoteSoftwareToneMapStartFailed, err)
		}
		return err
	}

	// A remote HTTP start may finish after another request has installed a
	// local fallback for the same upstream generation. Serialize publication
	// with local replacement and adopt that winner without overwriting its
	// execution facts or durable recipe.
	publishUnlock := h.tm.LockSessionLifecycle(upstreamSessionID)
	remoteStillOwnsRoute := true
	localWinner := h.tm.GetTranscodeSession(upstreamSessionID) != nil
	if h.playbackStore != nil {
		if currentPlay, ok := h.playbackStore.Get(playSessionID); !ok || currentPlay.UpstreamSessionID != upstreamSessionID {
			remoteStillOwnsRoute = false
		}
	}
	if h.sessionMgr != nil {
		if currentUpstream, currentErr := h.sessionMgr.GetSession(upstreamSessionID); currentErr != nil || currentUpstream == nil ||
			strings.TrimRight(currentUpstream.TranscodeNodeURL, "/") != strings.TrimRight(transcodeNodeURL, "/") {
			remoteStillOwnsRoute = false
		}
	}
	if localWinner {
		remoteStillOwnsRoute = false
	}
	if !remoteStillOwnsRoute {
		publishUnlock()
		h.tm.StopRemoteTranscode(upstreamSessionID, transcodeNodeURL)
		if localWinner {
			return errRemoteStartAdoptedLocal
		}
		return errors.New("remote transcode route ownership changed before publication")
	}
	defer publishUnlock()

	// Mirror the byte-affecting opts sent to the node into a RecipeCard and persist
	// it for restart resilience. The node-hop token is identity-only by design (see
	// internal/noderecipe), and central serves Jellyfin clients that carry no native
	// token of their own, so without a persisted recipe a node or central restart
	// cannot rebuild ffmpeg and segment serves 404.
	opts := playback.TranscodeOpts{
		SessionID:           upstreamSessionID,
		InputPath:           reqBody.InputPath,
		SourceVideoCodec:    reqBody.SourceVideoCodec,
		SourceVideoProfile:  reqBody.SourceVideoProfile,
		SourceVideoBitDepth: reqBody.SourceVideoBitDepth,
		SeekSeconds:         reqBody.SeekSeconds,
		StartSegmentNumber:  reqBody.StartSegmentNumber,
		TargetCodecVideo:    reqBody.TargetCodecVideo,
		TargetCodecAudio:    reqBody.TargetCodecAudio,
		TargetResolution:    reqBody.TargetResolution,
		TargetBitrateKbps:   reqBody.TargetBitrateKbps,
		VideoSampleEntry:    reqBody.VideoSampleEntry,
		SegmentDuration:     reqBody.SegmentDuration,
		AudioTrackIndex:     reqBody.AudioTrackIndex,
		SourceAudioChannels: reqBody.SourceAudioChannels,
		TargetAudioChannels: reqBody.TargetAudioChannels,
		TotalDuration:       reqBody.TotalDuration,
	}
	toneMapRecipe.apply(&opts)
	opts.HWAccel = strings.TrimSpace(nodeResponse.HWAccel)
	opts.ToneMapMode = nodeResponse.ToneMapMode
	if compatHLSCopiesVideo(source) {
		opts.TargetCodecVideo = compatCopyCodec
	}

	if err := h.persistTranscodeRecipe(ctx, playSessionID, upstreamSessionID, opts); err != nil {
		// Roll back the already-started node ffmpeg so it isn't leaked.
		h.tm.StopRemoteTranscode(upstreamSessionID, transcodeNodeURL)
		return err
	}

	// Publish admin/session execution facts only after the durable recipe
	// commits. A failed recipe write must not leave the upstream mirror claiming
	// the rejected audio tuple after the selection transaction rolls back.
	h.recordTranscodeStreamDetails(ctx, upstreamSessionID, opts)

	return nil
}

// persistTranscodeRecipe builds the reconstruction recipe from the upstream
// session's identity and persists it for restart resilience. It is shared by the
// local (ensureLocalTranscode) and remote (startRemoteTranscode) transcode paths
// so both stores stay in lock-step.
//
// For dedicated nodes, the recipe is first committed to the node recipe store
// (Redis), then recorded in the compat store in the same Update that marks the
// transcode started. The node
// URL is taken from the upstream session (bound before start on the remote path),
// so it is "" for integrated transcodes and the node-store write is skipped.
// A Jellyfin client carries no native token of its own and the node-hop token is
// deliberately identity-only (see internal/noderecipe), so the persisted recipe is
// the only way a node or central restart can rebuild ffmpeg.
//
// The caller owns rolling back its transcode when either durable write fails.
// A missing upstream
// session (a start/build race, or no session manager in tests) is logged and the
// live transcode is left serving — only restart resilience is forfeited.
func (h *PlaybackHandler) persistTranscodeRecipe(
	ctx context.Context,
	playSessionID, upstreamSessionID string,
	opts playback.TranscodeOpts,
) error {
	var playSession *PlaybackSession
	if h.playbackStore != nil {
		playSession, _ = h.playbackStore.Get(playSessionID)
	}
	var recipe *playback.RecipeCard
	if h.sessionMgr != nil {
		if upstream, err := h.sessionMgr.GetSession(upstreamSessionID); err == nil && upstream != nil {
			card := playback.NewRecipeCard(upstream.UserID, upstream.ProfileID, upstream.MediaFileID, upstream.TranscodeNodeURL, opts)
			// Mirror the client metadata into the card so a session rebuilt
			// after a restart keeps its client label and Jellyfin
			// identification instead of syncing with empty client fields.
			card.ClientName = upstream.ClientName
			card.ClientVersion = upstream.ClientVersion
			card.ClientUserAgent = upstream.ClientUserAgent
			card.IsJellyfinCompat = upstream.IsJellyfinCompat
			if playSession != nil {
				card.OriginalStartedAt = playSession.CreatedAt
			}
			recipe = &card
		}
	}
	if recipe == nil {
		slog.WarnContext(ctx, "transcode recipe not persisted: upstream session unavailable", "component", "jellycompat",
			"playback_session_id", upstreamSessionID)
	}

	var previousRecipe *playback.RecipeCard
	previousTranscodeStarted := false
	if previous, ok := h.playbackStore.Get(playSessionID); ok && previous != nil {
		previousTranscodeStarted = previous.TranscodeStarted
		if previous.Recipe != nil {
			copy := *previous.Recipe
			previousRecipe = &copy
		}
	}
	nodeRecipeCommitted := recipe != nil && recipe.TranscodeNodeURL != "" && h.RecipeNodeStore != nil
	if nodeRecipeCommitted {
		putCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		err := h.RecipeNodeStore.Put(putCtx, upstreamSessionID, *recipe)
		cancel()
		if err != nil {
			return fmt.Errorf("persist node transcode recipe: %w", err)
		}
	}
	remoteToLocal := recipe != nil && recipe.TranscodeNodeURL == "" && previousRecipe != nil && previousRecipe.TranscodeNodeURL != ""
	oldNodeRecipeRemoved := false
	if remoteToLocal {
		h.tm.StopRemoteTranscode(upstreamSessionID, previousRecipe.TranscodeNodeURL)
		if h.RecipeNodeStore != nil {
			deleteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			err := h.RecipeNodeStore.Delete(deleteCtx, upstreamSessionID)
			cancel()
			if err != nil {
				return fmt.Errorf("delete replaced node transcode recipe: %w", err)
			}
			oldNodeRecipeRemoved = true
		}
	}

	if updateErr := h.playbackStore.Update(playSessionID, func(current *PlaybackSession) error {
		current.TranscodeStarted = true
		current.Recipe = recipe
		return nil
	}); updateErr != nil {
		var restoreErr error
		if nodeRecipeCommitted || oldNodeRecipeRemoved {
			restoreCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if previousRecipe != nil && previousRecipe.TranscodeNodeURL != "" {
				if err := h.RecipeNodeStore.Put(restoreCtx, upstreamSessionID, *previousRecipe); err != nil {
					restoreErr = fmt.Errorf("restore prior node transcode recipe: %w", err)
				}
			} else {
				if err := h.RecipeNodeStore.Delete(restoreCtx, upstreamSessionID); err != nil {
					restoreErr = fmt.Errorf("remove uncommitted node transcode recipe: %w", err)
				}
			}
		}
		// Durable stores can surface a DB failure after updating their live
		// cache. Compensate only the exact candidate this call published so a
		// concurrent newer recipe remains authoritative.
		compatRestoreErr := h.playbackStore.Update(playSessionID, func(current *PlaybackSession) error {
			if current.TranscodeStarted && reflect.DeepEqual(current.Recipe, recipe) {
				current.TranscodeStarted = previousTranscodeStarted
				current.Recipe = previousRecipe
			}
			return nil
		})
		updateErr = fmt.Errorf("update playback session: %w", updateErr)
		if restoreErr != nil || compatRestoreErr != nil {
			return errors.Join(updateErr, restoreErr, compatRestoreErr)
		}
		return updateErr
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
	// Jellyfin's authenticated bandwidth probe: transfer-observed, cap-exempt
	// (§4.2 "classify but exempt"). It resolves no play session, so the subject
	// is all the identity there is; an unauthenticated probe attaches nothing and
	// its bytes fall into Unattributed*.
	attachCompatTransfer(r.Context(), SessionFromContext(r.Context()), 0)
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
	// PlaybackInfo is authorized by the token-derived session. Some clients
	// retain a previous UserId in their request body while moving to the next
	// item; that advisory value must not turn an otherwise authorized playback
	// negotiation into a 404.

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
	toneMapPolicy := tonemap.PolicyNone
	toneMapPolicyLoaded := false
	var toneMapCapabilities tonemap.Capabilities
	var toneMapCapabilityErr error
	toneMapCapabilitiesLoaded := false
	if req.MediaSourceID != "" {
		matched := false
		for _, version := range detail.Versions {
			candidate := h.buildPlaybackSource(routeItemID, playSessionID, version, profile, req, allow4KTranscode)
			if mediaSourceIDsEqual(candidate.ID, req.MediaSourceID) {
				matched = true
				break
			}
		}
		if !matched {
			// Continue Watching and autoplay clients can carry the previous
			// episode's MediaSourceId into the next PlaybackInfo request. The
			// authenticated item route remains authoritative; fall back to its
			// available versions instead of returning a misleading 404.
			slog.InfoContext(r.Context(), "jellycompat ignored stale playback media source", "component", "jellycompat")
			req.MediaSourceID = ""
		}
	}
	for _, version := range detail.Versions {
		source := h.buildPlaybackSource(routeItemID, playSessionID, version, profile, req, allow4KTranscode)
		if req.MediaSourceID != "" && !mediaSourceIDsEqual(source.ID, req.MediaSourceID) {
			continue
		}
		if source.SupportsTranscoding && !compatHLSCopiesVideo(source) && compatVersionRequiresToneMap(version) {
			if !toneMapPolicyLoaded {
				var policyErr error
				toneMapPolicy, policyErr = h.toneMapPolicyResult(r.Context())
				if policyErr != nil {
					if !source.SupportsDirectPlay && !source.SupportsDirectStream {
						writeError(w, http.StatusServiceUnavailable, "PlaybackUnavailable", "Tone-map policy is temporarily unavailable")
						return
					}
					toneMapPolicy = tonemap.PolicyNone
				}
				toneMapPolicyLoaded = true
			}
			if !toneMapCapabilitiesLoaded {
				if toneMapPolicy != tonemap.PolicyNone {
					toneMapCapabilities, toneMapCapabilityErr = h.availableCompatToneMapCapabilities(r.Context(), compatToneMapNegotiationTimeout)
				}
				toneMapCapabilitiesLoaded = true
			}
			if toneMapCapabilityErr != nil {
				metadata := tonemap.MetadataForFile(&models.MediaFile{HDR: version.HDR, VideoTracks: version.VideoTracks})
				resolution := tonemap.ResolveSource(metadata)
				if resolution.Kind != "" && toneMapCapabilities.PreferredMode(toneMapPolicy, resolution.Kind) == "" &&
					!source.SupportsDirectPlay && !source.SupportsDirectStream {
					writeError(w, http.StatusServiceUnavailable, "PlaybackUnavailable", "Tone-map capability discovery is temporarily unavailable")
					return
				}
			}
			source = applyCompatToneMapAvailabilityWithPolicy(source, toneMapCapabilities, toneMapPolicy)
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
				slog.WarnContext(r.Context(), "jellycompat downloaded subtitle lookup failed", "component", "jellycompat",
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

	clientDeviceID := firstNonEmpty(
		firstMediaBrowserAuthorizationValue(r, "DeviceId"),
		newCaseInsensitiveQuery(r.URL.Query()).Get("DeviceId"),
	)
	clientDeviceID = stripCompatNUL(clientDeviceID)
	h.playbackStore.PutNegotiated(PlaybackSession{
		ID:                 playSessionID,
		CompatToken:        session.Token,
		ClientDeviceID:     clientDeviceID,
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

// stripCompatNUL removes the only code point PostgreSQL rejects in JSONB text.
// DeviceId and static PlaySessionId come from clients, then become fields in a
// durable PlaybackSession. Without this boundary check one malformed value
// leaves the session cache-only; its first revalidation drops playback.
func stripCompatNUL(value string) string {
	return strings.ReplaceAll(value, "\x00", "")
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
	enableTranscoding := boolDefault(req.EnableTranscoding, true)
	hlsAudioCopy := enableTranscoding &&
		enableDirectStream &&
		allowVideoCopy &&
		allowAudioCopy &&
		!supportsDirectPlay &&
		profile.SupportsHLSRemuxForAudioStream(version, selectedAudioIndex)
	hlsAudioTranscode := !hlsAudioCopy &&
		enableTranscoding &&
		!supportsDirectPlay &&
		(!allowAudioCopy || !audioSupported) &&
		profile.supportsHLSRemuxWithAudioTranscodeForAudioStream(version, selectedAudioIndex)
	transcodeAudio := !hlsAudioCopy &&
		enableDirectStream &&
		allowVideoCopy &&
		videoSupported &&
		(!audioSupported || hlsAudioTranscode)
	hlsRemux := hlsAudioCopy || transcodeAudio
	var hlsRemuxAudioStreamIndexes []int
	if hlsRemux && !transcodeAudio {
		hlsRemuxAudioStreamIndexes = supportedHLSRemuxAudioStreamIndexes(version, profile, selectedAudioIndex)
	}
	supportsDirectStream := !hlsRemux &&
		enableDirectStream &&
		allowVideoCopy &&
		videoSupported &&
		allowAudioCopy &&
		audioSupported
	// A remux route is only advertised when some transcoding profile vouched
	// for it: the copy and AAC legs each verified the fMP4 HLS output above,
	// and the legacy audio-transcode leg keeps the pre-remux SupportsTranscoding
	// gate rather than minting a TranscodingURL no profile ever matched.
	supportsTranscoding := enableTranscoding &&
		(hlsAudioCopy || (transcodeAudio && hlsAudioTranscode) || profile.SupportsTranscoding(version))
	// Don't offer full video encodes of 4K sources when allow_4k_transcode is
	// off. HLS remuxes stream-copy the video and stay available regardless of
	// whether their audio is copied or encoded.
	if supportsTranscoding && !hlsRemux && !allow4KTranscode && is4KResolution(version.Resolution) {
		supportsTranscoding = false
	}

	return PlaybackMediaSource{
		ID:                         sourceID,
		FileID:                     version.FileID,
		Version:                    version,
		SupportsDirectPlay:         supportsDirectPlay,
		SupportsDirectStream:       supportsDirectStream,
		SupportsTranscoding:        supportsTranscoding,
		HLSRemux:                   hlsRemux,
		HLSRemuxAudioStreamIndexes: hlsRemuxAudioStreamIndexes,
		TranscodeAudio:             transcodeAudio,
		DefaultAudioStreamIndex:    audioIndex,
		SelectedAudioStreamIndex:   selectedAudioIndex,
		DefaultSubtitleStreamIndex: subtitleIndex,
		ETag:                       mediaSourceETag(version),
	}
}

// supportedHLSRemuxAudioStreamIndexes freezes the copy-safe switch targets. A
// copy playlist reuses the same playlist and EXT-X-MAP URLs across a track
// switch, so the client's SourceBuffer keeps the init segment negotiated for
// the initial track; only tracks whose fMP4 init is interchangeable with it
// (same codec and channel layout) are safe to switch without renegotiation.
func supportedHLSRemuxAudioStreamIndexes(version catalog.FileVersion, profile DeviceProfile, selectedAudioIndex *int) []int {
	selected := compatAudioTrack(version, selectedAudioIndex)
	indexes := make([]int, 0, len(version.AudioTracks))
	for audioTrackIndex, track := range version.AudioTracks {
		if !strings.EqualFold(track.Codec, selected.Codec) || track.Channels != selected.Channels {
			continue
		}
		streamIndex := len(version.VideoTracks) + audioTrackIndex
		if profile.SupportsHLSRemuxForAudioStream(version, &streamIndex) {
			indexes = append(indexes, streamIndex)
		}
	}
	return indexes
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
		// This URL is explicitly static=true, so HandleVideoStream always serves
		// the source file directly. It never executes the versioned audio recipe.
		basePath := compatVideoPath(routeItemID, false)
		dto.DirectStreamURL = fmt.Sprintf(
			"%s/stream?static=true&mediaSourceId=%s&api_key=%s&PlaySessionId=%s",
			basePath,
			url.QueryEscape(source.ID),
			url.QueryEscape(compatToken),
			url.QueryEscape(playSessionID),
		)
	}
	dto.TranscodingSubProtocol = "hls"
	if source.SupportsTranscoding {
		basePath := compatVideoPath(routeItemID, false)
		if routePathSegment := compatHLSRoutePathSegment(source); routePathSegment != "" {
			basePath += "/" + routePathSegment
		}
		dto.TranscodingURL = fmt.Sprintf("%s/master.m3u8?PlaySessionId=%s&MediaSourceId=%s", basePath, playSessionID, source.ID)
		if compatHLSCopiesVideo(source) {
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
		anamorphic, anamorphicKnown := compatIsAnamorphic(track)
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
			IsAnamorphic:           anamorphicKnown && anamorphic,
			NalLengthSize:          "4",
			BitDepth:               track.BitDepth,
			RefFrames:              track.ReferenceFrames,
			Profile:                track.Profile,
			Level:                  track.Level,
			AspectRatio:            track.AspectRatio,
			VideoRange:             compatVideoRange(track, version.HDR),
			VideoRangeType:         compatVideoRangeType(track, version.HDR),
			ColorRange:             compatColorRange(track.ColorRange),
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
			Profile:                track.Profile,
			AudioSpatialFormat:     compatAudioSpatialFormat(track.Profile),
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

func compatColorRange(colorRange string) string {
	colorRange = strings.TrimSpace(colorRange)
	if strings.EqualFold(colorRange, "unknown") {
		// The scanner persists this sentinel so legacy probe repair converges,
		// but Jellyfin omits ColorRange when ffprobe did not provide a value.
		return ""
	}
	return colorRange
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

func compatSourceAudioChannels(source PlaybackMediaSource) int {
	audioTrackIndex := compatAudioTrackIndexOrDefault(source)
	if audioTrackIndex < 0 || audioTrackIndex >= len(source.Version.AudioTracks) {
		return 0
	}
	channels := source.Version.AudioTracks[audioTrackIndex].Channels
	if channels <= 2 {
		return 0
	}
	return channels
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

// compatAudioSpatialFormat mirrors Jellyfin's MediaStream.AudioSpatialFormat,
// derived from the ffprobe profile string by case-insensitive substring match.
func compatAudioSpatialFormat(profile string) string {
	lower := strings.ToLower(profile)
	switch {
	case strings.Contains(lower, "dolby atmos"):
		return "DolbyAtmos"
	case strings.Contains(lower, "dts:x"):
		return "DTSX"
	default:
		return "None"
	}
}

func audioTrackDisplayTitle(track models.AudioTrack) string {
	lang := compatLanguageName(track.Language)
	// Jellyfin prefers the ffprobe profile over the codec name in audio display
	// titles (e.g. "DTS-HD MA", "Dolby Digital Plus + Dolby Atmos"), except the
	// uninformative AAC "LC" profile.
	codec := audioCodecDisplayName(track.Codec)
	if profile := strings.TrimSpace(track.Profile); profile != "" && !strings.EqualFold(profile, "lc") {
		codec = profile
	}
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

func compatVideoPath(routeItemID string, audioV2 bool) string {
	base := "/Videos/" + routeItemID
	if audioV2 {
		base += "/" + compatAudioV2PathSegment
	}
	return base
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
