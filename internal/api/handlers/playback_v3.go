package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/keptsub"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/playback/connectionstore"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/streamlocation"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const (
	maxPlaybackV3BodyBytes        = 256 << 10
	maxPlaybackV3EventBodyBytes   = 32 << 10
	replanLeaseDurationV3         = 15 * time.Second
	replanReleaseTimeoutV3        = 3 * time.Second
	v3NodeCapabilityTTL           = time.Minute
	playbackNodeIntegratedV3      = "integrated"
	subtitleFormatVTTV3           = "vtt"
	subtitleCodecPGSFFmpegV3      = "hdmv_pgs_subtitle"
	subtitleMIMEVTTV3             = "text/vtt"
	subtitleUnavailableReasonV3   = "subtitle_artifact_unavailable"
	subtitleTrackKindV3           = "subtitle"
	transcodeStartFailedReasonV3  = "transcode_start_failed"
	trackUnavailableReasonV3      = "track_unavailable"
	capabilityUnavailableReasonV3 = "transcode_node_capability_unavailable"
	// failureSubtitleTrackChangedV3 and failureAudioTrackChangedV3 are the
	// failure_recovery classifications of a track the client switched to on
	// its own.
	failureSubtitleTrackChangedV3 = "subtitle_track_changed"
	failureAudioTrackChangedV3    = "audio_track_changed"
	// transcodingDisabledCode is the "transcoding disabled" answer shared by
	// the API error surface and the planner's transport reason.
	transcodingDisabledCode = "transcoding_disabled"
	// sourceUnavailableReason is the transport reason for a request whose
	// media source cannot be resolved at all.
	sourceUnavailableReason = "source_unavailable"
	// Probe range-type tokens, shared by the virtual-probe mapping and the
	// playback fixtures that carry probe-shaped video tracks.
	videoRangeDOVI  = "DOVI"
	videoRangeHLG   = "HLG"
	videoRangeHDR10 = "HDR10"
	videoRangeSDR   = "SDR"
	// maxHLSRouteAttemptLoopV3 bounds the transport route-selection loop. Without
	// it, a chain of fast-failing remote candidates (e.g. HTTP 500 from a node
	// missing a recipe) can spend 32 × (remote timeout + local spawn) inside
	// session_transport_commit, far beyond what a client start or replan lease
	// tolerates. This is an admission check between attempts, not a hard
	// overall deadline: an in-flight prepareLocalTransportV3 or
	// prepareRemoteTransportV3 call still runs under its own context, and a
	// software tone-map fallback starts under the same budget remainder.
	maxHLSRouteAttemptLoopV3 = 90 * time.Second
	// copySeekAnchorTransientBackoff is the bounded pause before re-probing the
	// same candidate after an upstream 5xx. The provider is already failing, so
	// an immediate second probe hammers it; the pause is short enough to still
	// fit a resumed session's seek budget.
	copySeekAnchorTransientBackoff = 750 * time.Millisecond // maxVirtualTransientTransportAlternates bounds how many compatible
	// alternates one start may try after a transport failure whose cause is a
	// transient provider error (upstream 5xx). A failing upstream should not be
	// hammered once per alternate; one alternate keeps a chance to recover on a
	// different release without churning the whole list.
	maxVirtualTransientTransportAlternates = 1
	// trackUnavailableReasonV3 is the transport reason for an audio remap miss:
	// the candidate's audio inventory did not bind, not its video stream, so the
	// client may pick another track without swapping the release.
	// subtitleDroppedUnavailableCodeV3 is the degradation-warning code for a
	// successful plan whose selected subtitle was dropped; playback continues on
	// the same release without it.
	subtitleDroppedUnavailableCodeV3 = "subtitle_dropped_unavailable"
	// audioAdaptationFailedReasonV3 is the transport reason for a remote node
	// that could not confirm the audio adaptation recipe for a release. It is
	// deliberately distinct from transcodeStartFailedReasonV3: an audio problem
	// must be resolved on the release already mounted (another audio track, or
	// the planner's own audio transcode/downmix), so the start/replan
	// alternate-version hunts must not treat it as a video/policy trigger.
	audioAdaptationFailedReasonV3 = "audio_adaptation_failed"
	seekRestorationPlayerV3       = "player_position"
	// virtualSourceUnavailableReasonV3 and replanVirtualInputTimeoutReasonV3 are
	// the retryable replan failure reasons that describe a virtual-source stall —
	// the provider did not answer, or the pinned candidate is absent — rather
	// than a verdict on the session. A terminal carrying one of these must
	// survive a concurrent stop instead of collapsing to a not-found.
	virtualSourceUnavailableReasonV3  = "virtual_source_unavailable"
	replanVirtualInputTimeoutReasonV3 = "replan_virtual_input_timeout"
	// sourceMismatchReasonV3 is the terminal reason for a selected version whose
	// provider served another release's bytes. It names the same verdict the
	// version list reports as `source_mismatch`: the release is alive, its bytes
	// were wrong. A client that does not recognize the value still gets a
	// retryable terminal with the version-list hint in its message.
	sourceMismatchReasonV3 = "source_mismatch"
	// seekReanchorEvidenceUnavailableReasonV3 is the retryable transport reason
	// for a seek reanchor whose frozen candidate's own evidence and installation
	// owner cannot be established from the session. The seek fails closed rather
	// than replaying the frozen recipe through a moved binding's owner.
	seekReanchorEvidenceUnavailableReasonV3 = "seek_reanchor_evidence_unavailable"
	// candidateSourceDecodeRejectedReasonV3 is the internal transport reason for
	// a local generation whose decoder already rejected the source. It is never
	// a client terminal: the start/replan rotation loop consumes it to substitute
	// another provider candidate, and exhaustion persists sourceDecodeFailedReasonV3.
	candidateSourceDecodeRejectedReasonV3  = "candidate_source_decode_rejected"
	candidateSourceDecodeRejectedMessageV3 = "The selected media source was rejected by the video decoder."
	effectiveSourceUnavailableMessageV3    = "The effective media source is unavailable."
	remuxSeekPositionFailureMessageV3      = "failed to resolve remux seek position"
	replanVirtualInputTimeoutMessageV3     = "Virtual stream input resolution timed out during replan."
	// sourceDecodeFailedReasonV3 is the durable terminal reason for a decode
	// rejection that could not be recovered, matching the media-route
	// X-Vio-Decode-Error code clients already classify on.
	sourceDecodeFailedReasonV3 = playback.DecodeErrorSourceRejectedCode
	// sourceDecodeFailedMessageV3 is the user-facing text for
	// sourceDecodeFailedReasonV3, shared by the start terminal and the replan
	// paths so they cannot drift.
	sourceDecodeFailedMessageV3 = "The selected media source could not be decoded."
	// transportStartupReadyV3 is the "outcome" of a transport startup whose
	// first manifest became ready.
	transportStartupReadyV3 = "ready"
	// Substitution reasons published on a plan when the effective release
	// differs from the requested one. They are additive wire values; a client
	// that does not recognize one renders the generic substitution notice.
	substitutionReasonDeadReleaseV3     = "dead_release"
	substitutionReasonListingFailedV3   = "listing_failed"
	substitutionReasonTransportFailedV3 = "transport_failed"
	substitutionReasonDecodeRejectedV3  = "decode_rejected"
	// substitutionReasonVersionNotPlayableV3 names a requested release the
	// planner refused: the release exists and the provider lists it, but its
	// dynamic range, codec, or quality cannot be delivered to this client. The
	// auto fallback replaced it; an explicit pick refuses instead.
	substitutionReasonVersionNotPlayableV3 = "version_not_playable"
	substitutionReasonUnknownV3            = "unknown"
	// Failed capability fetches are memoized briefly so an unreachable node
	// costs one timeout per window instead of one per planning request.
	v3NodeCapabilityErrorTTL = 15 * time.Second
	// Planning degrades around slow pooled nodes. Full cold-probe budgets are
	// reserved for transport-time validation of the executor already selected.
	v3NodeCapabilityPlanTimeout = 5 * time.Second
	// Older or not-yet-contacted nodes cannot advertise their effective probe
	// matrix. This conservative cold-fetch bound avoids borrowing the central
	// server's unrelated hardware configuration.
	remoteNodeProbeFallbackTimeout = 2 * time.Minute
	// The node-recipe handoff is restart insurance written while the client is
	// blocked on the start response, so a stalled store must lose the insurance
	// rather than the start. Matches the jellycompat handoff's budget.
	nodeRecipeWriteTimeoutV3            = 2 * time.Second
	playbackStartSideEffectsTimeoutV3   = 15 * time.Second
	playbackStartSideEffectsWorkersV3   = 4
	playbackStartSideEffectsQueueSizeV3 = 256
	playbackCapabilityWarmupWorkersV3   = 4
	requestIDLogKeyV3                   = "request_id"
	bandwidthEstimateLogKeyV3           = "bandwidth_estimate_kbps"
	linkDownstreamLogKeyV3              = "link_downstream_kbps"
	playbackLogValueV3                  = "playback"
	playbackRemoteOutcomeFailedV3       = "failed"
	routeCapabilityUnavailableReasonV3  = "route_capability_unavailable"
)

// playbackStartRetryAfterSeconds is the Retry-After a retryable start failure
// advertises: long enough for a saturated route or a stalled provider to
// settle, short enough that the client retries promptly.
const playbackStartRetryAfterSeconds = 5

// playbackDependencyUnavailableCode is the v1 error code for a temporary
// playback dependency failure; the v2 catalog maps it to the 503
// dependency_unavailable problem.
const playbackDependencyUnavailableCode = "dependency_unavailable"

// v3NodeCapabilityRefreshInterval is both how often the background refresher
// revisits pooled nodes and the freshness margin at which it re-probes one: a
// quarter of the TTL keeps a healthy node warmed ahead of expiry, so a plan-time
// read is a cache hit, without re-probing a node that just answered. It
// collapses with a planning-triggered lazy refresh through the same per-node
// singleflight slot (claimCapabilityRefreshLockedV3).
const v3NodeCapabilityRefreshInterval = v3NodeCapabilityTTL / 4

// copySeekAnchorSeekResolveTimeout is the floor for one copy-video seek-anchor
// resolve (the pinned-relay lookup and the FFmpeg keyframe probe) on a start
// that seeks. A healthy probe costs ~1.8s, so a link that answers inside this
// budget behaves exactly as it did when the budget was fixed; a link whose own
// recent provider round trips exceeded it is extended instead, up to
// copySeekAnchorSeekResolveTimeoutCap (see copySeekAnchorResolveBudget). The
// floor is a var so tests can shorten it. It only shortens work on a seek
// start: a caller with no seek position never resolves an anchor. When it
// elapses the failure is classified as a retryable seek error, not evidence
// against the release, so the transport is not rotated or torn down over it.
var copySeekAnchorSeekResolveTimeout = 5 * time.Second

// copySeekProbeRetryMargin is the slack one anchor attempt keeps beyond its own
// window, so process startup, the probe-slot wait, and result handling never
// spend the caller's last budget. It is a var so tests can shorten it alongside
// copySeekAnchorSeekResolveTimeout.
var copySeekProbeRetryMargin = 5 * time.Second

// seekAnchorFirstOutputWindowV3 is the wall-clock window a progressive-remux
// seek has to put a first media byte on screen. A progressive remux is one
// forward-only response with no segment layer to wait or restart for the
// target, so an anchor probe that spends this whole window leaves nothing for
// FFmpeg to spawn and answer in: the viewer sees the failed target with no
// media, and the retry card arrives only after 15-25 s (production:
// 15.0 s and 24.9 s). Eight seconds is longer than a healthy seek start
// (spawn 1.6-3 s plus the first segment) and still keeps a failed seek inside
// a few seconds instead of the probe's own 15 s cap.
//
// A segmented HLS seek is deliberately excluded: its segment layer waits or
// restarts for the requested segment, so it keeps the adaptive probe budget
// (see prepareTransportTimelineV3).
var seekAnchorFirstOutputWindowV3 = 8 * time.Second

// seekAnchorTransportReserveV3 is the share of the first-output window a
// rebuilt progressive-remux transport needs for spawn and its first byte
// (production: 1.6-3 s). The anchor probe may spend only what is left, so a
// successful probe still leaves time for output and a failed one answers the
// retryable seek verdict at the probe bound instead of at the transport's
// no-byte timeout.
var seekAnchorTransportReserveV3 = 3 * time.Second

// seekAnchorProgressiveProbeWindowV3 is the time a progressive-remux seek may
// spend resolving its copy-video anchor. It is what remains of the seek's
// first-output window after the transport's own share, so it is the window
// that decides the probe budget, not CopySeekProbeTimeout.
func seekAnchorProgressiveProbeWindowV3() time.Duration {
	window := seekAnchorFirstOutputWindowV3 - seekAnchorTransportReserveV3
	if window <= 0 {
		return 0
	}
	return window
}

type serverBitrateCapContextKeyV3 struct{}

func withServerBitrateCapV3(ctx context.Context, capKbps int) context.Context {
	return context.WithValue(ctx, serverBitrateCapContextKeyV3{}, capKbps)
}

func serverBitrateCapV3(ctx context.Context) int {
	cap, _ := ctx.Value(serverBitrateCapContextKeyV3{}).(int)
	return cap
}

// requestBitrateCapV3 is the administrator's stream bitrate ceiling for the
// caller's network location, or 0 when none applies. A start persists it with
// the attempt; the connection endpoint reports Auto's budget under it.
func requestBitrateCapV3(ctx context.Context) int {
	scope, ok := access.GetScope(ctx)
	if !ok {
		return 0
	}
	return streamlocation.BitrateCap(ctx, scope.MaxLocalStreamBitrateKbps, scope.MaxRemoteStreamBitrateKbps)
}

var errSubtitleStoreUnavailableV3 = errors.New("subtitle store unavailable")

// terminalSessionExpiredV3 tells a client its attempt's session has ended and
// it should mint a new attempt.
const terminalSessionExpiredV3 = "session_expired"

type v3NodeCapabilityCache struct {
	transformations     []playback.TransformationV3
	transportFeatures   []string
	toneMapCapabilities tonemap.Capabilities
	err                 error
	expiresAt           time.Time
}

type preparedTransportV3 struct {
	url                string
	nodeURL            string
	transportID        string
	hwAccel            string
	toneMapMode        tonemap.Mode
	routingWorkload    noderouting.Workload
	routingExecution   noderouting.Execution
	routingExecutorID  int
	routingExecutorURL string
	routingEgress      noderouting.Egress
	routingEgressID    int
	routingEgressURL   string
	commit             func() *transportErrorV3
	rollback           func()
	rollbackRequired   func() error
	applySession       func() (func() error, error)
	afterDurableCommit func()
	// sessionRecipe is the recovery card of a header-authenticated transport,
	// recorded once the transport commits (recordSessionRecipeV3). Nil for a
	// token-bearing attempt, whose URLs carry the recipe themselves.
	sessionRecipe *playback.RecipeCard
}

type preparedTimelineV3 struct {
	seekSeconds            float64
	streamOriginSeconds    float64
	startSegmentNumber     int
	copySeekAnchorResolved bool
}

type playbackStartTimingsV3 struct {
	started time.Time
	last    time.Time
	attrs   []any
}

func newPlaybackStartTimingsV3() *playbackStartTimingsV3 {
	now := time.Now()
	return &playbackStartTimingsV3{started: now, last: now}
}

func (t *playbackStartTimingsV3) mark(stage string) {
	now := time.Now()
	t.attrs = append(t.attrs, stage+"_ms", now.Sub(t.last).Milliseconds())
	t.last = now
}

func (t *playbackStartTimingsV3) log(ctx context.Context, attemptID string) {
	attrs := []any{
		logComponentKey, playbackLogValueV3,
		requestIDLogKeyV3, chimw.GetReqID(ctx),
		"playback_attempt_id", attemptID,
		"total_ms", time.Since(t.started).Milliseconds(),
	}
	attrs = append(attrs, t.attrs...)
	slog.InfoContext(ctx, "protocol v3 start timing", attrs...)
}

// The phases added after the original split, named as constants so their
// repeated use at the recorder call sites stays a single source of truth.
const (
	transportPhasePersistV3    = "transport_persist"
	transportPhaseAuthorityV3  = "transport_authority"
	transportPhaseCapabilityV3 = "transport_capability"
	transportPhaseManifestV3   = "transport_manifest"
)

// transportPhaseNamesV3 is the fixed, ordered phase set the transport commit is
// split into. Ordering keeps the emitted attributes stable across requests so a
// log processor can diff two starts field by field.
var transportPhaseNamesV3 = []string{
	"transport_anchor",
	"transport_upstream",
	"transport_queue",
	"transport_route",
	"transport_spawn",
	"transport_readiness",
	"transport_remote_ready",
	// The durable-write, authority-write, capability-probe, and manifest-poll
	// spans below were left dark by the original split: they are on the start
	// critical path but no phase named them. Appended after the original set so
	// the existing fields keep their position and a log processor can diff.
	transportPhasePersistV3,
	transportPhaseAuthorityV3,
	transportPhaseCapabilityV3,
	transportPhaseManifestV3,
}

// transportPhaseTimingsV3 accumulates the wall time of the transport-commit
// sub-phases for one start request. It exists because the composite
// session_transport_commit mark covers everything startPlannedPlaybackV3 does,
// so a multi-second stall cannot be attributed to the anchor probe, the
// upstream reopen, a serialized wait, FFmpeg spawn, or first-manifest readiness.
// Each recorder observes only the span it names, so the phases never
// double-count and sum to at most the commit's wall time.
//
// A mutex guards the map: transport preparation is synchronous on the start
// path today, but a phase recorded from a detached helper (a warm, a retry)
// must not race the reader.
type transportPhaseTimingsV3 struct {
	mu     sync.Mutex
	phases map[string]time.Duration
}

type transportPhaseTimingsKeyV3 struct{}

// transportPhaseSuppressKeyV3 marks a context subtree whose phase writes are
// discarded. The pooled capability fan-out installs it on the context shared by
// its concurrent workers: each worker records its own duration, so parallel
// spans would sum additively past the wall time the route span can net out. The
// batch records its own wall time once instead, while standalone callers keep
// their per-lookup recording.
type transportPhaseSuppressKeyV3 struct{}

// withoutTransportPhaseRecordingV3 returns ctx marked so phase writes on it and
// its descendants are no-ops. A nil ctx stays nil.
func withoutTransportPhaseRecordingV3(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, transportPhaseSuppressKeyV3{}, true)
}

// transportPhaseRecordingSuppressedV3 reports whether ctx belongs to a subtree
// whose phase writes are discarded.
func transportPhaseRecordingSuppressedV3(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	suppressed, _ := ctx.Value(transportPhaseSuppressKeyV3{}).(bool)
	return suppressed
}

// withTransportPhaseTimingsV3 attaches a fresh phase recorder to ctx and
// returns it. A second call replaces the recorder so a nested or retried start
// measures its own commit rather than accumulating into an outer one.
func withTransportPhaseTimingsV3(ctx context.Context) (context.Context, *transportPhaseTimingsV3) {
	recorder := &transportPhaseTimingsV3{phases: make(map[string]time.Duration, len(transportPhaseNamesV3))}
	return context.WithValue(ctx, transportPhaseTimingsKeyV3{}, recorder), recorder
}

// transportPhaseTimingsFromV3 returns the recorder attached by
// withTransportPhaseTimingsV3, or nil when the caller did not install one. A nil
// recorder makes every record a no-op, so uninstalled call sites pay nothing.
func transportPhaseTimingsFromV3(ctx context.Context) *transportPhaseTimingsV3 {
	if ctx == nil {
		return nil
	}
	recorder, _ := ctx.Value(transportPhaseTimingsKeyV3{}).(*transportPhaseTimingsV3)
	return recorder
}

// recordTransportPhaseV3 adds the wall time since start to the named phase on
// the context's recorder. It is a no-op without an installed recorder and on a
// subtree marked by withoutTransportPhaseRecordingV3.
func recordTransportPhaseV3(ctx context.Context, phase string, start time.Time) {
	if transportPhaseRecordingSuppressedV3(ctx) {
		return
	}
	recorder := transportPhaseTimingsFromV3(ctx)
	if recorder == nil {
		return
	}
	recorder.mu.Lock()
	recorder.phases[phase] += time.Since(start)
	recorder.mu.Unlock()
}

// addTransportPhaseDurationV3 adds an already-measured duration to the named
// phase. It lets a caller record an enclosing span net of a span recorded inside
// it (spawn inside the hw_accel=auto walk) so the phases never overlap.
func addTransportPhaseDurationV3(ctx context.Context, phase string, d time.Duration) {
	if transportPhaseRecordingSuppressedV3(ctx) {
		return
	}
	recorder := transportPhaseTimingsFromV3(ctx)
	if recorder == nil {
		return
	}
	if d < 0 {
		d = 0
	}
	recorder.mu.Lock()
	recorder.phases[phase] += d
	recorder.mu.Unlock()
}

// recordTransportPhaseNetOfV3 records the wall time since start under outer,
// net of any time inner accumulated during the same span. A caller passes the
// inner total it read before the span began so the enclosing phase stays
// disjoint from the phase that names work it performed inline. The subtraction
// floors at zero (addTransportPhaseDurationV3 clamps a negative duration), so a
// clock or ordering surprise can only report a zero outer span; it never emits
// a negative millisecond.
func recordTransportPhaseNetOfV3(ctx context.Context, outer string, start time.Time, inner string, innerBefore time.Duration) {
	innerDelta := transportPhaseDurationV3(ctx, inner) - innerBefore
	addTransportPhaseDurationV3(ctx, outer, time.Since(start)-innerDelta)
}

// transportPhaseDurationV3 reads the wall time accumulated for one phase on the
// context's recorder, or zero without one. A caller uses it to subtract a span
// recorded inside an enclosing one.
func transportPhaseDurationV3(ctx context.Context, phase string) time.Duration {
	recorder := transportPhaseTimingsFromV3(ctx)
	if recorder == nil {
		return 0
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.phases[phase]
}

// transportPhaseAttrsV3 snapshots a recorder and returns the ordered phase
// attributes every transport phase line emits. A missing recorder (or an
// unmeasured phase) emits a zero, which reads as "this attempt did not pay it" —
// the same shape the other timing fields use. Shared by the composite start line
// and the separate retry line so both carry the same phase set in the same order.
func transportPhaseAttrsV3(recorder *transportPhaseTimingsV3) []any {
	if recorder == nil {
		return nil
	}
	recorder.mu.Lock()
	snapshot := make(map[string]time.Duration, len(recorder.phases))
	for name, d := range recorder.phases {
		snapshot[name] = d
	}
	recorder.mu.Unlock()
	attrs := make([]any, 0, 2*len(transportPhaseNamesV3))
	for _, name := range transportPhaseNamesV3 {
		attrs = append(attrs, name+"_ms", snapshot[name].Milliseconds())
	}
	return attrs
}

// observeTransportPhasesV3 appends every transport sub-phase to the start timing
// line. It appends measured attributes directly rather than through mark, so a
// sub-phase cannot disturb the delta the enclosing session_transport_commit mark
// reports.
func (t *playbackStartTimingsV3) observeTransportPhasesV3(recorder *transportPhaseTimingsV3) {
	if t == nil {
		return
	}
	t.attrs = append(t.attrs, transportPhaseAttrsV3(recorder)...)
}

// logTransportRetryPhasesV3 emits a separate timing line for a retried start
// attempt: the same-release transient retry or an alternate-candidate walk. The
// retry is a second commit with its own phases, so it is logged on its own line
// with a distinct message instead of folding into the request's composite start
// line. It appends nothing to the composite timings, so no field on that line
// moves. This is additive observation only: no behavior or wire change.
func logTransportRetryPhasesV3(ctx context.Context, attemptID string, started time.Time, recorder *transportPhaseTimingsV3) {
	if recorder == nil {
		return
	}
	attrs := []any{
		logComponentKey, playbackLogValueV3,
		requestIDLogKeyV3, chimw.GetReqID(ctx),
		"playback_attempt_id", attemptID,
		"total_ms", time.Since(started).Milliseconds(),
	}
	attrs = append(attrs, transportPhaseAttrsV3(recorder)...)
	slog.InfoContext(ctx, "protocol v3 start retry timing", attrs...)
}

type playbackStartSideEffectsV3 struct {
	session   playback.Session
	file      models.MediaFile
	userID    int
	profileID string
	state     *playbackStartSideEffectsStateV3
	// legacyAudioTrackIndex is the audio an /api/v1 start played, which the
	// frozen bridge still saves as the series preference; nil on /api/v2.
	legacyAudioTrackIndex *int
}

type playbackStartSideEffectsStateV3 struct {
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	started       bool
	stopRequested bool
}

// mediaAuthModeV3 is the attempt's negotiated media transport mode: how a
// client-visible media URL authenticates, and therefore which origins may serve
// it. Both bits are resolved once from the attempt's (pinned) feature list and
// threaded down every branch rather than re-derived per URL builder.
type mediaAuthModeV3 struct {
	// headerAuth is header_authenticated_media_v1: no client-visible URL
	// carries a signed playback credential, and the client authenticates every
	// media request with its own access token instead.
	headerAuth bool
	// proxyEgress is authorized_media_origins_v1 negotiated on top of
	// headerAuth: the client also honors credential-free absolute URLs on
	// server-designated proxy origins, so media bytes need not all egress from
	// the API server. Never true without headerAuth — on a legacy attempt the
	// signed proxy URL already carries its own authority.
	proxyEgress bool
	// hlsVariableSubstitution is the hls delivery's
	// hls_variable_substitution_v1 feature on /api/v2: a playlist the server
	// rewrites (FFmpeg's real playlist, a relayed node playlist) may carry a
	// credential-sized query once through EXT-X-DEFINE. It changes playlist
	// text only, so it is not part of plan identity.
	hlsVariableSubstitution bool
}

// withHLSVariableSubstitutionV3 adds the request's playlist-substitution
// opt-in to mode. The frozen /api/v1 surface never negotiates it.
func withHLSVariableSubstitutionV3(r *http.Request, mode mediaAuthModeV3, req playback.StartRequestV3) mediaAuthModeV3 {
	mode.hlsVariableSubstitution = isNativeAPIV2(r.Context()) && playback.HLSVariableSubstitutionV3(req)
	return mode
}

// headerAuthenticatedMediaV3 resolves the negotiated media transport mode from
// a client's advertised feature set.
//
// The mode is a bounded value threaded from the v3 request decoder down through
// transport preparation and into the session's stream state, the same way local
// egress is. It deliberately carries no credential, and the durable normalized
// request stays the source of truth for the attempt.
func headerAuthenticatedMediaV3(clientFeatures []string) mediaAuthModeV3 {
	headerAuth := playback.HasFeatureV3(clientFeatures, playback.FeatureHeaderAuthenticatedMediaV3)
	return mediaAuthModeV3{
		headerAuth:  headerAuth,
		proxyEgress: headerAuth && playback.HasFeatureV3(clientFeatures, playback.FeatureAuthorizedMediaOriginsV3),
	}
}

// recipeCardStoreV3 is the shared control-plane store central keeps a recipe
// card in for another Silo process, or for itself after a restart
// (*noderecipe.Store). One instance owns one key space; the handler holds three
// of them, and what a stored card authorizes or rebuilds is the field's
// business, not the interface's — see PlaybackHandler.ProxyGrantStore,
// NodeRecipeStore and SessionRecipeStore.
type recipeCardStoreV3 interface {
	// Enabled reports whether the store can actually carry a card. A disabled
	// store accepts Put silently, so a URL that only a stored card can serve
	// must not be published without checking it.
	Enabled() bool
	// Get reads the card currently stored under key. A replan uses it to
	// remember the card it is about to overwrite, so a failed replacement can
	// hand the restored plan its authority back.
	Get(ctx context.Context, key string) (*playback.RecipeCard, bool)
	Put(ctx context.Context, key string, card playback.RecipeCard) error
	Delete(ctx context.Context, key string) error
}

type virtualPlanDecisionV3 struct {
	preferredAudioLanguage                        string
	candidateRank, candidateCount                 int
	substitutionReason, startAudioSelectionOrigin string
	effectiveVirtualOwner                         int
}
type dvRPUMemoKeyV3 struct {
	fileID              int
	size, mtimeUnixNano int64
}

type transportErrorV3 struct {
	reason    string
	message   string
	retryable bool
	cause     error
	// executorIneligible marks a route skipped because its executor
	// definitely cannot run the recipe, so nothing was attempted there.
	// combineTransportErrorsV3 lets an attempted route's verdict win over it.
	executorIneligible bool
	// seekFailure marks failures resolving the seek anchor, allowing callers to
	// retry the same release instead of rotating to an alternate candidate.
	seekFailure bool
}

func subtitleArtifactErrorV3(message string, cause error) *transportErrorV3 {
	return &transportErrorV3{
		reason:    subtitleUnavailableReasonV3,
		message:   message,
		retryable: errors.Is(cause, errSubtitleStoreUnavailableV3),
		cause:     cause,
	}
}

func wrapSubtitleStoreErrorV3(err error) error {
	return fmt.Errorf("%w: %w", errSubtitleStoreUnavailableV3, err)
}

type v3ReplanLock struct {
	mu   sync.Mutex
	refs int
}

type v3EventRate struct {
	windowStart time.Time
	count       int
}

type replacementAdmissionCheckerV3 interface {
	CheckReplacementAllowed(context.Context, string, playback.PlayMethod, bool) error
}

type replacementReservationCancellerV3 interface {
	CancelReplacementReservation(string)
}

type replacementStateManagerV3 interface {
	ApplyReplacement(string, playback.SessionReplacement) (playback.SessionReplacementRollback, error)
	RollbackReplacement(string, playback.SessionReplacementRollback) error
}

type sessionReservationReleaserV3 interface {
	ReleaseSession(string)
}

func (e *transportErrorV3) Error() string {
	if e.cause != nil {
		return e.reason + ": " + e.cause.Error()
	}
	return e.reason
}

func (h *PlaybackHandler) transformationRegistryV3(ctx context.Context) *playback.TransformationRegistryV3 {
	registry, _ := h.probedTransformationRegistryV3(ctx)
	return registry
}

// probedTransformationRegistryV3 also returns the error of a probe that just
// failed. That probe's registry reports what it couldn't read as unavailable,
// which a later probe may clear.
func (h *PlaybackHandler) probedTransformationRegistryV3(ctx context.Context) (*playback.TransformationRegistryV3, error) {
	h.v3RegistryMu.Lock()
	defer h.v3RegistryMu.Unlock()
	if !h.v3Registry.NeedsRefresh(time.Now()) {
		return h.v3Registry, nil
	}
	probe := playback.ProbeTransformationRegistryWithToneMapV3Result
	if h.v3RegistryProbe != nil {
		probe = h.v3RegistryProbe
	}
	registry, err := probe(context.WithoutCancel(ctx), h.playbackConfig().FFmpegPath, nil)
	if err == nil {
		h.v3Registry = registry
	}
	return registry, err
}

// v3LocalToneMapNegativeTTL bounds how long an incomplete-but-error-free local
// probe result is reused before the probe is retried. It mirrors the tonemap
// package's own probeNegativeTTL so a missing executor that hardware contention
// hid from one probe is retried on the same cadence there and here.
const v3LocalToneMapNegativeTTL = 15 * time.Second

// localToneMapCapabilitiesV3 returns a defensive copy of the capabilities
// validated for the current local FFmpeg, backend, and device configuration.
func (h *PlaybackHandler) localToneMapCapabilitiesV3(ctx context.Context) (tonemap.Capabilities, error) {
	capabilities, _, err := h.localToneMapCapabilitiesWithBackendV3(ctx)
	return capabilities, err
}

// localToneMapCapabilitiesWithBackendV3 is localToneMapCapabilitiesV3 plus the
// resolved hardware backend the probe ran against, which the caller needs to
// judge whether the inventory is complete.
func (h *PlaybackHandler) localToneMapCapabilitiesWithBackendV3(ctx context.Context) (tonemap.Capabilities, string, error) {
	cfg := h.playbackConfig()
	ffmpegPath := playback.ResolveFFmpegPath(cfg.FFmpegPath)
	hwDevice := strings.TrimSpace(cfg.HWDevice)
	resolved := playback.ResolveHWAccelWithFFmpegContext(ctx, cfg.HWAccel, cfg.FFmpegPath, hwDevice)
	if err := ctx.Err(); err != nil {
		return nil, resolved, err
	}
	probe := tonemap.Probe
	if h.v3ToneMapProbe != nil {
		probe = h.v3ToneMapProbe
	}
	capabilities, err := probe(ctx, ffmpegPath, resolved, hwDevice)
	return append(tonemap.Capabilities(nil), capabilities...), resolved, err
}

// localToneMapCapabilitiesCachedV3 returns the local tone-map inventory,
// probing at most once per process. It mirrors transformationRegistryV3: the
// local FFmpeg binary and hardware configuration are fixed for the process
// lifetime, so a complete, successful probe is reused for every later playback
// start.
//
// A failed probe is deliberately not cached, so a transient failure (a busy
// encoder, a timeout) is retried by the next caller instead of being frozen for
// the life of the process. An error-free but incomplete inventory is treated
// the same way for a shorter interval: the probe can report a nil error while a
// configured hardware executor is missing (temporary device contention, a
// driver that has not finished coming up), and a nil error alone must not freeze
// that verdict for the process lifetime. Incomplete results are reused only for
// v3LocalToneMapNegativeTTL so a recovery is picked up within one negative
// window without every caller re-running the smoke matrix in the meantime.
//
// The first caller still pays for the probe on its own context. The startup
// warmup is expected to have paid it already; when it has not, falling back to
// a live probe preserves the planning outcome a live probe would produce.
func (h *PlaybackHandler) localToneMapCapabilitiesCachedV3(ctx context.Context) (tonemap.Capabilities, error) {
	h.v3LocalToneMapMu.Lock()
	if h.v3LocalToneMapCached {
		capabilities := append(tonemap.Capabilities(nil), h.v3LocalToneMapCaps...)
		h.v3LocalToneMapMu.Unlock()
		return capabilities, nil
	}
	if now := time.Now(); !h.v3LocalToneMapNegativeUntil.IsZero() && now.Before(h.v3LocalToneMapNegativeUntil) {
		// An incomplete result is still being negative-cached: serve it without
		// re-running the probe so a burst of starts cannot hammer the encoder.
		capabilities := append(tonemap.Capabilities(nil), h.v3LocalToneMapCaps...)
		h.v3LocalToneMapMu.Unlock()
		return capabilities, nil
	}
	h.v3LocalToneMapMu.Unlock()

	capabilities, resolvedBackend, err := h.localToneMapCapabilitiesWithBackendV3(ctx)
	if err != nil {
		return capabilities, err
	}
	complete := tonemap.CapabilitiesComplete(capabilities, resolvedBackend)
	h.v3LocalToneMapMu.Lock()
	if h.v3LocalToneMapCached {
		// A concurrent complete probe already won the lifetime cache; keep its
		// inventory rather than overwriting it with this (possibly older) result.
		cached := append(tonemap.Capabilities(nil), h.v3LocalToneMapCaps...)
		h.v3LocalToneMapMu.Unlock()
		return cached, nil
	}
	h.v3LocalToneMapCaps = append(tonemap.Capabilities(nil), capabilities...)
	if complete {
		h.v3LocalToneMapCached = true
		h.v3LocalToneMapNegativeUntil = time.Time{}
	} else {
		h.v3LocalToneMapNegativeUntil = time.Now().Add(v3LocalToneMapNegativeTTL)
	}
	h.v3LocalToneMapMu.Unlock()
	return capabilities, nil
}

func (h *PlaybackHandler) localToneMapCapabilitiesForTransportV3(ctx context.Context) (tonemap.Capabilities, error) {
	// The transport-time local tone-map probe is a hardware walk that can take
	// seconds on a cold device set. It was uncovered by the original split, so
	// name it as its own capability phase on the request recorder. The startup
	// warmup calls the cached variant on a context without a recorder and is
	// unaffected.
	defer recordTransportPhaseV3(ctx, transportPhaseCapabilityV3, time.Now())
	probeCtx, cancel := context.WithTimeout(ctx, h.localToneMapProbeTimeoutV3())
	defer cancel()
	return h.localToneMapCapabilitiesV3(probeCtx)
}

func (h *PlaybackHandler) localToneMapProbeTimeoutV3() time.Duration {
	cfg := h.playbackConfig()
	// The whole read, not its tone-map half: localToneMapCapabilitiesV3 resolves
	// the backend first, and on Linux that is a full hardware walk whose cost
	// scales with the configured device set.
	return playback.CapabilityEndpointTimeout(cfg.HWAccel, cfg.HWDevice)
}

// remoteToneMapProbeTimeoutV3 returns how long to allow one capability read of
// a node, from the budget that node last advertised.
//
// The budget is kept apart from the inventory because it describes the node
// rather than its hardware, and the two are invalidated for different reasons.
// An acceleration change makes the inventory wrong and the matrix cold — which
// is exactly when the next read is slowest — while how long that node takes to
// answer has not changed. Storing them together meant an invalidation dropped
// the budget with the inventory and the refresh it triggered fell back to two
// minutes, short of the ~151 seconds a two-device node legitimately asks for,
// so protocol-v3 planning lost its inventory precisely after an invalidation.
func (h *PlaybackHandler) remoteToneMapProbeTimeoutV3(nodeURL string) time.Duration {
	nodeURL = nodepool.NormalizeNodeURL(nodeURL)
	h.v3NodeCapabilitiesMu.Lock()
	budget := h.v3NodeProbeBudgets[nodeURL]
	h.v3NodeCapabilitiesMu.Unlock()
	// Never less than what this node currently describes, whatever was learned
	// from it before.
	//
	// A learned budget is kept across invalidations on purpose — an invalidation
	// is the moment the next read is coldest and slowest, so dropping the budget
	// with the inventory would fall back to a figure short of what the node
	// needs. But a per-node policy edit invalidates through the same path, and
	// widening hw_device_override is precisely a change that makes the learned
	// number too small: the node reloads, walks four devices instead of one, and
	// gets canceled at the one-device deadline. Nothing recovers from that on its
	// own, because a budget is only ever learned from a read that completes.
	if cold := h.coldNodeProbeTimeoutV3(nodeURL); cold > budget {
		return cold
	}
	return budget
}

// coldNodeProbeTimeoutV3 prices one capability read of a node this process has
// not read successfully yet, from that node rather than from a cluster-wide
// guess. Without a pooled record — a planner that cannot look nodes up, or a
// URL that is no longer in the pool — the cluster's own policy is the closest
// description available.
func (h *PlaybackHandler) coldNodeProbeTimeoutV3(nodeURL string) time.Duration {
	cfg := h.playbackConfig()
	var node *nodepool.Node
	if lookup, ok := h.NodePlanner.(transcodeNodeLookupV3); ok {
		if found, ok := lookup.TranscodeNodeByURL(nodeURL); ok {
			node = found
		}
	}
	if node == nil {
		if lookup, ok := h.NodePlanner.(proxyNodeLookupV3); ok {
			if found, ok := lookup.ProxyNodeByURL(nodeURL); ok {
				node = found
			}
		}
	}
	return playback.ColdCapabilityRequestTimeout(
		node.StoredCapabilities(),
		node.EffectiveHWAccel(cfg.HWAccel),
		node.EffectiveHWDevice(cfg.HWDevice),
		remoteNodeProbeFallbackTimeout,
	)
}

// transcodeNodeLookupV3 resolves the pooled record behind a transcode node URL,
// which carries that node's stored capability report and its acceleration
// override. Optional, like the planner itself: without it this path falls back
// to the cluster-wide setting. *nodepool.Planner implements it.
type transcodeNodeLookupV3 interface {
	TranscodeNodeByURL(nodeURL string) (*nodepool.Node, bool)
}

type proxyNodeLookupV3 interface {
	ProxyNodeByURL(nodeURL string) (*nodepool.Node, bool)
}

// rememberNodeProbeBudgetV3 records what a node says its capability read costs.
// Callers must hold v3NodeCapabilitiesMu.
func (h *PlaybackHandler) rememberNodeProbeBudgetLockedV3(nodeURL string, budget time.Duration) {
	if budget <= 0 {
		return
	}
	if h.v3NodeProbeBudgets == nil {
		h.v3NodeProbeBudgets = make(map[string]time.Duration)
	}
	h.v3NodeProbeBudgets[nodeURL] = budget
}

func (h *PlaybackHandler) toneMapPlanningTimeoutV3(localFallbackAllowed bool) time.Duration {
	if localFallbackAllowed {
		return v3NodeCapabilityPlanTimeout
	}
	return remoteNodeProbeFallbackTimeout
}

// remoteTransformationsV3 is the transport-time capability lookup for a
// selected node. It never trusts memoized failures: those may be planning
// deadlines far shorter than this path's fetch budget, and rejecting the
// already-selected node on a stale planning timeout would fail a start the
// fetch could still validate.
func (h *PlaybackHandler) remoteTransformationsV3(ctx context.Context, nodeURL string) ([]playback.TransformationV3, error) {
	return h.lookupRemoteTransformationsV3(ctx, nodeURL, false)
}

// remoteTransformationsPlanningV3 is the planning-time variant: it honors
// negatively-cached fetch failures so an unreachable node costs one timeout
// per error-TTL window instead of one per playback start.
func (h *PlaybackHandler) remoteTransformationsPlanningV3(ctx context.Context, nodeURL string) ([]playback.TransformationV3, error) {
	return h.lookupRemoteTransformationsV3(ctx, nodeURL, true)
}

// lookupRemoteTransformationsV3 returns a node's cached or freshly fetched transformation inventory.
func (h *PlaybackHandler) lookupRemoteTransformationsV3(ctx context.Context, nodeURL string, honorCachedFailure bool) ([]playback.TransformationV3, error) {
	entry, err := h.lookupRemoteCapabilitiesV3(ctx, nodeURL, honorCachedFailure)
	if err != nil {
		return nil, err
	}
	return append([]playback.TransformationV3(nil), entry.transformations...), nil
}

// lookupRemoteCapabilitiesV3 fetches and jointly caches a node's transformation
// and tone-map inventory, optionally reusing short-lived fetch failures.
func (h *PlaybackHandler) lookupRemoteCapabilitiesV3(ctx context.Context, nodeURL string, honorCachedFailure bool) (v3NodeCapabilityCache, error) {
	// A remote capability read is a node round-trip on the start path that the
	// original split left dark. Record it as its own phase wherever the request
	// carries a recorder; a background refresh or warmup runs on a context
	// without one, so it pays nothing and stays out of the request's phases.
	defer recordTransportPhaseV3(ctx, transportPhaseCapabilityV3, time.Now())
	// Canonical here and in RefreshNodeCapabilitiesV3, which are the two ways
	// into these maps; everything below is reached from one of them and so is
	// already keyed the same way.
	nodeURL = nodepool.NormalizeNodeURL(nodeURL)
	now := time.Now()
	h.v3NodeCapabilitiesMu.Lock()
	entry, ok := h.v3NodeCapabilities[nodeURL]
	h.v3NodeCapabilitiesMu.Unlock()
	if ok && now.Before(entry.expiresAt) {
		if entry.err == nil {
			return entry, nil
		}
		if honorCachedFailure {
			return v3NodeCapabilityCache{}, entry.err
		}
	}
	if ok && entry.err == nil && honorCachedFailure {
		// A successful inventory is safe for planning after its freshness window:
		// transport-time validation still refreshes before relying on it, while
		// planning can use the stale snapshot and refresh behind the request. This
		// keeps sparse traffic from paying a multi-second node probe every minute.
		h.refreshRemoteCapabilitiesV3(nodeURL)
		return entry, nil
	}

	// An invalidation landing mid-fetch means the report this probe is about to
	// return describes the node as it was, not as it is — and this caller is
	// about to select transformations and a tone-map executor from it. Re-probe
	// rather than plan on it.
	//
	// Bounded at two attempts, and the second result is used even if it is
	// overtaken too. Failing the request instead would be worse than a slightly
	// stale inventory: most hash changes are not "the hardware went away" — a
	// driver update, a new identity field, a raised probe budget all move it —
	// so refusing would reject playback that the report in hand describes
	// perfectly well, and on a single-transcode-node deployment there is nothing
	// to fall back to. A node changing faster than two probes can read it is a
	// different problem, and one this path cannot fix by failing.
	var (
		info        playback.HWAccelInfo
		err         error
		completedAt time.Time
		overtaken   bool
	)
	for attempt := range v3CapabilityFetchAttempts {
		// Snapshot the invalidation count before probing: anything this fetch
		// learns describes the node as it was when the request left.
		invalidations := h.nodeCapabilityInvalidationsV3(nodeURL)
		requestCtx, cancel := context.WithTimeout(ctx, h.remoteToneMapProbeTimeoutV3(nodeURL))
		info, err = fetchRemoteTranscodeCapabilities(requestCtx, nodeURL, h.JWTSecret)
		cancel()
		nodepool.ReportConnectFailure(h.NodePlanner, nodeURL, err)
		completedAt = time.Now()
		overtaken = h.nodeCapabilityInvalidationsV3(nodeURL) != invalidations
		if !overtaken || attempt+1 == v3CapabilityFetchAttempts {
			break
		}
	}

	if err != nil {
		h.v3NodeCapabilitiesMu.Lock()
		if h.v3NodeCapabilities == nil {
			h.v3NodeCapabilities = make(map[string]v3NodeCapabilityCache)
		}
		if overtaken {
			h.v3NodeCapabilitiesMu.Unlock()
			return v3NodeCapabilityCache{}, err
		}
		if current, currentOK := h.v3NodeCapabilities[nodeURL]; currentOK && current.err == nil && completedAt.Before(current.expiresAt) {
			h.v3NodeCapabilitiesMu.Unlock()
			return current, nil
		}
		h.v3NodeCapabilities[nodeURL] = v3NodeCapabilityCache{err: err, expiresAt: completedAt.Add(v3NodeCapabilityErrorTTL)}
		h.v3NodeCapabilitiesMu.Unlock()
		return v3NodeCapabilityCache{}, err
	}
	entry = v3NodeCapabilityCache{
		transformations:     append([]playback.TransformationV3(nil), info.Transformations...),
		transportFeatures:   append([]string(nil), info.TransportFeatures...),
		toneMapCapabilities: append(tonemap.Capabilities(nil), info.ToneMapCapabilities...),
		expiresAt:           completedAt.Add(v3NodeCapabilityTTL),
	}
	h.v3NodeCapabilitiesMu.Lock()
	// Recorded even when the entry below is discarded as overtaken: what the
	// node says its read costs is true regardless of whether this particular
	// answer is still current.
	h.rememberNodeProbeBudgetLockedV3(nodeURL,
		playback.NormalizeProbeRequestTimeout(info.ProbeRequestTimeoutMillis, remoteNodeProbeFallbackTimeout))
	if overtaken {
		// Still overtaken after the retry. Hand the result to this caller, which
		// has nothing better, but leave the cache empty so the next lookup
		// re-probes instead of serving it for a full TTL to everyone else.
		h.v3NodeCapabilitiesMu.Unlock()
		return entry, nil
	}
	if h.v3NodeCapabilities == nil {
		h.v3NodeCapabilities = make(map[string]v3NodeCapabilityCache)
	}
	h.v3NodeCapabilities[nodeURL] = entry
	h.v3NodeCapabilitiesMu.Unlock()
	return entry, nil
}

// v3CapabilityFetchAttempts is how many times one lookup will re-probe a node
// whose capabilities changed while it was being read.
const v3CapabilityFetchAttempts = 2

func (h *PlaybackHandler) nodeCapabilityInvalidationsV3(nodeURL string) uint64 {
	h.v3NodeCapabilitiesMu.Lock()
	defer h.v3NodeCapabilitiesMu.Unlock()
	return h.v3NodeCapabilityInvalidations[nodeURL]
}

// RefreshNodeCapabilitiesV3 discards one node's cached capability inventory and
// re-probes it in the background. It exists for the node health sweep, which
// learns from a node's advertised capability hash that its hardware changed
// long before this cache's freshness window would expire — and a cache that
// outlives the hardware it describes plans transcodes onto a GPU that is gone.
func (h *PlaybackHandler) RefreshNodeCapabilitiesV3(nodeURL string) {
	if h == nil || nodeURL == "" {
		return
	}
	// Every map here is keyed by the node's canonical address. This entry point
	// is reached from the admin route with the URL exactly as the row stores it,
	// which may carry a trailing slash the pools have already dropped — and then
	// the entry this deletes is not the entry planning reads, so a node keeps
	// serving the backend it was just moved off until the old key expires.
	nodeURL = nodepool.NormalizeNodeURL(nodeURL)
	h.v3NodeCapabilitiesMu.Lock()
	delete(h.v3NodeCapabilities, nodeURL)
	if h.v3NodeCapabilityInvalidations == nil {
		h.v3NodeCapabilityInvalidations = make(map[string]uint64)
	}
	// Bumping the count is what makes the delete stick. A refresh already in
	// flight fetched this node before its hardware changed, so it must neither
	// re-install its answer over this delete nor be mistaken for the re-probe
	// this invalidation is owed.
	h.v3NodeCapabilityInvalidations[nodeURL]++
	claimed := h.claimCapabilityRefreshLockedV3(nodeURL)
	h.v3NodeCapabilitiesMu.Unlock()
	if claimed {
		h.runCapabilityRefreshV3(nodeURL)
	}
}

func (h *PlaybackHandler) refreshRemoteCapabilitiesV3(nodeURL string) {
	if h == nil || nodeURL == "" {
		return
	}
	h.v3NodeCapabilitiesMu.Lock()
	claimed := h.claimCapabilityRefreshLockedV3(nodeURL)
	h.v3NodeCapabilitiesMu.Unlock()
	if claimed {
		h.runCapabilityRefreshV3(nodeURL)
	}
}

// claimCapabilityRefreshLockedV3 takes the node's single background-refresh
// slot. The caller must hold v3NodeCapabilitiesMu: taking the slot under the
// same lock as the invalidation counter is what guarantees that an invalidation
// either starts a refresh or is seen by the one already running.
func (h *PlaybackHandler) claimCapabilityRefreshLockedV3(nodeURL string) bool {
	if _, inFlight := h.v3NodeCapabilityRefresh[nodeURL]; inFlight {
		return false
	}
	if h.v3NodeCapabilityRefresh == nil {
		h.v3NodeCapabilityRefresh = make(map[string]struct{})
	}
	h.v3NodeCapabilityRefresh[nodeURL] = struct{}{}
	return true
}

// runCapabilityRefreshV3 probes one node in the background until its result is
// current: a probe that an invalidation overtook was discarded, so it repeats
// rather than leave the cache empty until the next viewer pays for a probe.
func (h *PlaybackHandler) runCapabilityRefreshV3(nodeURL string) {
	go func() {
		for {
			invalidations := h.nodeCapabilityInvalidationsV3(nodeURL)
			ctx, cancel := context.WithTimeout(context.Background(), h.remoteToneMapProbeTimeoutV3(nodeURL))
			_, err := h.lookupRemoteCapabilitiesV3(ctx, nodeURL, false)
			cancel()
			if err != nil {
				slog.Debug("protocol v3 background node capability refresh failed", "component", "api", "node", logredact.SanitizeURL(nodeURL), "error", err)
			}
			h.v3NodeCapabilitiesMu.Lock()
			current := h.v3NodeCapabilityInvalidations[nodeURL] == invalidations
			if current {
				delete(h.v3NodeCapabilityRefresh, nodeURL)
			}
			h.v3NodeCapabilitiesMu.Unlock()
			if current {
				return
			}
		}
	}()
}

// StartCapabilityWarmupV3 moves local and pooled-node capability discovery off
// the first viewer's start request. It is best effort: failed probes remain
// retryable through the ordinary lookup path and never prevent API startup.
func (h *PlaybackHandler) StartCapabilityWarmupV3(ctx context.Context) {
	if h == nil || ctx == nil {
		return
	}
	go h.warmPlaybackCapabilitiesV3(ctx)
	h.startNodeCapabilityRefresherV3(ctx)
}

// startNodeCapabilityRefresherV3 keeps the pooled-node capability cache warm so
// plan-time reads are cache hits instead of node round-trips. The startup
// warmup populates the cache once; this refreshes each node before its TTL
// lapses, so sparse or bursty traffic never meets a cold entry. It is started
// once and stops with the application context.
func (h *PlaybackHandler) startNodeCapabilityRefresherV3(ctx context.Context) {
	h.v3RefresherOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(v3NodeCapabilityRefreshInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					h.refreshStaleNodeCapabilitiesV3()
				}
			}
		}()
	})
}

// refreshStaleNodeCapabilitiesV3 re-probes every pooled node whose cached
// capabilities are missing, failed, or close enough to expiry that they would
// lapse before the next sweep. Each refresh claims the node's existing
// singleflight slot, so it collapses with a planning-triggered lazy refresh
// rather than running beside it. A node whose entry is still comfortably fresh
// is left alone.
func (h *PlaybackHandler) refreshStaleNodeCapabilitiesV3() {
	now := time.Now()
	for _, nodeURL := range h.nodeCapabilityURLsV3() {
		if h.nodeCapabilityExpiringV3(nodeURL, now) {
			h.refreshRemoteCapabilitiesV3(nodeURL)
		}
	}
}

// nodeCapabilityURLsV3 lists every pooled node whose capabilities planning or
// transport may read, transcode and proxy alike. A planner that enumerates
// neither pool contributes none.
func (h *PlaybackHandler) nodeCapabilityURLsV3() []string {
	var nodeURLs []string
	if enumerator, ok := h.NodePlanner.(transcodeNodeEnumeratorV3); ok {
		nodeURLs = append(nodeURLs, enumerator.TranscodeNodeURLs()...)
	}
	if enumerator, ok := h.NodePlanner.(proxyNodeEnumeratorV3); ok {
		nodeURLs = append(nodeURLs, enumerator.ProxyNodeURLs()...)
	}
	return nodeURLs
}

// nodeCapabilityExpiringV3 reports whether a node's cached inventory should be
// refreshed now. A missing or failed entry is always due; a successful entry is
// due once it is within one refresh interval of expiry, which keeps the 1min
// TTL semantics while moving the probe off the plan-time path.
func (h *PlaybackHandler) nodeCapabilityExpiringV3(nodeURL string, now time.Time) bool {
	nodeURL = nodepool.NormalizeNodeURL(nodeURL)
	h.v3NodeCapabilitiesMu.Lock()
	defer h.v3NodeCapabilitiesMu.Unlock()
	entry, ok := h.v3NodeCapabilities[nodeURL]
	if !ok {
		return true
	}
	if entry.err != nil {
		return true
	}
	return !now.Add(v3NodeCapabilityRefreshInterval).Before(entry.expiresAt)
}

func (h *PlaybackHandler) warmPlaybackCapabilitiesV3(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, remoteNodeProbeFallbackTimeout)
	defer cancel()
	_ = h.transformationRegistryV3(ctx)

	settings := h.plannerSettingsV3(ctx)
	policy := tonemap.NewPolicy(settings.HardwareToneMapEnabled, settings.SoftwareToneMapEnabled)
	if policy != tonemap.PolicyNone {
		if _, err := h.localToneMapCapabilitiesCachedV3(ctx); err != nil {
			slog.DebugContext(ctx, "protocol v3 local capability warmup failed", "component", "api", "error", err)
		}
	}
	// Keep ordinary SDR playback warm independently of the tone-map probe: its
	// smoke graph has different filters and may already be satisfied by cache.
	cfg := h.playbackConfig()
	if err := playback.WarmHardwareEncoder(ctx, cfg.FFmpegPath, cfg.HWAccel, cfg.HWDevice); err != nil {
		slog.DebugContext(ctx, "protocol v3 hardware encoder warmup failed", "component", "api", "error", err)
	}
	enumerator, ok := h.NodePlanner.(transcodeNodeEnumeratorV3)
	if !ok {
		return
	}
	nodeURLs := enumerator.TranscodeNodeURLs()
	workerCount := min(playbackCapabilityWarmupWorkersV3, len(nodeURLs))
	if workerCount == 0 {
		return
	}
	jobs := make(chan string)
	var group sync.WaitGroup
	group.Add(workerCount)
	for range workerCount {
		go func() {
			defer group.Done()
			for nodeURL := range jobs {
				if _, err := h.lookupRemoteCapabilitiesV3(ctx, nodeURL, false); err != nil {
					slog.DebugContext(ctx, "protocol v3 node capability warmup failed", logComponentKey, "api", "node", logredact.SanitizeURL(nodeURL), "error", err)
				}
			}
		}()
	}
	for _, nodeURL := range nodeURLs {
		select {
		case jobs <- nodeURL:
		case <-ctx.Done():
			close(jobs)
			group.Wait()
			return
		}
	}
	close(jobs)
	group.Wait()
}

// remoteToneMapCapabilitiesV3 returns a defensive copy of one node's validated
// tone-map inventory; planning lookups may honor the negative cache.
func (h *PlaybackHandler) remoteToneMapCapabilitiesV3(ctx context.Context, nodeURL string, planning bool) (tonemap.Capabilities, error) {
	entry, err := h.lookupRemoteCapabilitiesV3(ctx, nodeURL, planning)
	if err != nil {
		return nil, err
	}
	return append(tonemap.Capabilities(nil), entry.toneMapCapabilities...), nil
}

// transcodeNodeEnumeratorV3 exposes the pooled transcode nodes whose
// advertised transformations widen HLS planning; *nodepool.Planner implements
// it.
type transcodeNodeEnumeratorV3 interface {
	TranscodeNodeURLs() []string
}

// transcodeNodeURLsForNewPlanV3 lists the pooled transcode nodes whose
// capabilities a new plan may count. A draining node refuses every new v3
// transport, so it is left out when the planner can tell; it keeps serving the
// transports it already runs.
func transcodeNodeURLsForNewPlanV3(enumerator transcodeNodeEnumeratorV3) []string {
	if newWork, ok := enumerator.(interface{ TranscodeNodeURLsForNewWork() []string }); ok {
		return newWork.TranscodeNodeURLsForNewWork()
	}
	return enumerator.TranscodeNodeURLs()
}

// proxyNodeEnumeratorV3 is the proxy-pool counterpart, letting identity
// planning narrow selection to proxies that can execute the plan's recipe.
type proxyNodeEnumeratorV3 interface {
	ProxyNodeURLs() []string
}

// hlsToneMapCapabilityInventoryV3 separates locally executable tone-map
// capabilities from the union advertised by local and pooled-node executors.
type hlsToneMapCapabilityInventoryV3 struct {
	local                    tonemap.Capabilities
	union                    tonemap.Capabilities
	localTranscodeFallbackOK bool
}

// localHLSExecutionRegistryV3 returns the transformations this API process can
// execute for an HLS delivery. Tone mapping is added only when local execution
// is allowed, the administrator permits a mode, and the configured FFmpeg and
// device have validated an executor in that mode. Planning and transport-time
// validation must share this view or a locally planned recipe can be rejected
// before FFmpeg starts.
func (h *PlaybackHandler) localHLSExecutionRegistryV3(ctx context.Context) (*playback.TransformationRegistryV3, error) {
	settings := h.plannerSettingsV3(ctx)
	policy := tonemap.NewPolicy(settings.HardwareToneMapEnabled, settings.SoftwareToneMapEnabled)
	inventory := hlsToneMapCapabilityInventoryV3{}
	if policy != tonemap.PolicyNone {
		var err error
		inventory.localTranscodeFallbackOK, inventory.local, err = h.localHLSToneMapCapabilitiesForTransportV3(ctx)
		if err != nil {
			return nil, err
		}
	}
	return h.localHLSExecutionRegistryWithInputsV3(ctx, settings, inventory), nil
}

// localHLSExecutionRegistryWithInputsV3 builds the local HLS execution
// registry from one consistent settings and capability snapshot.
func (h *PlaybackHandler) localHLSExecutionRegistryWithInputsV3(
	ctx context.Context,
	settings playback.PlannerSettingsV3,
	inventory hlsToneMapCapabilityInventoryV3,
) *playback.TransformationRegistryV3 {
	local := h.transformationRegistryV3(ctx)
	policy := tonemap.NewPolicy(settings.HardwareToneMapEnabled, settings.SoftwareToneMapEnabled)
	if policy == tonemap.PolicyNone || !inventory.localTranscodeFallbackOK {
		return local
	}
	capabilityAvailable := false
	for _, capability := range inventory.local {
		if policy.Allows(capability.Mode) && len(capability.SourceKinds) > 0 {
			capabilityAvailable = true
			break
		}
	}
	if capabilityAvailable {
		local = local.WithAdvertised([]playback.TransformationV3{{
			Name: playback.TransformationHDRToSDRToneMapV3, Executor: playback.ExecutorServerV3,
			RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		}})
	}
	return local
}

// hlsPlanningRegistryV3 returns the video-transcode registry. Playback planning
// uses workload-specific registries so remux and video policy cannot leak
// executor capabilities into one another.
func (h *PlaybackHandler) hlsPlanningRegistryV3(ctx context.Context) *playback.TransformationRegistryV3 {
	settings := h.plannerSettingsV3(ctx)
	inventory := hlsToneMapCapabilityInventoryV3{}
	policy := tonemap.NewPolicy(settings.HardwareToneMapEnabled, settings.SoftwareToneMapEnabled)
	if policy != tonemap.PolicyNone {
		inventory, _ = h.hlsToneMapCapabilityInventoryV3(ctx)
	}
	return h.hlsPlanningRegistryWithInputsV3(ctx, settings, inventory)
}

// hlsPlanningRegistryWithInputsV3 combines locally executable
// transformations with transformations advertised by pooled transcode nodes.
func (h *PlaybackHandler) hlsPlanningRegistryWithInputsV3(
	ctx context.Context,
	settings playback.PlannerSettingsV3,
	inventory hlsToneMapCapabilityInventoryV3,
) *playback.TransformationRegistryV3 {
	return h.hlsPlanningRegistryWithInputsForWorkloadV3(ctx, settings, inventory, noderouting.WorkloadVideoTranscode)
}

// progressiveRemuxPlanningRegistryWithInputsV3 reports only the remux
// transformations executable by policy-eligible progressive routes. The API
// proxy, and transcode pools are admitted only when policy leaves a legal
// progressive route through that executor.
func (h *PlaybackHandler) progressiveRemuxPlanningRegistryWithInputsV3(
	ctx context.Context,
	local *playback.TransformationRegistryV3,
	proxyAllowed bool,
) *playback.TransformationRegistryV3 {
	local = local.OnlyAdvertised(progressiveRemuxTransformationsV3(local.Advertised()))
	localAllowed, proxyExecutionAllowed, transcodeExecutionAllowed := progressiveRemuxExecutorAvailabilityV3(
		h.playbackRoutingPolicyForContextV3(ctx), proxyAllowed,
	)
	var merged []playback.TransformationV3
	if proxyExecutionAllowed {
		if enumerator, ok := h.NodePlanner.(proxyNodeEnumeratorV3); ok {
			for _, transformations := range h.pooledNodeTransformationsV3(ctx, enumerator.ProxyNodeURLs()) {
				merged = append(merged, progressiveRemuxTransformationsV3(transformations)...)
			}
		}
	}
	if transcodeExecutionAllowed && h.NodeRecipeStore != nil && h.NodeRecipeStore.Enabled() &&
		h.progressiveRemuxRelayAvailableV3(ctx) {
		if enumerator, ok := h.NodePlanner.(transcodeNodeEnumeratorV3); ok {
			for _, entry := range h.pooledNodeCapabilitiesV3(ctx, transcodeNodeURLsForNewPlanV3(enumerator)) {
				if slices.Contains(entry.transportFeatures, playback.TransportFeatureProgressiveRemuxExecutionV1) {
					merged = append(merged, progressiveRemuxTransformationsV3(entry.transformations)...)
				}
			}
		}
	}
	if !localAllowed {
		return local.OnlyAdvertised(merged)
	}
	return local.WithAdvertised(merged)
}

func (h *PlaybackHandler) progressiveRemuxRelayAvailableV3(ctx context.Context) bool {
	enumerator, ok := h.NodePlanner.(proxyNodeEnumeratorV3)
	if !ok {
		return false
	}
	for _, entry := range h.pooledNodeCapabilitiesV3(ctx, enumerator.ProxyNodeURLs()) {
		if slices.Contains(entry.transportFeatures, playback.TransportFeatureProgressiveRemuxRelayV1) {
			return true
		}
	}
	return false
}

func progressiveRemuxTransformationsV3(transformations []playback.TransformationV3) []playback.TransformationV3 {
	result := make([]playback.TransformationV3, 0, len(transformations))
	for _, transformation := range transformations {
		name := strings.TrimSpace(transformation.Name)
		if strings.EqualFold(name, playback.TransformationAudioToAACV3) ||
			strings.EqualFold(name, playback.TransformationServerDV7HDR10V3) {
			result = append(result, transformation)
		}
	}
	return result
}

func progressiveRemuxExecutorAvailabilityV3(policy config.PlaybackRoutingPolicy, proxyAllowed bool) (bool, bool, bool) {
	routes, err := noderouting.Candidates(noderouting.Request{
		Workload: noderouting.WorkloadRemux, Delivery: noderouting.DeliveryProgressiveRemux,
		Policy: policy, ProxyAllowed: proxyAllowed,
	})
	if err != nil {
		return false, false, false
	}
	var localAllowed, proxyExecutionAllowed, transcodeExecutionAllowed bool
	for _, candidate := range routes.Candidates {
		localAllowed = localAllowed || candidate.Execution == noderouting.ExecutionAPI
		proxyExecutionAllowed = proxyExecutionAllowed || candidate.Execution == noderouting.ExecutionProxy
		transcodeExecutionAllowed = transcodeExecutionAllowed || candidate.Execution == noderouting.ExecutionTranscode
	}
	return localAllowed, proxyExecutionAllowed, transcodeExecutionAllowed
}

// hlsCapabilityRegistryWithInputsV3 reports the union of transformations that
// can execute on any admitted playback workload. The capability contract is
// not workload-scoped, while planning must keep these registries separate.
func (h *PlaybackHandler) hlsCapabilityRegistryWithInputsV3(
	ctx context.Context,
	settings playback.PlannerSettingsV3,
	inventory hlsToneMapCapabilityInventoryV3,
) *playback.TransformationRegistryV3 {
	local := h.localHLSExecutionRegistryWithInputsV3(ctx, settings, inventory)
	progressive := h.progressiveRemuxPlanningRegistryWithInputsV3(
		ctx, local, h.JWTSecret != "" || h.proxyEgressOriginsAvailableV3(),
	)
	remux := h.hlsPlanningRegistryWithInputsForWorkloadV3(
		ctx, settings, hlsToneMapCapabilityInventoryV3{}, noderouting.WorkloadRemux,
	)
	video := h.hlsPlanningRegistryWithInputsForWorkloadV3(
		ctx, settings, inventory, noderouting.WorkloadVideoTranscode,
	)
	return progressive.WithAdvertised(remux.Advertised()).WithAdvertised(video.Advertised())
}

// hlsPlanningRegistryWithInputsForWorkloadV3 keeps transformation
// availability inside the hard execution boundary for one HLS workload.
func (h *PlaybackHandler) hlsPlanningRegistryWithInputsForWorkloadV3(
	ctx context.Context,
	settings playback.PlannerSettingsV3,
	inventory hlsToneMapCapabilityInventoryV3,
	workload noderouting.Workload,
) *playback.TransformationRegistryV3 {
	return h.hlsPlanningRegistryWithInputsForClientV3(ctx, settings, inventory, workload, true)
}

// hlsPlanningRegistryWithInputsForClientV3 keeps transformation availability
// inside both the workload's execution boundary and this client's admitted
// egress boundary. The unscoped capability document uses proxyAllowed=true;
// one playback attempt supplies the client's actual proxy support.
func (h *PlaybackHandler) hlsPlanningRegistryWithInputsForClientV3(
	ctx context.Context,
	settings playback.PlannerSettingsV3,
	inventory hlsToneMapCapabilityInventoryV3,
	workload noderouting.Workload,
	proxyAllowed bool,
) *playback.TransformationRegistryV3 {
	local := h.localHLSExecutionRegistryWithInputsV3(ctx, settings, inventory)
	routingPolicy := h.playbackRoutingPolicyForContextV3(ctx)
	localAllowed, workerAllowed := hlsRouteExecutorAvailabilityForClientV3(workload, routingPolicy, proxyAllowed)
	if !workerAllowed {
		if !localAllowed {
			return local.OnlyAdvertised(nil)
		}
		return local
	}
	enumerator, ok := h.NodePlanner.(transcodeNodeEnumeratorV3)
	if !ok {
		if !localAllowed {
			return local.OnlyAdvertised(nil)
		}
		return local
	}
	nodeURLs := transcodeNodeURLsForNewPlanV3(enumerator)
	if len(nodeURLs) == 0 {
		if !localAllowed {
			return local.OnlyAdvertised(nil)
		}
		return local
	}
	var merged []playback.TransformationV3
	for _, transformations := range h.pooledNodeTransformationsV3(ctx, nodeURLs) {
		merged = append(merged, transformations...)
	}
	if !localAllowed {
		return local.OnlyAdvertised(merged)
	}
	return local.WithAdvertised(merged)
}

func (h *PlaybackHandler) localHLSToneMapCapabilitiesForTransportV3(ctx context.Context) (bool, tonemap.Capabilities, error) {
	if !localHLSVideoRouteAllowedV3(h.playbackRoutingPolicyForContextV3(ctx)) {
		return false, nil, nil
	}
	capabilities, err := h.localToneMapCapabilitiesForTransportV3(ctx)
	return true, capabilities, err
}

func localHLSVideoRouteAllowedV3(policy config.PlaybackRoutingPolicy) bool {
	return localHLSRouteAllowedV3(noderouting.WorkloadVideoTranscode, policy)
}

func localHLSRouteAllowedV3(workload noderouting.Workload, policy config.PlaybackRoutingPolicy) bool {
	localAllowed, _ := hlsRouteExecutorAvailabilityV3(workload, policy)
	return localAllowed
}

func hlsRouteExecutorAvailabilityV3(workload noderouting.Workload, policy config.PlaybackRoutingPolicy) (bool, bool) {
	return hlsRouteExecutorAvailabilityForClientV3(workload, policy, true)
}

func hlsRouteExecutorAvailabilityForClientV3(
	workload noderouting.Workload,
	policy config.PlaybackRoutingPolicy,
	proxyAllowed bool,
) (bool, bool) {
	var delivery noderouting.Delivery
	switch workload {
	case noderouting.WorkloadRemux:
		delivery = noderouting.DeliveryHLSRemux
	case noderouting.WorkloadVideoTranscode:
		delivery = noderouting.DeliveryHLSVideo
	default:
		return false, false
	}
	routes, err := noderouting.Candidates(noderouting.Request{
		Workload: workload, Delivery: delivery, Policy: policy, ProxyAllowed: proxyAllowed,
	})
	if err != nil {
		return false, false
	}
	var localAllowed, workerAllowed bool
	for _, candidate := range routes.Candidates {
		localAllowed = localAllowed || candidate.Execution == noderouting.ExecutionAPI
		workerAllowed = workerAllowed || candidate.NeedsTranscodeNode()
	}
	return localAllowed, workerAllowed
}

// hlsToneMapCapabilityInventoryV3 snapshots local and pooled-node tone-map
// capabilities for a single planning operation.
func (h *PlaybackHandler) hlsToneMapCapabilityInventoryV3(ctx context.Context) (hlsToneMapCapabilityInventoryV3, error) {
	return h.hlsToneMapCapabilityInventoryForClientV3(ctx, true)
}

func (h *PlaybackHandler) hlsToneMapCapabilityInventoryForClientV3(
	ctx context.Context,
	proxyAllowed bool,
) (hlsToneMapCapabilityInventoryV3, error) {
	routingPolicy := h.playbackRoutingPolicyForContextV3(ctx)
	localAllowed, workerAllowed := hlsRouteExecutorAvailabilityForClientV3(
		noderouting.WorkloadVideoTranscode, routingPolicy, proxyAllowed,
	)
	fetchCtx, cancel := context.WithTimeout(ctx, h.toneMapPlanningTimeoutV3(localAllowed))
	defer cancel()

	type capabilityResult struct {
		capabilities tonemap.Capabilities
		err          error
	}
	var localResult capabilityResult
	var localWG sync.WaitGroup
	if localAllowed {
		localWG.Add(1)
		go func() {
			defer localWG.Done()
			localResult.capabilities, localResult.err = h.localToneMapCapabilitiesCachedV3(fetchCtx)
		}()
	}

	var nodeURLs []string
	if enumerator, ok := h.NodePlanner.(transcodeNodeEnumeratorV3); ok && workerAllowed {
		nodeURLs = withoutUnreachableRouteNodesV3(ctx, transcodeNodeURLsForNewPlanV3(enumerator))
	}
	results := make([]capabilityResult, len(nodeURLs))
	var wg sync.WaitGroup
	for i, nodeURL := range nodeURLs {
		wg.Add(1)
		go func(i int, nodeURL string) {
			defer wg.Done()
			remote, err := h.remoteToneMapCapabilitiesV3(fetchCtx, nodeURL, true)
			results[i].err = err
			if err != nil {
				slog.DebugContext(ctx, "protocol v3 node tone-map capability unavailable for planning", "component", "api", "node", logredact.SanitizeURL(nodeURL), "error", err)
				return
			}
			results[i].capabilities = remote
		}(i, nodeURL)
	}
	wg.Wait()
	localWG.Wait()

	inventory := hlsToneMapCapabilityInventoryV3{localTranscodeFallbackOK: localAllowed}
	var probeErr error
	if localAllowed {
		if localResult.err != nil {
			probeErr = errors.Join(probeErr, localResult.err)
		} else {
			inventory.local = localResult.capabilities
			inventory.union = append(inventory.union, localResult.capabilities...)
		}
	}
	for _, remote := range results {
		if remote.err != nil {
			probeErr = errors.Join(probeErr, remote.err)
			continue
		}
		inventory.union = append(inventory.union, remote.capabilities...)
	}
	return inventory, probeErr
}

// hlsToneMapCapabilitiesV3 builds the executor union available to HLS planning
// from eligible local fallback and all reachable transcode nodes.
func (h *PlaybackHandler) hlsToneMapCapabilitiesV3(ctx context.Context) tonemap.Capabilities {
	inventory, _ := h.hlsToneMapCapabilityInventoryV3(ctx)
	return inventory.union
}

type hlsPlanningSnapshotV3 struct {
	handler             *PlaybackHandler
	ctx                 context.Context
	settings            playback.PlannerSettingsV3
	localRegistry       *playback.TransformationRegistryV3
	proxyAllowed        bool
	progressiveOnce     sync.Once
	progressiveRegistry *playback.TransformationRegistryV3
	remuxOnce           sync.Once
	remuxRegistry       *playback.TransformationRegistryV3
	videoOnce           sync.Once
	videoRegistry       *playback.TransformationRegistryV3
	inventoryOnce       sync.Once
	inventory           hlsToneMapCapabilityInventoryV3
	inventoryErr        error
	inventoryResolved   bool
	renditionOnce       sync.Once
	renditionExecutable bool
	remuxRenditionOnce  sync.Once
	remuxRendition      bool
	onDemandOnce        sync.Once
	onDemand            bool
	remuxOnDemandOnce   sync.Once
	remuxOnDemand       bool
}

func (snapshot *hlsPlanningSnapshotV3) subtitleRenditionExecutable() bool {
	snapshot.renditionOnce.Do(func() {
		snapshot.renditionExecutable = snapshot.handler.hlsSubtitleRenditionExecutableV3(snapshot.ctx, snapshot.proxyAllowed, playback.PlayTranscode)
	})
	return snapshot.renditionExecutable
}

func (snapshot *hlsPlanningSnapshotV3) remuxSubtitleRenditionExecutable() bool {
	snapshot.remuxRenditionOnce.Do(func() {
		snapshot.remuxRendition = snapshot.handler.hlsSubtitleRenditionExecutableV3(snapshot.ctx, snapshot.proxyAllowed, playback.PlayRemux)
	})
	return snapshot.remuxRendition
}

func (snapshot *hlsPlanningSnapshotV3) onDemandSubtitleRenditionsExecutable() bool {
	snapshot.onDemandOnce.Do(func() {
		snapshot.onDemand = snapshot.handler.hlsSubtitleRenditionExecutableV3(snapshot.ctx, snapshot.proxyAllowed, playback.PlayTranscode, true)
	})
	return snapshot.onDemand
}

func (snapshot *hlsPlanningSnapshotV3) remuxOnDemandSubtitleRenditionsExecutable() bool {
	snapshot.remuxOnDemandOnce.Do(func() {
		snapshot.remuxOnDemand = snapshot.handler.hlsSubtitleRenditionExecutableV3(snapshot.ctx, snapshot.proxyAllowed, playback.PlayRemux, true)
	})
	return snapshot.remuxOnDemand
}

// hlsSubtitleRenditionExecutableV3 reports whether a policy-eligible executor
// of an HLS transcode (playMethod PlayTranscode) or HLS copy remux (PlayRemux)
// can emit a subtitle rendition: this server, or a pooled transcode node
// advertising the transport feature for it. Node selection then admits only
// capable nodes for a plan that carries one (transcodeEligibilityV3).
func (h *PlaybackHandler) hlsSubtitleRenditionExecutableV3(ctx context.Context, proxyAllowed bool, playMethod playback.PlayMethod, legacy ...bool) bool {
	workload, feature := noderouting.WorkloadVideoTranscode, playback.TransportFeatureHLSSubtitleRenditionV1
	if playMethod == playback.PlayRemux {
		workload, feature = noderouting.WorkloadRemux, playback.TransportFeatureHLSRemuxSubtitleRenditionV1
	}
	required := []string{feature}
	if len(legacy) > 0 && legacy[0] {
		// On-demand renditions list every embedded text track as its own
		// playlist, which a node must execute as well as the base rendition.
		required = append(required, playback.TransportFeatureHLSOnDemandSubtitleRenditionsV1)
	}
	localAllowed, workerAllowed := hlsRouteExecutorAvailabilityForClientV3(
		workload, h.playbackRoutingPolicyForContextV3(ctx), proxyAllowed,
	)
	if localAllowed {
		return true
	}
	enumerator, ok := h.NodePlanner.(transcodeNodeEnumeratorV3)
	if !workerAllowed || !ok {
		return false
	}
	for _, entry := range h.pooledNodeCapabilitiesV3(ctx, transcodeNodeURLsForNewPlanV3(enumerator)) {
		if transportFeaturesPresentV3(entry.transportFeatures, required) {
			return true
		}
	}
	return false
}

// transportFeaturesPresentV3 reports whether every required transport feature
// is advertised by a node.
func transportFeaturesPresentV3(advertised, required []string) bool {
	for _, feature := range required {
		if !slices.Contains(advertised, feature) {
			return false
		}
	}
	return true
}

// subtitleRenditionRoutableV3 reports whether one policy-eligible executor can
// run a rendition plan whole: this server, or a pooled node that passes the
// filter node selection applies to the plan (transcodeEligibilityV3).
func (h *PlaybackHandler) subtitleRenditionRoutableV3(ctx context.Context, result playback.PlannerResultV3, proxyAllowed bool) bool {
	localAllowed, workerAllowed := hlsRouteExecutorAvailabilityForClientV3(
		routingWorkloadV3(result), h.playbackRoutingPolicyForContextV3(ctx), proxyAllowed,
	)
	if localAllowed {
		return true
	}
	enumerator, ok := h.NodePlanner.(transcodeNodeEnumeratorV3)
	if !workerAllowed || !ok {
		return false
	}
	eligible := h.transcodeEligibilityV3(ctx, result, nil)
	if eligible == nil {
		return true
	}
	for _, nodeURL := range transcodeNodeURLsForNewPlanV3(enumerator) {
		if eligible(&nodepool.Node{URL: nodeURL}) {
			return true
		}
	}
	return false
}

func (snapshot *hlsPlanningSnapshotV3) progressiveRemuxRegistry() *playback.TransformationRegistryV3 {
	snapshot.progressiveOnce.Do(func() {
		snapshot.progressiveRegistry = snapshot.handler.progressiveRemuxPlanningRegistryWithInputsV3(
			snapshot.ctx, snapshot.localRegistry, snapshot.proxyAllowed,
		)
	})
	return snapshot.progressiveRegistry
}

func (snapshot *hlsPlanningSnapshotV3) resolveInventory() {
	snapshot.inventoryOnce.Do(func() {
		snapshot.inventoryResolved = true
		policy := tonemap.NewPolicy(snapshot.settings.HardwareToneMapEnabled, snapshot.settings.SoftwareToneMapEnabled)
		if policy != tonemap.PolicyNone {
			snapshot.inventory, snapshot.inventoryErr = snapshot.handler.hlsToneMapCapabilityInventoryForClientV3(
				snapshot.ctx, snapshot.proxyAllowed,
			)
		}
	})
}

func (snapshot *hlsPlanningSnapshotV3) hlsRemuxRegistry() *playback.TransformationRegistryV3 {
	snapshot.remuxOnce.Do(func() {
		snapshot.remuxRegistry = snapshot.handler.hlsPlanningRegistryWithInputsForClientV3(
			snapshot.ctx, snapshot.settings, hlsToneMapCapabilityInventoryV3{}, noderouting.WorkloadRemux,
			snapshot.proxyAllowed,
		)
	})
	return snapshot.remuxRegistry
}

func (snapshot *hlsPlanningSnapshotV3) hlsVideoRegistry() *playback.TransformationRegistryV3 {
	snapshot.videoOnce.Do(func() {
		snapshot.resolveInventory()
		snapshot.videoRegistry = snapshot.handler.hlsPlanningRegistryWithInputsForClientV3(
			snapshot.ctx, snapshot.settings, snapshot.inventory, noderouting.WorkloadVideoTranscode,
			snapshot.proxyAllowed,
		)
	})
	return snapshot.videoRegistry
}

func (snapshot *hlsPlanningSnapshotV3) toneMapCapabilities() tonemap.Capabilities {
	snapshot.resolveInventory()
	return snapshot.inventory.union
}

func (snapshot *hlsPlanningSnapshotV3) capabilityError() error {
	if !snapshot.inventoryResolved {
		return nil
	}
	return snapshot.inventoryErr
}

func retryIncompleteToneMapPlanningV3(result playback.PlannerResultV3, capabilityErr error) playback.PlannerResultV3 {
	if capabilityErr == nil || result.Terminal == nil {
		return result
	}
	switch result.Terminal.Reason {
	case playback.TerminalHDRTranscodeUnsupportedV3, terminalSubtitleConversionUnsupportedV3:
	default:
		return result
	}
	result.Terminal = &playback.TerminalV3{
		Reason:    transcodeStartFailedReasonV3,
		Message:   "Tone-map capability discovery is temporarily unavailable.",
		Retryable: true,
	}
	return result
}

func retryIncompletePlaybackSettingsV3(result playback.PlannerResultV3, settingsErr error) playback.PlannerResultV3 {
	if settingsErr == nil || result.Terminal == nil {
		return result
	}
	terminal := result.Terminal
	settingsDependent := terminal.Reason == playback.TerminalHDRTranscodeUnsupportedV3 ||
		(terminal.Reason == terminalNoAlternateVersionV3 && terminal.Message == playback.TerminalMessage4KTranscodeDisabledV3) ||
		(terminal.Reason == terminalSubtitleConversionUnsupportedV3 &&
			(strings.Contains(terminal.Message, "this HDR source") || strings.Contains(terminal.Message, "4K transcoding is disabled")))
	if !settingsDependent {
		return result
	}
	result.Terminal = &playback.TerminalV3{
		Reason:    transcodeStartFailedReasonV3,
		Message:   "Playback settings are temporarily unavailable.",
		Retryable: true,
	}
	return result
}

func (h *PlaybackHandler) planPlaybackWithCapabilitiesV3(ctx context.Context, input playback.PlannerInputV3) (playback.PlannerResultV3, error) {
	mode := headerAuthenticatedMediaV3(input.Request.ClientFeatures)
	proxyAllowed := !mode.headerAuth && h.JWTSecret != "" || mode.proxyEgress && h.proxyEgressOriginsAvailableV3()
	snapshot := &hlsPlanningSnapshotV3{
		handler: h, ctx: ctx, settings: input.Settings, localRegistry: input.Registry, proxyAllowed: proxyAllowed,
	}
	input.ProgressiveRemuxRegistry = snapshot.progressiveRemuxRegistry
	input.HLSRemuxRegistry = snapshot.hlsRemuxRegistry
	input.HLSVideoRegistry = snapshot.hlsVideoRegistry
	input.HLSToneMapCapabilities = snapshot.toneMapCapabilities
	input.SubtitleRenditionExecutable = snapshot.subtitleRenditionExecutable
	input.RemuxSubtitleRenditionExecutable = snapshot.remuxSubtitleRenditionExecutable
	input.OnDemandSubtitleRenditionsExecutable = snapshot.onDemandSubtitleRenditionsExecutable
	input.RemuxOnDemandSubtitleRenditionsExecutable = snapshot.remuxOnDemandSubtitleRenditionsExecutable
	if input.CueIndexedSubtitleTracks == nil {
		input.CueIndexedSubtitleTracks = cueIndexedSubtitleTracksV3(ctx, input.EffectiveFile)
	}
	input.LocalNetwork = streamlocation.FromContext(ctx) == streamlocation.Local
	if h.SubtitleTrackFacts != nil {
		input.SubtitleTrackFacts = h.SubtitleTrackFacts.SubtitleTrackFacts(ctx, input.EffectiveFile)
	}
	result := playback.PlanPlaybackV3(input)
	// On-demand renditions fall back to the single pipeline rendition, which
	// falls back to the sidecar.
	for range 2 {
		if !result.SubtitleRendition || result.Plan == nil || h.subtitleRenditionRoutableV3(ctx, result, proxyAllowed) {
			break
		}
		// The planning gate counts any executor that emits renditions, but
		// this recipe may need transformations only nodes without one
		// advertise. Marking the rendition attempted plans the next shape.
		input.AttemptedKeys = append(slices.Clone(input.AttemptedKeys),
			playback.PlanAttemptKeyV3(*result.Plan, input.Request.ClientPlaybackContext.Output.OutputContextID, nil))
		result = playback.PlanPlaybackV3(input)
	}
	if result.Terminal != nil {
		switch result.Terminal.Reason {
		case playback.TerminalHDRTranscodeUnsupportedV3, terminalSubtitleConversionUnsupportedV3:
			return result, snapshot.capabilityError()
		}
	}
	return result, nil
}

// pooledNodeTransformationsV3 collects the advertised transformations of the
// given transcode nodes, keyed by node URL. Stale cache entries are refreshed
// concurrently under a short planning deadline; nodes that cannot be reached
// contribute nothing (their failures are negatively cached), so planning
// degrades toward the local registry instead of blocking the start path.
func (h *PlaybackHandler) pooledNodeTransformationsV3(ctx context.Context, nodeURLs []string) map[string][]playback.TransformationV3 {
	capabilities := h.pooledNodeCapabilitiesV3(ctx, nodeURLs)
	byURL := make(map[string][]playback.TransformationV3, len(capabilities))
	for nodeURL, entry := range capabilities {
		byURL[nodeURL] = append([]playback.TransformationV3(nil), entry.transformations...)
	}
	return byURL
}

func (h *PlaybackHandler) pooledNodeCapabilitiesV3(ctx context.Context, nodeURLs []string) map[string]v3NodeCapabilityCache {
	if len(nodeURLs) == 0 {
		return map[string]v3NodeCapabilityCache{}
	}
	// The lookups below run concurrently. Each one records its own capability
	// span, and parallel spans would sum additively into transport_capability —
	// more than the wall time route resolution can net out (and the batch itself
	// only spans one wall-clock interval). Suppress their recording on the shared
	// batch context and name the fan-out's own wall time once below; standalone
	// callers outside the pool keep their per-lookup recording.
	batchStart := time.Now()
	nodeURLs = withoutUnreachableRouteNodesV3(ctx, nodeURLs)
	fetchCtx, cancel := context.WithTimeout(ctx, v3NodeCapabilityPlanTimeout)
	defer cancel()
	lookupCtx := withoutTransportPhaseRecordingV3(fetchCtx)
	results := make([]v3NodeCapabilityCache, len(nodeURLs))
	available := make([]bool, len(nodeURLs))
	var wg sync.WaitGroup
	for i, nodeURL := range nodeURLs {
		wg.Add(1)
		go func(i int, nodeURL string) {
			defer wg.Done()
			entry, err := h.lookupRemoteCapabilitiesV3(lookupCtx, nodeURL, true)
			if err != nil {
				slog.DebugContext(ctx, "protocol v3 node capability unavailable for planning", "component", "api", "node", logredact.SanitizeURL(nodeURL), "error", err)
				return
			}
			results[i] = entry
			available[i] = true
		}(i, nodeURL)
	}
	wg.Wait()
	recordTransportPhaseV3(ctx, transportPhaseCapabilityV3, batchStart)
	byURL := make(map[string]v3NodeCapabilityCache, len(nodeURLs))
	for i, entry := range results {
		if available[i] {
			byURL[nodeURLs[i]] = entry
		}
	}
	return byURL
}

// planNodeSessionV3 remains as a focused test seam. Production starts resolve
// the complete policy in resolveHLSRouteV3 below; this wrapper asks that same
// resolver for a hard worker route with the requested egress.
func (h *PlaybackHandler) planNodeSessionV3(ctx context.Context, session *playback.Session, result playback.PlannerResultV3, localEgress bool) nodepool.Plan {
	return h.planNodeSessionExcludingV3(ctx, session, result, localEgress, nil)
}

// planNodeSessionExcludingV3 is planNodeSessionV3 with a set of node URLs the
// caller has already exhausted. The tone-map software fallback uses it so a
// second attempt cannot land back on the node that just refused the recipe.
func (h *PlaybackHandler) planNodeSessionExcludingV3(ctx context.Context, session *playback.Session, result playback.PlannerResultV3, localEgress bool, excluded map[string]struct{}) nodepool.Plan {
	workload, _, ok := routingClassV3(result)
	if !ok {
		return nodepool.Plan{}
	}
	egress := config.PlaybackEgressPreferProxy
	if localEgress {
		egress = config.PlaybackEgressAPIOnly
	}
	policy := config.DefaultPlaybackRoutingPolicy()
	if workload == noderouting.WorkloadRemux {
		policy.RemuxExecution = config.PlaybackExecutionWorkerOnly
		policy.RemuxEgress = egress
	} else {
		policy.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
		policy.VideoTranscodeEgress = egress
	}
	return h.resolveHLSRouteWithPolicyV3(ctx, session, result, policy, true, excluded, nil).Plan
}

func routingClassV3(result playback.PlannerResultV3) (noderouting.Workload, noderouting.Delivery, bool) {
	if result.Plan == nil {
		return "", "", false
	}
	switch result.Plan.Delivery {
	case playback.DeliveryOriginalHTTPV3:
		return noderouting.WorkloadDirectPlay, noderouting.DeliveryDirect, true
	case playback.DeliveryRemuxProgressiveV3:
		return noderouting.WorkloadRemux, noderouting.DeliveryProgressiveRemux, true
	case playback.DeliveryRemuxHLSV3:
		return noderouting.WorkloadRemux, noderouting.DeliveryHLSRemux, true
	case playback.DeliveryTranscodeHLSV3:
		return noderouting.WorkloadVideoTranscode, noderouting.DeliveryHLSVideo, true
	default:
		return "", "", false
	}
}

func routingWorkloadV3(result playback.PlannerResultV3) noderouting.Workload {
	workload, _, _ := routingClassV3(result)
	return workload
}

func (h *PlaybackHandler) playbackRoutingPolicyV3() config.PlaybackRoutingPolicy {
	return config.EffectivePlaybackRoutingPolicy(h.playbackConfig().Routing)
}

type playbackRoutingPolicySnapshotKeyV3 struct{}

func withPlaybackRoutingPolicySnapshotV3(ctx context.Context, policy config.PlaybackRoutingPolicy) context.Context {
	return context.WithValue(ctx, playbackRoutingPolicySnapshotKeyV3{}, config.EffectivePlaybackRoutingPolicy(policy))
}

func (h *PlaybackHandler) playbackRoutingPolicyForContextV3(ctx context.Context) config.PlaybackRoutingPolicy {
	if ctx != nil {
		if policy, ok := ctx.Value(playbackRoutingPolicySnapshotKeyV3{}).(config.PlaybackRoutingPolicy); ok {
			return policy
		}
	}
	return h.playbackRoutingPolicyV3()
}

func (h *PlaybackHandler) resolveHLSRouteWithPolicyV3(
	ctx context.Context,
	session *playback.Session,
	result playback.PlannerResultV3,
	policy config.PlaybackRoutingPolicy,
	proxyAllowed bool,
	excludedNodes map[string]struct{},
	excludedShapes map[string]struct{},
) noderouting.Decision {
	workload, delivery, ok := routingClassV3(result)
	if !ok || session == nil {
		return noderouting.Decision{Outcome: noderouting.OutcomePolicyUnsatisfied}
	}
	eligible := excludeUnreachableRouteNodesV3(ctx, h.transcodeEligibilityV3(ctx, result, excludedNodes))
	// A client that arrived through a network access provider can only use a
	// proxy that has a connected origin on that same overlay; on the default
	// path this is nil and every healthy proxy stays eligible. Filtering here
	// rather than at the URL builder keeps the resolver's fallbacks (API
	// egress, API relay to the transcode node) in charge of what happens next.
	proxyEligible := excludeUnreachableRouteNodesV3(ctx, nodepool.ClientReachableVia(netaccess.PathFromContext(ctx), nil))
	decision, err := noderouting.Resolve(noderouting.AdaptSessionPlanner(h.NodePlanner), noderouting.ResolveRequest{
		Request: noderouting.Request{
			Workload: workload, Delivery: delivery, Policy: policy, ProxyAllowed: proxyAllowed,
		},
		SessionID: session.ID, CurrentTranscodeURL: session.TranscodeNodeURL,
		EstimatedBitrateKbps: result.TargetBitrateKbps,
		TranscodeEligible:    eligible, ProxyEligible: proxyEligible, ExcludedShapeIDs: excludedShapes,
		UnreachableNodes: unreachableRouteNodesV3(ctx),
	})
	if err != nil {
		slog.ErrorContext(ctx, "compile playback node route", "component", "noderouting", "error", err)
		return noderouting.Decision{Outcome: noderouting.OutcomePolicyUnsatisfied}
	}
	return decision
}

// predicateSessionPlannerV3 is the legacy planner seam that accepts an
// eligibility predicate without implementing the exact-route contract.
type predicateSessionPlannerV3 interface {
	PlanSessionWith(sessionID, currentTranscodeURL string, needsTranscode bool, estBitrateKbps int, eligible func(*nodepool.Node) bool) nodepool.Plan
}

// transcodeEligibilityV3 builds the node predicate the route resolver applies
// before it reserves. Plans that carry server transformations restrict
// selection to nodes whose advertised capabilities validate against the plan,
// so load balancing in a heterogeneous pool cannot land a recipe on a node that
// would reject it when a capable sibling exists. A nil predicate means every
// pooled node is acceptable.
func (h *PlaybackHandler) transcodeEligibilityV3(ctx context.Context, result playback.PlannerResultV3, excluded map[string]struct{}) func(*nodepool.Node) bool {
	// The per-node capability fan-out only pays for itself when a planner can
	// actually consume the predicate it produces. A planner that implements
	// neither the exact route contract nor the predicate seam would spend a
	// round of capability lookups on a filter nothing reads.
	_, exactPlanner := h.NodePlanner.(nodepool.RoutePlanner)
	_, predicatePlanner := h.NodePlanner.(predicateSessionPlannerV3)
	requiresProgressiveRemux := result.Plan != nil && result.Plan.Delivery == playback.DeliveryRemuxProgressiveV3
	if enumerator, ok := h.NodePlanner.(transcodeNodeEnumeratorV3); ok && (exactPlanner || predicatePlanner) &&
		(planRequiresServerTransformationsV3(result.Plan) || requiresProgressiveRemux || result.SubtitleRendition) {
		capable := make(map[string]struct{})
		for nodeURL, entry := range h.pooledNodeCapabilitiesV3(ctx, transcodeNodeURLsForNewPlanV3(enumerator)) {
			if requiresProgressiveRemux && !slices.Contains(entry.transportFeatures, playback.TransportFeatureProgressiveRemuxExecutionV1) {
				continue
			}
			if result.SubtitleRendition && !transportFeaturesPresentV3(entry.transportFeatures, playback.HLSSubtitleRenditionTransportFeaturesV3(result)) {
				continue
			}
			// A rehomed rendition whose plan record is gone has no plan to
			// check transformations against, as at that session's own start.
			if result.Plan != nil && validateAdvertisedTransformationsV3(result.Plan, entry.transformations) != nil {
				continue
			}
			if err := validateToneMapExecutorV3(result, entry.toneMapCapabilities); err != nil {
				continue
			}
			capable[nodeURL] = struct{}{}
		}
		// The predicate runs under the planner lock: a set lookup only.
		return func(node *nodepool.Node) bool {
			if node == nil {
				return false
			}
			if _, skip := excluded[node.URL]; skip {
				return false
			}
			_, found := capable[node.URL]
			return found
		}
	}
	if len(excluded) == 0 {
		return nil
	}
	// No capability filter applies to this plan, but an exhausted node must
	// still be kept out of the selection.
	return func(node *nodepool.Node) bool {
		if node == nil {
			return false
		}
		_, skip := excluded[node.URL]
		return !skip
	}
}

// planRequiresToneMapV3 reports whether a plan contains the server-owned HDR
// to SDR transformation.
func planRequiresToneMapV3(plan *playback.PlanV3) bool {
	if plan == nil {
		return false
	}
	for _, transformation := range plan.Transformations {
		if transformation.Executor == playback.ExecutorServerV3 &&
			transformation.Name == playback.TransformationHDRToSDRToneMapV3 {
			return true
		}
	}
	return false
}

// validateToneMapExecutorV3 confirms that the selected executor still supports
// the exact mode and source kind frozen by planning.
func validateToneMapExecutorV3(result playback.PlannerResultV3, capabilities tonemap.Capabilities) error {
	if !planRequiresToneMapV3(result.Plan) {
		return nil
	}
	if result.ToneMapMode == "" || result.ToneMapSourceKind == "" ||
		!capabilities.Supports(result.ToneMapMode, result.ToneMapSourceKind) {
		return fmt.Errorf("executor lacks %s %s tone mapping", result.ToneMapMode, result.ToneMapSourceKind)
	}
	return nil
}

// validateAdvertisedTransformationsV3 verifies that every server-executed
// transformation the plan requires is advertised — at the exact recipe
// version — by the executor under consideration (a pooled node's capability
// response or the local registry's Advertised set).
func validateAdvertisedTransformationsV3(plan *playback.PlanV3, advertised []playback.TransformationV3) error {
	available := make(map[string]string, len(advertised))
	for _, transformation := range advertised {
		available[strings.ToLower(strings.TrimSpace(transformation.Name))] = strings.TrimSpace(transformation.RecipeVersion)
	}
	if plan == nil {
		return errors.New("playback plan is unavailable")
	}
	for _, required := range plan.Transformations {
		if strings.EqualFold(required.Executor, "client") {
			continue
		}
		version, ok := available[strings.ToLower(strings.TrimSpace(required.Name))]
		if !ok || version != strings.TrimSpace(required.RecipeVersion) {
			return fmt.Errorf("executor lacks transformation %s@%s", required.Name, required.RecipeVersion)
		}
	}
	return nil
}

// HandlePlaybackCapabilityV3 reports only transformations that the installed
// runtime has actually probed. Protocol v3 is the server's only playback
// protocol, so `enabled` is constant; it stays in the response because clients
// feature-detect against it and the field is part of the frozen contract.
func (h *PlaybackHandler) HandlePlaybackCapabilityV3(w http.ResponseWriter, r *http.Request) {
	if apimw.GetUserID(r.Context()) == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	response := playback.CapabilityResponseV3{Enabled: true, ProtocolVersions: []int{playback.ProtocolV3}}
	response.Features = playback.ServerFeaturesV3()
	response.Deliveries = []playback.DeliveryV3{playback.DeliveryOriginalHTTPV3, playback.DeliveryRemuxProgressiveV3, playback.DeliveryRemuxHLSV3, playback.DeliveryTranscodeHLSV3}
	settings := h.plannerSettingsV3(r.Context())
	policy := tonemap.NewPolicy(settings.HardwareToneMapEnabled, settings.SoftwareToneMapEnabled)
	inventory := hlsToneMapCapabilityInventoryV3{}
	if policy != tonemap.PolicyNone {
		inventory, _ = h.hlsToneMapCapabilityInventoryV3(r.Context())
	}
	registry := h.hlsCapabilityRegistryWithInputsV3(r.Context(), settings, inventory)
	toneMapAvailable := false
	if policy != tonemap.PolicyNone {
		for _, capability := range inventory.union {
			if policy.Allows(capability.Mode) && len(capability.SourceKinds) > 0 {
				toneMapAvailable = true
				break
			}
		}
	}
	for _, transformation := range registry.Advertised() {
		if transformation.Name == playback.TransformationHDRToSDRToneMapV3 && !toneMapAvailable {
			continue
		}
		response.Transformations = append(response.Transformations, transformation)
	}
	writeJSON(w, http.StatusOK, response)
}

// handleStartPlaybackV3 validates, plans, and starts a protocol-v3 request.
func (h *PlaybackHandler) handleStartPlaybackV3(w http.ResponseWriter, r *http.Request, body []byte) {
	response, err := h.startPlaybackApplicationV3(r, body)
	if err != nil {
		writePlaybackOperationError(w, err)
		return
	}
	if response.Outcome == playback.OutcomePlayableV3 {
		h.recordStartingDevice(r.Context(), apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), deviceMetadataFromRequest(r))
	}
	writeJSON(w, http.StatusCreated, frozenV1DecisionV3(response))
}

// recordStartingDevice registers the device that started playback, so a
// device that plays without ever writing a device setting still appears in
// the profile's device registry with a current last_seen_at. Callers record
// only a playable decision; a terminal decision means the device did not play. A start always
// plays as the caller's own profile (profile_id must match X-Profile-Id), so
// the declared device is the caller's own device on the caller's own profile.
func (h *PlaybackHandler) recordStartingDevice(ctx context.Context, userID int, profileID string, device DeviceMetadata) {
	h.DeviceSightings.RecordFor(ctx, h.StoreProvider, userID, profileID, device)
}

// planStartV3 plans a start of effectiveFile. When the planner refuses it for a
// reason another version can avoid and the request allows other versions, the
// first authorized version of alternateBase that plans replaces it, with the
// warnings that choice adds.
//
// An explicit version pick is never replaced here, however unplannable it is on
// this client: see startAllowsAlternateFileV3. The refusal stands, and the
// caller's explicit-selection hint points the viewer at the version list.
func (h *PlaybackHandler) planStartV3(r *http.Request, req playback.StartRequestV3, requestedFile, alternateBase, effectiveFile *models.MediaFile, audioIndex int, settings playback.PlannerSettingsV3, lowerVersion *playback.LowerVersionV3, profileID string, resolutionProvenance ProbeProvenance) (alternateCandidateV3, []playback.DegradationWarningV3) { //nolint:funlen,gocyclo // planStartV3 exceeds Vio's 100-line and complexity-30 limits; refactor tracked separately
	subtitleInventoryByFileID := map[int][]playback.SubtitleInventoryEntryV3{}
	subtitleInventoryFor := func(file *models.MediaFile) []playback.SubtitleInventoryEntryV3 {
		if file == nil {
			return nil
		}
		if inventory, ok := subtitleInventoryByFileID[file.ID]; ok {
			return inventory
		}
		inventory := h.downloadedSubtitleInventoryV3(r.Context(), file)
		if file.ID != 0 {
			subtitleInventoryByFileID[file.ID] = inventory
		}
		return inventory
	}
	// Resolve the profile's stored subtitle selection before planning so the
	// first plan carries the burn-in requirement: discovering it only after the
	// first FFmpeg start costs a second plan and a second spawn. An explicit
	// client selection (or an explicit subtitles-off) is never overridden.
	if req.SubtitleTrackID == "" && req.SubtitleTrackIndex == nil {
		if subtitleIndex, ok := h.resolvePreferredSubtitleSelectionV3(r.Context(), apimw.GetUserID(r.Context()), profileID, deviceMetadataFromRequest(r).DeviceID, effectiveFile, subtitleInventoryFor(effectiveFile)); ok {
			indexCopy := subtitleIndex
			req.SubtitleTrackIndex = &indexCopy
		}
	}
	input := playback.PlannerInputV3{
		Request: req, RequestedFile: requestedFile, EffectiveFile: effectiveFile, LowerVersion: lowerVersionForFileV3(lowerVersion, effectiveFile),
		ServerBitrateCapKbps: serverBitrateCapV3(r.Context()),
		AudioTrackIndex:      audioIndex, Settings: settings,
		Registry: h.transformationRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), effectiveFile), Now: time.Now(),
		AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), effectiveFile), InventoryProvenance: string(resolutionProvenance),
	}
	result, toneMapCapabilityErr := h.planPlaybackWithCapabilitiesV3(r.Context(), input)
	var warnings []playback.DegradationWarningV3
	// Audio adaptation is release-local: when the selected track cannot be
	// converted, try another track in this file before considering (or
	// reporting) a sibling release. If no same-release track can play, keep
	// the planner's adaptation_unavailable terminal intact.
	if audioOnlyTerminalV3(result.Terminal) {
		degradedReq, degradedIndex, degradedResult, degradedToneMapErr, ok := h.degradeStartAudioInPlaceV3(
			r, req, requestedFile, effectiveFile, audioIndex, settings, ProbeProvenance(""),
		)
		if ok {
			req = degradedReq
			audioIndex = degradedIndex
			result = degradedResult
			toneMapCapabilityErr = degradedToneMapErr
			if result.Plan != nil {
				warnings = append(warnings, result.Plan.DegradationWarnings...)
				result.Plan.DegradationWarnings = nil
			}
		}
	}
	// A subtitle-only refusal is resolved on the release already mounted: before
	// any alternate-file hunt, re-plan that file with the subtitle dropped.
	subtitleDegradeUnresolved := false
	if subtitleOnlyTerminalV3(result.Terminal) { //nolint:nestif // planStartV3 exceeds Vio's nestif complexity-4 limit; refactor tracked separately
		degradedReq, degradedResult, degradedToneMapErr, ok := h.degradeStartSubtitleInPlaceV3(
			r, req, requestedFile, effectiveFile, audioIndex, settings, resolutionProvenance,
		)
		if ok {
			req = degradedReq
			result = degradedResult
			toneMapCapabilityErr = degradedToneMapErr
			if result.Plan != nil {
				result.Plan.DegradationWarnings = appendWarningOnceV3(result.Plan.DegradationWarnings, playback.SubtitleNotShownWarningV3())
			}
		} else {
			// The same release cannot play without the subtitle either, so the
			// release itself is blocked and the alternate hunt applies.
			subtitleDegradeUnresolved = true
		}
	}
	huntAllowed := terminalAllowsAlternateFileV3(result.Terminal) ||
		(subtitleDegradeUnresolved && subtitleOnlyTerminalV3(result.Terminal))
	entryEffectiveFileID := effectiveFile.ID
	if huntAllowed && startAllowsAlternateFileV3(req) { //nolint:nestif // existing alternate-version fallback; only its quality condition changed
		accessFilter := requestAccessFilter(r)
		alternateOrder := alternateOrderingForClient(req.Capabilities)
		if alternates, alternateErr := h.findAlternateFilesOrdered(r.Context(), alternateBase, accessFilter, alternateOrder); alternateErr == nil {
			if alternateBase != requestedFile {
				alternates = slices.DeleteFunc(alternates, func(candidate *models.MediaFile) bool {
					return candidate == nil || candidate.PresentationPartIndex != alternateBase.PresentationPartIndex
				})
			}
			baseReq := req
			baseAudioIndex := audioIndex
			var firstFailureResult playback.PlannerResultV3
			var firstFailureToneMapErr error
			var firstFailureFile *models.MediaFile
			var firstFailureReq playback.StartRequestV3
			firstFailureAudioIndex := 0
			var droppedSubtitle *alternateCandidateV3
			// A candidate that cannot honor the subtitle selection is held as a
			// last-resort degrade and only used after every candidate has been
			// tried, so an explicit pick still hunts for a version that has it.
			var subtitleMissFile *models.MediaFile
			var subtitleMissReq playback.StartRequestV3
			var subtitleMissProvenance ProbeProvenance
			subtitleMissAudioIndex := 0
			// A tone-map capability failure is server-wide, not per-candidate:
			// every sibling would plan to the same verdict, so paying a
			// resolve+probe+plan round-trip per file only delays the start the
			// client is already waiting on. Stop at the first.
			var capabilityBlocked bool
			for _, alternate := range alternates {
				if capabilityBlocked {
					break
				}
				candidateFile, candidateProvenance, err := h.prepareVirtualAlternateFileV3(r, alternate, profileID, true)
				if err != nil || candidateFile == nil {
					continue
				}
				// The sibling was authorized on its stored resolution, which
				// its first probe can fill in above the viewer ceiling: recheck
				// after the probe before planning it.
				if !catalog.FileAllowedByAccess(candidateFile, accessFilter) {
					continue
				}
				candidateReq := baseReq
				candidateAudioIndex := remapAudioIndexV3(alternateBase, candidateFile, baseAudioIndex)
				var candidateResult playback.PlannerResultV3
				var candidateToneMapErr error
				subtitleDropped, err := h.remapSubtitleSelectionV3(r.Context(), requestedFile, candidateFile, &candidateReq)
				if err != nil {
					candidateResult = playback.PlannerResultV3{Terminal: &playback.TerminalV3{Reason: terminalSubtitleUnavailableInVersionV3, Message: err.Error(), Retryable: false}}
					if errors.Is(err, errSubtitleUnavailableInTargetV3) && subtitleMissFile == nil {
						subtitleMissFile = candidateFile
						subtitleMissReq = candidateReq
						subtitleMissProvenance = candidateProvenance
						subtitleMissAudioIndex = candidateAudioIndex
					}
				} else {
					if err := preflightPlaybackFile(r.Context(), candidateFile, h.MissingMarker, h.EventsHub); err != nil {
						continue
					}
					candidateResult, candidateToneMapErr = h.planPlaybackWithCapabilitiesV3(r.Context(), playback.PlannerInputV3{Request: candidateReq, RequestedFile: requestedFile, EffectiveFile: candidateFile, LowerVersion: lowerVersionForFileV3(lowerVersion, candidateFile), ServerBitrateCapKbps: serverBitrateCapV3(r.Context()), AudioTrackIndex: candidateAudioIndex, Settings: settings, Registry: h.transformationRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), candidateFile), Now: time.Now(), AdditionalSubtitles: subtitleInventoryFor(candidateFile), InventoryProvenance: string(candidateProvenance)})
					// A retryable tone-map discovery failure converts to
					// transcode_start_failed below; that verdict will not
					// change for a sibling file, so stop here.
					if candidateToneMapErr != nil && candidateResult.Terminal != nil &&
						candidateResult.Terminal.Reason == transcodeStartFailedReasonV3 {
						capabilityBlocked = true
					}
				}
				if candidateResult.Terminal == nil && subtitleDropped {
					// Prefer a version that keeps the viewer's subtitle; hold
					// this one back in case none does.
					if droppedSubtitle == nil {
						droppedSubtitle = &alternateCandidateV3{file: candidateFile, request: candidateReq, audioIndex: candidateAudioIndex, result: candidateResult, toneMapErr: candidateToneMapErr}
					}
					continue
				}
				clampPlannerTargetResolution(&candidateResult, candidateFile)
				if candidateResult.Terminal == nil {
					req = candidateReq
					effectiveFile = candidateFile
					audioIndex = candidateAudioIndex
					result = candidateResult
					toneMapCapabilityErr = candidateToneMapErr
					// The candidate is now the selected source, so the
					// provenance that reaches any replacement input (a degrade,
					// an escalation) must be its resolver verdict, not the
					// primary source's.
					resolutionProvenance = candidateProvenance
					break
				}
				if firstFailureFile == nil {
					firstFailureResult = candidateResult
					firstFailureToneMapErr = candidateToneMapErr
					firstFailureFile = candidateFile
					firstFailureReq = candidateReq
					firstFailureAudioIndex = candidateAudioIndex
				}
			}
			subtitleNotShown := false
			if result.Terminal != nil && droppedSubtitle != nil && subtitleRefusalV3(result.Terminal) {
				// The planner pinned the requested file's refusal on the
				// subtitle, and every sibling that plays drops the subtitle
				// too. Playing the requested file without it keeps its
				// picture, when that plan can run.
				if candidate := h.planWithoutSubtitleV3(r.Context(), playback.PlannerInputV3{Request: baseReq, RequestedFile: requestedFile, EffectiveFile: effectiveFile, LowerVersion: lowerVersionForFileV3(lowerVersion, effectiveFile), ServerBitrateCapKbps: serverBitrateCapV3(r.Context()), AudioTrackIndex: baseAudioIndex, Settings: settings, Registry: h.transformationRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), effectiveFile), Now: time.Now(), AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), effectiveFile)}); candidate != nil {
					droppedSubtitle = candidate
					subtitleNotShown = true
				}
			}
			if result.Terminal != nil && droppedSubtitle != nil {
				req = droppedSubtitle.request
				effectiveFile = droppedSubtitle.file
				audioIndex = droppedSubtitle.audioIndex
				result = droppedSubtitle.result
				toneMapCapabilityErr = droppedSubtitle.toneMapErr
				if subtitleNotShown {
					warnings = append(warnings, playback.SubtitleNotShownWarningV3())
				} else {
					warnings = append(warnings, playback.SubtitleTrackUnavailableWarningV3())
				}
			} else if result.Terminal != nil && firstFailureFile != nil {
				req = firstFailureReq
				effectiveFile = firstFailureFile
				audioIndex = firstFailureAudioIndex
				result = firstFailureResult
				toneMapCapabilityErr = firstFailureToneMapErr
			}
			if result.Terminal != nil && subtitleMissFile != nil {
				// Every alternate that could honor the subtitle selection has
				// been exhausted. Playability wins over fidelity: continue on
				// the first candidate that lacked the track with subtitles off
				// instead of terminalling the start.
				degradeReq := subtitleMissReq
				degradeReq.SubtitleTrackIndex = nil
				degradeReq.SubtitleTrackID = ""
				if preflightErr := preflightPlaybackFile(r.Context(), subtitleMissFile, h.MissingMarker, h.EventsHub); preflightErr == nil {
					degradeResult, degradeToneMapErr := h.planPlaybackWithCapabilitiesV3(r.Context(), playback.PlannerInputV3{
						Request: degradeReq, RequestedFile: requestedFile, EffectiveFile: subtitleMissFile,
						AudioTrackIndex: subtitleMissAudioIndex, Settings: settings,
						Registry: h.transformationRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), subtitleMissFile), Now: time.Now(),
						AdditionalSubtitles: subtitleInventoryFor(subtitleMissFile),
						InventoryProvenance: string(subtitleMissProvenance),
					})
					clampPlannerTargetResolution(&degradeResult, subtitleMissFile)
					if degradeResult.Terminal == nil {
						slog.InfoContext(r.Context(), "subtitle unavailable in every alternate; continuing with subtitles off",
							logComponentKey, playbackLogValueV3, "alternate_file_id", subtitleMissFile.ID)
						req = degradeReq
						effectiveFile = subtitleMissFile
						audioIndex = subtitleMissAudioIndex
						result = degradeResult
						toneMapCapabilityErr = degradeToneMapErr
						annotateSubtitleDroppedV3(&result)
					}
				}
			}
		}
	}
	if result.Terminal != nil {
		if fallback := h.planWithoutLowerVersionV3(r.Context(), input); fallback != nil {
			return *fallback, nil
		}
	}
	// Name the hunt as the cause when it replaced the release this plan started
	// with. The plan then publishes a specific substitution reason instead of the
	// generic one, so the client's notice says the version could not be played
	// rather than that it disappeared.
	reason := ""
	if effectiveFile != nil && effectiveFile.ID != entryEffectiveFileID {
		reason = substitutionReasonVersionNotPlayableV3
	}
	return alternateCandidateV3{file: effectiveFile, request: req, audioIndex: audioIndex, result: result, toneMapErr: toneMapCapabilityErr, substitutionReason: reason}, warnings
}

// startAllowsAlternateFileV3 reports whether a fresh start may plan a sibling
// version in place of the requested row. An explicit version pick is the
// viewer's decision: the start must serve that row or refuse with the honest
// reason and the version-list hint, never quietly play a different resolution
// or dynamic range. This mirrors the replan policy (replanFallbackAllowed in
// the replan path, which also requires FileSelection != explicit), so a pick
// cannot be substituted at start and then kept on the replanned session.
//
// The auto policy keeps its fallback: a client that picked nothing still gets
// the best playable version, and the substitution is published with its cause.
func startAllowsAlternateFileV3(req playback.StartRequestV3) bool {
	return req.AllowsAlternateVersions() && req.FileSelection != playback.FileSelectionExplicitV3
}

// planWithoutLowerVersionV3 is the last resort when input's lower qualities
// belonged to a lower version and no version planned: input planned as though
// the item had none, so the source follows its own policy, transcoded when 4K
// transcoding allows it and at original quality with a warning when it does
// not. Nil when no lower version served input or that plan is refused too.
func (h *PlaybackHandler) planWithoutLowerVersionV3(ctx context.Context, input playback.PlannerInputV3) *alternateCandidateV3 {
	if input.LowerVersion == nil {
		return nil
	}
	input.LowerVersion = nil
	result, toneMapErr := h.planPlaybackWithCapabilitiesV3(ctx, input)
	if result.Terminal != nil {
		return nil
	}
	return &alternateCandidateV3{file: input.EffectiveFile, request: input.Request, audioIndex: input.AudioTrackIndex, result: result, toneMapErr: toneMapErr}
}

func (h *PlaybackHandler) startPlaybackApplicationV3(r *http.Request, body []byte) (playback.DecisionResponseV3, error) { //nolint:funlen,gocyclo // startPlaybackApplicationV3 exceeds Vio's 100-line and complexity-30 limits; refactor tracked separately
	r = r.WithContext(withPlaybackRoutingPolicySnapshotV3(r.Context(), h.playbackRoutingPolicyV3()))
	timings := newPlaybackStartTimingsV3()
	var req playback.StartRequestV3
	defer func() { timings.log(r.Context(), req.PlaybackAttemptID) }()
	if err := json.Unmarshal(body, &req); err != nil {
		return playback.DecisionResponseV3{}, playbackOperationError(http.StatusBadRequest, "bad_request", "Invalid protocol v3 request body")
	}
	if !isNativeAPIV2(r.Context()) {
		// The frozen /api/v1 surface ignored these fields as unknown. Drop them
		// before validation so v1 neither rejects nor acts on them.
		req.BandwidthEstimateSource, req.ObservedThroughputKbps, req.LearnedLimit = "", nil, ""
		withoutNativeDeliveryFieldsV3(&req.ClientPlaybackContext)
	}
	warnings, err := req.NormalizeAndValidate()
	if err != nil {
		return playback.DecisionResponseV3{}, playbackOperationError(http.StatusBadRequest, "bad_request", err.Error())
	}
	if !isNativeAPIV2(r.Context()) {
		// The frozen /api/v1 surface negotiates only its original features.
		// subrip_sidecar_v1 is attempt-sticky, so dropping it here keeps it out
		// of every later replan of this attempt too.
		req.ClientFeatures = playback.WithoutFeatureV3(req.ClientFeatures, playback.FeatureBufferingQualityV1)
		req.ClientFeatures = playback.WithoutFeatureV3(req.ClientFeatures, playback.FeaturePlaybackPlanSummaryV1)
		req.ClientFeatures = playback.WithoutFeatureV3(req.ClientFeatures, playback.FeatureSubripSidecarV3)
		req.ClientFeatures = playback.WithoutFeatureV3(req.ClientFeatures, playback.FeatureMarkerThumbnailsV3)
		req.ClientFeatures = playback.WithoutFeatureV3(req.ClientFeatures, playback.FeatureHLSSubtitleRenditionsV3)
		req.ClientFeatures = playback.WithoutFeatureV3(req.ClientFeatures, playback.FeatureHLSRemuxSubtitleRenditionsV3)
		withoutNativeDeliveryFeaturesV3(&req.ClientPlaybackContext)
	}
	timings.mark("decode_validate")
	profileID := apimw.GetProfileID(r.Context())
	if profileID == "" {
		return playback.DecisionResponseV3{}, playbackOperationError(http.StatusBadRequest, "bad_request", "X-Profile-Id header is required")
	}
	if req.ProfileID != profileID {
		return playback.DecisionResponseV3{}, playbackOperationError(http.StatusBadRequest, "bad_request", "profile_id must match X-Profile-Id")
	}
	userID := apimw.GetUserID(r.Context())
	var catalogRequestedFile *models.MediaFile
	resolutionProvenance := ProbeProvenance("")
	virtualDecision := virtualPlanDecisionV3{candidateCount: -1}
	var deferredProbe *virtualDeferredProbeV3
	enqueueDeferredPostCommitProbe := func(sessionID string) {
		if deferredProbe == nil {
			return
		}
		deferredProbe.sessionID = sessionID
		h.enqueueDeferredVirtualProbeV3(r.Context(), deferredProbe)
	}
	subtitleInventoryByFileID := map[int][]playback.SubtitleInventoryEntryV3{}
	subtitleInventoryFor := func(file *models.MediaFile) []playback.SubtitleInventoryEntryV3 {
		if file == nil {
			return nil
		}
		if v, ok := subtitleInventoryByFileID[file.ID]; ok {
			return v
		}
		v := h.downloadedSubtitleInventoryV3(r.Context(), file)
		if file.ID != 0 {
			subtitleInventoryByFileID[file.ID] = v
		}
		return v
	}
	deviceID := deviceMetadataFromRequest(r).DeviceID
	requestDigests := newPlaybackStartRequestDigestsV3(body, deviceID)
	if existing, lookupErr := h.PlanStoreV3.GetAttemptByPlaybackAttemptID(r.Context(), req.PlaybackAttemptID); lookupErr == nil { //nolint:nestif // startPlaybackApplicationV3 exceeds Vio's nestif complexity-4 limit; refactor tracked separately
		if existing.UserID != userID || existing.ProfileID != profileID || existing.RequestedMediaFileID != req.FileID ||
			!requestDigests.matches(existing.RequestDigest) {
			return playback.DecisionResponseV3{}, playbackOperationError(http.StatusConflict, "playback_attempt_reused", "The playback attempt ID belongs to a different request")
		}
		response := decisionResponseFromAttemptV3(existing)
		// A terminal publishes no subtitle URLs, so it replays on either
		// surface; the surface check protects only a playable plan.
		if response.Terminal != nil {
			return response, nil
		}
		if err := requireAttemptAPISurfaceV3(r.Context(), existing, req.ClientFeatures); err != nil {
			return playback.DecisionResponseV3{}, err
		}
		// The replayed plan is only usable while its session is alive; a dead
		// session must surface as a retryable terminal so the client mints a
		// fresh attempt instead of replaying a plan it can never stream. A
		// stopped attempt is dead on every replica, whether or not this one
		// still holds the session.
		if existing.SessionID == "" {
			return playback.DecisionResponseV3{}, playbackOperationError(http.StatusInternalServerError, "internal_error", "Stored playback attempt has no replayable decision")
		}
		if existing.StoppedAt != nil {
			return playback.NewTerminalResponseV3(terminalSessionExpiredV3, "The playback session for this attempt has ended.", true), nil
		}
		if _, sessionErr := h.sessionMgr.GetSession(existing.SessionID); sessionErr != nil {
			return playback.NewTerminalResponseV3(terminalSessionExpiredV3, "The playback session for this attempt has ended.", true), nil
		}
		return response, nil
	} else if !errors.Is(lookupErr, playback.ErrSessionNotFound) {
		return playback.DecisionResponseV3{}, playbackOperationError(http.StatusInternalServerError, "internal_error", "Failed to check playback attempt idempotency")
	}
	timings.mark("idempotency")
	r = r.WithContext(withServerBitrateCapV3(r.Context(), requestBitrateCapV3(r.Context())))
	requestedFile, err := h.loadAuthorizedFile(r, req.FileID)
	if err != nil {
		return playback.DecisionResponseV3{}, playbackFileOperationError(err)
	}
	// The authorized catalog row is kept so a decode-rejection rotation can
	// re-resolve the same request with each rejected provider candidate
	// excluded, after requestedFile has been replaced by a resolved candidate.
	catalogRequestedFile = requestedFile
	// resolvedEffectiveFileID is the catalog row id of the candidate the
	// virtual resolver actually served. It differs from the requested row when
	// the resolver substituted a different candidate that has its own row
	// (dedup keeper, rotation, sibling); the plan's effective_media_file_id
	// must name that row so the substitution is visible to the client.
	resolvedEffectiveFileID := 0
	// resolved holds the virtual resolver's result when this start took the
	// virtual path. It is kept so the post-commit spawn can read the deferred
	// probe the resolve handed back (see resolved.DeferredProbe).
	var resolved resolvedVirtualPlaybackSource
	// persistCtx carries every terminal decision this start has to store. It is
	// the request context, captured before the start phase below narrows it:
	// the phase deadline is precisely what expires first when a start runs out
	// of time, and a terminal saved on that dead context fails deterministically
	// — no attempt row, so the retry re-walks the whole resolve into the same
	// wall. The replan path persists its terminal on the request context the
	// same way.
	persistCtx := r.Context()
	// Virtual sources are provider-neutral URIs, not FFmpeg inputs. Resolve and
	// probe them through the virtual provider before the generic probe repair
	// path, which only understands local/HTTP media files.
	if isVirtualPlaybackFile(requestedFile) { //nolint:nestif // startPlaybackApplicationV3 exceeds Vio's nestif complexity-4 limit; refactor tracked separately
		// One deadline owns the whole user-visible start: the candidate
		// resolve, transport startup, and the completed-release pin retry all
		// derive from it. The detached catalog discovery may already be
		// holding the provider, so a start that paid that wait must not then
		// hand the pin retry a fresh 20 s and stack both onto one click.
		startPhaseCtx, startPhaseCancel := withVirtualStartPhaseDeadline(r.Context(), virtualStartPhaseBudget)
		defer startPhaseCancel()
		r = r.WithContext(startPhaseCtx)
		// The resolve half gets its own deadline inside the phase. Everything
		// that names a candidate — the primary resolve, the alternate-version
		// walk with its per-candidate budgets, the stale-pin recovery — derives
		// from it, so a slow listing cannot spend the window the phase reserved
		// for the transport's readiness wait. When the resolve half is spent the
		// walk is refused rather than shortened into that window.
		resolveCtx, resolveCancel := withVirtualStartPhaseDeadline(startPhaseCtx, virtualStartResolveBudget)
		defer resolveCancel()
		r = r.WithContext(resolveCtx)
		requestedCatalogFileID := requestedFile.ID
		// A detail-page read may have scheduled a detached probe for this row.
		// It is left running: the ffprobe it opened is behind the shared
		// VirtualProbeCache single-flight, so this start's own probe (deferred
		// past the transport commit, or synchronous inside a replan) joins that
		// flight instead of opening a second one, and its durable evidence
		// stamps the row the next start reuses. Canceling it here would throw
		// away the work the detail page already paid for and then re-do it.
		if offered := strings.TrimSpace(req.OfferedVirtualURI); offered != "" {
			if virtualResultCandidateID(offered) == "" ||
				virtualPlaybackNeutralKey(offered) != virtualPlaybackNeutralKey(requestedFile.FilePath) {
				return playback.DecisionResponseV3{}, playbackOperationError(http.StatusBadRequest, "invalid_offered_candidate",
					"The offered release does not belong to this title")
			}
		}
		// An auto selection skips a catalog row the catalog marked failed, even
		// when the row already carries a concrete result= identity adopted by an
		// earlier auto pick; only an explicit pick or a forced relink re-tries
		// the known-bad candidate.
		allowFailedCandidate := req.FileSelection == playback.FileSelectionExplicitV3 || req.ForceRelink
		// The start path is a fresh selection: declare the unbound intent on the
		// request handed to the walk, which threads it (rather than forcing it)
		// so the stale-pin recovery may re-pin here while a future session-bound
		// caller reusing the walk keeps its own binding intent and refuses.
		var resolveErr error
		walkSourceReq := r.WithContext(withVirtualSessionBindingV3(r.Context(), false))
		if retained, ok := h.retainedVirtualStartSourceV3(r, userID, profileID, requestedFile, req); ok {
			// A still-live session already proved this candidate and its
			// inventory. A start re-entering after a failure serves that
			// generation instead of paying another discovery/listing/probe
			// round-trip for the same bytes.
			resolved = retained
		} else {
			resolved, resolveErr = h.resolveVirtualStartWithVersionFallback(walkSourceReq, requestedFile, profileID, req, allowFailedCandidate, intOrZeroHandlerV3(req.BandwidthCapKbps))
		}
		if resolveErr == nil {
			// The plan must name an effective row from the same version list
			// the client shows. A resolver substitution that lands on any other
			// row is refused here, before planning, and the walk tries the next
			// candidate; when none qualifies the start answers the version-list
			// hint instead of planning a row the client cannot reconcile.
			resolved, resolveErr = h.confineStartEffectiveVersionV3(
				r, walkSourceReq, requestedFile, resolved, req, profileID, allowFailedCandidate,
				intOrZeroHandlerV3(req.BandwidthCapKbps),
			)
		}
		if resolveErr != nil {
			if r.Context().Err() == context.DeadlineExceeded {
				// The resolve half ran out during catalog/provider work. Keep
				// it distinct from a release verdict: the release was not
				// indicted, the start simply hit the bound it was given.
				slog.WarnContext(r.Context(), "virtual playback start budget exhausted during candidate resolve",
					"component", "api", "signal", "start_budget_exhausted",
					"file_id", requestedFile.ID, "error", resolveErr)
			}
			if virtualStartBudgetExhausted(r.Context(), persistCtx) {
				// Nothing about the release was established: the start ran out
				// of its resolve window while the request itself is alive. A
				// stored terminal would replay a verdict this start never
				// reached, so answer the retryable 503 and let the retry resolve
				// again — usually against the listing discovery has since
				// seeded.
				slog.WarnContext(persistCtx, "virtual playback start answering the exhausted resolve budget as retryable",
					"component", "api", "signal", "start_budget_exhausted",
					"file_id", requestedFile.ID,
					"resolve_budget_ms", virtualStartResolveBudget.Milliseconds(),
					"phase_budget_ms", virtualStartPhaseBudget.Milliseconds(),
					"error", resolveErr)
				return playback.DecisionResponseV3{}, startBudgetExhaustedOperationErrorV3()
			}
			// A confirmed-dead pinned release is indicted here so a retry does
			// not re-resolve it; an empty provider listing is not a verdict and
			// is left unmarked.
			h.stampStartVirtualCandidateFailed(r.Context(), requestedFile, resolveErr)
			termFileID := requestedFile.ID
			// An explicit pick is recorded against the row the viewer chose. A
			// discovery rewrite may have replaced the item's default row, but that
			// row is not this request: a retry re-sends the selected id, and a
			// terminal that named a sibling would report a different file's fate
			// (the measured production case terminalised on the item's default HDR
			// row while the viewer had picked a 720p release).
			if !explicitPickRequestedV3(req) {
				if requestedFile.EpisodeID != "" && h.VirtualEpisodeFileLookup != nil {
					if dbFile, err := h.VirtualEpisodeFileLookup(r.Context(), requestedFile.EpisodeID); err == nil && dbFile != nil && dbFile.ID > 0 {
						termFileID = dbFile.ID
					}
				} else if requestedFile.EpisodeID == "" && h.VirtualContentFileLookup != nil && requestedFile.ContentID != "" {
					if dbFile, err := h.VirtualContentFileLookup(r.Context(), requestedFile.ContentID); err == nil && dbFile != nil && dbFile.ID > 0 {
						termFileID = dbFile.ID
					}
				}
			}
			req.FileID = termFileID
			terminal := virtualStartUnresolvedTerminalV3(resolveErr)
			response, persistErr := h.persistTerminalStartDecisionV3(persistCtx, userID, profileID, req, requestDigests, termFileID, termFileID, terminal)
			if persistErr != nil {
				return playback.DecisionResponseV3{}, startTerminalPersistOperationErrorV3(persistCtx, persistErr, terminalStartFailureV3(terminal))
			}
			return response, nil
		}
		if resolved.File == nil {
			return playback.DecisionResponseV3{}, playbackOperationError(http.StatusBadGateway, "virtual_resolve_failed", "Failed to resolve virtual source")
		}
		// The resolve half is over. Hand the rest of the start the whole-phase
		// deadline so planning, the transport commit and its readiness wait are
		// bounded by the phase rather than by the resolve's share of it.
		resolveCancel()
		r = r.WithContext(startPhaseCtx)
		resolvedEffectiveFileID = resolved.File.ID
		resolvedFile := *resolved.File
		resolvedFile.ID = requestedCatalogFileID
		requestedFile = &resolvedFile
		requestedFile.FilePath = resolved.URI
		requestedFile.VirtualOwnerInstallationID = resolved.OwnerID
		// Do NOT mutate req.FileID here: the original caller-supplied file ID
		// must survive into the attempt record for idempotent replay.
		resolutionProvenance = resolved.Provenance
		deferredProbe = resolved.DeferredProbe
		virtualDecision.effectiveVirtualOwner = effectiveVirtualOwner(resolved.OwnerID, resolved.File.VirtualOwnerInstallationID)
		virtualDecision.candidateRank = resolved.CandidateRank
		virtualDecision.candidateCount = resolved.CandidateCount
		// The alternate-version walk resolved a different release than the one
		// asked for. Carry that truth onto the plan so the client can show an
		// honest substitution notice naming the requested release.
		if resolved.SubstitutedFromFileID > 0 {
			virtualDecision.substitutionReason = resolved.SubstitutionReason
		}
	} else {
		requestedFile = h.ensurePlaybackProbeStart(r.Context(), requestedFile)
	}
	if requestedFile.Duration <= 0 { //nolint:nestif // startPlaybackApplicationV3 exceeds Vio's nestif complexity-4 limit; refactor tracked separately
		if requestedFile.EpisodeID != "" && h.EpisodeLookup != nil {
			if ep, err := h.EpisodeLookup.GetByID(r.Context(), requestedFile.EpisodeID); err == nil && ep != nil && ep.Runtime > 0 {
				requestedFile.Duration = ep.Runtime * 60
			}
		} else if requestedFile.ContentID != "" && h.ItemLookup != nil {
			if item, err := h.ItemLookup.GetByID(r.Context(), requestedFile.ContentID); err == nil && item != nil && item.Runtime > 0 {
				requestedFile.Duration = item.Runtime * 60
			}
		}
	}
	// A session restart (the client tears down and starts a new playback
	// attempt, e.g. after a recovery replan) historically dropped the viewer's
	// explicit audio choice and subtitle selection because the new request
	// carried neither. Carry both from the live session for the same profile
	// and file when the request names neither. An out-of-range carry is
	// reported as a degradation warning instead of disappearing.
	audioCarried, carrySubtitleUnavailable, audioUnavailable := h.carrySelectionsFromActiveSessionV3(
		r.Context(), userID, profileID, requestedFile, &req,
		clientip.FromContext(r.Context()), playbackClientInfoFromRequest(r).UserAgent)
	if audioCarried {
		virtualDecision.startAudioSelectionOrigin = SelectionOriginExplicit
	}
	if audioUnavailable {
		warnings = append(warnings, playback.AudioTrackUnavailableWarningV3())
	}
	if carrySubtitleUnavailable {
		warnings = append(warnings, playback.SubtitleTrackUnavailableWarningV3())
	}
	timings.mark("file_load_probe")
	audioIndex, audioDegraded, err := resolveV3AudioIndex(requestedFile, req.AudioTrackID, req.AudioTrackIndex)
	if err != nil {
		return playback.DecisionResponseV3{}, playbackOperationError(http.StatusBadRequest, "bad_request", err.Error())
	}
	if audioDegraded {
		warnings = append(warnings, playback.AudioTrackUnavailableWarningV3())
	}
	// serverAudio marks a track the server chose by preference alone, which
	// the request's capabilities may still refine below.
	serverAudio := false
	if req.AudioTrackID == "" && req.AudioTrackIndex == nil {
		var saved bool
		audioIndex, saved, err = h.preferredAudioTrackIndexV3(r.Context(), userID, profileID, deviceID, requestedFile)
		if err != nil {
			return playback.DecisionResponseV3{}, playbackOperationError(http.StatusInternalServerError, "internal_error", "Failed to load the saved audio preference")
		}
		serverAudio = !saved
	}
	// A version switch carries the viewer's explicit audio choice by identity.
	// Resolve it on the file this start actually selects; a track this release
	// cannot carry degrades to the server's choice and is reported, never
	// silently substituted.
	if strings.TrimSpace(req.CarriedAudioTrackID) != "" {
		source := h.carriedAudioSourceV3(r.Context(), &req, requestedFile, userID, profileID,
			clientip.FromContext(r.Context()), playbackClientInfoFromRequest(r).UserAgent)
		if carriedIndex, ok := h.resolveCarriedAudioTrackV3(r.Context(), req.CarriedAudioTrackID, requestedFile, source); ok {
			audioIndex = carriedIndex
			req.AudioTrackIndex = &audioIndex
			req.AudioTrackID = playback.TrackIDV3(requestedFile.ID, "audio", carriedIndex)
			serverAudio = false
		} else {
			warnings = append(warnings, playback.AudioTrackUnavailableWarningV3())
		}
	}
	timings.mark("audio_preference")
	effectiveFile := requestedFile
	// The plan's effective file is the candidate the resolver actually served.
	// requestedFile keeps the caller's requested row id (for the attempt record
	// and idempotent replay), so a substituted candidate needs its own copy
	// carrying the served row id; otherwise effective_media_file_id would
	// silently report the requested row for bytes it did not describe.
	if resolvedEffectiveFileID > 0 && resolvedEffectiveFileID != requestedFile.ID {
		effectiveCopy := *requestedFile
		effectiveCopy.ID = resolvedEffectiveFileID
		effectiveFile = &effectiveCopy
	}
	settings, settingsErr := h.plannerSettingsV3Result(r.Context())
	timings.mark("planner_settings")
	if err := preflightPlaybackFile(r.Context(), effectiveFile, h.MissingMarker, h.EventsHub); err != nil {
		return playback.DecisionResponseV3{}, playbackPreflightOperationError(err)
	}
	timings.mark("file_preflight")
	// preferredAudio is that choice before the refinement, which a later
	// switch to another version refines again on that version.
	preferredAudio := audioIndex
	if serverAudio && settingsErr == nil {
		audioIndex = h.playableStartAudioV3(r.Context(), req, requestedFile, requestedFile, audioIndex, settings)
		timings.mark("audio_capabilities")
	}
	// The viewer's intent on the requested file, before any version fallback
	// remaps it; replans restore it on a version that has it.
	intent := startIntentV3{audioIndex: audioIndex}
	if req.SubtitleTrackID != "" || req.SubtitleTrackIndex != nil {
		intent.subtitle = &playback.TrackIdentityV3{ID: req.SubtitleTrackID, Index: copyOptionalIntV3(req.SubtitleTrackIndex)}
	}
	if req.StartPosition == nil { //nolint:nestif // startPlaybackApplicationV3 exceeds Vio's nestif complexity-4 limit; refactor tracked separately
		req.StartPosition, err = h.resumePositionV3(r.Context(), userID, profileID, effectiveFile)
		if err != nil {
			return playback.DecisionResponseV3{}, playbackOperationError(http.StatusInternalServerError, "internal_error", "Failed to load saved playback progress")
		}
		// Multipart audiobook files share one item progress row, while the
		// planner starts a single file-local timeline. When the stored resume
		// point is item-absolute, select the corresponding part and translate
		// it to that part's local clock. Explicit client positions remain
		// unchanged.
		if req.StartPosition != nil && effectiveFile.PresentationPartTotal > 1 {
			target, local, resolveErr := h.multipartResumeFileV3(r.Context(), effectiveFile, *req.StartPosition, requestAccessFilter(r))
			if resolveErr != nil {
				// An item-absolute position cannot be projected onto this
				// file's part-local clock without the complete ordered part
				// list. Applying it to the requested part would seek that part
				// far past its end, so start from the beginning instead.
				slog.DebugContext(r.Context(), "protocol v3 multipart resume mapping unavailable", "component", "api", "file_id", effectiveFile.ID, "error", resolveErr)
				req.StartPosition = nil
			} else if target != nil && target.ID != requestedFile.ID && !req.AllowsAlternateVersions() {
				// A fixed-file attempt cannot resume into a different part.
				req.StartPosition = new(float64(0))
			} else if target != nil {
				effectiveFile = h.ensurePlaybackProbeStart(r.Context(), target)
				audioIndex = remapAudioIndexV3(requestedFile, effectiveFile, audioIndex)
				if err := preflightPlaybackFile(r.Context(), effectiveFile, h.MissingMarker, h.EventsHub); err != nil {
					return playback.DecisionResponseV3{}, playbackPreflightOperationError(err)
				}
				req.StartPosition = &local
			} else {
				req.StartPosition = nil
			}
		}
	}
	timings.mark("resume")
	// If resume selected a specific presentation part, alternate playback must
	// stay on that same part. Sibling parts are not interchangeable versions.
	alternateBase := requestedFile
	if effectiveFile.PresentationPartTotal > 1 && effectiveFile.PresentationPartIndex > 0 {
		alternateBase = effectiveFile
	}
	if err := h.applyConnectionReading(r, &req); err != nil {
		return playback.DecisionResponseV3{}, playbackStoreOperationError()
	}
	// The start without the limit, planned only when the plan would carry the
	// limit's label, to check that the limit lowered it. A stall's estimate
	// goes with the limit; the connection's reading then stands in for it.
	unlimitedReq, unlimitedFile, unlimitedAudioIndex := withoutObservedEstimateV3(req), effectiveFile, audioIndex
	learnedLimit, limitErr := h.applyLearnedLimit(r, &req, true, nil)
	if limitErr != nil {
		return playback.DecisionResponseV3{}, playbackStoreOperationError()
	}
	bufferedBase := effectiveFile
	effectiveFile, req, audioIndex = h.lowerBufferedVersion(r, req, effectiveFile, audioIndex, settings)
	if serverAudio && settingsErr == nil && effectiveFile != bufferedBase {
		// A track refined on the requested file means little on another
		// version: refine the preference again on the one that plays.
		audioIndex = h.playableStartAudioV3(r.Context(), req, requestedFile, effectiveFile, remapAudioIndexV3(requestedFile, effectiveFile, preferredAudio), settings)
	}
	lowerVersion := h.lowerVersionV3(r.Context(), req, alternateBase, settings, requestAccessFilter(r))
	planned, fallbackWarnings := h.planStartV3(r, req, requestedFile, alternateBase, effectiveFile, audioIndex, settings, lowerVersion, profileID, resolutionProvenance)
	req, effectiveFile, audioIndex = planned.request, planned.file, planned.audioIndex
	warnings = append(warnings, fallbackWarnings...)
	timings.mark("planning")
	result := retryIncompleteToneMapPlanningV3(planned.result, planned.toneMapErr)
	result = retryIncompletePlaybackSettingsV3(result, settingsErr)
	// Carry the fallback's own cause onto whichever plan finally commits, so the
	// plan can publish a specific substitution reason. publishSubstitutionFieldsV3
	// keeps a reason already present on the plan, so this wins over the generic
	// cause it would otherwise derive.
	if planned.substitutionReason != "" && virtualDecision.substitutionReason == "" {
		virtualDecision.substitutionReason = planned.substitutionReason
	}
	h.clarifyOriginalQuality4KTerminalV3(r.Context(), requestAccessFilter(r), result.Terminal, requestedFile, !shouldTryAlternateFileV3(req.QualityPreference))
	hintExplicitSelectionAlternateAvailableV3(result.Terminal, req.FileSelection)
	// The exact app identity is logged with every decision so a route or
	// terminal reported against one build is attributable without asking the
	// user which version they are running.
	clientInfo := playbackClientInfoForStartV3(r, req.ClientPlaybackContext)
	if result.Terminal != nil {
		slog.InfoContext(r.Context(), "playback plan decided", append([]any{
			logComponentKey, playbackLogValueV3,
			requestIDLogKeyV3, chimw.GetReqID(r.Context()),
			"outcome", "terminal",
			"reason", result.Terminal.Reason,
			"detail", result.Terminal.Detail,
			"file_id", effectiveFile.ID,
			"quality_preference", req.QualityPreference,
		}, clientInfo.LogAttrs()...)...)
		terminal := playback.NewTerminalResponseFromTerminalV3(result.Terminal)
		response, persistErr := h.persistTerminalStartDecisionV3(persistCtx, userID, profileID, req, requestDigests, requestedFile.ID, effectiveFile.ID, terminal)
		if persistErr != nil {
			return playback.DecisionResponseV3{}, startTerminalPersistOperationErrorV3(persistCtx, persistErr, terminalStartFailureV3(terminal))
		}
		if response.Terminal != nil {
			h.enqueueRouteEventV3(playback.RouteEventRecordV3{RouteEventV3: playback.RouteEventV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: req.PlaybackAttemptID, Event: playback.RouteEventTerminalV3, FallbackReason: response.Terminal.Reason, OutputContextID: req.ClientPlaybackContext.Output.OutputContextID}, UserID: userID, ProfileID: profileID, ClientName: clientInfo.Name, ClientVersion: clientInfo.Version, ClientBuild: clientInfo.Build, ClientChannel: clientInfo.Channel, ClientModel: req.ClientPlaybackContext.Device.Model})
		}
		return response, nil
	}
	// A refused progressive remux is escalated before the decision is logged or
	// a session is opened, so the logged route is the one that will actually run.
	//
	// The mark below isolates the escalation helper itself. Everything between
	// the planning mark and here — the subtitle/audio in-place degrades and
	// the alternate-version hunt with its per-alternate resolves — is
	// post-plan recovery, measured by the preceding mark: without that split
	// a 13s recovery walk (Citizen Vigilante: three ~3.2s forced re-lists)
	// misattributes onto the escalation helper, which is usually ~0ms.
	timings.mark("post_plan_recovery")
	escalated, escalateErr := h.escalateRefusedProgressiveRemuxV3(r.Context(), headerAuthenticatedMediaV3(req.ClientFeatures),
		func() playback.PlannerInputV3 {
			input := h.plannerInputV3(r.Context(), req, requestedFile, effectiveFile, audioIndex, nil, resolutionProvenance)
			input.LowerVersion = lowerVersionForFileV3(lowerVersion, effectiveFile)
			return input
		}, result)
	if escalateErr != nil {
		persistedResponse, persistErr := h.startFailureDecisionV3(persistCtx, userID, profileID, req, requestDigests, requestedFile.ID, effectiveFile.ID, escalateErr)
		if persistErr != nil {
			return playback.DecisionResponseV3{}, startTerminalPersistOperationErrorV3(persistCtx, persistErr, escalateErr)
		}
		return persistedResponse, nil
	}
	result = escalated
	timings.mark("remux_escalation")
	// Marked on the plan that will run, after every fallback that can replace it.
	markLearnedLimit(&result, learnedLimit, requestedFile, req, serverBitrateCapV3(r.Context()))
	if result.Plan != nil && result.Plan.BandwidthLimit != nil {
		// Bitrates alone cannot tell whether the requested file could play. When
		// compatibility, or the budget the start has without the limit, picks
		// the same version, or nothing plays without it, the limit lowered
		// nothing.
		readingErr := h.applyConnectionReading(r, &unlimitedReq)
		unlimitedFile, unlimitedReq, unlimitedAudioIndex = h.lowerBufferedVersion(r, unlimitedReq, unlimitedFile, unlimitedAudioIndex, settings)
		unlimited, _ := h.planStartV3(r, unlimitedReq, requestedFile, alternateBase, unlimitedFile, unlimitedAudioIndex, settings, lowerVersion, profileID, resolutionProvenance)
		if readingErr != nil || unlimited.result.Plan == nil || deliveredKbpsV3(result.Plan) >= deliveredKbpsV3(unlimited.result.Plan) {
			result.Plan.BandwidthLimit = nil
		}
		timings.mark("unlimited_planning")
	}
	appendStartWarningsV3(&result, warnings)
	// The fresh-start resolve deferred its full audio/subtitle enumeration and
	// handed back the probe to run once the transport has committed. Mark the
	// plan provisional now, before the transport commit and before the attempt
	// record is written, so an idempotent replay of this attempt republishes the
	// same provisional truth instead of appearing fully probed. tracks_pending
	// is part of the deferred_track_inventory_v1 lifecycle, so it is only
	// advertised to a surface/client that negotiated it: the frozen /api/v1
	// contract and a v2 client that did not advertise the feature keep the
	// byte-for-byte response they had before.
	if deferredProbe != nil && result.Plan != nil && h.deferTrackInventoryNegotiatedV3(r, req) {
		result.Plan.TracksPending = true
	}
	// Hand the substitution cause to the plan before the commit.
	// publishSubstitutionFieldsV3 runs after the transport is prepared and only
	// fills an empty reason, so this is what lets the wire carry the resolver's
	// or the fallback's real cause instead of "unknown".
	if result.Plan != nil && result.Plan.SubstitutionReason == "" {
		result.Plan.SubstitutionReason = virtualDecision.substitutionReason
	}
	phasesCtx, phaseTimings := withTransportPhaseTimingsV3(r.Context())
	response, statusErr := h.startPlannedPlaybackV3(r.WithContext(phasesCtx), userID, profileID, req, requestDigests, requestedFile, effectiveFile, audioIndex, intent, result, clientInfo)
	timings.mark("session_transport_commit")
	timings.observeTransportPhasesV3(phaseTimings)
	if statusErr != nil {
		if virtualStartBudgetExhausted(r.Context(), persistCtx) {
			// The phase ran out while the transport was starting. The readiness
			// wait reports that as a transport failure, but the transport never
			// got the window the phase reserved for it: report the budget, not a
			// verdict about the release, and store nothing so a retry starts a
			// fresh transport instead of replaying this one.
			slog.WarnContext(persistCtx, "virtual playback start phase exhausted during transport startup; answering retryable",
				"component", "api", "signal", "start_budget_exhausted",
				"file_id", requestedFile.ID,
				"reason", statusErr.reason,
				"phase_budget_ms", virtualStartPhaseBudget.Milliseconds(),
				"error", statusErr.cause)
			return playback.DecisionResponseV3{}, startBudgetExhaustedOperationErrorV3()
		}
		// A decoder-rejected source is a candidate failure, not a route failure:
		// rotate to the next provider release before any terminal. An explicit
		// pin is never substituted; it terminalls with the version-list hint.
		if statusErr.reason == candidateSourceDecodeRejectedReasonV3 && isVirtualPlaybackFile(requestedFile) {
			if req.FileSelection != playback.FileSelectionExplicitV3 {
				rejectedID := virtualResultCandidateID(effectiveFile.FilePath)
				if rejectedID == "" {
					rejectedID = virtualResultCandidateID(requestedFile.FilePath)
				}
				if rotated, ok := h.rotateRejectedVirtualCandidateStartV3(r, userID, profileID, req, requestDigests, catalogRequestedFile, settings, settingsErr, clientInfo, rejectedID); ok {
					return rotated, nil
				}
			}
			terminal := sourceDecodeFailedTerminalResponseV3(req.FileSelection)
			persisted, persistErr := h.persistTerminalStartDecisionV3(persistCtx, userID, profileID, req, requestDigests, requestedFile.ID, effectiveFile.ID, terminal)
			if persistErr != nil {
				return playback.DecisionResponseV3{}, startTerminalPersistOperationErrorV3(persistCtx, persistErr, terminalStartFailureV3(terminal))
			}
			return persisted, nil
		}
		// A transport failure that names an audio condition (a node declining
		// the audio recipe, audio transcoding disabled) must not rotate the
		// release. The reason split already keeps those out of this branch; the
		// guard makes the audio exclusion explicit so a future caller cannot
		// fold an audio reason back into the video transport reason.
		//
		// A seek-anchor resolve that failed transiently (relay 5xx) or exhausted
		// its bounded budget is a retryable seek error, not a verdict about the
		// release. The same-release retry above may still recover it; the
		// alternate walk below resolves and probes every sibling through the same
		// failing relay and cannot answer faster, so it is skipped. Surface the
		// seek error for the client to retry instead of rotating.
		if statusErr.reason == transcodeStartFailedReasonV3 && !audioOnlyTransportReasonV3(statusErr.reason) && isVirtualPlaybackFile(requestedFile) && req.FileSelection != playback.FileSelectionExplicitV3 {
			// A single transient provider error (an upstream 5xx or timeout on
			// the seek-anchor probe or the first bytes) must not change the
			// release. Retry the same release once with a fresh relay
			// registration and a short bounded backoff before considering a
			// sibling; only a second transient failure — or a hard signal —
			// walks the alternates below. The fresh marker mints a new relay
			// entry so the retry presents new bytes instead of replaying the
			// registration whose upstream just failed.
			if retriedResponse, retried := h.recoverVirtualTransportSameReleaseV3(r.Context(), playback.IsTransientProviderError(statusErr.cause), func(retryCtx context.Context) (playback.DecisionResponseV3, *transportErrorV3) {
				// The same-release retry is a second commit with its own
				// phases. Install a fresh recorder on the retry context and
				// emit its phase line separately, so the retry is attributable
				// without disturbing the first attempt's composite line.
				retryPhasesCtx, retryRecorder := withTransportPhaseTimingsV3(retryCtx)
				retryStartedAt := time.Now()
				retryResponse, retryStatusErr := h.startPlannedPlaybackV3(r.WithContext(retryPhasesCtx), userID, profileID, req, requestDigests, requestedFile, effectiveFile, audioIndex, intent, result, clientInfo)
				logTransportRetryPhasesV3(r.Context(), req.PlaybackAttemptID, retryStartedAt, retryRecorder)
				return retryResponse, retryStatusErr
			}); retried {
				// The retry committed a transport for the same deferred plan, so
				// run the enumeration it still promises before returning.
				enqueueDeferredPostCommitProbe(retriedResponse.SessionID)
				return retriedResponse, nil
			}
			if statusErr.seekFailure {
				slog.WarnContext(r.Context(), "virtual playback seek anchor failed; answering the retryable seek error without rotating the release",
					logComponentKey, playbackLogValueV3,
					"playback_attempt_id", req.PlaybackAttemptID,
					"requested_file_id", requestedFile.ID,
					"effective_file_id", effectiveFile.ID,
					"error", statusErr.cause)
			}
			if !statusErr.seekFailure {
				alternateOrder := alternateOrderingForClient(req.Capabilities)
				// A transient provider (upstream 5xx) failure caps the alternates
				// tried in this start so the failing upstream is not hammered once
				// per alternate; a generic transport failure keeps the full list.
				if alternates, alternateErr := h.virtualTransportAlternatesV3(r.Context(), requestedFile, requestAccessFilter(r), alternateOrder, playback.IsTransientProviderError(statusErr.cause)); alternateErr == nil && len(alternates) > 0 {
					for alternateRank, altCandidate := range alternates {
						alternate, alternateProvenance, err := h.prepareVirtualAlternateFileV3(r, altCandidate, profileID, true)
						if err != nil || alternate == nil {
							continue
						}
						alternateAudio := remapAudioIndexV3(effectiveFile, alternate, audioIndex)
						alternateRequest := req
						// A transport failure that moves the release is a candidate
						// rotation: run the same carry-or-degrade decision the replan
						// drift and stream-state paths use. The generic edition-switch
						// remap accepts the first language/format survivor and crosses
						// output muxers, which is how a rotated release's wrong track
						// gets served. A same-inventory fast path still keeps the exact
						// ordinal without re-identifying it, and a miss clears the
						// selection for one fast degrade instead of hunting candidates
						// with a stale ordinal.
						carryDecision, subtitleErr := h.rekeySubtitleSelectionWithRotationV3(
							r.Context(), &alternateRequest, carriedInventorySourceV3{file: effectiveFile}, alternate, true,
						)
						if subtitleErr != nil {
							// The subtitle store could not prove whether the carried
							// track survived the rotation. Reusing the old ordinal
							// would play whatever now occupies it, so skip this
							// candidate rather than guess.
							continue
						}
						alternateResult, _ := h.planPlaybackWithCapabilitiesV3(r.Context(), playback.PlannerInputV3{
							Request: alternateRequest, RequestedFile: requestedFile, EffectiveFile: alternate,
							AudioTrackIndex: alternateAudio, Settings: settings,
							Registry:        h.transformationRegistryV3(r.Context()),
							DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), alternate), Now: time.Now(),
							AdditionalSubtitles: subtitleInventoryFor(alternate),
							InventoryProvenance: string(alternateProvenance),
						})
						clampPlannerTargetResolution(&alternateResult, alternate)
						if alternateResult.Terminal == nil {
							slog.WarnContext(r.Context(), "virtual playback transport failed; retrying compatible alternate", "component", "playback", "requested_file_id", requestedFile.ID, "alternate_file_id", alternate.ID, "alternate_order_4k_first", alternateOrder.Prefer4K, "error", statusErr.cause)
							alternateDecision := virtualDecision
							alternateDecision.candidateRank = alternateRank
							alternateDecision.candidateCount = len(alternates)
							// A transport failure moved the release: name the
							// requested row and the cause on the plan.
							alternateDecision.substitutionReason = substitutionReasonTransportFailedV3
							if alternateResult.Plan != nil {
								alternateResult.Plan.SubstitutionReason = alternateDecision.substitutionReason
							}
							// The alternate is a second commit with its own
							// phases: install a fresh recorder and emit its
							// phase line separately, so the walk is
							// attributable too.
							alternatePhasesCtx, alternateRecorder := withTransportPhaseTimingsV3(r.Context())
							// Carry the matcher's verdict into the commit so the new
							// session records the carried position and reports a
							// fuzzy carry. A confirmed miss stays out: the alternate
							// is a fresh commit, not a pre-rotation subtitle URL that
							// must be failed closed.
							alternateCtx := alternatePhasesCtx
							if carryDecision.outcome.Recorded && carryDecision.outcome.Matched &&
								carryDecision.outcome.ToFileID == alternate.ID {
								alternateCtx = withSubtitleRotationOutcomeV3(alternatePhasesCtx, carryDecision.outcome)
							}
							alternateStartedAt := time.Now()
							alternateResponse, alternateStatusErr := h.startPlannedPlaybackV3(r.WithContext(alternateCtx), userID, profileID, alternateRequest, requestDigests, requestedFile, alternate, alternateAudio, startIntentV3{audioIndex: alternateAudio}, alternateResult, clientInfo)
							logTransportRetryPhasesV3(r.Context(), req.PlaybackAttemptID, alternateStartedAt, alternateRecorder)
							if alternateStatusErr == nil {
								return alternateResponse, nil
							}
						}
					}
				}
			}
		}
		if statusErr.reason == "playback_attempt_reused" {
			return playback.DecisionResponseV3{}, playbackOperationError(http.StatusConflict, "playback_attempt_reused", statusErr.message)
		}
		failureAttrs := []any{
			logComponentKey, playbackLogValueV3,
			"reason", statusErr.reason,
			"retryable", statusErr.retryable,
			"playback_attempt_id", req.PlaybackAttemptID,
			"requested_file_id", requestedFile.ID,
			"effective_file_id", effectiveFile.ID,
		}
		if statusErr.cause != nil {
			failureAttrs = append(failureAttrs, "error", statusErr.cause)
		}
		// The error text is sanitized centrally by the opslog sink; keep the
		// structured cause here so the failure line stays diagnosable.
		switch {
		case statusErr.reason == policyErrorInternal:
			slog.ErrorContext(r.Context(), "protocol v3 planned playback failed", failureAttrs...)
		case statusErr.cause != nil:
			slog.WarnContext(r.Context(), "protocol v3 planned playback failed", failureAttrs...)
		default:
			slog.InfoContext(r.Context(), "protocol v3 planned playback failed", failureAttrs...)
		}
		persistedResponse, persistErr := h.startFailureDecisionV3(persistCtx, userID, profileID, req, requestDigests, requestedFile.ID, effectiveFile.ID, statusErr)
		if persistErr != nil {
			return playback.DecisionResponseV3{}, startTerminalPersistOperationErrorV3(persistCtx, persistErr, statusErr)
		}
		return persistedResponse, nil
	}
	// The transport has committed and the first-byte URL is on the response.
	// The fresh-start resolve deferred its full audio/subtitle enumeration, so
	// schedule it now: the client can start streaming immediately while ffprobe
	// runs, and the existing inventory_updated push (or the inventory poll)
	// delivers the probed track menu as a follow-up. The plan already carries
	// tracks_pending so a client keeps the provisional menu until then.
	enqueueDeferredPostCommitProbe(response.SessionID)
	timings.mark("response_ready")
	return response, nil
}

// virtualStartVersionFallbackWorkers bounds how many alternate versions the
// cold-start version-fallback walk resolves at once. Each alternate resolve is
// dominated by one upstream listing, so fan-out turns a walk of N dead pins
// from N serial listing rounds into roughly one. Four is small enough that a
// provider outage is not amplified into a storm of concurrent listings.
//
// The fan-out is a sliding wave, not a single round: a worker that finishes a
// candidate takes the next index immediately, so only the first
// virtualStartVersionFallbackWorkers candidates share the per-candidate budget
// from the start. A candidate in a later wave inherits whatever remains of the
// decision budget, so a slow early wave is allowed to shorten a later
// candidate's window rather than let the walk stretch toward one full
// candidate budget per candidate: with both waves alive, a fifth candidate gets
// at most the decision budget minus what the first wave spent. It is pinned by
// TestRunVirtualVersionFallbackCandidatesLaterWaveSharesDecisionBudget.
const virtualStartVersionFallbackWorkers = 4

// virtualStartVersionFallbackListingBudget bounds the provider listing portion
// of one alternate's resolve in the walk. A dead pin (empty listing, provider
// outage, absent candidate) answers or times out inside this window, so a walk
// never pays a full startup budget per dead version. It is a var so tests can
// pin it.
var virtualStartVersionFallbackListingBudget = 1500 * time.Millisecond

// virtualStartVersionFallbackCandidateBudget bounds one alternate's whole
// resolve: its listing portion above plus the start-path probe the resolve runs
// after it. The two are bounded separately inside the resolve — the listing by
// virtualStartVersionFallbackListingBudget (passed as a listing-only deadline)
// and the probe by virtualStartProbeBudget — so an alternate's probe is never
// silently "no probe" because an earlier listing spent the candidate's window.
// The sum is the worst case for one candidate.
var virtualStartVersionFallbackCandidateBudget = virtualStartVersionFallbackListingBudget + virtualStartProbeBudget

// virtualStartVersionFallbackDecisionBudget bounds the walk as a whole: the
// first healthy candidate wins; if none does, the terminal is returned once
// every candidate has failed or this budget elapses, whichever is first. It
// contains one candidate budget so the first wave can finish its listing and
// then its probe, and stays far below the start resolve half
// (virtualStartResolveBudget) the walk runs inside, so the walk cannot itself
// burn the start.
var virtualStartVersionFallbackDecisionBudget = virtualStartVersionFallbackCandidateBudget + 500*time.Millisecond

// runVirtualVersionFallbackCandidatesV3 resolves alternate version candidates
// with bounded parallel fan-out and returns the first healthy candidate. It is
// the scheduling half of the cold-start version-fallback walk, kept separate
// from the provider calls so the fan-out (worker bound, per-candidate budget,
// first-wins, cancel stragglers, all-failed terminal) is testable without a
// provider.
//
// candidates is in fallback order. Each is resolved by resolve within a
// per-candidate budget derived from ctx. The budget covers the whole resolve
// (its listing stage and its probe), not just the provider listing: the
// alternate walk's resolve applies its own shorter listing-only deadline
// inside this one, so the probe keeps the start-path bound instead of
// inheriting whatever the listing left. The first success (ok=true) wins: the
// remaining resolves are canceled and this returns at once, without waiting
// for them to unwind, so a healthy candidate is never delayed behind a
// straggler. false is returned only after every candidate has failed or the
// decision context elapses, whichever happens first; on both of those paths the
// in-flight resolves are canceled and the workers are joined before returning,
// so no resolve mutates handler state after a terminal verdict. The caller
// keeps the original resolve error in the false case.
//
// The winner is returned with the index of the candidate that produced it, so
// the caller never has to correlate the returned source with a side-channel
// write made by the winning worker. Two workers can both succeed; whichever
// publishes first wins the buffered channel, and its own index travels with it.
// On a false result (or no publish) index is -1.
func runVirtualVersionFallbackCandidatesV3(
	decisionCtx context.Context,
	candidates []*models.MediaFile,
	workers int,
	candidateBudget time.Duration,
	resolve func(context.Context, *models.MediaFile) (resolvedVirtualPlaybackSource, bool),
) (resolvedVirtualPlaybackSource, int, bool) {
	if len(candidates) == 0 || workers <= 0 {
		return resolvedVirtualPlaybackSource{}, -1, false
	}
	if workers > len(candidates) {
		workers = len(candidates)
	}
	ctx, cancel := context.WithCancel(decisionCtx)
	defer cancel()

	// virtualVersionFallbackResultV3 pairs a resolved source with the index of
	// the candidate that produced it, so the identity of the winning alternate
	// is published atomically with its source rather than through a shared
	// write a competing success could overwrite before the caller reads it.
	type virtualVersionFallbackResultV3 struct {
		source resolvedVirtualPlaybackSource
		index  int
	}

	indexes := make(chan int)
	winnerCh := make(chan virtualVersionFallbackResultV3, 1)

	var workersWG sync.WaitGroup
	workersWG.Add(workers)
	for range workers {
		go func() {
			defer workersWG.Done()
			for {
				var idx int
				select {
				case <-ctx.Done():
					return
				case i, ok := <-indexes:
					if !ok {
						return
					}
					idx = i
				}
				// A winner or the decision budget already canceled the walk;
				// do not start another provider listing.
				if ctx.Err() != nil {
					return
				}
				// The per-candidate bound comes from what the decision budget
				// has left, never from a bare WithTimeout on an already-spent
				// parent: that produced a negative bound and a resolve
				// pre-empted before it started (see
				// virtualResolveAttemptBudget).
				listBudget, listAllowed := virtualResolveAttemptBudget(ctx, candidateBudget)
				if !listAllowed {
					return
				}
				listCtx, listCancel := context.WithTimeout(ctx, listBudget)
				resolved, ok := resolve(listCtx, candidates[idx])
				listCancel()
				if ok {
					select {
					case winnerCh <- virtualVersionFallbackResultV3{source: resolved, index: idx}:
					default:
					}
					cancel()
					return
				}
			}
		}()
	}

	go func() {
		defer close(indexes)
		for i := range candidates {
			select {
			case <-ctx.Done():
				return
			case indexes <- i:
			}
		}
	}()

	drained := make(chan struct{})
	go func() {
		workersWG.Wait()
		close(drained)
	}()

	// preferWinner drains a result that may have landed exactly as the final
	// worker exited, so a success is never dropped in favor of the terminal.
	preferWinner := func() (resolvedVirtualPlaybackSource, int, bool) {
		select {
		case winner := <-winnerCh:
			return winner.source, winner.index, true
		default:
			return resolvedVirtualPlaybackSource{}, -1, false
		}
	}

	select {
	case winner := <-winnerCh:
		return winner.source, winner.index, true
	case <-drained:
		return preferWinner()
	case <-decisionCtx.Done():
		// The walk's decision budget elapsed. Cancel the in-flight listings and
		// wait for the workers to stop, so no resolve mutates handler state
		// after this returns. A candidate that finished just as the budget fired
		// still counts as the winner.
		cancel()
		<-drained
		return preferWinner()
	}
}

// resolveVirtualStartWithVersionFallback resolves the virtual source for a
// fresh start. When the pinned release's listing fails and nothing is playing
// yet, it walks the content's alternate versions/files — each with its own
// listing attempt — and returns the first working candidate, so pressing play
// lands on a playable version whenever one exists.
//
// The walk only runs for an eligible auto selection (the same gate the
// planning-time alternate hunt uses) and never for an explicit user version
// pick: the viewer chose that release and must not be silently moved off it.
// A version the catalog already stamped failed (an AltMount SourceFailed
// verdict) is skipped, never attempted. An empty provider listing stays
// transient: it advances the walk without indicting the version, matching the
// deliberate decision that a zero-count answer is a provider hiccup and not a
// verdict about any release.
//
// The alternates are resolved with bounded parallel fan-out rather than one at
// a time: a cold start whose pin is dead commonly has every alternative dead
// too, and resolving them serially paid one full upstream listing timeout per
// dead version before the terminal. Each alternate's listing is bounded to
// virtualStartVersionFallbackListingBudget and its probe to
// virtualStartProbeBudget, so one candidate resolves within
// virtualStartVersionFallbackCandidateBudget (~4.5s), and the walk as a whole
// to virtualStartVersionFallbackDecisionBudget (~5s) instead of seven to
// eleven. That figure describes the alternate walk, not the end-to-end start
// request: when the walk finds no candidate, the caller's synchronous primary
// stamp (stampStartVirtualCandidateFailed) runs after it and can add up to
// startCandidateFailStampBudget (~3s) of database write before the terminal
// response. The first healthy candidate wins and cancels the rest. When no
// version resolves, the original listing failure is returned so the caller
// reports the honest underlying cause (an edge 5xx, an empty listing, or every
// version failed) instead of a generic error.
func (h *PlaybackHandler) resolveVirtualStartWithVersionFallback(
	r *http.Request,
	file *models.MediaFile,
	profileID string,
	req playback.StartRequestV3,
	allowFailedCandidate bool,
	bandwidthCapKbps int,
) (resolvedVirtualPlaybackSource, error) {
	return h.resolveVirtualStartWithVersionFallbackExcluding(r, file, profileID, req, allowFailedCandidate, bandwidthCapKbps, nil)
}

// resolveVirtualStartWithVersionFallbackExcluding is the walk with candidates an
// earlier hop already refused. The exclusion is carried into both the primary
// resolve and the alternate walk, so a candidate that resolved to a row outside
// the item's version list cannot be served again by the retry that follows it.
func (h *PlaybackHandler) resolveVirtualStartWithVersionFallbackExcluding( //nolint:funlen // the walk's stages share one decision budget and are ordered; splitting them would hide that ordering
	r *http.Request,
	file *models.MediaFile,
	profileID string,
	req playback.StartRequestV3,
	allowFailedCandidate bool,
	bandwidthCapKbps int,
	excludedCandidateIDs []string,
) (resolvedVirtualPlaybackSource, error) {
	offeredURI := strings.TrimSpace(req.OfferedVirtualURI)
	offeredCandidateID := virtualResultCandidateID(offeredURI)
	resolved, resolveErr := h.resolveVirtualPlaybackSource(
		r, file, profileID, true, excludedCandidateIDs, offeredCandidateID, req.QualityPreference, bandwidthCapKbps, req.ForceRelink,
		virtualResolveOptionsV3{
			allowFailedCandidate: allowFailedCandidate,
			sessionBound:         false,
			explicitSelection:    req.FileSelection == playback.FileSelectionExplicitV3 || offeredCandidateID != "",
			// Defer the candidate-declared upgrade probe past the transport
			// commit only when the surface AND the client negotiated the
			// deferred track-inventory lifecycle (see
			// deferTrackInventoryNegotiatedV3). On the frozen v1 surface, or a
			// v2 client that did not advertise deferred_track_inventory_v1, the
			// resolve keeps the pre-#228 behavior: it upgrades the inventory with
			// a background probe and never marks the plan tracks_pending, so the
			// v1 contract is byte-for-byte unchanged.
			deferProbePastCommit: h.deferTrackInventoryNegotiatedV3(r, req),
		},
	)
	if offeredCandidateID != "" {
		// An offered-track pick names one concrete candidate. Falling back to
		// another candidate would apply the offered track ordinals to different
		// bytes; fail the start and let the client refresh the version offer.
		if resolveErr != nil {
			return resolved, fmt.Errorf("%w: %w", errOfferedCandidateUnavailable, resolveErr)
		}
		resolvedURI := strings.TrimSpace(resolved.URI)
		if resolvedURI == "" {
			resolvedURI = strings.TrimSpace(resolved.File.FilePath)
		}
		if !sameVirtualCandidate(resolvedURI, offeredURI) {
			return resolved, errOfferedCandidateUnavailable
		}
		return resolved, nil
	}
	if resolveErr == nil || !virtualStartVersionFallbackEligibleV3(req) || !virtualProviderListingOutage(resolveErr) {
		return resolved, resolveErr
	}
	// A timeout is not a verdict about the release. When the start's own budget
	// is spent, the selected row was never disproven — every alternate would be
	// resolved with the same expired deadline, so the walk could only land on a
	// sibling that happens to answer from cache while the viewer's release was
	// never given a chance. Observed production shape: four resolve attempts
	// reporting a negative bound (bound_ms=-34/-65/-95/-126), then a plan whose
	// requested_file_id and effective_file_id named different releases. Keep the
	// selection and let the caller answer the honest retryable terminal.
	walkBudget, walkAllowed := virtualResolveAttemptBudget(r.Context(), virtualStartVersionFallbackDecisionBudget)
	if !walkAllowed || r.Context().Err() != nil {
		slog.WarnContext(r.Context(), "virtual start version fallback skipped: the start budget is exhausted",
			"component", "api", "signal", "start_budget_exhausted",
			"file_id", file.ID,
			"remaining_ms", virtualResolveRemainingMilliseconds(r.Context()),
			"budget_exhausted", true,
			"error", logredact.SanitizeURLError(resolveErr))
		return resolved, resolveErr
	}
	// One deadline owns the whole walk. resolveVirtualPlaybackSource re-bases
	// its own cold path on r.Context() and context.WithTimeout keeps the
	// earlier deadline, so this bounds every alternate without restarting the
	// budget once per version. The decision budget is deliberately separate
	// from the per-listing budget: a healthy candidate is returned the moment
	// it resolves, while a walk of dead pins terminals once the decision budget
	// elapses rather than waiting out the full cold-start budget per version.
	walkCtx, cancel := context.WithTimeout(r.Context(), walkBudget)
	defer cancel()
	// Thread the caller's session-binding intent rather than forcing it false.
	// The fresh-start start path declares unbound on the request context before
	// calling this walk, so the stale-pin recovery may re-pin; a future
	// session-bound caller that reuses the walk keeps its declaration (or the
	// conservative session-bound default) and the recovery refuses to touch a
	// live session's binding.
	walkReq := r.WithContext(withVirtualSessionBindingV3(walkCtx, VirtualSessionBinding(r.Context())))

	// Guarded stale-pin recovery for identity-less rows: when the pin id is
	// absent from a non-empty live listing and the row carries no durable
	// provider identity to rematch on, re-pin in memory to the best
	// fingerprint-matched live candidate and retry once. Bounded to one attempt
	// inside its own sub-budget; a miss falls through to the alternate walk
	// below with the terminal intact.
	if recovered, recoveredOK := h.recoverStaleIdentityLessPinV3(walkReq, file, profileID); recoveredOK {
		return recovered, nil
	}

	alternates, alternateErr := h.findAlternateFilesOrdered(walkCtx, file, requestAccessFilter(r), alternateOrderingForClient(req.Capabilities))
	if alternateErr != nil || len(alternates) == 0 {
		return resolved, resolveErr
	}

	// candidateRows keeps one entry per walkable alternate, in fallback order.
	// Skipping an AltMount-failed version here (rather than inside the resolve
	// closure) keeps the walk order and the fan-out deterministic: a skipped
	// version is never handed a worker slot.
	candidateRows := make([]*models.MediaFile, 0, len(alternates))
	for _, alternate := range alternates {
		if alternate == nil || alternate.ID == file.ID || !isVirtualPlaybackFile(alternate) {
			continue
		}
		if virtualCandidateVerdictActive(alternate.FailedAt, time.Now()) {
			// The provider's AltMount verdict indicted this version; never
			// attempt it, however the primary listing failed.
			continue
		}
		if virtualCandidateIDExcluded(alternate.FilePath, excludedCandidateIDs) {
			// An earlier hop already proved this candidate resolves to a row
			// the item's version list does not carry; walking it again would
			// only reproduce that refusal.
			continue
		}
		candidateRows = append(candidateRows, alternate)
	}
	if len(candidateRows) == 0 {
		return resolved, resolveErr
	}

	// resolveAlternate runs one alternate's existing resolve. It is exactly the
	// call the serial walk made, so its side effects (pinning, caching) and its
	// verdict classification are unchanged. A confirmed-dead verdict is
	// persisted off this worker's critical path by
	// stampStartVirtualCandidateFailedAsync: the stamp detaches from listCtx and
	// carries its own budget, so the runner joining a worker that is mid-stamp
	// cannot stretch the walk past its decision budget. The candidate that won
	// is published together with its index on the runner's channel, never via a
	// shared variable, so two concurrent successes cannot race for it and a
	// straggler cannot overwrite the winner the caller reads.
	//
	// A substitute is accepted only when it can serve the viewer's selection:
	// the same rotation guard #448 applies to a rotation (a contradictory
	// runtime or a poorer audio/subtitle menu is refused, never served) is
	// applied here to the cross-version fallback as well, so the two paths
	// share one policy.
	expectedRuntimeMinutes := h.virtualExpectedRuntimeMinutes(walkCtx, file)
	var refusedSubstitutes atomic.Int32
	resolveAlternate := func(listCtx context.Context, alternate *models.MediaFile) (resolvedVirtualPlaybackSource, bool) {
		listReq := walkReq.WithContext(listCtx)
		altResolved, altErr := h.resolveVirtualPlaybackSource(
			listReq, alternate, profileID, true, excludedCandidateIDs, "", req.QualityPreference, bandwidthCapKbps, false,
			virtualResolveOptionsV3{
				sessionBound:        false,
				startPathProbe:      true,
				bypassProviderFloor: true,
				listingBudget:       virtualStartVersionFallbackListingBudget,
			},
		)
		if altErr == nil && altResolved.File != nil {
			if reason := virtualVersionFallbackSubstitutionRejectionV3(expectedRuntimeMinutes, file, altResolved.File); reason != "" {
				refusedSubstitutes.Add(1)
				slog.WarnContext(listCtx, "virtual start version fallback refused a substitute that contradicts the selected release",
					"component", "api", "requested_file_id", file.ID,
					"effective_file_id", altResolved.File.ID,
					"candidate_uri", alternate.FilePath,
					"reason", reason,
					"probed_duration_seconds", altResolved.File.Duration,
					"expected_runtime_minutes", expectedRuntimeMinutes,
					"selected_audio_tracks", len(file.AudioTracks),
					"selected_subtitle_tracks", len(file.SubtitleTracks),
					"effective_audio_tracks", len(altResolved.File.AudioTracks),
					"effective_subtitle_tracks", len(altResolved.File.SubtitleTracks))
				return resolvedVirtualPlaybackSource{}, false
			}
			return altResolved, true
		}
		if altErr != nil {
			// A confirmed-dead version is indicted so a later start skips it;
			// an empty listing or provider outage is transient and only
			// advances the walk. Fire-and-forget: the stamp writes through a
			// detached budgeted context, so a canceled straggler still records
			// the verdict it earned without holding the walk open for it.
			h.stampStartVirtualCandidateFailedAsync(listCtx, alternate, altErr)
		}
		return resolvedVirtualPlaybackSource{}, false
	}

	winner, winnerIndex, ok := runVirtualVersionFallbackCandidatesV3(
		walkCtx, candidateRows, virtualStartVersionFallbackWorkers, virtualStartVersionFallbackCandidateBudget, resolveAlternate,
	)
	if !ok {
		if refusedSubstitutes.Load() > 0 && virtualRequestedRowIsSelection(file) {
			// A substitute existed but none could serve the selection. A
			// truthful, actionable terminal beats serving different bytes with
			// fewer tracks: name the version list so the viewer can choose.
			return resolved, fmt.Errorf("%w: %w", errVirtualSelectedReleaseCannotPlay, resolveErr)
		}
		return resolved, resolveErr
	}
	// Mark the substitution so the plan can tell the client the requested
	// release was replaced, and why. The requested row is the one the caller
	// asked for, which this walk deliberately walked away from because its
	// listing failed.
	winner.SubstitutedFromFileID = file.ID
	winner.SubstitutionReason = virtualSubstitutionReasonV3(resolveErr)
	// The winner resolved and ranked through the normal path, so it carries
	// its own measured rank/count; the primary's unknown or zero values must
	// not leak onto the substituted plan's log line.
	logAttrs := []any{
		logComponentKey, playbackLogValueV3,
		"requested_file_id", file.ID,
		"candidate_id", virtualResultCandidateID(winner.URI),
	}
	if winnerIndex >= 0 && winnerIndex < len(candidateRows) {
		logAttrs = append(logAttrs, "alternate_file_id", candidateRows[winnerIndex].ID)
	}
	slog.InfoContext(walkCtx, "virtual start fell back to an alternate version after a listing failure", logAttrs...)
	return winner, nil
}

// virtualStartVersionFallbackEligibleV3 is the cross-version fallback gate,
// mirroring the planning-time alternate hunt: the client must allow alternate
// versions, the quality preference must not pin the exact release, and the
// request must not be an explicit version pick the viewer chose.
func virtualStartVersionFallbackEligibleV3(req playback.StartRequestV3) bool {
	return req.AllowsAlternateVersions() &&
		shouldTryAlternateFileV3(req.QualityPreference) &&
		req.FileSelection != playback.FileSelectionExplicitV3
}

// retainedVirtualStartSourceV3 reuses the candidate binding and probed
// inventory a still-live session retained for this exact release. A start that
// re-enters after a transport failure would otherwise re-list the provider and
// re-probe the candidate before planning the same bytes; when the prior session
// already holds verified evidence for the requested row, serving that evidence
// skips the discovery/listing/probe round-trip without changing which release
// plays. The reuse is deliberately narrow:
//
//   - it only fires when the client has not asked for a forced relink or a
//     specific offered candidate, where a fresh answer is the point;
//   - the prior session must be the unique client/profile match for this row and
//     its binding must name a concrete candidate on the same release;
//   - the evidence must belong to the bound row, and the row must still be
//     accessible and not marked failed;
//   - a requested row that already names a complete, probed candidate keeps the
//     existing fast path, so a healthy start is not diverted.
//
// The returned source has no DeferredProbe: the inventory is already proven, and
// a fresh probe is not a prerequisite for planning a retained generation.
func (h *PlaybackHandler) retainedVirtualStartSourceV3(
	r *http.Request,
	userID int,
	profileID string,
	requested *models.MediaFile,
	req playback.StartRequestV3,
) (resolvedVirtualPlaybackSource, bool) {
	if !h.retainedVirtualStartAllowedV3(r, requested, req) {
		return resolvedVirtualPlaybackSource{}, false
	}
	previous, candidateURI, ownerID, ok := h.retainedVirtualStartBindingV3(r, userID, profileID, requested)
	if !ok {
		return resolvedVirtualPlaybackSource{}, false
	}
	effective, err := h.fileResolver.GetByID(r.Context(), previous.MediaFileID)
	if err != nil || effective == nil || !isVirtualPlaybackFile(effective) {
		return resolvedVirtualPlaybackSource{}, false
	}
	if !catalog.FileAllowedByAccess(effective, requestAccessFilter(r)) {
		return resolvedVirtualPlaybackSource{}, false
	}
	if virtualCandidateVerdictActive(effective.FailedAt, time.Now()) {
		return resolvedVirtualPlaybackSource{}, false
	}
	bound, ok := retainedVirtualBoundFileV3(effective, previous, candidateURI, ownerID)
	if !ok {
		return resolvedVirtualPlaybackSource{}, false
	}
	resolved := resolvedVirtualPlaybackSource{
		URI:            candidateURI,
		OwnerID:        ownerID,
		File:           bound,
		ProbeSucceeded: true,
		Provenance:     ProbeProvenanceVerified,
		CandidateRank:  -1,
		CandidateCount: -1,
	}
	if effective.ID != requested.ID {
		resolved.SubstitutedFromFileID = requested.ID
		resolved.SubstitutionReason = substitutionReasonUnknownV3
	}
	slog.InfoContext(r.Context(), "resume start reused retained virtual evidence",
		"component", "playback",
		"requested_file_id", requested.ID,
		"effective_file_id", effective.ID,
		"candidate_id", virtualResultCandidateID(candidateURI),
		"subtitle_evidence", true,
	)
	return resolved, true
}

// retainedVirtualStartAllowedV3 reports whether a start may reuse a live
// session's retained evidence at all. A forced relink, an offered candidate, a
// row the catalog already indicted, and a row that already names a complete
// probed candidate (the ordinary repeat-play fast path) all need a fresh answer.
func (h *PlaybackHandler) retainedVirtualStartAllowedV3(r *http.Request, requested *models.MediaFile, req playback.StartRequestV3) bool {
	if h == nil || h.fileResolver == nil || requested == nil || r == nil || !isVirtualPlaybackFile(requested) {
		return false
	}
	if req.ForceRelink || strings.TrimSpace(req.OfferedVirtualURI) != "" {
		return false
	}
	allowFailedCandidate := req.FileSelection == playback.FileSelectionExplicitV3 || req.ForceRelink
	if !allowFailedCandidate && virtualCandidateVerdictActive(requested.FailedAt, time.Now()) {
		// The catalog already indicted this row. Reusing a live session's older
		// binding would resurrect a release the provider marked dead.
		return false
	}
	// A requested row that already names a complete probed candidate is served
	// by the ordinary repeat-play fast path; leaving it there preserves the
	// existing revalidation of a healthy row.
	return virtualResultCandidateID(requested.FilePath) == "" ||
		requested.ProbeUpdatedAt == nil ||
		!completeVirtualVideoEvidenceV3(requested) ||
		!completeVirtualAudioEvidenceV3(requested)
}

// retainedVirtualStartBindingV3 finds the unique live session for this client,
// profile and row, then checks that its binding names a candidate on the same
// release. It returns the session, the bound candidate URI and the owner the
// planning copy must carry.
func (h *PlaybackHandler) retainedVirtualStartBindingV3(r *http.Request, userID int, profileID string, requested *models.MediaFile) (*playback.Session, string, int, bool) {
	if h.sessionMgr == nil {
		return nil, "", 0, false
	}
	lister, ok := h.sessionMgr.(interface {
		GetUserSessions(userID int) []*playback.Session
	})
	if !ok {
		return nil, "", 0, false
	}
	previous := findCarryCandidateSessionV3(
		lister.GetUserSessions(userID), profileID, requested.ID,
		clientip.FromContext(r.Context()), playbackClientInfoFromRequest(r).UserAgent,
	)
	if previous == nil || previous.MediaFileID <= 0 || !previous.VirtualSubtitleEvidenceSet {
		return nil, "", 0, false
	}
	candidateURI := strings.TrimSpace(previous.VirtualSourceURI)
	if virtualResultCandidateID(candidateURI) == "" {
		return nil, "", 0, false
	}
	if virtualPlaybackNeutralKey(candidateURI) != virtualPlaybackNeutralKey(requested.FilePath) {
		return nil, "", 0, false
	}
	if requestedID := virtualResultCandidateID(requested.FilePath); requestedID != "" &&
		requestedID != virtualResultCandidateID(candidateURI) {
		return nil, "", 0, false
	}
	ownerID := previous.VirtualSourceOwnerInstallationID
	if ownerID <= 0 {
		ownerID = requested.VirtualOwnerInstallationID
	}
	if ownerID > 0 && requested.VirtualOwnerInstallationID > 0 && ownerID != requested.VirtualOwnerInstallationID {
		return nil, "", 0, false
	}
	// Evidence is bound to the row it was captured from. A moved binding whose
	// evidence still names an older row cannot prove this candidate's inventory.
	if evidenceID := previous.VirtualSubtitleEvidenceFileID; evidenceID > 0 && evidenceID != previous.MediaFileID {
		return nil, "", 0, false
	}
	return previous, candidateURI, ownerID, true
}

// retainedVirtualBoundFileV3 builds the planning copy from a retained binding:
// the session's probed inventory carried onto the candidate the binding names.
func retainedVirtualBoundFileV3(effective *models.MediaFile, previous *playback.Session, candidateURI string, ownerID int) (*models.MediaFile, bool) {
	bound := *effective
	bound.FilePath = candidateURI
	bound.VirtualOwnerInstallationID = ownerID
	bound.AudioTracks = append([]models.AudioTrack(nil), previous.VirtualAudioTracks...)
	bound.SubtitleTracks = append([]models.SubtitleTrack(nil), previous.VirtualSubtitleTracks...)
	bound.ExternalSubtitles = append([]models.ExternalSubtitle(nil), previous.VirtualExternalSubtitles...)
	// A stored provider URL belongs to the candidate the row names. When the
	// binding moved to a different candidate, serving the row's old URL would
	// fetch the wrong bytes; let the transport resolve the retained candidate
	// instead.
	if virtualResultCandidateID(effective.FilePath) != virtualResultCandidateID(candidateURI) {
		bound.ResolvedURL = ""
		bound.ResolvedURLExpiresAt = nil
		bound.ProviderRequestHeaders = nil
	}
	// The retained generation was verified by the session's probe, so it is a
	// valid evidence generation even when the catalog row's own stamp is absent
	// (the evidence write may still be queued). Stamp the planning copy only.
	if bound.ProbeUpdatedAt == nil {
		stamp := time.Now()
		bound.ProbeUpdatedAt = &stamp
	}
	if !completeVirtualVideoEvidenceV3(&bound) || !completeVirtualAudioEvidenceV3(&bound) {
		return nil, false
	}
	return &bound, true
}

// prepareVirtualAlternateFileV3 resolves an alternate candidate row into the
// file a plan will serve. It returns the resolver's provenance alongside the
// file so each alternate planner input can publish the same inventory
// provenance the primary plan carries; a local alternate has no resolver and
// reports empty provenance.
//
// The resolve reuses a fresh provider answer instead of forcing a re-list.
// Every alternate of one title shares a provider-neutral cache key, so forcing
// the listing per alternate turned one replan into one multi-second provider
// round-trip per sibling for the same candidate set. A listing that is stale
// past the TTL still fetches (a miss, a stale-grace refresh, or the failure
// path's own rotation), so a moved or dead release is re-listed when that is
// what the evidence says, not on every sibling.
//
// startPath marks the walk as start-path work: the alternate hunt inside a
// start and the start's transport-failure walk both set it, so their synchronous
// probe is bounded by virtualStartProbeBudget like the primary start probe. The
// walk still probes synchronously (a substitute's inventory must be verified
// before the planner remaps a version's tracks), and a replan rehydration
// leaves it false and keeps virtualProbeBudget.
func (h *PlaybackHandler) prepareVirtualAlternateFileV3(r *http.Request, alternate *models.MediaFile, profileID string, startPath bool) (*models.MediaFile, ProbeProvenance, error) {
	if alternate == nil {
		return nil, "", errors.New("nil alternate file")
	}
	if !isVirtualPlaybackFile(alternate) {
		return h.ensurePlaybackProbe(r.Context(), alternate), "", nil
	}
	resolved, err := h.resolveVirtualPlaybackSource(r, alternate, profileID, false, nil, "", "", 0, false, virtualResolveOptionsV3{sessionBound: false, startPathProbe: startPath})
	if err != nil {
		return nil, "", err
	}
	if resolved.File == nil {
		return nil, "", errors.New("virtual playback resolver returned no file")
	}
	resolvedFile := *resolved.File
	// Keep the candidate's own catalog row id when the resolver substituted a
	// different release that has a row; only a same-row resolve carries the
	// alternate row id. Overwriting it with alternate.ID hid the substitution.
	resolved.File = &resolvedFile
	resolved.File.FilePath = resolved.URI
	resolved.File.VirtualOwnerInstallationID = resolved.OwnerID
	if resolved.File.Duration <= 0 {
		if resolved.File.EpisodeID != "" && h.EpisodeLookup != nil {
			if ep, err := h.EpisodeLookup.GetByID(r.Context(), resolved.File.EpisodeID); err == nil && ep != nil && ep.Runtime > 0 {
				resolved.File.Duration = ep.Runtime * 60
			}
		} else if resolved.File.ContentID != "" && h.ItemLookup != nil {
			if item, err := h.ItemLookup.GetByID(r.Context(), resolved.File.ContentID); err == nil && item != nil && item.Runtime > 0 {
				resolved.File.Duration = item.Runtime * 60
			}
		}
	}
	return resolved.File, resolved.Provenance, nil
}

// sourceDecodeFailedTerminalResponseV3 builds the durable terminal for a
// decode rejection that could not be recovered. The explicit-pick version-list
// hint is appended here because an explicit pin is never substituted.
func sourceDecodeFailedTerminalResponseV3(fileSelection playback.FileSelectionV3) playback.DecisionResponseV3 {
	terminal := &playback.TerminalV3{
		Reason:    sourceDecodeFailedReasonV3,
		Message:   sourceDecodeFailedMessageV3,
		Retryable: false,
	}
	hintExplicitSelectionAlternateAvailableV3(terminal, fileSelection)
	return playback.NewTerminalResponseFromTerminalV3(terminal)
}

// rotateRejectedVirtualCandidateStartV3 retries a virtual start whose local
// transport the decoder rejected, excluding each rejected provider result id so
// the provider offers the next-ranked release. It is bounded by
// maxVirtualFailoverAttempts; excluding the failed id on every iteration is what
// guarantees termination even when a provider keeps returning the dead release.
//
// It rotates on the provider result id rather than the catalog failed_at stamp,
// so rotation does not depend on the asynchronous mark having landed. It returns
// ok=false when no replacement candidate starts, leaving the caller to persist
// the source_decode_failed terminal.
func (h *PlaybackHandler) rotateRejectedVirtualCandidateStartV3(
	r *http.Request,
	userID int,
	profileID string,
	req playback.StartRequestV3,
	requestDigests playbackStartRequestDigestsV3,
	catalogFile *models.MediaFile,
	settings playback.PlannerSettingsV3,
	settingsErr error,
	clientInfo playback.ClientInfo,
	firstRejectedID string,
) (playback.DecisionResponseV3, bool) {
	if catalogFile == nil || firstRejectedID == "" {
		return playback.DecisionResponseV3{}, false
	}
	maxAttempts := h.maxVirtualFailoverAttempts(r.Context())
	excluded := []string{firstRejectedID}
	for attempt := 1; attempt < maxAttempts; attempt++ {
		// An auto selection: allowFailedCandidate=false keeps the catalog
		// failed_at rule, and the explicit exclusion carries the live verdict.
		resolved, resolveErr := h.resolveVirtualPlaybackSource(r, catalogFile, profileID, true, excluded, "", req.QualityPreference, intOrZeroHandlerV3(req.BandwidthCapKbps), req.ForceRelink, virtualResolveOptionsV3{rotateCandidates: true, sessionBound: false})
		if resolveErr != nil || resolved.File == nil {
			return playback.DecisionResponseV3{}, false
		}
		nextID := virtualResultCandidateID(resolved.URI)
		if nextID != "" && containsStringExactV3(excluded, nextID) {
			// The resolver handed back an already-excluded candidate; stop
			// rather than spin on the same release.
			return playback.DecisionResponseV3{}, false
		}
		resolvedFile := *resolved.File
		// Keep the rotated candidate's own catalog row id so the replacement
		// plan names the bytes it will play; the request keeps catalogFile as
		// its requested row below.
		resolvedFile.FilePath = resolved.URI
		resolvedFile.VirtualOwnerInstallationID = resolved.OwnerID
		audioIndex, _, audioErr := resolveV3AudioIndex(&resolvedFile, req.AudioTrackID, req.AudioTrackIndex)
		if audioErr != nil {
			return playback.DecisionResponseV3{}, false
		}
		planResult, toneMapCapabilityErr := h.planPlaybackWithCapabilitiesV3(r.Context(), playback.PlannerInputV3{
			Request: req, RequestedFile: catalogFile, EffectiveFile: &resolvedFile,
			AudioTrackIndex: audioIndex, Settings: settings,
			Registry:            h.transformationRegistryV3(r.Context()),
			DVRPUStrippable:     h.lazyDVRPUStrippableV3(r.Context(), &resolvedFile),
			Now:                 time.Now(),
			InventoryProvenance: string(resolved.Provenance),
		})
		planResult = retryIncompleteToneMapPlanningV3(planResult, toneMapCapabilityErr)
		planResult = retryIncompletePlaybackSettingsV3(planResult, settingsErr)
		clampPlannerTargetResolution(&planResult, &resolvedFile)
		if planResult.Terminal != nil || planResult.Plan == nil {
			return playback.DecisionResponseV3{}, false
		}
		response, statusErr := h.startPlannedPlaybackV3(r, userID, profileID, req, requestDigests, catalogFile, &resolvedFile, audioIndex, startIntentV3{audioIndex: audioIndex}, planResult, clientInfo)
		if statusErr == nil {
			// The start committed a replacement candidate after excluding the
			// rejected ones. Persist the chain on the new attempt so a later
			// replan — on this replica or another after a reload — skips every
			// release this start already proved bad, even if the asynchronous
			// failed_at marker never landed.
			h.persistStartRotationExclusionsV3(r.Context(), response.SessionID, resolved.URI, excluded)
			return response, true
		}
		if statusErr.reason != candidateSourceDecodeRejectedReasonV3 || nextID == "" {
			return playback.DecisionResponseV3{}, false
		}
		excluded = append(excluded, nextID)
	}
	return playback.DecisionResponseV3{}, false
}

// persistStartRotationExclusionsV3 records the candidates a start rotation
// excluded onto the freshly committed attempt. It is best-effort only in the
// sense that the start has already succeeded and returned a playable plan;
// the exclusion is insurance for the next hop, so a failure here is logged and
// does not fail the start (the marker remains a supplementary suppression).
func (h *PlaybackHandler) persistStartRotationExclusionsV3(ctx context.Context, sessionID, candidateURI string, excludedIDs []string) {
	if sessionID == "" || len(excludedIDs) == 0 {
		return
	}
	store := h.recoveryStateStoreV3()
	if store == nil {
		slog.WarnContext(ctx, "start rotation exclusions could not be persisted: no recovery state store", "component", "api", "session_id", sessionID)
		return
	}
	record, err := h.PlanStoreV3.GetAttempt(ctx, sessionID)
	if err != nil || record == nil {
		slog.WarnContext(ctx, "start rotation exclusions could not load the committed attempt", "component", "api", "session_id", sessionID, "error", err)
		return
	}
	providerSource := virtualAttemptProviderSourceV3(candidateURI)
	if providerSource == "" {
		providerSource = virtualAttemptProviderSourceV3(record.CurrentPlan.EffectiveVirtualURI)
	}
	exclusions := make([]playback.RecoveryExclusionV3, 0, len(excludedIDs))
	for _, id := range excludedIDs {
		if strings.TrimSpace(id) == "" {
			continue
		}
		exclusions = append(exclusions, playback.RecoveryExclusionV3{
			ProviderSource: providerSource,
			CandidateID:    id,
			FileID:         record.EffectiveMediaFileID,
			ConfirmedAt:    time.Now().UTC(),
		})
	}
	if len(exclusions) == 0 {
		return
	}
	if _, err := h.appendRecoveryExclusionsV3(ctx, record, exclusions); err != nil {
		slog.WarnContext(ctx, "start rotation exclusions could not be persisted", "component", "api", "session_id", sessionID, "error", err)
	}
}

type playbackStartRequestDigestsV3 struct {
	current string
	legacy  string
}

// playbackClientInfoForStartV3 resolves the client's app identity for a v3
// start request. The X-Silo-Client-* headers win because they are present on
// every request; the start body's client_playback_context is the fallback for
// clients that report their app identity only there. All values stay opaque —
// they are trimmed and length-clamped when the session stamps them.
//
// The device is the one the client declared for the profile device registry:
// the X-Silo-Device-* headers on the v1 bridge, PlaybackCaller.DeclaredDevice
// on /api/v2.
//
// The fallback applies only to a client that named itself. client_playback_context
// carries no app name, so nothing in the body can identify a nameless client
// anyway — it is labeled from its user agent, and its app_version is a
// free-form platform string rather than the marketing version client_version
// promises. The web player, for one, reports the literal "web" there; taking it
// unconditionally would write "web" into the one field that is contractually
// semver, on every browser session.
func playbackClientInfoFromRequest(r *http.Request) playback.ClientInfo {
	return playback.ClientInfoFromRequest(r).Normalized()
}

func playbackClientInfoForStartV3(r *http.Request, clientContext playback.ClientPlaybackContextV3) playback.ClientInfo {
	info := playback.ClientInfoFromRequest(r)
	device := DeviceMetadataFromHeaders(r.Header)
	if isNativeAPIV2(r.Context()) {
		device, _ = r.Context().Value(playbackDeclaredDeviceContextKey{}).(DeviceMetadata)
	}
	info.DeviceID, info.DevicePlatform = device.DeviceID, device.DevicePlatform
	if info.Name == "" {
		return info.Normalized()
	}
	if info.Version == "" {
		info.Version = strings.TrimSpace(clientContext.AppVersion)
	}
	if info.Build == "" {
		info.Build = strings.TrimSpace(clientContext.AppBuild)
	}
	if info.Channel == "" {
		info.Channel = strings.TrimSpace(clientContext.AppChannel)
	}
	// The header half is already normalized; body-sourced values have to be
	// clamped too before they reach the decision log and the route event.
	return info.Normalized()
}

// playbackClientInfoWithSessionFallbackV3 completes a header-derived identity
// from the session the event belongs to. Route events posted out of band carry
// no client_playback_context, so without this a client that reports its build
// only in the start body would attribute its plan_selected event to a build and
// every later event of the same attempt to none.
func (h *PlaybackHandler) playbackClientInfoWithSessionFallbackV3(sessionID string, info playback.ClientInfo) playback.ClientInfo {
	if sessionID == "" || h.sessionMgr == nil {
		return info
	}
	if info.Name != "" && info.Version != "" && info.Build != "" && info.Channel != "" {
		return info
	}
	session, err := h.sessionMgr.GetSession(sessionID)
	if err != nil || session == nil {
		return info
	}
	stamped := session.ClientInfo()
	if info.Name == "" {
		info.Name = stamped.Name
	}
	if info.Version == "" {
		info.Version = stamped.Version
	}
	if info.Build == "" {
		info.Build = stamped.Build
	}
	if info.Channel == "" {
		info.Channel = stamped.Channel
	}
	return info
}

// newPlaybackStartRequestDigestsV3 fingerprints both the body and normalized
// device identity because either can change the selected playback plan. It
// also retains the pre-device digest while attempts written by an older
// server can still be replayed during a rolling deployment.
func newPlaybackStartRequestDigestsV3(body []byte, deviceID string) playbackStartRequestDigestsV3 {
	hasher := sha256.New()
	_, _ = fmt.Fprintf(hasher, "%d:", len(body))
	_, _ = hasher.Write(body)
	_, _ = hasher.Write([]byte(deviceID))
	legacy := sha256.Sum256(body)
	return playbackStartRequestDigestsV3{
		current: hex.EncodeToString(hasher.Sum(nil)),
		legacy:  hex.EncodeToString(legacy[:]),
	}
}

func (d playbackStartRequestDigestsV3) matches(stored string) bool {
	return stored == "" || stored == d.current || stored == d.legacy
}

func appendStartWarningsV3(result *playback.PlannerResultV3, warnings []playback.DegradationWarningV3) {
	if result == nil || len(warnings) == 0 || result.Plan == nil {
		return
	}
	for _, warning := range warnings {
		if slices.Contains(result.Plan.DegradationWarnings, warning) {
			continue
		}
		result.Plan.DegradationWarnings = append(result.Plan.DegradationWarnings, warning)
	}
}

// annotateSubtitleDroppedV3 records a successful subtitle degradation.
func annotateSubtitleDroppedV3(result *playback.PlannerResultV3) {
	if result == nil || result.Terminal != nil || result.Plan == nil {
		return
	}
	result.Plan.DegradationWarnings = append(result.Plan.DegradationWarnings, playback.DegradationWarningV3{Code: subtitleDroppedUnavailableCodeV3, Message: "The selected subtitle is unavailable on this release; playback continues without it on the same release."})
}

var errOfferedCandidateUnavailable = errors.New("offered candidate unavailable")

func selectedAudioTrackLogFieldsV3(result playback.PlannerResultV3, audioIndex int) (string, string, int) {
	if result.Plan == nil {
		return "", "", audioIndex
	}
	// The plan's selected identity is authoritative: the caller's index is the
	// requested-file ordinal and can differ after a version fallback or a
	// server-side selection.
	index := audioIndex
	if selected := result.Plan.SelectedTracks.Audio; selected != nil && selected.Index != nil {
		index = *selected.Index
	}
	if index >= 0 && index < len(result.Plan.AudioTracks) {
		t := result.Plan.AudioTracks[index]
		language := t.Language
		if language == "" && len(t.Languages) > 0 {
			language = t.Languages[0]
		}
		return language, t.Codec, index
	}
	return "", "", audioIndex
}

func virtualPlanDecisionAttrsV3(result playback.PlannerResultV3, audioIndex int, decision virtualPlanDecisionV3) []any {
	if result.Plan == nil || result.Plan.EffectiveVirtualURI == "" {
		return nil
	}
	language, codec, ordinal := selectedAudioTrackLogFieldsV3(result, audioIndex)
	attrs := []any{"candidate_uri", result.Plan.EffectiveVirtualURI, "preferred_audio_language", decision.preferredAudioLanguage, "audio_track_language", language, "audio_track_codec", codec, "audio_track_index", ordinal}
	if decision.candidateCount >= 0 {
		attrs = append(attrs, "candidate_count", decision.candidateCount)
	}
	if decision.candidateCount > 0 {
		attrs = append(attrs, "candidate_rank", decision.candidateRank)
	}
	return attrs
}

func localTransportReadinessErrorV3(opts playback.TranscodeOpts, running bool, cause error) *transportErrorV3 {
	// A typed decoder verdict is evidence about the source bytes, not the
	// subtitle pipeline: it outranks the burn-in classification so the start or
	// replan rotation loop can substitute the candidate.
	if errors.Is(cause, playback.ErrSourceDecodeRejected) {
		return &transportErrorV3{reason: candidateSourceDecodeRejectedReasonV3, message: candidateSourceDecodeRejectedMessageV3, retryable: false, cause: cause}
	}
	if running && opts.SubtitleBurnIn && opts.SubtitleTrackIndex >= 0 {
		return &transportErrorV3{reason: subtitleUnavailableReasonV3, message: "The selected subtitle could not be prepared in time on this release; retry, or start with subtitles off.", retryable: true, cause: cause}
	}
	return manifestStartupTransportErrorV3(running, cause)
}

// startPlannedPlaybackV3 creates a session and transport for an accepted plan.
func (h *PlaybackHandler) startPlannedPlaybackV3(r *http.Request, userID int, profileID string, req playback.StartRequestV3, requestDigests playbackStartRequestDigestsV3, requestedFile, effectiveFile *models.MediaFile, audioIndex int, intent startIntentV3, result playback.PlannerResultV3, clientInfo playback.ClientInfo) (playback.DecisionResponseV3, *transportErrorV3) { //nolint:funlen // startPlannedPlaybackV3 exceeds Vio's 100-line limit; refactor tracked separately
	if result.Plan == nil {
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: "internal_error", message: "The server produced no playback plan."}
	}
	mode := withHLSVariableSubstitutionV3(r, headerAuthenticatedMediaV3(req.ClientFeatures), req)
	ctx := playback.WithClientInfo(r.Context(), clientInfo)
	session, err := h.sessionMgr.StartSessionWithFilesContext(ctx, userID, profileID, effectiveFile.ID, requestedFile.ID, result.PlayMethod, result.TranscodeAudio)
	if err != nil {
		return playback.DecisionResponseV3{}, sessionStartErrorV3(err)
	}
	abort := func() { _ = h.stopPlaybackSessionByID(context.WithoutCancel(r.Context()), session.ID, "") }
	if req.ProgressPersistence == playback.ProgressPersistenceClientV3 || !sessionOwnsResumeTimelineV3(effectiveFile) {
		if err := h.sessionMgr.SetProgressPersistenceDisabled(session.ID, true); err != nil {
			abort()
			return playback.DecisionResponseV3{}, &transportErrorV3{
				reason:  "internal_error",
				message: "Failed to establish the requested progress persistence policy.",
				cause:   err,
			}
		}
	}
	if err := h.sessionMgr.UpdateAudioTrack(session.ID, audioIndex, result.PlayMethod); err != nil {
		abort()
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: "internal_error", message: "Failed to select the playback audio track.", cause: err}
	}
	position := floatOrZeroHandlerV3(req.StartPosition)
	if err := h.sessionMgr.UpdateProgress(session.ID, position, false); err != nil {
		abort()
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: "internal_error", message: "Failed to initialize the playback timeline.", cause: err}
	}
	session, err = h.sessionMgr.GetSession(session.ID)
	if err != nil {
		abort()
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: "internal_error", message: "Failed to load the initialized playback session.", cause: err}
	}
	result.Plan.SessionID = session.ID
	result.Plan.InventoryURL = "/api/v2/playback/" + session.ID + "/inventory"
	transport, transportErr := h.prepareTransportV3(r, session, effectiveFile, result, mode)
	if transportErr != nil {
		abort()
		return playback.DecisionResponseV3{}, transportErr
	}
	// Publish the substitution truth additively on the plan: when the effective
	// release differs from the requested row, name the requested row and a
	// machine-readable cause so the client can show an honest notice without
	// diffing ids first. These fields are set after the plan identity is
	// finalized inside the planner, so they never change the plan hash. The web
	// player still compares the live effective identity against the requested
	// one at render time, so a serve-layer rotation is covered too.
	substitutionReason := substitutionReasonUnknownV3
	if requestedFile.ID == effectiveFile.ID {
		substitutionReason = ""
	}
	publishSubstitutionFieldsV3(result.Plan, requestedFile.ID, effectiveFile.ID, substitutionReason)
	// One line per final plan decision so route selection is reconstructible
	// from server logs.
	planDecisionAttrs := []any{
		logComponentKey, playbackLogValueV3,
		requestIDLogKeyV3, chimw.GetReqID(r.Context()),
		"outcome", "plan",
		"decision_reason", result.Plan.DecisionReason,
		routeDowngradeLogKeyV3, result.RouteDowngrade,
		"delivery", result.Plan.Delivery,
		"play_method", string(result.PlayMethod),
		"requested_file_id", requestedFile.ID,
		"effective_file_id", effectiveFile.ID,
		"dv_profile", result.Plan.Source.DVProfile,
		"dynamic_range", result.Plan.Source.DynamicRange,
		"target_resolution", result.TargetResolution,
		"target_bitrate_kbps", result.TargetBitrateKbps,
		"quality_preference", req.QualityPreference,
		bandwidthEstimateLogKeyV3, intOrZeroHandlerV3(req.BandwidthEstimateKbps),
	}
	// Virtual playback gets the candidate and language decision on the same
	// line: which provider candidate was selected, where it ranked in the
	// considered list, the audio track the plan will play, and the resolved
	// playback.audio_language that steered it. Without these a cold playback
	// that ignored the configured language was unattributable.
	planDecisionAttrs = append(planDecisionAttrs, virtualPlanDecisionAttrsV3(result, audioIndex, virtualPlanDecisionV3{candidateCount: -1})...)
	slog.InfoContext(r.Context(), "playback plan decided", append(planDecisionAttrs, clientInfo.LogAttrs()...)...)
	applyTransportToneMapModeV3(&result, transport)
	frozenRecipe, frozenErr := h.freezeExecutableRecipeV3(r.Context(), effectiveFile, result)
	if frozenErr != nil {
		transport.rollback()
		abort()
		return playback.DecisionResponseV3{}, subtitleArtifactErrorV3("Failed to freeze the selected subtitle identity.", frozenErr)
	}
	result.Plan.Stream.URL = transport.url
	if err := h.attachSubtitleArtifactV3(r.Context(), session.ID, effectiveFile, result.Plan, result.SubtitleTrackIndex, &frozenRecipe, req.ClientFeatures); err != nil {
		transport.rollback()
		abort()
		return playback.DecisionResponseV3{}, subtitleArtifactErrorV3("Failed to prepare the selected subtitle artifact.", err)
	}
	response := playback.DecisionResponseV3{ProtocolVersion: playback.ProtocolV3, ServerFeatures: serverFeaturesForRequestV3(r.Context()), Outcome: playback.OutcomePlayableV3, SessionID: session.ID, PlaybackPlan: result.Plan}
	record := playback.AttemptRecordV3{PlaybackAttemptID: req.PlaybackAttemptID, SessionID: session.ID, UserID: userID, ProfileID: profileID, RequestedMediaFileID: requestedFile.ID, EffectiveMediaFileID: effectiveFile.ID, CurrentPlanID: result.Plan.PlanID, CurrentPlan: *result.Plan, FrozenRecipe: frozenRecipe, NormalizedRequest: req, ServerBitrateCapKbps: serverBitrateCapV3(r.Context()), StartResponse: response, RequestDigest: requestDigests.current, ExpiresAt: time.Now().Add(playback.MaxTokenTTL), DesiredTracks: h.startDesiredTracksV3(r.Context(), requestedFile, intent, effectiveFile, result.Plan, frozenRecipe)}
	if err := h.updateV3SessionState(r.Context(), session, effectiveFile, result, transport, mode); err != nil {
		recordTransportPhaseV3(r.Context(), transportPhasePersistV3, time.Now())
		transport.rollback()
		abort()
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: "internal_error", message: "Failed to commit the live playback session.", cause: err}
	}
	// The startup grace runs from here, not from admission: a slow transport
	// preparation must not use up the client's wait for its startup timeout.
	if err := h.sessionMgr.MarkPlanCommitted(session.ID); err != nil {
		transport.rollback()
		abort()
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: "internal_error", message: "Failed to commit the live playback session.", cause: err}
	}
	if err := h.PlanStoreV3.SaveAttempt(r.Context(), record); err != nil {
		recordTransportPhaseV3(r.Context(), transportPhasePersistV3, time.Now())
		transport.rollback()
		abort()
		return h.replayDuplicateStartV3(r.Context(), userID, profileID, req, requestDigests, err)
	}
	recordTransportPhaseV3(r.Context(), transportPhasePersistV3, time.Now())
	if commitErr := transport.commit(); commitErr != nil {
		abort()
		return playback.DecisionResponseV3{}, commitErr
	}
	h.recordSessionRecipeV3(r.Context(), result.Plan.PlanID, &transport)
	if started, err := h.sessionMgr.GetSession(session.ID); err == nil {
		frontend := LifecycleFrontendBridge
		if isNativeAPIV2(r.Context()) {
			frontend = LifecycleFrontendNative
		}
		h.SessionStarted(r.Context(), started, frontend)
	}
	// Start-side effects belong after both the attempt and transport commits:
	// retries that lose the idempotency race must not emit duplicate provider
	// scrobbles or analysis work for the short-lived session they roll back.
	h.raceCopySafetyV3(effectiveFile.ID, result.Plan)
	// The frozen /api/v1 bridge keeps saving the audio a start played, but
	// only when the client picked one: a track resolved from the profile's
	// language preference must not become a saved track that then outranks a
	// different preference on the next start. The native API saves only
	// explicit picks too.
	var legacyAudioTrackIndex *int
	if !isNativeAPIV2(r.Context()) && (req.AudioTrackID != "" || req.AudioTrackIndex != nil) {
		index := plannedAudioTrackIndexV3(result, audioIndex)
		legacyAudioTrackIndex = &index
	}
	h.enqueuePlaybackStartSideEffectsV3(r.Context(), session, effectiveFile, userID, profileID, legacyAudioTrackIndex)
	h.prefetchSubtitlesV3(session.ID, effectiveFile, req, result)
	h.enqueueRouteEventV3(playback.RouteEventRecordV3{RouteEventV3: playback.RouteEventV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: req.PlaybackAttemptID, SessionID: session.ID, PlanID: result.Plan.PlanID, Event: playback.RouteEventPlanSelectedV3, AppliedQuirkIDs: appliedQuirkIDsV3(result.Plan), QuirkRegistryRevision: appliedQuirkRevisionV3(result.Plan), OutputContextID: req.ClientPlaybackContext.Output.OutputContextID}, UserID: userID, ProfileID: profileID, ClientName: clientInfo.Name, ClientVersion: clientInfo.Version, ClientBuild: clientInfo.Build, ClientChannel: clientInfo.Channel, ClientModel: req.ClientPlaybackContext.Device.Model})
	return response, nil
}

// replayDuplicateStartV3 answers a start whose attempt row could not be saved
// because a concurrent duplicate saved it first: the duplicate's decision when
// it is the same request on a live session, otherwise the matching error.
func (h *PlaybackHandler) replayDuplicateStartV3(ctx context.Context, userID int, profileID string, req playback.StartRequestV3, requestDigests playbackStartRequestDigestsV3, saveErr error) (playback.DecisionResponseV3, *transportErrorV3) {
	persistFailed := &transportErrorV3{reason: "internal_error", message: "Failed to persist the playback plan.", cause: saveErr}
	reused := errors.Is(saveErr, playback.ErrIdempotencyKeyReusedV3)
	if !reused && !errors.Is(saveErr, playback.ErrPlaybackAttemptExistsV3) {
		return playback.DecisionResponseV3{}, persistFailed
	}
	existing, lookupErr := h.PlanStoreV3.GetAttemptByPlaybackAttemptID(ctx, req.PlaybackAttemptID)
	if lookupErr != nil || existing.UserID != userID || existing.ProfileID != profileID || existing.RequestedMediaFileID != req.FileID || !requestDigests.matches(existing.RequestDigest) {
		if reused {
			return playback.DecisionResponseV3{}, &transportErrorV3{reason: "playback_attempt_reused", message: "The playback attempt ID was reused with different input."}
		}
		return playback.DecisionResponseV3{}, persistFailed
	}
	if err := requireAttemptAPISurfaceV3(ctx, existing, req.ClientFeatures); err != nil {
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: "playback_attempt_reused", message: "The playback attempt belongs to a different API surface."}
	}
	// Replaying a concurrent duplicate is only valid while its session is
	// alive; otherwise tell the client to mint a new attempt rather than hand
	// it a plan it can never stream.
	if _, sessionErr := h.sessionMgr.GetSession(existing.SessionID); sessionErr != nil {
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: terminalSessionExpiredV3, message: "The playback session for this attempt has ended.", retryable: true}
	}
	// A durable decision written by a mixed-version or partially applied writer
	// can describe a different session than the attempt owns; replaying it
	// would hand this client another session's plan.
	existingResponse := decisionResponseFromAttemptV3(existing)
	if !startReplayResponseBelongsToAttemptV3(existingResponse, existing) {
		return foreignAttemptSessionDecisionV3(), nil
	}
	return existingResponse, nil
}

func (h *PlaybackHandler) enqueuePlaybackStartSideEffectsV3(ctx context.Context, session *playback.Session, file *models.MediaFile, userID int, profileID string, legacyAudioTrackIndex *int) {
	if h == nil || session == nil || file == nil {
		return
	}
	h.v3StartEffectsOnce.Do(func() {
		h.v3StartEffectsQueue = make(chan playbackStartSideEffectsV3, playbackStartSideEffectsQueueSizeV3)
		h.v3StartEffectsPending = make(map[string]*playbackStartSideEffectsStateV3)
		for range playbackStartSideEffectsWorkersV3 {
			go func() {
				for task := range h.v3StartEffectsQueue {
					h.runPlaybackStartSideEffectsV3(task)
				}
			}()
		}
	})

	taskCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), playbackStartSideEffectsTimeoutV3)
	state := &playbackStartSideEffectsStateV3{
		ctx:    taskCtx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	task := playbackStartSideEffectsV3{
		session:   *session,
		file:      *file,
		userID:    userID,
		profileID: profileID,
		state:     state,

		legacyAudioTrackIndex: legacyAudioTrackIndex,
	}
	h.v3StartEffectsMu.Lock()
	h.v3StartEffectsPending[session.ID] = state
	h.v3StartEffectsMu.Unlock()
	select {
	case h.v3StartEffectsQueue <- task:
	default:
		// Preserve side-effect durability and ordering under overload. The queue
		// bounds background work; a saturated server pays the old synchronous
		// cost instead of silently dropping a provider event.
		h.runPlaybackStartSideEffectsV3(task)
	}
}

// prefetchSubtitlesV3 asks for the embedded subtitle tracks a playback will
// likely want soon: the next episode's in the language of the subtitle it
// plays, and, for a client whose native embedded entry declares
// literal_override_tags, the playing file's plain-text tracks, whose facts
// the next plan or replan needs. The reads run detached and end with the
// session.
func (h *PlaybackHandler) prefetchSubtitlesV3(sessionID string, file *models.MediaFile, req playback.StartRequestV3, result playback.PlannerResultV3) {
	if h.SubtitlePrefetch == nil || file == nil {
		return
	}
	h.SubtitlePrefetch.SessionStarted(keptsub.PrefetchRequest{
		SessionID:         sessionID,
		File:              file,
		Language:          plannedSubtitleLanguageV3(file, result.SubtitleTrackIndex),
		LearnOverrideTags: declaresLiteralOverrideTagsV3(req),
	})
}

// plannedSubtitleLanguageV3 is the language of the sidecar or embedded
// subtitle at a plan's combined subtitle ordinal, or "".
func plannedSubtitleLanguageV3(file *models.MediaFile, ordinal int) string {
	switch {
	case ordinal < 0:
		return ""
	case ordinal < len(file.ExternalSubtitles):
		return file.ExternalSubtitles[ordinal].Language
	case ordinal-len(file.ExternalSubtitles) < len(file.SubtitleTracks):
		return file.SubtitleTracks[ordinal-len(file.ExternalSubtitles)].Language
	}
	return ""
}

// declaresLiteralOverrideTagsV3 reports a native embedded entry, on any
// delivery, that lists literal_override_tags.
func declaresLiteralOverrideTagsV3(req playback.StartRequestV3) bool {
	for _, delivery := range req.ClientPlaybackContext.Deliveries {
		for _, entry := range delivery.Subtitles.NativeEmbedded {
			for _, limitation := range entry.Limitations {
				if strings.EqualFold(strings.TrimSpace(limitation), playback.NativeEmbeddedLimitationLiteralOverrideTagsV3) {
					return true
				}
			}
		}
	}
	return false
}

func (h *PlaybackHandler) runPlaybackStartSideEffectsV3(task playbackStartSideEffectsV3) {
	state := task.state
	if state == nil {
		return
	}
	h.v3StartEffectsMu.Lock()
	state.started = true
	stopRequested := state.stopRequested
	h.v3StartEffectsMu.Unlock()
	defer func() {
		state.cancel()
		close(state.done)
		h.v3StartEffectsMu.Lock()
		if h.v3StartEffectsPending[task.session.ID] == state {
			delete(h.v3StartEffectsPending, task.session.ID)
		}
		h.v3StartEffectsMu.Unlock()
	}()
	if stopRequested || state.ctx.Err() != nil {
		return
	}
	ctx := state.ctx

	if !task.session.DisableProgressPersistence && h.WatchScrobbler != nil {
		targetID, targetErr := h.playbackProgressTarget(ctx, &task.file)
		if targetErr != nil {
			slog.WarnContext(ctx, "failed to resolve canonical episode for start scrobble", "component", "api",
				"session", task.session.ID, "error", targetErr)
		} else if targetID != "" {
			event := h.scrobbleEventForSession(ctx, &task.session, targetID, float64(task.file.Duration), task.session.Position)
			if err := h.WatchScrobbler.ScrobbleStart(ctx, event); err != nil {
				slog.WarnContext(ctx, "failed to queue watch provider start scrobble", "component", "api", "session", task.session.ID, "error", err)
			}
		}
	}
	if ctx.Err() != nil {
		return
	}
	if h.ChapterThumbnailQueuer != nil {
		slog.InfoContext(ctx,
			"queueing chapter thumbnails", "component", "api",
			"source", "playback_start",
			"content_id", task.file.ContentID,
			"file_id", task.file.ID,
			"target_seconds", task.session.Position,
		)
		h.ChapterThumbnailQueuer.QueuePriorityFileAtPosition(ctx, task.file.ID, task.session.Position)
	}
	if ctx.Err() != nil {
		return
	}
	h.maybeQueueLazyPlaybackMarkers(ctx, &task.session, &task.file)
	if ctx.Err() != nil {
		return
	}
	if task.legacyAudioTrackIndex != nil {
		h.persistLegacyAudioPreferenceV3(ctx, task.session.ID, task.userID, task.profileID, &task.file, *task.legacyAudioTrackIndex)
	}
	h.persistSeriesPlaybackPreference(ctx, task.userID, task.profileID, &task.file)
	h.syncSessionsNow(ctx, "v3_start")
}

func (h *PlaybackHandler) waitForPlaybackStartSideEffectsV3(ctx context.Context, sessionID string) {
	if h == nil || sessionID == "" {
		return
	}
	h.v3StartEffectsMu.Lock()
	state := h.v3StartEffectsPending[sessionID]
	h.v3StartEffectsMu.Unlock()
	if state == nil {
		return
	}
	// Detached from the request's cancel but clamped to any enclosing deadline:
	// under the v2 stop finalization this wait must not reset past the one
	// end-to-end budget that keeps the whole finalization inside its lease.
	waitCtx, cancel := detachedWithin(ctx, playbackStartSideEffectsTimeoutV3)
	defer cancel()
	select {
	case <-state.done:
	case <-waitCtx.Done():
		slog.WarnContext(ctx, "timed out waiting for playback start side effects", logComponentKey, "api", "session", sessionID)
	}
}

func (h *PlaybackHandler) cancelPlaybackStartSideEffectsV3(ctx context.Context, sessionID string) {
	if h == nil || sessionID == "" {
		return
	}
	if h.SubtitlePrefetch != nil {
		h.SubtitlePrefetch.SessionEnded(sessionID)
	}
	h.v3StartEffectsMu.Lock()
	state := h.v3StartEffectsPending[sessionID]
	if state == nil {
		h.v3StartEffectsMu.Unlock()
		return
	}
	state.stopRequested = true
	state.cancel()
	started := state.started
	done := state.done
	h.v3StartEffectsMu.Unlock()
	// A queued task observes stopRequested before issuing any side effect. A
	// running task is allowed to unwind so provider start cannot cross the stop.
	if !started {
		return
	}
	// Same detached, clamped budget as waitForPlaybackStartSideEffectsV3: the
	// unwind may not run past an enclosing finalization deadline.
	waitCtx, cancel := detachedWithin(ctx, playbackStartSideEffectsTimeoutV3)
	defer cancel()
	select {
	case <-done:
	case <-waitCtx.Done():
		slog.WarnContext(ctx, "timed out canceling playback start side effects", logComponentKey, "api", "session", sessionID)
	}
}

// persistLegacyAudioPreferenceV3 is the audio preference write the frozen
// /api/v1 bridge makes on every start and track change. It serializes start
// and replan writes and rejects a delayed start value after the live session
// has already adopted a newer track.
func (h *PlaybackHandler) persistLegacyAudioPreferenceV3(ctx context.Context, sessionID string, userID int, profileID string, file *models.MediaFile, audioTrackIndex int) {
	h.v3AudioPreferenceMu.Lock()
	defer h.v3AudioPreferenceMu.Unlock()
	current, err := h.sessionMgr.GetSession(sessionID)
	if err != nil || current.AudioTrackIndex != audioTrackIndex {
		return
	}
	h.persistAudioPreference(ctx, userID, profileID, file, audioTrackIndex)
}

// prepareTransportV3 selects and prepares the local, remote, or identity transport.
func (h *PlaybackHandler) prepareTransportV3(r *http.Request, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, mode mediaAuthModeV3) (preparedTransportV3, *transportErrorV3) {
	return h.prepareTransportWithPolicyV3(r, session, file, result, mode, h.playbackRoutingPolicyForContextV3(r.Context()))
}

func (h *PlaybackHandler) prepareTransportWithPolicyV3(r *http.Request, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, mode mediaAuthModeV3, policy config.PlaybackRoutingPolicy) (preparedTransportV3, *transportErrorV3) {
	return h.prepareTransportWithPolicyAndExclusionsV3(r, session, file, result, mode, policy, nil)
}

func (h *PlaybackHandler) prepareTransportWithPolicyAndExclusionsV3(
	r *http.Request,
	session *playback.Session,
	file *models.MediaFile,
	result playback.PlannerResultV3,
	mode mediaAuthModeV3,
	policy config.PlaybackRoutingPolicy,
	initialExcludedShapes map[string]struct{},
) (preparedTransportV3, *transportErrorV3) {
	if !shouldUsePooledPlaybackNodeV3(file) {
		policy.RemuxExecution = config.PlaybackExecutionAPIOnly
		policy.VideoTranscodeExecution = config.PlaybackExecutionAPIOnly
		policy.RemuxEgress = config.PlaybackEgressAPIOnly
		policy.VideoTranscodeEgress = config.PlaybackEgressAPIOnly
		policy.DirectPlayEgress = config.PlaybackEgressAPIOnly
	} else if isVirtualPlaybackFile(file) || strings.HasPrefix(strings.ToLower(strings.TrimSpace(file.FilePath)), virtualPlaybackPrefix) {
		policy.RemuxExecution = config.PlaybackExecutionAPIOnly
		policy.RemuxEgress = config.PlaybackEgressAPIOnly
		policy.DirectPlayEgress = config.PlaybackEgressAPIOnly
	}
	timeline, timelineErr := h.prepareTransportTimelineV3(r.Context(), session, file, result)
	if timelineErr != nil {
		return preparedTransportV3{}, timelineErr
	}
	if result.Plan.Delivery != playback.DeliveryTranscodeHLSV3 && result.Plan.Delivery != playback.DeliveryRemuxHLSV3 {
		return h.prepareIdentityTransportV3(r, session, file, result, timeline, mode, policy, initialExcludedShapes)
	}
	proxyAllowed := mode.proxyEgress || (!mode.headerAuth && h.JWTSecret != "")
	excludedNodes := make(map[string]struct{})
	excludedShapes := make(map[string]struct{}, len(initialExcludedShapes))
	for shapeID := range initialExcludedShapes {
		excludedShapes[shapeID] = struct{}{}
	}
	var lastErr *transportErrorV3
	// Every candidate failure being audio-local is reported as the audio reason
	// rather than the generic route exhaustion, so the client gets a clear
	// audio outcome. A single non-audio failure makes the exhaustion a route
	// failure.
	loopStartedAt := time.Now()
	for attempts := 0; attempts < 32; attempts++ {
		if time.Since(loopStartedAt) >= maxHLSRouteAttemptLoopV3 {
			slog.WarnContext(r.Context(), "protocol v3 transport route loop reached time budget", logComponentKey, playbackLogValueV3, "elapsed_ms", time.Since(loopStartedAt).Milliseconds(), "attempts", attempts)
			break
		}
		// The route resolver fans out per-node capability lookups to build its
		// eligibility predicate, and those record transport_capability inside
		// this span. Record route net of that delta so the two phases stay
		// disjoint: the node probes are named as capability, the pure routing
		// decision as route.
		routeStart := time.Now()
		capabilityBefore := transportPhaseDurationV3(r.Context(), transportPhaseCapabilityV3)
		decision := h.resolveHLSRouteWithPolicyV3(r.Context(), session, result, policy, proxyAllowed, excludedNodes, excludedShapes)
		recordTransportPhaseNetOfV3(r.Context(), "transport_route", routeStart, transportPhaseCapabilityV3, capabilityBefore)
		if !decision.Selected() {
			if canRetrySoftwareToneMapV3(result) {
				if fallback, attempted, fallbackErr := h.prepareSoftwareToneMapFallbackWithPolicyV3(r, session, file, result, mode, policy); attempted {
					if fallbackErr == nil {
						return fallback, nil
					}
					return fallback, combineTransportErrorsV3(lastErr, fallbackErr)
				}
			}
			if lastErr != nil {
				return preparedTransportV3{}, lastErr
			}
			retryable := decision.Outcome == noderouting.OutcomeCapacityUnavailable
			return preparedTransportV3{}, &transportErrorV3{
				reason: string(decision.Outcome), message: "No playback route satisfies the configured policy and current node availability.", retryable: retryable,
			}
		}

		if decision.Shape.Execution == noderouting.ExecutionAPI {
			if capabilityErr := h.validateLocalTransportCapabilitiesV3(r.Context(), result); capabilityErr != nil {
				lastErr = combineTransportErrorsV3(lastErr, capabilityErr)
				excludedShapes[decision.Shape.ID] = struct{}{}
				continue
			}
			transport, transportErr := h.prepareLocalTransportV3(r, session, file, result, timeline, mode)
			if transportErr == nil {
				return transport, nil
			}
			if transportErr.reason == candidateSourceDecodeRejectedReasonV3 {
				// A source the decoder rejected is bad for every executor, so
				// rotating to another delivery shape would only re-encode the
				// same undecodable bytes. Surface it immediately so the start or
				// replan rotation loop can substitute a provider candidate.
				return preparedTransportV3{}, transportErr
			}
			if transportErr.reason == subtitleUnavailableReasonV3 {
				// A subtitle-local failure is not a route problem: every other
				// shape or node would run the same release and prepare the same
				// subtitle. Surface it unchanged so the release and the
				// subtitle decision stay with the caller; never route around it
				// into a different delivery.
				return preparedTransportV3{}, transportErr
			}
			if !audioOnlyTransportReasonV3(transportErr.reason) {
			}
			lastErr = combineTransportErrorsV3(lastErr, transportErr)
			excludedShapes[decision.Shape.ID] = struct{}{}
			continue
		}

		plan := decision.Plan
		nodeURL := plan.TranscodeNode.URL
		transformations, capabilityErr := h.remoteTransformationsV3(r.Context(), nodeURL)
		// A failed capability read may be transient; only a node whose
		// advertised capabilities rule the recipe out is ineligible.
		ineligible := false
		if capabilityErr == nil {
			capabilityErr = validateAdvertisedTransformationsV3(result.Plan, transformations)
			ineligible = capabilityErr != nil
		}
		if capabilityErr == nil && planRequiresToneMapV3(result.Plan) {
			capabilities, err := h.remoteToneMapCapabilitiesV3(r.Context(), nodeURL, false)
			if err != nil {
				capabilityErr = err
			} else {
				capabilityErr = validateToneMapExecutorV3(result, capabilities)
				ineligible = capabilityErr != nil
			}
		}
		if capabilityErr != nil {
			slog.WarnContext(r.Context(), "protocol v3 transcode node capability mismatch", "node", logredact.SanitizeURL(nodeURL), "error", capabilityErr)
			if releaser, ok := h.NodePlanner.(sessionReservationReleaserV3); ok {
				releaser.ReleaseSession(session.ID)
			}
			excludedNodes[nodeURL] = struct{}{}
			lastErr = combineTransportErrorsV3(lastErr, &transportErrorV3{reason: routeCapabilityUnavailableReasonV3, message: "No available worker can execute the selected playback recipe.", retryable: true, cause: capabilityErr, executorIneligible: ineligible})
			continue
		}

		transport, transportErr := h.prepareRemoteTransportV3(r, session, file, result, plan, timeline, mode)
		proxyAuthorityFailed := false
		if transportErr == nil {
			// URL construction is part of route preparation. A proxy route that
			// could not establish its authority must retry a legal API-egress shape,
			// never silently mutate the selected topology.
			if decision.Shape.Egress != noderouting.EgressProxy || strings.HasPrefix(transport.url, "http") {
				return transport, nil
			}
			apiEgressAllowed := false
			switch decision.Shape.Workload {
			case noderouting.WorkloadRemux:
				apiEgressAllowed = policy.RemuxEgress != config.PlaybackEgressProxyOnly
			case noderouting.WorkloadVideoTranscode:
				apiEgressAllowed = policy.VideoTranscodeEgress != config.PlaybackEgressProxyOnly
			}
			if apiEgressAllowed {
				// prepareRemoteTransportV3 already released only the unused proxy
				// half. Keep the running executor and publish its API relay rather
				// than stopping and restarting an identical generation.
				return transport, nil
			}
			transport.rollback()
			transportErr = &transportErrorV3{reason: "route_preparation_failed", message: "The selected proxy could not establish playback authority.", retryable: true}
			proxyAuthorityFailed = true
		}
		if releaser, ok := h.NodePlanner.(sessionReservationReleaserV3); ok {
			releaser.ReleaseSession(session.ID)
		}
		if proxyAuthorityFailed {
			excludedShapes[decision.Shape.ID] = struct{}{}
		} else {
			excludedNodes[nodeURL] = struct{}{}
		}
		lastErr = combineTransportErrorsV3(lastErr, transportErr)
	}
	return preparedTransportV3{}, exhaustedRoutesErrorV3(lastErr)
}

// exhaustedRoutesErrorV3 ends a route loop that ran out of attempts. A final
// preflight rejection stays final; anything else may clear on a retry.
func exhaustedRoutesErrorV3(lastErr *transportErrorV3) *transportErrorV3 {
	if lastErr != nil && !lastErr.retryable && errors.Is(lastErr.cause, tonemap.ErrSourcePreflightRejected) {
		return lastErr
	}
	return &transportErrorV3{reason: "route_preparation_failed", message: "Playback route preparation exhausted every candidate.", retryable: true, cause: lastErr}
}

func (h *PlaybackHandler) checkReplacementAdmissionV3(
	ctx context.Context,
	session *playback.Session,
	result playback.PlannerResultV3,
) *transportErrorV3 {
	if session == nil {
		return &transportErrorV3{reason: "internal_error", message: "Playback route admission has no live session."}
	}
	if checker, ok := h.sessionMgr.(replacementAdmissionCheckerV3); ok {
		if err := checker.CheckReplacementAllowed(ctx, session.ID, result.PlayMethod, result.TranscodeAudio); err != nil {
			return sessionStartErrorV3(err)
		}
		return nil
	}
	if checker, ok := h.sessionMgr.(transcodePermissionChecker); ok &&
		(result.PlayMethod == playback.PlayTranscode || result.TranscodeAudio) {
		if err := checker.CheckTranscodingAllowed(ctx, session.UserID, result.PlayMethod == playback.PlayTranscode); err != nil {
			return sessionStartErrorV3(err)
		}
	}
	return nil
}

// canRetrySoftwareToneMapV3 permits a software retry only for an initial
// hardware selection whose policy allows it; frozen recovery recipes stay exact.
func canRetrySoftwareToneMapV3(result playback.PlannerResultV3) bool {
	return result.FrozenSourceMetadata == nil && result.ToneMapMode == tonemap.ModeHardware &&
		result.ToneMapPolicy.Allows(tonemap.ModeSoftware)
}

// prepareSoftwareToneMapFallbackV3 retries an eligible failed hardware recipe
// on a software-capable remote node or, when allowed, on the API host.
func (h *PlaybackHandler) prepareSoftwareToneMapFallbackV3(r *http.Request, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, mode mediaAuthModeV3) (preparedTransportV3, bool, *transportErrorV3) {
	return h.prepareSoftwareToneMapFallbackWithPolicyV3(r, session, file, result, mode, h.playbackRoutingPolicyForContextV3(r.Context()))
}

// prepareSoftwareToneMapFallbackWithPolicyV3 re-enters route preparation with a
// software tone-map recipe, so the retry re-derives its own timeline rather than
// inheriting the failed attempt's.
func (h *PlaybackHandler) prepareSoftwareToneMapFallbackWithPolicyV3(r *http.Request, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, mode mediaAuthModeV3, policy config.PlaybackRoutingPolicy) (preparedTransportV3, bool, *transportErrorV3) {
	if !canRetrySoftwareToneMapV3(result) {
		return preparedTransportV3{}, false, nil
	}
	fallbackResult := result
	fallbackResult.ToneMapMode = tonemap.ModeSoftware
	fallback, fallbackErr := h.prepareTransportWithPolicyV3(r, session, file, fallbackResult, mode, policy)
	return fallback, true, fallbackErr
}

func (h *PlaybackHandler) validateLocalTransportCapabilitiesV3(ctx context.Context, result playback.PlannerResultV3) *transportErrorV3 {
	if !planRequiresServerTransformationsV3(result.Plan) {
		return nil
	}
	// Only a tone-mapped recipe depends on the tone-map probe. Any other recipe
	// is checked against the base registry, so an SDR transcode never waits on
	// a cold probe or fails when one times out.
	requiresToneMap := planRequiresToneMapV3(result.Plan)
	localRegistry, registryErr := h.probedTransformationRegistryV3(ctx)
	if requiresToneMap {
		var capabilityErr error
		localRegistry, capabilityErr = h.localHLSExecutionRegistryV3(ctx)
		if capabilityErr != nil {
			return &transportErrorV3{reason: capabilityUnavailableReasonV3, message: "Local transcode capability validation is temporarily unavailable.", retryable: true, cause: capabilityErr}
		}
	}
	if err := validateAdvertisedTransformationsV3(result.Plan, localRegistry.Advertised()); err != nil {
		// A mismatch read from a failed probe may clear on a retry.
		return &transportErrorV3{reason: capabilityUnavailableReasonV3, message: "No available transcode executor can run the selected playback recipe.", retryable: true, cause: errors.Join(err, registryErr), executorIneligible: registryErr == nil}
	}
	if !requiresToneMap {
		return nil
	}
	capabilities, capabilityErr := h.localToneMapCapabilitiesForTransportV3(ctx)
	if capabilityErr != nil {
		return &transportErrorV3{reason: capabilityUnavailableReasonV3, message: "Local tone-map capability validation is temporarily unavailable.", retryable: true, cause: capabilityErr}
	}
	if err := validateToneMapExecutorV3(result, capabilities); err != nil {
		return &transportErrorV3{reason: capabilityUnavailableReasonV3, message: "No available transcode executor can run the selected tone-map recipe.", retryable: true, cause: err, executorIneligible: true}
	}
	return nil
}

func (h *PlaybackHandler) validateLocalProgressiveCapabilitiesV3(ctx context.Context, result playback.PlannerResultV3) *transportErrorV3 {
	if !planRequiresServerTransformationsV3(result.Plan) {
		return nil
	}
	if err := validateAdvertisedTransformationsV3(result.Plan, h.transformationRegistryV3(ctx).Advertised()); err != nil {
		return &transportErrorV3{
			reason: routeCapabilityUnavailableReasonV3, message: "The API server cannot execute the selected progressive-remux recipe.",
			retryable: true, cause: err,
		}
	}
	return nil
}

// copySeekAnchorAttemptBudget sizes one copy-video seek-anchor attempt against
// the link's adaptive per-attempt budget and the caller's remaining time, and
// reports whether a retry is reserved for it. remaining is the caller's whole
// remaining budget, for logging.
//
// The budget is the adaptive attempt size clamped to what the caller has left
// after the scheduling margin, so an attempt started here can never be
// truncated by the caller's deadline and answer that deadline instead of the
// anchor failure. When the caller can hold two adaptive attempts the first one
// is capped at its adaptive size and a retry is reserved; when it cannot, the
// first attempt keeps everything usable and no retry is planned, because
// splitting a window that cannot hold two full attempts would truncate both of
// them — a link whose probe needs 4.7s cut to half its window twice serves
// neither attempt, while one honest attempt plus a clear retryable verdict
// leaves the client's own seek retry the chance to land.
//
// This replaces the previous "does a full second probe still fit?" gate, which
// compared a *freshly re-derived* adaptive attempt (sized from the observation
// the expired attempt had just recorded, so twice a 10s stall) against the
// remaining deadline. On a slow-but-alive provider that gate could never pass,
// so the retry was skipped and one slow attempt ended the session.
func copySeekAnchorAttemptBudget(ctx context.Context, adaptive time.Duration, reserveRetry bool) (budget time.Duration, retryReserved bool, remaining time.Duration) {
	if ctx == nil {
		return adaptive, reserveRetry, 0
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return adaptive, reserveRetry, 0
	}
	remaining = time.Until(deadline)
	usable := remaining - copySeekProbeRetryMargin
	if usable <= 0 {
		// No room to reserve a retry. One attempt bounded by what is actually
		// left keeps the single-probe behavior a tight caller had before: a fast
		// provider still lands, and a slow one fails with the retryable anchor
		// verdict instead of being handed a second attempt the deadline would
		// truncate.
		if remaining <= 0 {
			return 0, false, remaining
		}
		if adaptive > remaining {
			adaptive = remaining
		}
		return adaptive, false, remaining
	}
	if adaptive > usable {
		adaptive = usable
		retryReserved = false
	} else {
		retryReserved = reserveRetry && usable >= 2*adaptive
	}
	return adaptive, retryReserved, remaining
}

// virtualTransportSameReleaseRetryKey marks a start request that has already
// spent its one same-release transport retry, so a retry that itself fails
// transiently walks to an alternate instead of retrying in a loop.
type virtualTransportSameReleaseRetryKey struct{}

func withVirtualTransportSameReleaseRetried(ctx context.Context) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, virtualTransportSameReleaseRetryKey{}, true)
}

func virtualTransportSameReleaseRetried(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	retried, _ := ctx.Value(virtualTransportSameReleaseRetryKey{}).(bool)
	return retried
}

// publishSubstitutionFieldsV3 sets the additive substitution fields on a plan
// when the effective release differs from the requested row. It is called after
// the plan identity is finalized inside the planner, so the UI-only fields
// never change the plan hash, and it is a no-op for the ordinary same-release
// case (effectiveFileID == requestedFileID) or an unknown effective row. A
// reason already carried from the resolve walk wins over the caller's fallback
// so the more specific cause is preserved.
func publishSubstitutionFieldsV3(plan *playback.PlanV3, requestedFileID, effectiveFileID int, fallbackReason string) {
	if plan == nil || effectiveFileID <= 0 || effectiveFileID == requestedFileID {
		return
	}
	if plan.SubstitutionReason == "" {
		plan.SubstitutionReason = fallbackReason
	}
	plan.SubstitutedFromFileID = requestedFileID
}

// replanSubstitutionReasonV3 classifies why a replan commits a release other
// than the row the viewer selected, into the additive substitution_reason
// vocabulary. A cause already named on the plan wins (a version-pick hunt or a
// transport alternate knows more than the replan does); otherwise the replan's
// own evidence names the move: a candidate rotation for a decode rejection, a
// failure-recovery rotation for a transport failure, and anything else as a
// release the planner could not keep. It never invents a dead-release verdict,
// which needs a confirmation only the resolver can give.
func replanSubstitutionReasonV3(plan *playback.PlanV3, req playback.ReplanRequestV3, decodeRotation bool) string {
	if plan != nil && plan.SubstitutionReason != "" {
		return plan.SubstitutionReason
	}
	switch {
	case decodeRotation:
		return substitutionReasonDecodeRejectedV3
	case failureRecoveryOperationV3(req.EffectiveOperation()):
		return substitutionReasonTransportFailedV3
	default:
		return substitutionReasonVersionNotPlayableV3
	}
}

// recoverVirtualTransportSameReleaseV3 spends one bounded same-release retry
// after a transient provider error, before the caller walks to another edition.
// It fires only for a transient cause, only once per request (the retry marks
// its context so a second transient failure does not loop), and only after a
// short pause so a provider that is already failing is not hammered.
//
// attempt re-runs the transport for the same release; it returns the retry's
// response and true only when that attempt succeeded. retried=false covers a
// non-transient cause, a request that already retried, a context that cannot
// fit the pause, and a retry that failed again — every case where the caller
// should proceed to its alternate walk or persist the terminal.
func (h *PlaybackHandler) recoverVirtualTransportSameReleaseV3(
	ctx context.Context,
	transient bool,
	attempt func(context.Context) (playback.DecisionResponseV3, *transportErrorV3),
) (playback.DecisionResponseV3, bool) {
	if !transient || attempt == nil || virtualTransportSameReleaseRetried(ctx) {
		return playback.DecisionResponseV3{}, false
	}
	retryCtx := withVirtualTransportSameReleaseRetried(withVirtualRelayFreshRegistration(ctx))
	if !h.waitCopySeekAnchorBackoff(retryCtx, 0) {
		return playback.DecisionResponseV3{}, false
	}
	slog.InfoContext(ctx, "virtual playback transport failed once on a transient provider error; retrying the same release",
		logComponentKey, playbackLogValueV3)
	response, retryErr := attempt(retryCtx)
	if retryErr != nil {
		return playback.DecisionResponseV3{}, false
	}
	return response, true
}

// waitCopySeekAnchorBackoff pauses before retrying a candidate after a
// transient provider (upstream 5xx) failure. It returns false when the pause
// would leave no room for the retry it precedes, so the retry is skipped
// instead of started without time to finish. remaining is the caller's whole
// remaining budget as reported by copySeekAnchorAttemptBudget; 0 means the
// caller had no deadline.
func (h *PlaybackHandler) waitCopySeekAnchorBackoff(ctx context.Context, remaining time.Duration) bool {
	d := copySeekAnchorTransientBackoff
	if remaining > 0 && remaining <= copySeekProbeRetryMargin+d {
		return false
	}
	if d <= 0 {
		return false
	}
	if h != nil && h.copySeekAnchorBackoff != nil {
		return h.copySeekAnchorBackoff(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// lookupCachedCopySeekAnchorV3 answers a remux copy seek from the shared anchor
// cache without resolving the source, so a proven cache hit skips the eager
// pinned-relay reopen and the FFmpeg probe. It returns true only for a position
// strictly inside an interval a prior probe already proved; the exact observed
// boundary and any unproven position fall through to a fresh probe, so seek
// accuracy is unchanged. A test that installs its own probe seam
// (copySeekAnchor) without a lookup seam owns the whole anchor path, so the
// process-wide cache is bypassed there and the injected probe still runs.
func (h *PlaybackHandler) lookupCachedCopySeekAnchorV3(ffmpegPath, sourceIdentity string, requestedSeekSeconds float64, segmentDuration int) (float64, int, bool) {
	if h == nil {
		return 0, 0, false
	}
	if h.copySeekAnchorLookup != nil {
		return h.copySeekAnchorLookup(ffmpegPath, sourceIdentity, requestedSeekSeconds, segmentDuration)
	}
	if h.copySeekAnchor != nil {
		return 0, 0, false
	}
	return playback.LookupCachedCopySeekAnchor(ffmpegPath, sourceIdentity, requestedSeekSeconds, segmentDuration)
}

// copySeekAnchorFailureLevel reports how a failed copy-video seek-anchor resolve
// is logged. A transient provider failure or the probe's own per-attempt budget
// expiring is not evidence against the release: the enclosing start may retry the
// same release on another candidate and succeed (production logged ERROR for a
// first attempt that timed out at its 5 s budget and then resolved on the retry
// four seconds later). Those are warnings. ERROR is reserved for a failure
// neither category explains, which is the terminal case an operator must look
// at.
func copySeekAnchorFailureLevel(seekFailure bool) slog.Level {
	if seekFailure {
		return slog.LevelWarn
	}
	return slog.LevelError
}

func (h *PlaybackHandler) prepareTransportTimelineV3(ctx context.Context, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3) (preparedTimelineV3, *transportErrorV3) {
	// The copy-video seek anchor is resolved here, before any route is selected
	// or FFmpeg spawned. Attribute its cost to its own phase so a seek start's
	// stall is not misread as route or spawn time.
	defer recordTransportPhaseV3(ctx, "transport_anchor", time.Now())
	if result.Plan == nil {
		return preparedTimelineV3{}, nil
	}

	requested := result.Plan.Timeline.SourceStartSeconds
	switch result.Plan.Delivery {
	case playback.DeliveryRemuxProgressiveV3, playback.DeliveryRemuxHLSV3:
		// Audio-only remuxes have no copied video keyframe to resolve. Keep the
		// requested position as their exact stream origin: the chunked output
		// clock restarts at zero and later seeks require another server reanchor.
		if file != nil && file.IsAudioOnly() {
			configureCopyRemuxTimelineV3(result.Plan, requested)
			return preparedTimelineV3{seekSeconds: requested, streamOriginSeconds: requested}, nil
		}
		origin, startSegment := 0.0, 0
		if requested > 0 {
			if file == nil || strings.TrimSpace(file.FilePath) == "" {
				return preparedTimelineV3{}, &transportErrorV3{reason: transcodeStartFailedReasonV3, message: remuxSeekPositionFailureMessageV3, retryable: true, cause: errors.New("copy seek anchor requires a media file path"), seekFailure: true}
			}
			// The whole anchor resolve — pinned-relay lookup and FFmpeg probe —
			// runs under a per-attempt budget derived from the link's recent
			// provider latency (see copySeekAnchorResolveBudget). The floor is
			// the fixed budget this used to be, so a fast link keeps exactly
			// the old behavior; only a link that recently answered slower than
			// that floor gets room to open. A relay that is stuck (not merely
			// returning 5xx) still fails the seek well inside the replan
			// budget, and every attempt stays bounded by the caller's deadline.
			linkKey := virtualProviderLinkLatencyKey(file)
			// The stable source identity is the un-resolved file path: for a
			// virtual file that is the provider-neutral URI, identical across
			// concurrent starts even though each start pins its own relay URL.
			// Cache and singleflight key on it so identical probes coalesce
			// and survive across requests.
			sourceIdentity := file.FilePath
			ffmpegPath := h.playbackConfig().FFmpegPath
			// A prior probe may already have proven this position's keyframe.
			// The pinned-relay resolve and the FFmpeg probe below are then pure
			// overhead: a cache hit needs no bytes to read. Consult the cache
			// first and, on a proven hit, skip the eager relay reopen entirely.
			// A position inside a probed interval answers, including an exact
			// repeat of the probed request; only a request on the anchor
			// keyframe still re-probes, so seek accuracy is unchanged.
			if cachedOrigin, cachedSegment, ok := h.lookupCachedCopySeekAnchorV3(ffmpegPath, sourceIdentity, requested, playback.DefaultSegmentDuration); ok {
				// A proven hit is the "known good" half of the anchor story: it is
				// the line that shows a seek inside an already-probed GOP paid no
				// provider work at all.
				slog.InfoContext(ctx, "copy seek anchor served from the proven cache without a probe",
					logComponentKey, playbackLogValueV3,
					"playback_session_id", session.ID,
					"requested_seek_seconds", requested,
					"anchor_seconds", cachedOrigin,
					"start_segment_number", cachedSegment,
				)
				configureCopyRemuxTimelineV3(result.Plan, cachedOrigin)
				return preparedTimelineV3{seekSeconds: requested, streamOriginSeconds: cachedOrigin, startSegmentNumber: cachedSegment, copySeekAnchorResolved: true}, nil
			}
			// Virtual sources are provider-neutral URIs, not FFmpeg inputs.
			// Resolve the candidate to a pinned-IP relay URL — the same
			// resolution the transcode transport performs — so the anchor
			// probe reads the real stream instead of failing on a virtual://
			// protocol it cannot open.
			anchorInput := file.FilePath
			releaseAnchor := func() {}
			virtualAnchor := isVirtualPlaybackFile(file) && h.RemoteStreamRelay != nil && (h.VirtualMediaResolver != nil || h.VirtualMediaDetailedResolver != nil)
			anchorExpiresAt := time.Time{}
			// A seek that re-anchors an active copy transport already has a
			// live registration for these bytes: the running generation's
			// input. Probing it skips the provider resolve a seek used to pay
			// on top of its anchor probe. The reuse is gated on the session
			// still being bound to this candidate and the registration still
			// being live, so a moved binding or a dead token falls back to the
			// normal resolve below. A retry always re-resolves (and mints a
			// fresh token), so the same registration is never presented twice.
			liveAnchorInput, liveAnchorReusable := "", false
			if virtualAnchor {
				if input, ok := h.livePinnedAnchorInputV3(session, file); ok {
					liveAnchorInput, liveAnchorReusable = input, true
					slog.InfoContext(ctx, "copy seek anchor probing the live pinned transport without a provider call",
						logComponentKey, playbackLogValueV3,
						"playback_session_id", session.ID,
						"requested_seek_seconds", requested,
						"virtual_uri", file.FilePath,
					)
				}
			}
			// resolveAnchorInput resolves the candidate to a pinned-IP relay URL
			// — the same resolution the transcode transport performs — and
			// adopts the fresh registration. It runs again before a retry so the
			// second probe carries a new relay token instead of replaying a
			// token the provider already refused. The previous registration is
			// released first so a retry cannot leak the entry it replaced.
			resolveAnchorInput := func(resolveCtx context.Context) error {
				res, cleanup, resolveErr := h.resolveVirtualAnchorURIWithRotationV3(resolveCtx, session, file)
				if resolveErr != nil {
					return resolveErr
				}
				releaseAnchor()
				anchorInput = res.URL
				anchorExpiresAt = res.ExpiresAt
				releaseAnchor = cleanup
				return nil
			}
			// resolveFailure carries the terminal a pinned-relay lookup failure
			// classifies into, so the caller still answers exactly the
			// provider-unavailable shape it answered before the anchor had a
			// retry.
			var resolveFailure *transportErrorV3
			// resolveAnchorForAttempt performs the pinned-relay lookup for one
			// attempt. retry marks a second attempt, which must probe fresh
			// bytes: the fresh-registration context mints a new relay entry so
			// the same token is never presented twice.
			resolveAnchorForAttempt := func(attemptCtx context.Context, retry bool) error {
				if liveAnchorReusable && !retry {
					// The running generation is already reading these bytes
					// through a live registration. Probe that registration
					// instead of re-listing the provider for a token the
					// transport holds; a failed probe re-resolves on the retry,
					// which always takes the fresh-registration path below.
					anchorInput = liveAnchorInput
					anchorExpiresAt = time.Time{}
					return nil
				}
				// Route the anchor through the same absent-pin rotation policy as
				// the serve layer and the replan rehydration: a provider that
				// renumbered its result ids must not terminal a remux seek when a
				// same-identity candidate is still listed. A different release is
				// refused rather than silently anchored.
				resolveCtx := attemptCtx
				if retry {
					resolveCtx = withVirtualRelayFreshRegistration(attemptCtx)
				}
				if resolveErr := resolveAnchorInput(resolveCtx); resolveErr != nil {
					// This is the first live provider listing for an already-built
					// plan: a remux seek start binds the row's probe evidence via
					// the P0 fast path with no liveness check, by design. A
					// transient provider listing outage here (empty answer, 5xx)
					// is a dependency failure, not a verdict about the release, so
					// classify it as the retryable provider_unavailable — for
					// identity-less rows too, which the evidence-gated outage
					// retry cannot cover. A genuine absent-pin or marked-failed
					// release verdict is left as-is so the start's alternate walk
					// still runs.
					classified := classifyVirtualAnchorProviderOutage(resolveErr)
					fallback := &transportErrorV3{reason: transcodeStartFailedReasonV3, message: remuxSeekPositionFailureMessageV3, retryable: true, cause: classified}
					if attemptCtx.Err() != nil && ctx.Err() == nil {
						// Our bounded anchor budget elapsed while looking the
						// candidate up. That is a slow relay, not a release
						// verdict, so the start must not rotate over it.
						fallback.seekFailure = true
					}
					resolveFailure = transportStartFailureV3(classified, fallback)
					return classified
				}
				// A stored URL that has already lapsed is renewed before the probe,
				// bounded to virtualProbeBudget, so the seek does not fail on a
				// dead token. Best-effort: a failed renewal keeps the existing
				// registration and lets the probe/retry carry the verdict.
				if virtualResolvedURLExpired(anchorExpiresAt, time.Now()) {
					refreshCtx, refreshCancel := context.WithTimeout(attemptCtx, virtualProbeBudget)
					if resolveErr := resolveAnchorInput(refreshCtx); resolveErr != nil {
						slog.WarnContext(ctx, "virtual seek anchor stored-url refresh failed; probing the existing registration",
							"component", "api",
							"playback_session_id", session.ID,
							"requested_seek_seconds", requested,
							"error", resolveErr,
						)
					}
					refreshCancel()
				}
				return nil
			}
			probeAnchor := func(probeCtx context.Context) (float64, int, error) {
				if h.copySeekAnchor != nil {
					return h.copySeekAnchor(probeCtx, ffmpegPath, anchorInput, requested, playback.DefaultSegmentDuration)
				}
				return playback.ResolveCopySeekAnchorForSource(probeCtx, ffmpegPath, sourceIdentity, anchorInput, requested, playback.DefaultSegmentDuration)
			}
			// Virtual upstreams occasionally serve a range request slowly
			// enough to blow the anchor budget. One bounded retry converts most
			// of those transient timeouts into successful seeks. The retry is
			// skipped when the caller's remaining budget cannot fit another
			// attempt: a second attempt would consume the rest of the budget and
			// fail with the caller deadline instead of the anchor error, so
			// failing fast on the first error is both cheaper and clearer. A
			// caller with no deadline keeps the retry.
			//
			// The retry budget is reserved by the per-attempt sizing below: an
			// attempt is never granted more than the caller has left after the
			// scheduling margin (so it cannot be truncated by the deadline),
			// and the first attempt only reserves a retry when the caller can
			// hold a second attempt of the same adaptive size.
			//
			// An upstream 5xx and a probe signal-killed from outside (the OOM
			// killer or an operator) are transient failures: they are retried
			// the same way, but only after a short bounded backoff, so a
			// provider that is already failing is not hammered by an immediate
			// re-probe. A caller cancellation never reaches this branch: the
			// loop stops before scheduling a retry.
			//
			// Our own budget expiring is its own case: it says the link is slow,
			// not that the release or the provider failed, so the same pinned
			// release is retried once under a budget re-derived from the
			// observation the attempt just recorded. A second expiry is the
			// verdict and surfaces the retryable seek error.
			retryReserved := false
			var retryRemaining time.Duration
			lastProbedInput := ""
			anchorBudgetElapsed := false
			// indexBudgetElapsed marks the specific case where the keyframe
			// index itself could not be read inside this start's budget. That
			// is not a verdict on the release and not a reason to fail the
			// start: the copied stream can begin from the requested position
			// exactly as it did before the anchor existed, so the start
			// degrades instead of answering a retryable seek error.
			indexBudgetElapsed := false
			var err error
			var cancelAttempt context.CancelFunc = func() {}
			defer func() { cancelAttempt() }()
			// A progressive remux has no segment layer to wait for the target,
			// so its anchor work is bounded by the seek's first-output window
			// rather than by the probe's own cap. A segmented HLS seek keeps
			// the adaptive budget: its segment layer can still wait or restart
			// for the requested segment after the probe.
			progressiveSeek := result.Plan.Delivery == playback.DeliveryRemuxProgressiveV3
			progressiveProbeWindow := time.Duration(0)
			anchorStarted := time.Now()
			if progressiveSeek {
				progressiveProbeWindow = seekAnchorProgressiveProbeWindowV3()
			}
			for attempt := 1; attempt <= 2; attempt++ {
				// Release the previous attempt's budget before deriving this
				// one. Each attempt re-derives it because a timed-out attempt
				// just recorded the link's observed cost, so a retry is sized
				// from that measurement instead of inheriting an expired
				// context whose budget is the reason for retrying.
				cancelAttempt()
				resolveFailure = nil
				indexBudgetElapsed = false
				attemptStarted := time.Now()
				attemptBudget, reserved, remaining := copySeekAnchorAttemptBudget(ctx, copySeekAnchorAdaptiveBudgetFor(file, ffmpegPath, sourceIdentity), attempt == 1)
				if progressiveSeek && progressiveProbeWindow > 0 {
					// The probe may not outlive the part of the seek window the
					// transport is not already using for its own start. A fast
					// failure still leaves room for the bounded retry; a probe
					// that spends the whole window leaves none, so the second
					// attempt starts with no budget and the verdict is answered
					// at the window bound instead of at the probe's own cap.
					windowLeft := progressiveProbeWindow - time.Since(anchorStarted)
					if windowLeft <= 0 {
						attemptBudget = 0
					} else if attemptBudget > windowLeft {
						attemptBudget = windowLeft
					}
				}
				if attempt == 1 {
					retryReserved = reserved
				}
				retryRemaining = remaining
				if attemptBudget <= 0 {
					// No caller time is left for an attempt at all. Report it
					// as our own budget expiring below: a slow caller, not a
					// verdict about the release.
					anchorBudgetElapsed = ctx.Err() == nil
					err = fmt.Errorf("resolve copy seek anchor: no attempt budget left: %w", context.DeadlineExceeded)
					break
				}
				attemptCtx, cancel := context.WithTimeout(ctx, attemptBudget)
				cancelAttempt = cancel
				if virtualAnchor {
					if resolveErr := resolveAnchorForAttempt(attemptCtx, attempt > 1); resolveErr != nil {
						anchorBudgetElapsed = attemptCtx.Err() != nil && ctx.Err() == nil
						recordVirtualProviderLinkLatency(linkKey, time.Since(attemptStarted), time.Now())
						if attempt > 1 {
							// The retry's own re-resolve failed: surface this
							// attempt's failure, exactly as a single resolve
							// failure would. resolveFailure already carries the
							// classification resolved above, so a proven
							// provider outage still answers provider_unavailable
							// instead of being demoted to a stale
							// transcode_start_failed verdict from attempt 1.
							slog.WarnContext(ctx, "virtual seek anchor re-resolve failed; skipping retry",
								"component", "api",
								"playback_session_id", session.ID,
								"requested_seek_seconds", requested,
								"error", resolveErr,
							)
							err = resolveErr
							break
						}
						err = resolveErr
						if !anchorBudgetElapsed {
							break
						}
						if !retryReserved {
							slog.WarnContext(ctx, "virtual seek anchor resolve retry skipped: insufficient remaining budget",
								"component", "api",
								"playback_session_id", session.ID,
								"requested_seek_seconds", requested,
								"remaining", remaining,
								"error", err,
							)
							break
						}
						slog.WarnContext(ctx, "virtual seek anchor resolve hit its own budget; retrying the same pinned release with a fresh budget",
							"component", "api",
							"playback_session_id", session.ID,
							"requested_seek_seconds", requested,
							"budget", attemptBudget,
							"error", err,
						)
						continue
					}
					if attempt > 1 && !anchorBudgetElapsed && anchorInput == lastProbedInput {
						// The re-resolve handed back the token that just 5xxed:
						// probing it again repeats a known failure. On a
						// transient provider error, walk to an alternate
						// same-identity candidate once instead of giving up;
						// anything else (or no alternate) keeps the terminal.
						if playback.IsTransientProviderError(err) {
							if rotated, cleanup, rotErr := h.resolveVirtualAnchorURIExcludingFailedV3(ctx, session, file, virtualResultCandidateID(file.FilePath)); rotErr == nil {
								releaseAnchor()
								anchorInput = rotated.URL
								anchorExpiresAt = rotated.ExpiresAt
								releaseAnchor = cleanup
							} else {
								slog.WarnContext(ctx, "virtual seek anchor re-resolve returned the same relay token; skipping a duplicate probe",
									"component", "api",
									"playback_session_id", session.ID,
									"requested_seek_seconds", requested,
									"rotation_error", rotErr,
								)
								break
							}
						} else {
							slog.WarnContext(ctx, "virtual seek anchor re-resolve returned the same relay token; skipping a duplicate probe",
								"component", "api",
								"playback_session_id", session.ID,
								"requested_seek_seconds", requested,
							)
							break
						}
					}
					if attempt > 1 && !anchorBudgetElapsed && !retryReserved {
						slog.WarnContext(ctx, "copy-video seek anchor retry skipped after re-resolve: insufficient remaining budget",
							"component", "api",
							"playback_session_id", session.ID,
							"requested_seek_seconds", requested,
							"remaining", retryRemaining,
						)
						break
					}
				}
				lastProbedInput = anchorInput
				probeStarted := time.Now()
				// The probe gets its own adaptive budget on a measured link, so
				// a resolve that spent most of the attempt cannot truncate it.
				probeCtx, probeCancel := copySeekProbeContext(ctx, attemptCtx, recentVirtualProviderLinkLatency(linkKey))
				if retryReserved || progressiveSeek {
					// A retry is reserved: keep this probe inside the attempt
					// window it was granted so it cannot spend the retry's
					// share on one provider open. A progressive-remux seek has
					// no retry share to protect but a first-output window to
					// meet, so its probe must stay inside the attempt too: the
					// link's adaptive probe budget can otherwise outlive the
					// seek window and hold the retry card past it.
					probeCtx, probeCancel = capProbeToAttempt(probeCtx, probeCancel, attemptCtx)
				}
				if attempt > 1 {
					// A retry must open the provider under its own budget
					// instead of joining the first attempt's spent flight: that
					// flight was started for the attempt whose budget just
					// ended, and it answers that attempt's timeout.
					probeCtx = playback.WithCopySeekAnchorRetryFlight(probeCtx)
				}
				origin, startSegment, err = probeAnchor(probeCtx)
				// A deadline error with the caller still alive is this
				// attempt's own budget expiring — or a shared probe whose
				// budget did. It says the link is slow, not that the release
				// failed, so it must stay a retryable seek error that does not
				// rotate the release, even when this attempt's own probe
				// context still has time left.
				anchorBudgetElapsed = err != nil && ctx.Err() == nil &&
					(probeCtx.Err() != nil || errors.Is(err, context.DeadlineExceeded))
				indexBudgetElapsed = anchorBudgetElapsed
				probeCancel()
				recordVirtualProviderLinkLatency(linkKey, time.Since(attemptStarted), time.Now())
				if err == nil || ctx.Err() != nil || attempt == 2 {
					break
				}
				if anchorBudgetElapsed {
					if !retryReserved {
						slog.WarnContext(ctx, "copy-video seek anchor probe retry skipped: insufficient remaining budget",
							"component", "api",
							"playback_session_id", session.ID,
							"requested_seek_seconds", requested,
							"remaining", retryRemaining,
							"error", err,
						)
						break
					}
					slog.WarnContext(ctx, "copy-video seek anchor probe hit its own budget; retrying the same pinned release with a fresh budget",
						"component", "api",
						"playback_session_id", session.ID,
						"requested_seek_seconds", requested,
						"budget", attemptBudget,
						"error", err,
					)
					continue
				}
				// A fast transient failure (a relay 5xx in milliseconds, not a
				// consumed attempt) leaves room for a real second attempt even
				// when the first could not reserve one: the failed attempt
				// barely spent its window. A slow failure keeps the reserved
				// gate so a truncated second probe cannot mask the error.
				fastTransient := playback.IsTransientProviderError(err) && time.Since(probeStarted) < playback.CopySeekProbeTimeout
				if !retryReserved && !fastTransient {
					slog.WarnContext(ctx, "copy-video seek anchor retry skipped: insufficient remaining budget",
						"component", "api",
						"playback_session_id", session.ID,
						"requested_seek_seconds", requested,
						"remaining", retryRemaining,
						"error", err,
					)
					break
				}
				if playback.IsTransientProviderError(err) {
					if !h.waitCopySeekAnchorBackoff(ctx, retryRemaining) {
						slog.WarnContext(ctx, "copy-video seek anchor retry skipped: no budget for the provider backoff",
							"component", "api",
							"playback_session_id", session.ID,
							"requested_seek_seconds", requested,
							"error", err,
						)
						break
					}
					slog.WarnContext(ctx, "copy-video seek anchor failed once on a transient provider error; backing off before retry",
						"component", "api",
						"playback_session_id", session.ID,
						"requested_seek_seconds", requested,
						"error", err,
					)
					continue
				}
				slog.WarnContext(ctx, "copy-video seek anchor failed once; retrying",
					"component", "api",
					"playback_session_id", session.ID,
					"requested_seek_seconds", requested,
					"error", err,
				)
			}
			releaseAnchor()
			if err != nil {
				if resolveFailure != nil {
					// A pinned-relay lookup that never opened is not a probe
					// verdict: answer the same provider-unavailable terminal the
					// lookup classified, exactly as it did before the anchor
					// could retry.
					return preparedTimelineV3{}, resolveFailure
				}
				if indexBudgetElapsed && ctx.Err() == nil {
					// The keyframe index could not be read inside the start
					// budget (a slow provider, not a verdict on the release).
					// A segmented HLS start can still degrade: its segment layer
					// starts the generation and waits or restarts for the
					// requested segment, so the unanchored timeline is servable.
					//
					// A progressive remux is one forward-only response and has no
					// segment layer to wait for it. Its unanchored start makes
					// FFmpeg seek the source itself, which is the same read the
					// anchor probe just could not finish; production showed it
					// answering 15 s of silence instead of media (session
					// 03d27fc2-312c-4d56-adfe-22774a0fcd7b). Refuse that start with
					// the retryable seek verdict immediately, after the probe
					// budget rather than after another no-byte stream window. The
					// next attempt is sized from the probe cost just recorded, and
					// the client keeps its retry affordance instead of a frozen
					// playhead.
					if result.Plan.Delivery == playback.DeliveryRemuxProgressiveV3 {
						noticeLevel := slog.LevelInfo
						if copySeekAnchorNotices.first(copySeekAnchorNoticeKey(session.ID, sourceIdentity)) {
							noticeLevel = slog.LevelWarn
						}
						slog.Log(ctx, noticeLevel, "copy-video seek anchor unavailable inside the start budget; refusing a progressive remux start that would produce no bytes",
							"component", "api",
							"playback_session_id", session.ID,
							"requested_seek_seconds", requested,
							"copy_seek_anchor_resolved", false,
							"reason", "anchor_budget_elapsed",
							"delivery", result.Plan.Delivery,
							"probe_budget_hint", copySeekAnchorProbeBudgetHint(ffmpegPath, sourceIdentity, playback.DefaultSegmentDuration),
							"error", err,
						)
						return preparedTimelineV3{}, &transportErrorV3{
							reason:      transcodeStartFailedReasonV3,
							message:     remuxSeekPositionFailureMessageV3,
							retryable:   true,
							cause:       err,
							seekFailure: true,
						}
					}
					// Fail the start no longer: begin the copied stream from the
					// requested position with the non-anchored timeline it had
					// before the anchor existed. The plan reports the requested
					// origin and copy_seek_anchor_resolved=false, so the client
					// and the transport see exactly the unanchored shape.
					//
					// The notice is worth one WARN per session: a silent degrade
					// left an anchor-less session indistinguishable from a resolved
					// one, so the next seek looked like a fresh 5-19s mystery
					// instead of a known slow link paying for the anchor again.
					noticeLevel := slog.LevelInfo
					if copySeekAnchorNotices.first(copySeekAnchorNoticeKey(session.ID, sourceIdentity)) {
						noticeLevel = slog.LevelWarn
					}
					slog.Log(ctx, noticeLevel, "copy-video seek anchor unavailable inside the start budget; starting from the requested position without it",
						"component", "api",
						"playback_session_id", session.ID,
						"requested_seek_seconds", requested,
						"copy_seek_anchor_resolved", false,
						"reason", "anchor_budget_elapsed",
						"probe_budget_hint", copySeekAnchorProbeBudgetHint(ffmpegPath, sourceIdentity, playback.DefaultSegmentDuration),
						"error", err,
					)
					configureCopyRemuxTimelineV3(result.Plan, requested)
					return preparedTimelineV3{seekSeconds: requested, streamOriginSeconds: requested}, nil
				}
				// A transient relay failure or our own anchor budget expiring is
				// not evidence against the release: the enclosing start may
				// still retry the same release on another candidate and succeed
				// (production: a first probe timed out at its 5 s per-attempt
				// budget, logged ERROR, and the retry four seconds later
				// resolved the anchor). Report it as a retryable seek error so
				// the client retries the seek instead of the start path
				// rotating the version or a replan tearing down the transport,
				// and keep ERROR for a failure that is neither.
				seekFailure := playback.IsTransientProviderError(err) ||
					(anchorBudgetElapsed && ctx.Err() == nil)
				slog.Log(ctx, copySeekAnchorFailureLevel(seekFailure),
					"failed to resolve protocol v3 copy-video seek anchor",
					"component", "api",
					"playback_session_id", session.ID,
					"requested_seek_seconds", requested,
					"retryable", seekFailure,
					"error", err,
				)
				return preparedTimelineV3{}, &transportErrorV3{reason: transcodeStartFailedReasonV3, message: remuxSeekPositionFailureMessageV3, retryable: true, cause: err, seekFailure: seekFailure}
			}
		}
		configureCopyRemuxTimelineV3(result.Plan, origin)
		return preparedTimelineV3{seekSeconds: requested, streamOriginSeconds: origin, startSegmentNumber: startSegment, copySeekAnchorResolved: true}, nil
	case playback.DeliveryTranscodeHLSV3:
		sourceMetadata := sourceExecutionMetadataV3(file, result)
		seekSeconds, startSegment := configureHLSTimelineV3(result.Plan, result.TargetVideoCodec, playback.DefaultSegmentDuration, sourceMetadata.DurationSeconds)
		return preparedTimelineV3{seekSeconds: seekSeconds, streamOriginSeconds: result.Plan.Timeline.StreamOriginSeconds, startSegmentNumber: startSegment}, nil
	default:
		return preparedTimelineV3{}, nil
	}
}

// planRequiresServerTransformationsV3 reports whether the plan carries any
// transformation the serving executor (local binary or transcode node) must
// perform, as opposed to client-executed ones.
func planRequiresServerTransformationsV3(plan *playback.PlanV3) bool {
	if plan == nil {
		return false
	}
	for _, transformation := range plan.Transformations {
		if !strings.EqualFold(transformation.Executor, playback.ExecutorClientV3) {
			return true
		}
	}
	return false
}

func (h *PlaybackHandler) prepareIdentityTransportV3(r *http.Request, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, timeline preparedTimelineV3, mode mediaAuthModeV3, policy config.PlaybackRoutingPolicy, excludedShapes map[string]struct{}) (preparedTransportV3, *transportErrorV3) {
	// The shared resolver removes proxy shapes when this client cannot address
	// an authorized origin, then applies the same policy ordering as every HLS
	// path. Legacy and authorized-origin attempts differ only in URL authority.
	decision, routeErr := h.resolveIdentityRouteV3(r, session.ID, result, mode, policy, excludedShapes)
	if routeErr != nil {
		return preparedTransportV3{}, routeErr
	}
	proxyNode := decision.Plan.ProxyNode
	transcodeNode := decision.Plan.TranscodeNode
	releaseReservation := func() {
		if releaser, ok := h.NodePlanner.(sessionReservationReleaserV3); ok {
			releaser.ReleaseSession(session.ID)
		}
	}
	var unlockLifecycle func()
	releaseLifecycle := func() {
		if unlockLifecycle == nil {
			return
		}
		unlockLifecycle()
		unlockLifecycle = nil
	}
	fallbackFromSelectedRoute := func(routeErr *transportErrorV3) (preparedTransportV3, *transportErrorV3) {
		releaseLifecycle()
		releaseReservation()
		fallbackExclusions := maps.Clone(excludedShapes)
		if fallbackExclusions == nil {
			fallbackExclusions = make(map[string]struct{})
		}
		fallbackExclusions[decision.Shape.ID] = struct{}{}
		fallback, fallbackErr := h.prepareIdentityTransportV3(r, session, file, result, timeline, mode, policy, fallbackExclusions)
		if fallbackErr == nil {
			return fallback, nil
		}
		return preparedTransportV3{}, combineTransportErrorsV3(routeErr, fallbackErr)
	}
	if transcodeNode != nil {
		transcodeCapabilities, capabilityErr := h.lookupRemoteCapabilitiesV3(r.Context(), transcodeNode.URL, false)
		if capabilityErr != nil || !slices.Contains(transcodeCapabilities.transportFeatures, playback.TransportFeatureProgressiveRemuxExecutionV1) {
			return fallbackFromSelectedRoute(&transportErrorV3{
				reason: routeCapabilityUnavailableReasonV3, message: "The selected transcode node cannot execute a progressive remux.",
				retryable: true, cause: capabilityErr,
			})
		}
	}
	if transcodeNode != nil && proxyNode != nil {
		proxyCapabilities, capabilityErr := h.lookupRemoteCapabilitiesV3(r.Context(), proxyNode.URL, false)
		if capabilityErr != nil || !slices.Contains(proxyCapabilities.transportFeatures, playback.TransportFeatureProgressiveRemuxRelayV1) {
			return fallbackFromSelectedRoute(&transportErrorV3{
				reason: routeCapabilityUnavailableReasonV3, message: "The selected proxy cannot relay a progressive remux from a transcode node.",
				retryable: true, cause: capabilityErr,
			})
		}
	}
	var executorNode *nodepool.Node
	switch decision.Shape.Execution {
	case noderouting.ExecutionProxy:
		executorNode = proxyNode
	case noderouting.ExecutionTranscode:
		executorNode = transcodeNode
	}
	if executorNode != nil && planRequiresServerTransformationsV3(result.Plan) {
		advertised, capabilityErr := h.remoteTransformationsV3(r.Context(), executorNode.URL)
		if capabilityErr == nil {
			capabilityErr = validateAdvertisedTransformationsV3(result.Plan, advertised)
		}
		if capabilityErr != nil {
			return fallbackFromSelectedRoute(&transportErrorV3{
				reason: routeCapabilityUnavailableReasonV3, message: "The selected node cannot execute the progressive-remux recipe.",
				retryable: true, cause: capabilityErr,
			})
		}
	}

	// A stop and a replacement must agree on the exact moment a progressive
	// authority becomes externally usable. Re-read the session after entering
	// their shared lifecycle boundary: if stop won, fail before publishing the
	// successor recipe or proxy grant; if replacement won, stop waits until its
	// commit or rollback has made that authority match the live session.
	if h.beforeIdentityLifecycleLockV3 != nil {
		h.beforeIdentityLifecycleLockV3()
	}
	identityQueueStart := time.Now()
	unlockLifecycle = h.tm.LockSessionLifecycle(session.ID)
	recordTransportPhaseV3(r.Context(), "transport_queue", identityQueueStart)
	// Accepted start/replan plans always carry their live session ID. Low-level
	// transport tests deliberately omit it so they can exercise route assembly
	// without reconstructing the surrounding request transaction.
	if result.Plan.SessionID != "" {
		if result.Plan.SessionID != session.ID {
			releaseLifecycle()
			releaseReservation()
			return preparedTransportV3{}, &transportErrorV3{
				reason: "internal_error", message: "The playback plan does not belong to the session being prepared.",
			}
		}
		currentSession, err := h.sessionMgr.GetSession(session.ID)
		if err != nil {
			releaseLifecycle()
			releaseReservation()
			return preparedTransportV3{}, &transportErrorV3{
				reason: terminalSessionExpiredV3, message: "The playback session ended before its route could be prepared.", retryable: true, cause: err,
			}
		}
		session = currentSession
	}
	routeSession := *session
	// The URL builders below refuse to mint a stream token for a session that
	// requires media authorization. The live session only learns the mode when
	// its stream state is committed, so stamp the route copy the builders see.
	routeSession.RequireMediaAuthorization = mode.headerAuth
	routeSession.PlayMethod = result.PlayMethod
	routeSession.BasePlayMethod = result.PlayMethod
	routeSession.MediaFileID = result.Plan.EffectiveMediaFileID
	routeSession.AudioTrackIndex = plannedAudioTrackIndexV3(result, session.AudioTrackIndex)
	routeSession.TranscodeAudio = result.TranscodeAudio
	routeSession.TargetAudioCodec = result.TargetAudioCodec
	routeSession.SourceAudioChannels = result.SourceAudioChannels
	routeSession.TargetAudioChannels = result.TargetAudioChannels
	routeSession.TargetAudioBitrateKbps = result.TargetAudioBitrateKbps
	routeSession.RemuxDVMode = remuxDVModeForPlanV3(result.Plan)
	routeSession.RemuxResumeLeadingPictureDrop = result.RemuxResumeLeadingPictureDrop
	// A replan starts without an egress identity. Do not let the previous
	// proxy's row or internal URL leak into an API-served replacement route.
	routeSession.RoutingEgressNodeID = 0
	routeSession.RoutingEgressNodeURL = ""
	routeSession.RoutingNetworkProvider = new(netaccess.PathFromContext(r.Context()).Provider)
	routeSession.RoutingWorkload = string(routingWorkloadV3(result))
	routeSession.RoutingExecution = string(decision.Shape.Execution)
	routeSession.RoutingEgress = string(decision.Shape.Egress)
	routeSession.RoutingExecutionNodeID = 0
	routeSession.RoutingExecutionNodeURL = ""
	if executorNode != nil {
		routeSession.RoutingExecutionNodeID = executorNode.ID
		routeSession.RoutingExecutionNodeURL = executorNode.URL
	}
	if proxyNode != nil {
		routeSession.RoutingEgressNodeID = proxyNode.ID
		routeSession.RoutingEgressNodeURL = proxyNode.URL
	}
	transportID := ""
	routeSession.TranscodeNodeURL = ""
	routeSession.TranscodeTransportID = ""
	if transcodeNode != nil {
		transportID = transportGenerationV3(session.ID, result.Plan.PlanID)
		routeSession.TranscodeNodeURL = transcodeNode.URL
		routeSession.TranscodeTransportID = transportID
	}
	if transcodeNode != nil {
		card := identityRecipeCard(&routeSession)
		card.InputPath = file.FilePath
		card.DVProfile = file.PrimaryDVProfile()
		card.AudioOnly = file.IsAudioOnly()
		if isVirtualPlaybackFile(file) {
			card.VirtualSourceOwnerInstallationID = effectiveVirtualOwner(file.VirtualOwnerInstallationID, routeSession.VirtualSourceOwnerInstallationID)
		}
		if err := h.putRequiredNodeRecipeV3(r.Context(), transportID, card); err != nil {
			h.deleteNodeRecipeV3(r.Context(), transportID)
			authorityErr := &transportErrorV3{
				reason: string(noderouting.OutcomeCapacityUnavailable), message: "The transcode remux authority is temporarily unavailable.", retryable: true, cause: err,
			}
			return fallbackFromSelectedRoute(authorityErr)
		}
	}
	streamURL := fmt.Sprintf("/stream/%s", routeSession.ID)
	servedByProxy := false
	// priorGrant is the egress authority this attempt overwrote, if any. A
	// replan of a session that was already proxy-served must be able to put it
	// back: rolling back to the restored old plan leaves that plan's published
	// proxy URL live, and a deleted grant would 404 it.
	var priorGrant *playback.RecipeCard
	accessPath := netaccess.PathFromContext(r.Context())
	switch {
	case !mode.headerAuth:
		streamURL, servedByProxy = h.identityStreamURLV3(&routeSession, file, proxyNode, accessPath)
	case mode.proxyEgress:
		streamURL, servedByProxy, priorGrant = h.identityGrantStreamURLV3(r.Context(), &routeSession, file, proxyNode, accessPath)
	}
	reservationReleased := false
	if proxyNode != nil && !servedByProxy {
		// A planned proxy that could not be addressed (no signable token, no
		// file record, no writable grant) falls back to the local path, so its
		// reservation must be dropped now rather than pinning that node's budget
		// until it ages out.
		//
		// The fallback is local execution, so it honors the same
		// local-fallback gate the no-origins mode enforces: an authorized-origins
		// remux whose grant could not be written must not quietly spawn the
		// ffmpeg the operator disabled. Start-time escalation cannot cover this
		// case — it was legitimately skipped because the pool does offer a proxy.
		//
		// The pool did offer one, so the policy is satisfiable and only this
		// attempt's authority write failed. That is transient infrastructure, and
		// it is reported as such: the HLS path classifies the identical
		// proxy-authority failure retryable, and a non-retryable answer would
		// make a client give up on a route the next attempt can take.
		reservationReleased = true
		if transcodeNode != nil {
			h.deleteNodeRecipeV3(r.Context(), transportID)
			grantErr := &transportErrorV3{
				reason: string(noderouting.OutcomeCapacityUnavailable), message: "The transcode-to-proxy remux route is temporarily unavailable.", retryable: true,
			}
			return fallbackFromSelectedRoute(grantErr)
		}
		releaseReservation()
		if !identityLocalFallbackAllowedV3(result, policy) || h.validateLocalProgressiveCapabilitiesV3(r.Context(), result) != nil {
			releaseLifecycle()
			return preparedTransportV3{}, &transportErrorV3{
				reason:    string(noderouting.OutcomeCapacityUnavailable),
				message:   "The proxy egress route is temporarily unavailable.",
				retryable: true,
			}
		}
	}

	previousNodeURL := session.TranscodeNodeURL
	previousTransportID := remoteTransportID(session)
	committed := false
	if result.Plan != nil && result.Plan.Delivery == playback.DeliveryRemuxProgressiveV3 {
		if seek := timeline.seekSeconds; seek > 0 {
			streamURL = appendPlaybackQueryV3(streamURL, "seek", strconv.FormatFloat(seek, 'f', -1, 64))
		}
	}
	workload := routingWorkloadV3(result)
	routingExecution := decision.Shape.Execution
	routingEgress := noderouting.EgressAPI
	egressNodeID := 0
	egressNodeURL := ""
	if servedByProxy {
		routingEgress = decision.Shape.Egress
		egressNodeID = proxyNode.ID
		egressNodeURL = proxyNode.URL
	} else {
		transcodeNode = nil
		transportID = ""
		if workload == noderouting.WorkloadRemux {
			routingExecution = noderouting.ExecutionAPI
		} else {
			routingExecution = noderouting.ExecutionNone
		}
	}
	// Only a proxy that actually runs the remux is this route's executor. Direct
	// play executes nothing, so naming the proxy there would report an execution
	// node for a workload with no execution at all — the same distinction
	// jellycompat draws when it records its identity assignment.
	executorID := 0
	executorURL := ""
	if routingExecution == noderouting.ExecutionProxy {
		executorID = egressNodeID
		executorURL = egressNodeURL
	} else if routingExecution == noderouting.ExecutionTranscode && transcodeNode != nil {
		executorID = transcodeNode.ID
		executorURL = transcodeNode.URL
	}
	nodeURL := ""
	if transcodeNode != nil {
		nodeURL = transcodeNode.URL
	}
	adoptSuccessor := func() {
		h.tm.CloseTranscodeSession(session.ID, "")
		if previousNodeURL != "" {
			h.tm.StopRemoteTranscode(previousTransportID, previousNodeURL)
			h.deleteNodeRecipeV3(r.Context(), previousTransportID)
		}
		h.revokeStaleProxyGrantOnCommitV3(r.Context(), session.ID, mode, servedByProxy)
		h.applyRemoteTransportMarkV3(r.Context(), session.ID, servedByProxy)
	}
	rollbackTransport := func(requireCancellation bool) error {
		if committed {
			return nil
		}
		if requireCancellation && nodeURL != "" && transportID != "" {
			if err := h.tm.CancelRemoteTranscode(r.Context(), transportID, nodeURL); err != nil {
				// applySession has already made this the live route. Keep its
				// authority and reservation intact so a later stop can retry the
				// cancellation instead of orphaning an admitted remote stream.
				committed = true
				adoptSuccessor()
				releaseLifecycle()
				return err
			}
		}
		committed = true
		if !requireCancellation && nodeURL != "" && transportID != "" {
			h.tm.StopRemoteTranscode(transportID, nodeURL)
		}
		// The session never reached the client, so a proxy admitted for it
		// must not keep consuming that node's job/bandwidth budget until the
		// reservation ages out — nor keep an egress grant for a transport
		// that was never committed.
		if servedByProxy && !reservationReleased {
			releaseReservation()
			h.restoreProxyGrantV3(r.Context(), session.ID, priorGrant)
		}
		h.deleteNodeRecipeV3(r.Context(), transportID)
		releaseLifecycle()
		return nil
	}
	var sessionRecipe *playback.RecipeCard
	if mode.headerAuth {
		sessionRecipe = sessionRecipeCardV3(identityRecipeCard(&routeSession), session)
	}
	return preparedTransportV3{
		sessionRecipe:      sessionRecipe,
		url:                streamURL,
		nodeURL:            nodeURL,
		transportID:        transportID,
		routingWorkload:    workload,
		routingExecution:   routingExecution,
		routingExecutorID:  executorID,
		routingExecutorURL: executorURL,
		routingEgress:      routingEgress,
		routingEgressID:    egressNodeID,
		routingEgressURL:   egressNodeURL,
		commit: func() *transportErrorV3 {
			if committed {
				return nil
			}
			committed = true
			adoptSuccessor()
			releaseLifecycle()
			return nil
		},
		rollback: func() {
			_ = rollbackTransport(false)
		},
		rollbackRequired: func() error { return rollbackTransport(true) },
	}, nil
}

// Virtual sources resolve through the integrated server and relay before
// remote transcode dispatch, allowing pooled transcode nodes to serve them.
func shouldUsePooledPlaybackNodeV3(file *models.MediaFile) bool {
	return file != nil
}

// planIdentityProxyV3 selects the proxy node that will serve a direct-play or
// progressive-remux session. These deliveries need no transcode node — the
// bytes are either the source file or a single remux pipe — so the planner is
// asked for a proxy alone, exactly as the Jellyfin-compat transport does.
//
// The proxy serves from the grant alone, so the grant has to carry everything
// the API-local path would have read from the session and the file record — the
// media path it opens, and the source facts (Dolby Vision profile, audio-only)
// its remux needs. Omitting either would not fail loudly: the proxy would serve
// a subtly different stream than the plan promised.
//
// The bool reports whether the returned URL is actually a proxy URL, so the
// caller can release the planner reservation when it is not. A grant that
// cannot be written is not fatal: this attempt simply stays on the API origin,
// which is exactly the behavior of a header-authenticated attempt that
// negotiated no origins at all. The third value is the grant this write
// displaced, for the caller's rollback.
//
// path is the client's access path: a proxy with no origin on it is treated
// like no proxy at all, before any grant is written, so the API relays.
func (h *PlaybackHandler) identityGrantStreamURLV3(ctx context.Context, s *playback.Session, file *models.MediaFile, proxyNode *nodepool.Node, path netaccess.Path) (string, bool, *playback.RecipeCard) {
	if proxyNode == nil || file == nil || s == nil {
		return h.playbackStreamURL(s), false, nil
	}
	base := proxyNode.ClientURLFor(path)
	if base == "" {
		return h.playbackStreamURL(s), false, nil
	}
	card := identityRecipeCard(s)
	card.InputPath = file.FilePath
	card.DVProfile = file.PrimaryDVProfile()
	card.AudioOnly = file.IsAudioOnly()
	card.RoutingEgressNodeID = proxyNode.ID
	prior, stored := h.putProxyGrantV3(ctx, s.ID, card)
	if !stored {
		return h.playbackStreamURL(s), false, nil
	}
	return base + "/stream/v3/" + s.ID, true, prior
}

// putProxyGrantV3 stores the recipe a designated proxy origin serves this
// session from, reporting whether the grant is actually retrievable. A replan
// overwrites the previous grant under the same session id, so the grant it
// displaces is returned for the caller to thread into its rollback: a
// replacement that fails to commit restores the old plan, and that plan's
// already-published proxy URL is only serviceable while its grant exists.
//
// The overwrite is deliberately not staged behind the commit. Between this Put
// and the transport commit the previously published client URL resolves the new
// recipe — same session, same user, same media authority, bounded by the replan
// window — which is the accepted cost of keeping one grant per session.
//
// A disabled store is a negative answer rather than a silent success: it
// accepts writes it cannot retrieve (the Redis-less integrated box), and
// publishing a proxy URL against one would hand the client a route that 404s.
func (h *PlaybackHandler) putProxyGrantV3(ctx context.Context, sessionID string, card playback.RecipeCard) (*playback.RecipeCard, bool) {
	if h.ProxyGrantStore == nil || !h.ProxyGrantStore.Enabled() || sessionID == "" {
		return nil, false
	}
	// The proxy grant is this session's egress authority: a durable store
	// round-trip on the start path that the original split did not name. Record
	// it as the authority phase. grantManifestURLV3 stores through here, so its
	// write is covered by this same span rather than wrapped again. A disabled
	// store returns before this, so a no-op never inflates the phase.
	defer recordTransportPhaseV3(ctx, transportPhaseAuthorityV3, time.Now())
	prior, hadPrior := h.ProxyGrantStore.Get(ctx, sessionID)
	if !hadPrior {
		prior = nil
	}
	if err := h.ProxyGrantStore.Put(ctx, sessionID, card); err != nil {
		slog.WarnContext(ctx, "protocol v3 proxy egress grant write failed; serving from the API origin",
			"component", "api", "playback_session_id", sessionID, "error", err)
		return nil, false
	}
	return prior, true
}

// restoreProxyGrantV3 undoes a replan's grant overwrite. A session that was
// already egressing from a proxy keeps serving its restored plan, so its grant
// has to come back rather than be revoked; a session that had none is revoked
// as before, because a grant for a transport that never committed would point a
// proxy at work that no longer exists.
func (h *PlaybackHandler) restoreProxyGrantV3(ctx context.Context, sessionID string, prior *playback.RecipeCard) {
	if h == nil || h.ProxyGrantStore == nil || sessionID == "" {
		return
	}
	if prior == nil {
		h.deleteProxyGrantV3(ctx, sessionID)
		return
	}
	if err := h.ProxyGrantStore.Put(context.WithoutCancel(ctx), sessionID, *prior); err != nil {
		slog.WarnContext(ctx, "failed to restore the previous proxy egress grant",
			"component", "api", "playback_session_id", sessionID, "error", err)
	}
}

// deleteProxyGrantV3 revokes a session's proxy egress authority. It runs
// wherever the session ends or its transport fails to commit: a grant that
// outlived its session would let a proxy keep serving bytes for playback the
// server considers over.
func (h *PlaybackHandler) deleteProxyGrantV3(ctx context.Context, sessionID string) {
	if h == nil || h.ProxyGrantStore == nil || sessionID == "" {
		return
	}
	if err := h.ProxyGrantStore.Delete(context.WithoutCancel(ctx), sessionID); err != nil {
		slog.WarnContext(ctx, "failed to revoke proxy egress grant",
			"component", "api", "playback_session_id", sessionID, "error", err)
	}
}

// putNodeRecipeV3 hands the transcode node the recipe it rebuilds this job from
// after a restart, keyed by the transport id the node serves it under (which is
// the id in every relayed node URL, not the playback session id).
//
// It exists because a header-authenticated attempt publishes no stream token, so
// neither the client nor this server's relay has a recipe to forward when the
// node comes back empty — the node would 404 until the client replanned. A node
// dying mid-stream is a normal event, so tokenless playback recovers from it the
// way a legacy token attempt already does.
//
// Best effort, exactly like the jellycompat handoff: the write is bounded and a
// failure only forfeits restart resilience for this session, never the start.
func (h *PlaybackHandler) putNodeRecipeV3(ctx context.Context, transportID string, card playback.RecipeCard) {
	if h == nil || h.NodeRecipeStore == nil || transportID == "" {
		return
	}
	putCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeRecipeWriteTimeoutV3)
	defer cancel()
	if err := h.NodeRecipeStore.Put(putCtx, transportID, card); err != nil {
		slog.WarnContext(ctx, "persist node transcode recipe failed; this session cannot survive a node restart",
			"component", "api", "playback_session_id", card.SessionID, "transport", transportID,
			"node", card.TranscodeNodeURL, "error", err)
	}
}

// putRequiredNodeRecipeV3 persists the active authority for a progressive
// remux executed by a transcode node. Unlike the reconstruction-only write
// above, this one is part of route admission: the node checks the record before
// every new remux request, and stop deletes it, so a still-valid stream token
// cannot resurrect FFmpeg after a node replacement.
func (h *PlaybackHandler) putRequiredNodeRecipeV3(ctx context.Context, transportID string, card playback.RecipeCard) error {
	if h == nil || h.NodeRecipeStore == nil || !h.NodeRecipeStore.Enabled() || transportID == "" {
		return errors.New("node recipe store unavailable")
	}
	// The required node recipe is route admission, so this durable write is on
	// the start path and uncovered by the original split. Record it as the
	// authority phase. An unavailable store returns before this, so the failed
	// admission is not charged to the phase.
	defer recordTransportPhaseV3(ctx, transportPhaseAuthorityV3, time.Now())
	putCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeRecipeWriteTimeoutV3)
	defer cancel()
	return h.NodeRecipeStore.Put(putCtx, transportID, card)
}

// deleteNodeRecipeV3 drops a transport's stored recipe so a buffered or retrying
// request cannot resurrect a transcode the server has replaced or ended. The
// store's TTL is only the backstop for the paths that never run (a crashed API
// process); every deliberate teardown deletes here.
func (h *PlaybackHandler) deleteNodeRecipeV3(ctx context.Context, transportID string) {
	if h == nil || h.NodeRecipeStore == nil || transportID == "" {
		return
	}
	if err := h.NodeRecipeStore.Delete(context.WithoutCancel(ctx), transportID); err != nil {
		slog.WarnContext(ctx, "failed to drop the node transcode recipe",
			"component", "api", "transport", transportID, "error", err)
	}
}

// deleteRequiredProgressiveRemuxAuthorityV3 makes a user-visible stop durable
// before the session and its retry path disappear. A progressive remux token
// can remain valid for 24 hours, so an unavailable authority store is a failed
// stop, not a best-effort cleanup: the caller must retain the session and let
// the client retry.
func (h *PlaybackHandler) deleteRequiredProgressiveRemuxAuthorityV3(ctx context.Context, session *playback.Session) error {
	if !requiresProgressiveRemuxAuthorityV3(session) {
		return nil
	}
	if h == nil || h.NodeRecipeStore == nil || !h.NodeRecipeStore.Enabled() {
		return errors.New("node recipe store unavailable")
	}
	deleteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeRecipeWriteTimeoutV3)
	defer cancel()
	return h.NodeRecipeStore.Delete(deleteCtx, session.TranscodeTransportID)
}

func requiresProgressiveRemuxAuthorityV3(session *playback.Session) bool {
	return session != nil && session.TranscodeTransportID != "" && session.TranscodeNodeURL != "" &&
		session.RoutingWorkload == string(noderouting.WorkloadRemux) &&
		session.RoutingExecution == string(noderouting.ExecutionTranscode)
}

// resolveIdentityRouteV3 resolves direct and progressive routes through the
// same policy compiler as HLS, including the transcode-execution/proxy-egress
// progressive route.
func (h *PlaybackHandler) resolveIdentityRouteV3(r *http.Request, sessionID string, result playback.PlannerResultV3, mode mediaAuthModeV3, policy config.PlaybackRoutingPolicy, initialExcludedShapes map[string]struct{}) (noderouting.Decision, *transportErrorV3) {
	workload, delivery, ok := routingClassV3(result)
	if !ok {
		return noderouting.Decision{}, &transportErrorV3{reason: string(noderouting.OutcomePolicyUnsatisfied), message: "The playback delivery has no legal node route.", retryable: false}
	}
	proxyAllowed := mode.proxyEgress || (!mode.headerAuth && h.JWTSecret != "")
	excludedShapes := maps.Clone(initialExcludedShapes)
	if excludedShapes == nil {
		excludedShapes = make(map[string]struct{})
	}
	if result.Plan != nil && result.Plan.Delivery == playback.DeliveryRemuxProgressiveV3 &&
		h.validateLocalProgressiveCapabilitiesV3(r.Context(), result) != nil {
		excludedShapes[noderouting.ShapeProgressiveRemuxAPI] = struct{}{}
	}
	if delivery == noderouting.DeliveryProgressiveRemux &&
		(h.NodeRecipeStore == nil || !h.NodeRecipeStore.Enabled()) {
		// A signed stream token can outlive a transcode-node process by 24 hours.
		// Without durable active-transport authority, a stopped remux could be
		// replayed after that node restarts and start FFmpeg again.
		excludedShapes[noderouting.ShapeProgressiveRemuxTranscodeProxy] = struct{}{}
	}
	var relayEligible func(*nodepool.Node) bool
	if delivery == noderouting.DeliveryProgressiveRemux {
		relayEligible, _ = h.proxyTransportFeatureEligibilityV3(r.Context(), playback.TransportFeatureProgressiveRemuxRelayV1)
	}
	// Both proxy predicates are narrowed to proxies the client can actually
	// reach on its access path (see resolveHLSRouteWithPolicyV3). With no
	// reachable proxy the resolver falls through to the API-egress shapes, or
	// reports capacity unavailable under a proxy_only policy.
	accessPath := netaccess.PathFromContext(r.Context())
	decision, err := noderouting.Resolve(noderouting.AdaptSessionPlanner(h.NodePlanner), noderouting.ResolveRequest{
		Request: noderouting.Request{
			Workload: workload, Delivery: delivery,
			Policy: policy, ProxyAllowed: proxyAllowed,
		},
		SessionID: sessionID, EstimatedBitrateKbps: identityStreamBitrateKbpsV3(result),
		TranscodeEligible:      excludeUnreachableRouteNodesV3(r.Context(), h.transcodeEligibilityV3(r.Context(), result, nil)),
		ProxyEligible:          excludeUnreachableRouteNodesV3(r.Context(), nodepool.ClientReachableVia(accessPath, relayEligible)),
		ProxyExecutionEligible: excludeUnreachableRouteNodesV3(r.Context(), nodepool.ClientReachableVia(accessPath, h.identityProxyEligibilityV3(r.Context(), result))),
		ExcludedShapeIDs:       excludedShapes,
		UnreachableNodes:       unreachableRouteNodesV3(r.Context()),
	})
	if err != nil {
		return noderouting.Decision{}, &transportErrorV3{reason: string(noderouting.OutcomePolicyUnsatisfied), message: "The playback routing policy is invalid.", retryable: false, cause: err}
	}
	if !decision.Selected() {
		retryable := decision.Outcome == noderouting.OutcomeCapacityUnavailable
		return noderouting.Decision{}, &transportErrorV3{reason: string(decision.Outcome), message: "No playback route satisfies the configured policy and current node availability.", retryable: retryable}
	}
	return decision, nil
}

// proxyTransportFeatureEligibilityV3 narrows proxy selection to pooled proxies
// that advertise feature, and reports whether some proxy may not. Capabilities
// are read here, before planning, so the planner lock only sees a map lookup.
func (h *PlaybackHandler) proxyTransportFeatureEligibilityV3(ctx context.Context, feature string) (eligible func(*nodepool.Node) bool, someLack bool) {
	enumerator, ok := h.NodePlanner.(proxyNodeEnumeratorV3)
	if !ok {
		return func(*nodepool.Node) bool { return false }, true
	}
	nodeURLs := enumerator.ProxyNodeURLs()
	capable := make(map[string]struct{})
	for nodeURL, entry := range h.pooledNodeCapabilitiesV3(ctx, nodeURLs) {
		if slices.Contains(entry.transportFeatures, feature) {
			capable[nodeURL] = struct{}{}
		}
	}
	return func(node *nodepool.Node) bool {
		if node == nil {
			return false
		}
		_, supported := capable[node.URL]
		return supported
	}, len(capable) < len(nodeURLs)
}

// applyRemoteTransportMarkV3 records whether the committed route's media bytes
// leave this server or another node.
//
// Every committed transition calls it, including the ones that serve locally:
// a session replanned from a proxy onto the integrated transcoder would
// otherwise keep a stale mark, and the widened idle grace it grants would hold
// that session's stream and transcode slots for five minutes after the local
// stream disconnected without an explicit stop.
func (h *PlaybackHandler) applyRemoteTransportMarkV3(ctx context.Context, sessionID string, remote bool) {
	if err := h.sessionMgr.SetRemoteTransport(sessionID, remote); err != nil &&
		!errors.Is(err, playback.ErrSessionNotFound) {
		slog.WarnContext(ctx, "failed to record transport locality",
			"component", "api", "playback_session_id", sessionID, "remote", remote, "error", err)
	}
}

// identityProxyEligibilityV3 narrows proxy selection to proxies that can
// execute the plan's frozen recipe when one is required. A nil predicate means
// any healthy proxy will do.
//
// The narrowing happens before selection rather than after, mirroring how HLS
// filters transcode nodes: rejecting a single round-robin pick would abandon
// the whole pool, so a capable sibling with free capacity would sit unused
// while playback fell back to the API — or was refused outright when policy
// forbids API execution.
//
// Direct play copies bytes and needs no recipe, so it skips the probe entirely
// and any healthy proxy serves it.
func (h *PlaybackHandler) identityProxyEligibilityV3(ctx context.Context, result playback.PlannerResultV3) func(*nodepool.Node) bool {
	enumerator, enumerable := h.NodePlanner.(proxyNodeEnumeratorV3)
	if !enumerable || !planRequiresServerTransformationsV3(result.Plan) {
		return nil
	}

	capable := make(map[string]struct{})
	proxyURLs := enumerator.ProxyNodeURLs()
	for nodeURL, advertised := range h.pooledNodeTransformationsV3(ctx, proxyURLs) {
		if validateAdvertisedTransformationsV3(result.Plan, advertised) == nil {
			capable[nodeURL] = struct{}{}
		}
	}
	if len(capable) == 0 {
		// No proxy is configured at all (single-node deployment, or policy that
		// always serves locally): nothing could advertise the recipe, and the
		// API executes it by design. Warn only when the pool exists but is
		// behind — that is the degraded state worth an operator's attention.
		if len(proxyURLs) == 0 {
			slog.DebugContext(ctx, "protocol v3 no proxy configured; the API executes the planned recipe",
				"component", "api", "delivery", result.Plan.Delivery)
		} else {
			slog.WarnContext(ctx, "protocol v3 no proxy advertises the planned recipe",
				"component", "api", "delivery", result.Plan.Delivery, "proxy_count", len(proxyURLs))
		}
	}
	// The predicate runs under the planner lock: a set lookup only.
	return func(node *nodepool.Node) bool {
		if node == nil {
			return false
		}
		_, ok := capable[node.URL]
		return ok
	}
}

func identityLocalFallbackAllowedV3(result playback.PlannerResultV3, policy config.PlaybackRoutingPolicy) bool {
	workload, _, ok := routingClassV3(result)
	if !ok {
		return false
	}
	if workload == noderouting.WorkloadDirectPlay {
		return policy.DirectPlayEgress != config.PlaybackEgressProxyOnly
	}
	return policy.RemuxExecution != config.PlaybackExecutionWorkerOnly &&
		policy.RemuxEgress != config.PlaybackEgressProxyOnly
}

// plannerInputV3 assembles the planner input for one route decision. The
// escalation below re-plans with the same inputs the original decision used,
// plus the refused route's attempt key. The HLS registries and tone-map
// capabilities are deliberately left unset: planPlaybackWithCapabilitiesV3
// installs its own lazily memoized snapshot, so the inputs can never disagree.
func (h *PlaybackHandler) plannerInputV3(ctx context.Context, req playback.StartRequestV3, requestedFile, effectiveFile *models.MediaFile, audioIndex int, attemptedKeys []string, provenance ProbeProvenance) playback.PlannerInputV3 {
	return playback.PlannerInputV3{
		Request:              req,
		RequestedFile:        requestedFile,
		EffectiveFile:        effectiveFile,
		ServerBitrateCapKbps: serverBitrateCapV3(ctx),
		AudioTrackIndex:      audioIndex,
		Settings:             h.plannerSettingsV3(ctx),
		Registry:             h.transformationRegistryV3(ctx),
		DVRPUStrippable:      h.lazyDVRPUStrippableV3(ctx, effectiveFile),
		Now:                  time.Now(),
		AttemptedKeys:        attemptedKeys,
		AdditionalSubtitles:  h.downloadedSubtitleInventoryV3(ctx, effectiveFile),
		// The escalation rebuilds the same decision from the same source, so the
		// selected source's provenance travels with it. Dropping it here would
		// publish an empty provenance on an escalated plan whose primary plan
		// carried one.
		InventoryProvenance: string(provenance),
	}
}

// escalateRefusedProgressiveRemuxV3 replaces a progressive remux that the
// header-authenticated transport is guaranteed to refuse.
//
// Without authorized media origins that mode bypasses the proxy identity routes
// (a proxy authenticates from the signed URL token this mode exists to remove),
// so any remux left on the API is work with nowhere to run once policy forbids
// API execution — the route resolver turns it into a terminal routing conflict
// that nothing will ever satisfy. remux_execution=worker_only refuses the
// container-only copy exactly as it refuses one carrying server
// transformations, so both escalate: what decides is whether the resolver
// leaves this attempt any progressive route at all, not how heavy the recipe
// is. HLS is the same recipe on a delivery the API can relay from a pooled
// transcode node, so plan it here rather than making the client discover the
// refusal and recover through a replan round trip.
//
// A client that cannot execute an HLS delivery has no such alternative: it gets
// a non-retryable error naming the policy, because retrying is exactly what it
// must not do.
//
// An attempt that negotiated authorized media origins has an executor again —
// a proxy runs the remux from its grant, exactly as it does for a legacy
// attempt — so nothing is escalated while the pool actually offers a proxy.
// With origins negotiated but no proxy configured the refusal is back, and so
// is this escalation.
//
// plannerInput is evaluated only on the escalation path: rebuilding it costs a
// settings resolution and a downloaded-subtitle listing, which the overwhelming
// majority of starts must not pay for a route they never take.
func (h *PlaybackHandler) escalateRefusedProgressiveRemuxV3(ctx context.Context, mode mediaAuthModeV3, plannerInput func() playback.PlannerInputV3, result playback.PlannerResultV3) (playback.PlannerResultV3, *transportErrorV3) {
	if result.Terminal != nil || result.Plan == nil ||
		result.Plan.Delivery != playback.DeliveryRemuxProgressiveV3 ||
		h.progressiveRemuxRouteAvailableV3(ctx, mode) {
		return result, nil
	}
	input := plannerInput()
	outputContextID := input.Request.ClientPlaybackContext.Output.OutputContextID
	next := input
	next.AttemptedKeys = append(append([]string(nil), input.AttemptedKeys...),
		playback.PlanAttemptKeyV3(*result.Plan, outputContextID, nil))
	next.Now = time.Now()
	escalated, capabilityErr := h.planPlaybackWithCapabilitiesV3(ctx, next)
	if capabilityErr != nil {
		// The escalation target depends on a capability lookup that failed, so
		// nothing is known about whether HLS would run. Ask the client to retry
		// rather than report the remux terminal as final.
		return result, &transportErrorV3{
			reason:    capabilityUnavailableReasonV3,
			message:   "Transcode capability validation is temporarily unavailable.",
			retryable: true,
			cause:     capabilityErr,
		}
	}
	if escalated.Terminal != nil || escalated.Plan == nil || escalated.Plan.Delivery == playback.DeliveryRemuxProgressiveV3 {
		reason := ""
		if escalated.Terminal != nil {
			reason = escalated.Terminal.Reason
		}
		slog.WarnContext(ctx, "protocol v3 header-authenticated remux has no executable delivery",
			logComponentKey, playbackLogValueV3,
			"delivery", result.Plan.Delivery,
			"replanned_terminal_reason", reason,
		)
		return result, &transportErrorV3{
			reason:  "local_transcode_disabled",
			message: "This server does not run playback conversions locally, and the client accepts no delivery that a transcode node can serve.",
		}
	}
	slog.InfoContext(ctx, "protocol v3 escalated refused progressive remux",
		logComponentKey, playbackLogValueV3,
		"delivery", escalated.Plan.Delivery,
		"decision_reason", escalated.Plan.DecisionReason,
	)
	return escalated, nil
}

// progressiveRemuxRouteAvailableV3 reports whether policy leaves this attempt
// any progressive-remux route it could actually reach: an API-egress shape the
// API may execute, or a proxy-egress shape whose proxies this client can
// address. It gates the escalation above, so a false answer means the delivery
// is refused for every candidate, not merely that the preferred one is busy.
// A proxy this request found not answering does not count: a replan leaving a
// dead proxy would otherwise keep the remux and fail on route capacity instead
// of moving to HLS on a transcode node.
func (h *PlaybackHandler) progressiveRemuxRouteAvailableV3(ctx context.Context, mode mediaAuthModeV3) bool {
	proxyAllowed := mode.proxyEgress || (!mode.headerAuth && h.JWTSecret != "")
	reachableProxy := false
	if enumerator, ok := h.NodePlanner.(proxyNodeEnumeratorV3); ok {
		reachableProxy = len(withoutUnreachableRouteNodesV3(ctx, enumerator.ProxyNodeURLs())) > 0
	}
	compiled, err := noderouting.Candidates(noderouting.Request{
		Workload: noderouting.WorkloadRemux, Delivery: noderouting.DeliveryProgressiveRemux,
		Policy: h.playbackRoutingPolicyForContextV3(ctx), ProxyAllowed: proxyAllowed,
	})
	if err != nil {
		return false
	}
	for _, shape := range compiled.Candidates {
		if shape.Egress == noderouting.EgressAPI {
			return true
		}
		if mode.headerAuth {
			if reachableProxy && h.proxyEgressOriginsAvailableV3() {
				return true
			}
			continue
		}
		if reachableProxy && h.JWTSecret != "" {
			return true
		}
	}
	return false
}

// identityStreamBitrateKbpsV3 estimates the bitrate a proxy will egress for an
// identity delivery, so bandwidth-capped proxies admit it accurately. The plan's
// effective recipe is authoritative (a remux that downmixes audio egresses less
// than the source); the source descriptor is the fallback.
func identityStreamBitrateKbpsV3(result playback.PlannerResultV3) int {
	if result.Plan == nil {
		return 0
	}
	if effective := result.Plan.EffectiveRecipe.BitrateKbps; effective != nil && *effective > 0 {
		return *effective
	}
	if source := result.Plan.Source.BitrateKbps; source > 0 {
		return source
	}
	return 0
}

// identityStreamURLV3 builds the stream URL for a direct-play or
// progressive-remux session: an absolute proxy URL when one was planned,
// otherwise the API-local path.
//
// The proxy serves from the token alone, so the token has to carry everything
// the API-local path would have read from the session and the file record: the
// media path it opens, and the Dolby Vision profile its remux needs to strip a
// dangling Profile 7 RPU. Omitting either would not fail loudly — the proxy
// would serve a subtly different stream than the plan promised.
//
// The bool reports whether the returned URL is actually a proxy URL, so the
// caller can release the planner reservation when it is not.
//
// A session that requires media authorization never gets a proxy URL: the proxy
// serves from the signed token alone, which is exactly the credential that mode
// keeps out of client-visible URLs. It falls back to the API-local path, whose
// builder omits the token for the same reason.
//
// path is the client's access path; a proxy with no origin on it falls back
// the same way, and the caller releases the reservation.
func (h *PlaybackHandler) identityStreamURLV3(s *playback.Session, file *models.MediaFile, proxyNode *nodepool.Node, path netaccess.Path) (string, bool) {
	if proxyNode == nil || file == nil || (s != nil && s.RequireMediaAuthorization) {
		return h.playbackStreamURL(s), false
	}
	base := proxyNode.ClientURLFor(path)
	if base == "" {
		return h.playbackStreamURL(s), false
	}
	card := identityRecipeCard(s)
	card.InputPath = file.FilePath
	card.RoutingEgressNodeID = proxyNode.ID
	claims := card.ToClaims()
	claims.DVProfile = file.PrimaryDVProfile()
	claims.AudioOnly = file.IsAudioOnly()
	token := h.signStreamClaims(claims)
	if token == "" {
		return h.playbackStreamURL(s), false
	}
	if s.PlayMethod == playback.PlayRemux {
		if claims.PlayMethod == streamtoken.PlayMethodAudioDownmixRemux {
			return base + "/stream/remux/audio-v2/" + token, true
		}
		return base + "/stream/remux/" + token, true
	}
	return base + "/stream/direct/" + token, true
}

// sessionOwnsResumeTimelineV3 reports whether the session's own position is a
// valid resume point for the item it belongs to.
//
// Resume state is keyed on the item (playbackProgressTarget resolves a file to
// its episode or content ID), but every part of a multipart presentation shares
// that key while carrying its own file-local clock. Persisting part 4's
// position would therefore store "12 minutes into the book" as the book's
// resume point. The client that stitches the parts into one timeline is the
// only party that knows the item-absolute position, and it reports that through
// the sync/progress surface instead.
//
// This is derived rather than requested: the mismatch is a property of the
// media, not of the client, so a client that forgot to ask would corrupt resume
// exactly the same way.
func sessionOwnsResumeTimelineV3(file *models.MediaFile) bool {
	return file == nil || file.PresentationPartTotal <= 1
}

// multipartResumeFileV3 maps an item-absolute resume position to the part
// whose local timeline should be planned. It only returns a mapping when all
// ordered parts have positive durations; guessing across incomplete metadata
// would seek to the wrong file, so callers retain the existing safe fallback.
func (h *PlaybackHandler) multipartResumeFileV3(ctx context.Context, file *models.MediaFile, absolute float64, access catalog.AccessFilter) (*models.MediaFile, float64, error) {
	if h == nil || h.FileVersionFetcher == nil || file == nil || file.PresentationPartTotal <= 1 || absolute <= 0 {
		return nil, 0, nil
	}
	parts, err := h.FileVersionFetcher.GetByContentID(ctx, file.ContentID)
	if err != nil {
		return nil, 0, err
	}
	// The requested file has already passed requestAccessFilter. Keep the
	// resume timeline inside that same media folder; a content ID may be shared
	// by copies in several libraries, and an unscoped part lookup could move
	// playback onto a folder the viewer cannot access.
	parts = slices.DeleteFunc(parts, func(part *models.MediaFile) bool {
		return part == nil || part.MediaFolderID != file.MediaFolderID || !catalog.FileAllowedByAccess(part, access)
	})
	if len(parts) != file.PresentationPartTotal {
		return nil, 0, fmt.Errorf("multipart sequence incomplete")
	}
	for _, part := range parts {
		if part.PresentationGroupKey != file.PresentationGroupKey || part.PresentationPartTotal != file.PresentationPartTotal || part.PresentationPartIndex < 1 || part.PresentationPartIndex > file.PresentationPartTotal {
			return nil, 0, fmt.Errorf("multipart sequence inconsistent")
		}
	}
	parts = slices.Clone(parts)
	slices.SortStableFunc(parts, func(a, b *models.MediaFile) int {
		if a == nil && b == nil {
			return 0
		}
		if a == nil {
			return 1
		}
		if b == nil {
			return -1
		}
		if a.PresentationPartIndex != b.PresentationPartIndex {
			return a.PresentationPartIndex - b.PresentationPartIndex
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	for i, part := range parts {
		if part.PresentationPartIndex != i+1 {
			return nil, 0, fmt.Errorf("multipart sequence has missing part")
		}
	}
	var offset float64
	for _, part := range parts {
		if part == nil || part.Duration <= 0 {
			return nil, 0, fmt.Errorf("part duration unavailable")
		}
		end := offset + float64(part.Duration)
		if absolute < end || part == parts[len(parts)-1] {
			local := absolute - offset
			if local < 0 {
				local = 0
			}
			if local > float64(part.Duration) {
				local = float64(part.Duration)
			}
			return part, local, nil
		}
		offset = end
	}
	return nil, 0, nil
}

// committedStartAudioIntentV3 is the durable audio selection intent for one
// committed start: the origin, the resolved preference language, the series
// snapshot and the signature of the committed track.
type committedStartAudioIntentV3 struct {
	origin           string
	preferredLang    string
	seriesSignature  *userstore.AudioTrackSignature
	selectedOverride *userstore.AudioTrackSignature
}

// virtualPlanSelectionOriginForRequestV3 names the audio selection origin
// for a start that never passed through the omitted-selection resolution
// above (decode-rejection rotations, version fallback alternates): explicit
// when the request names a track, auto otherwise.
func virtualPlanSelectionOriginForRequestV3(req playback.StartRequestV3) string {
	if strings.TrimSpace(req.AudioTrackID) != "" || req.AudioTrackIndex != nil || strings.TrimSpace(req.CarriedAudioTrackID) != "" {
		return SelectionOriginExplicit
	}
	return SelectionOriginAuto
}

// intentForCommittedStartV3 snapshots the audio selection intent for the
// committed start. An omitted audio selection resolves through the same
// preference pipeline the catalog menu used (identical inputs: the committed
// audio index, the resolved language and the stored series preference), so
// reconciliation replays exactly the start's decision against the verified
// inventory. An explicit selection is marked explicit with only its committed
// signature: reconciliation may verify that it still exists, never move it.
func intentForCommittedStartV3(ctx context.Context, h *PlaybackHandler, userID int, profileID string, req playback.StartRequestV3, file *models.MediaFile, committedIndex int, decision virtualPlanDecisionV3) (committedStartAudioIntentV3, error) {
	var intent committedStartAudioIntentV3
	if file != nil && committedIndex >= 0 && committedIndex < len(file.AudioTracks) {
		intent.selectedOverride = playback.AudioTrackSignatureFromTrack(file.AudioTracks[committedIndex])
	}
	if strings.TrimSpace(req.AudioTrackID) != "" || req.AudioTrackIndex != nil || strings.TrimSpace(req.CarriedAudioTrackID) != "" {
		intent.origin = SelectionOriginExplicit
		return intent, nil
	}
	if decision.startAudioSelectionOrigin != "" {
		intent.origin = decision.startAudioSelectionOrigin
	} else {
		intent.origin = SelectionOriginAuto
	}
	intent.preferredLang = strings.TrimSpace(decision.preferredAudioLanguage)
	if h == nil || h.StoreProvider == nil {
		return intent, nil
	}
	store, err := h.StoreProvider.ForUser(ctx, userID)
	if err != nil {
		return committedStartAudioIntentV3{}, err
	}
	seriesID := h.resolveSeriesID(ctx, file)
	if seriesID != "" {
		stored, prefErr := store.GetAudioPreference(ctx, profileID, seriesID)
		if prefErr != nil {
			return committedStartAudioIntentV3{}, prefErr
		}
		if stored != nil {
			intent.seriesSignature = stored.TrackSignature
		}
	}
	return intent, nil
}

// preferredAudioTrackIndexV3 answers what an omitted audio track means: the
// language this profile has settled on for this series, this library, this
// device, or generally — the same resolution the catalog performs when it
// publishes `effective_audio_track_index`, so the track a client sees on the
// detail page is the track that plays when it does not ask for one, unless
// playableStartAudioV3 finds one this request plays without conversion.
// saved reports that the viewer's saved series track decided, which plays as
// saved.
//
// The client sends a track identity only when the viewer picked one. Defaulting
// to ordinal zero instead would silently play the first track on the reel.
func (h *PlaybackHandler) preferredAudioTrackIndexV3(ctx context.Context, userID int, profileID, deviceID string, file *models.MediaFile) (index int, saved bool, err error) {
	if file == nil || len(file.AudioTracks) == 0 || h.StoreProvider == nil {
		return 0, false, nil
	}
	store, err := h.StoreProvider.ForUser(ctx, userID)
	if err != nil {
		slog.ErrorContext(ctx, "protocol v3 start: audio preference store lookup failed", "component", "api", "user_id", userID, "error", err)
		return 0, false, err
	}
	seriesID := h.playbackPreferenceKey(ctx, file)
	contentID := seriesID
	var seriesPref *playback.AudioTrackPreference
	if contentID != "" {
		stored, prefErr := store.GetAudioPreference(ctx, profileID, contentID)
		if prefErr != nil {
			slog.ErrorContext(ctx, "protocol v3 start: series audio preference lookup failed", "component", "api", "profile_id", profileID, "series_id", seriesID, "error", prefErr)
			return 0, false, prefErr
		}
		if stored != nil {
			seriesPref = &playback.AudioTrackPreference{AudioTrackIndex: stored.AudioTrackIndex, AudioLanguage: stored.AudioLanguage, TrackSignature: stored.TrackSignature}
		}
	}
	rc := settingsresolve.Context{
		ProfileID:  profileID,
		DeviceID:   deviceID,
		LibraryIDs: []int{file.MediaFolderID},
	}
	if contentID != "" {
		rc.SeriesIDs = []string{contentID}
	}
	preferredLang, err := resolvedPlaybackAudioLanguage(ctx, store, rc)
	if err != nil {
		slog.ErrorContext(ctx, "protocol v3 start: canonical audio preference lookup failed", "component", "api", "profile_id", profileID, "device_id", deviceID, "error", err)
		return 0, false, err
	}
	// The "original language" sentinel resolves to the file's own detected
	// language. When the file carries none, the profile's preference answers,
	// and a second sentinel there falls back to the file again.
	resolveOriginal := func(current string) (string, error) {
		if !playback.IsOriginalLanguagePreference(current) {
			return current, nil
		}
		if lang := h.resolveOriginalLanguage(ctx, file); lang != "" {
			return lang, nil
		}
		lang, lookupErr := resolvedPlaybackAudioLanguage(ctx, store, settingsresolve.Context{ProfileID: profileID})
		if lookupErr != nil {
			slog.ErrorContext(ctx, "protocol v3 start: profile audio preference fallback failed", "component", "api", "profile_id", profileID, "error", lookupErr)
			return "", lookupErr
		}
		if playback.IsOriginalLanguagePreference(lang) {
			lang = h.resolveOriginalLanguage(ctx, file)
		}
		return lang, nil
	}
	preferredLang, err = resolveOriginal(preferredLang)
	if err != nil {
		return 0, false, err
	}
	if seriesPref != nil {
		// The specialized row supplies concrete track identity; canonical
		// settings own the language and its scope precedence.
		seriesPref.AudioLanguage = preferredLang
	}
	// Resolve against the same backfilled language view the catalog picker
	// publishes, so a MULTI/DUAL title whose language lives only in the embedded
	// title cannot select one track in the marker and another in the plan.
	backfilled := *file
	backfilled.AudioTracks = playback.BackfillAudioTrackLanguages(append([]models.AudioTrack(nil), file.AudioTracks...))
	chosen := playback.SelectAudioTrack(backfilled.AudioTracks, preferredLang, seriesPref)
	return normalizeAudioTrackIndex(file, chosen), playback.SavedAudioTrackChosen(file.AudioTracks, seriesPref, chosen), nil
}

// playableStartAudioV3 refines a preference-chosen start track by planning
// file, the version that plays, once per candidate
// (playback.PreferUnconvertedAudioTrackV3): a same-language track this request
// plays unconverted replaces one it would convert. Each plan takes the
// escalation the start applies to a progressive remux with no route. The plan
// for the chosen track alone settles most starts.
func (h *PlaybackHandler) playableStartAudioV3(ctx context.Context, req playback.StartRequestV3, requestedFile, file *models.MediaFile, chosen int, settings playback.PlannerSettingsV3) int {
	if file == nil || len(file.AudioTracks) < 2 {
		return chosen
	}
	registry := h.transformationRegistryV3(ctx)
	dvRPUStrippable := h.lazyDVRPUStrippableV3(ctx, file)
	subtitles := h.downloadedSubtitleInventoryV3(ctx, file)
	return playback.PreferUnconvertedAudioTrackV3(file, chosen, func(index int) playback.StartAudioOutcomeV3 {
		input := playback.PlannerInputV3{
			Request: req, RequestedFile: requestedFile, EffectiveFile: file, ServerBitrateCapKbps: serverBitrateCapV3(ctx),
			AudioTrackIndex: index, Settings: settings, Registry: registry, DVRPUStrippable: dvRPUStrippable,
			Now: time.Now(), AdditionalSubtitles: subtitles,
		}
		result, capabilityErr := h.planPlaybackWithCapabilitiesV3(ctx, input)
		if capabilityErr != nil {
			return playback.StartAudioOutcomeV3{}
		}
		result, escalateErr := h.escalateRefusedProgressiveRemuxV3(ctx, headerAuthenticatedMediaV3(req.ClientFeatures), func() playback.PlannerInputV3 { return input }, result)
		if escalateErr != nil {
			return playback.StartAudioOutcomeV3{}
		}
		return playback.StartAudioOutcomeFromResultV3(result)
	})
}

func subtitleTrackIdentityOfMediaTrackV3(track models.SubtitleTrack) subtitleTrackIdentityV3 {
	return subtitleTrackIdentityV3{
		source:          playback.SubtitleSourceEmbeddedV3,
		language:        strings.ToLower(strings.TrimSpace(track.Language)),
		codec:           strings.ToLower(strings.TrimSpace(track.Codec)),
		forced:          track.Forced,
		hearingImpaired: track.HearingImpaired,
	}
}

func subtitleTrackIdentityOfInventoryV3(item playback.SubtitleInventoryItemV3) subtitleTrackIdentityV3 {
	return subtitleTrackIdentityV3{
		source:          item.Source,
		language:        strings.ToLower(strings.TrimSpace(item.Language)),
		codec:           strings.ToLower(strings.TrimSpace(item.Codec)),
		forced:          item.Forced,
		hearingImpaired: item.HearingImpaired,
	}
}

// carriedSubtitleIndexV3 resolves the published ordinal of the one target
// track whose identity matches want. Duplicate identities are ambiguous and
// report no match: carrying either would silently change the viewer's subtitle.
func carriedSubtitleIndexV3(want subtitleTrackIdentityV3, file *models.MediaFile) (int, bool) {
	if file == nil {
		return 0, false
	}
	matched := -1
	for index, item := range playback.BuildSubtitleInventoryV3(file, nil) {
		if subtitleTrackIdentityOfInventoryV3(item) != want {
			continue
		}
		if matched >= 0 {
			return 0, false
		}
		matched = index
	}
	if matched < 0 {
		return 0, false
	}
	return matched, true
}

func (h *PlaybackHandler) previousAudioTracksV3(ctx context.Context, previous *playback.Session) []models.AudioTrack {
	if previous == nil {
		return nil
	}
	if len(previous.VirtualAudioTracks) > 0 {
		return previous.VirtualAudioTracks
	}
	if h.fileResolver == nil || previous.MediaFileID <= 0 {
		return nil
	}
	prior, err := h.fileResolver.GetByID(ctx, previous.MediaFileID)
	if err != nil || prior == nil {
		return nil
	}
	return prior.AudioTracks
}

func (h *PlaybackHandler) previousSubtitleTracksV3(ctx context.Context, previous *playback.Session) []models.SubtitleTrack {
	if previous == nil {
		return nil
	}
	if len(previous.VirtualSubtitleTracks) > 0 {
		return previous.VirtualSubtitleTracks
	}
	if h.fileResolver == nil || previous.MediaFileID <= 0 {
		return nil
	}
	prior, err := h.fileResolver.GetByID(ctx, previous.MediaFileID)
	if err != nil || prior == nil {
		return nil
	}
	return prior.SubtitleTracks
}

// carryAudioFromSessionV3 carries the previous session's explicit audio pick
// onto the start request by track identity. It reports whether the pick was
// carried and whether the viewer must be told their track is unavailable.
func (h *PlaybackHandler) carryAudioFromSessionV3(ctx context.Context, req *playback.StartRequestV3, previous *playback.Session, file *models.MediaFile) (carried, unavailable bool) {
	if req.AudioTrackIndex != nil || req.AudioTrackID != "" || req.CarriedAudioTrackID != "" ||
		previous.SelectionOrigin != SelectionOriginExplicit || previous.AudioTrackIndex < 0 {
		return false, false
	}
	// Strict remap, not a first-match scan: a duplicated language/codec/layout
	// identity is ambiguous, and carrying either one would silently switch the
	// viewer's stream. Ambiguity reports unavailable below.
	tracks := h.previousAudioTracksV3(ctx, previous)
	if index := remapCarriedAudioIndexV3(tracks, previous.AudioTrackIndex, file); index >= 0 {
		req.AudioTrackIndex = &index
		return true, false
	}
	// The viewer's explicit track does not exist on the release this
	// start binds to. The plan must play something, but it must not
	// pretend the choice was honored.
	return false, true
}

// carrySubtitleFromSessionV3 carries the previous session's subtitle pick onto
// the start request by track identity. It reports whether the viewer must be
// told their track is unavailable.
func (h *PlaybackHandler) carrySubtitleFromSessionV3(ctx context.Context, req *playback.StartRequestV3, previous *playback.Session, file *models.MediaFile) bool {
	if req.SubtitleTrackIndex != nil || req.SubtitleTrackID != "" {
		return false
	}
	if previous.SubtitleTrackIndex < 0 {
		// A transport index of -1 means no embedded subtitle; it conflates
		// subtitles-off with an external/downloaded selection. The durable
		// attempt records the selected combined ordinal, so use its plan when
		// this predecessor has one. Without it the legacy session carries no
		// evidence either way and must not invent a selection.
		if h.PlanStoreV3 == nil || previous.ID == "" {
			return false
		}
		record, err := h.PlanStoreV3.GetAttempt(ctx, previous.ID)
		if err != nil || record == nil || record.UserID != previous.UserID || record.ProfileID != previous.ProfileID ||
			(record.RequestedMediaFileID != file.ID && record.EffectiveMediaFileID != file.ID) {
			return false
		}
		return h.carrySubtitleFromPlanV3(ctx, req, record.CurrentPlan, file)
	}
	tracks := h.previousSubtitleTracksV3(ctx, previous)
	if previous.SubtitleTrackIndex >= len(tracks) {
		return true
	}
	if index, matched := carriedSubtitleIndexV3(subtitleTrackIdentityOfMediaTrackV3(tracks[previous.SubtitleTrackIndex]), file); matched {
		req.SubtitleTrackIndex = &index
		req.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", index)
		return false
	}
	return true
}

// carryAudioFromPlanV3 carries the predecessor plan's explicit audio pick onto
// the start request by track identity.
func carryAudioFromPlanV3(req *playback.StartRequestV3, plan playback.PlanV3, explicit bool, file *models.MediaFile) (carried, unavailable bool) {
	if req.AudioTrackIndex != nil || req.AudioTrackID != "" || req.CarriedAudioTrackID != "" ||
		!explicit || plan.SelectedTracks.Audio == nil || plan.SelectedTracks.Audio.Index == nil {
		return false, false
	}
	previousIndex := *plan.SelectedTracks.Audio.Index
	if index := remapCarriedAudioIndexV3(audioTracksFromInventoryV3(plan.AudioTracks), previousIndex, file); index >= 0 {
		req.AudioTrackIndex = &index
		return true, false
	}
	return false, true
}

// previousSubtitleSelectionIndexV3 returns the predecessor plan's selected
// combined subtitle ordinal. Plans persisted by the planner always carry the
// index; older records fall back to the track ID's ordinal.
func previousSubtitleSelectionIndexV3(plan playback.PlanV3) (int, bool) {
	if selected := plan.SelectedTracks.Subtitle; selected != nil {
		if selected.Index != nil {
			return *selected.Index, true
		}
		if _, kind, ordinal, ok := playback.ParseTrackIDV3(selected.ID); ok && kind == subtitleTrackKindV3 {
			return ordinal, true
		}
	}
	trackID := strings.TrimSpace(plan.Subtitle.TrackID)
	if trackID == "" {
		return 0, false
	}
	_, kind, ordinal, ok := playback.ParseTrackIDV3(trackID)
	if !ok || kind != subtitleTrackKindV3 {
		return 0, false
	}
	return ordinal, true
}

func subtitleInventoryItemAtV3(inventory []playback.SubtitleInventoryItemV3, index int) (playback.SubtitleInventoryItemV3, bool) {
	for _, item := range inventory {
		if item.CombinedIndex == index {
			return item, true
		}
	}
	return playback.SubtitleInventoryItemV3{}, false
}

// carrySubtitleFromPlanV3 carries the predecessor plan's subtitle pick onto the
// start request by track identity. Embedded and external tracks match on the
// target's published inventory; a downloaded selection is re-resolved through
// the durable subtitle rows. It reports whether the viewer must be told their
// track is unavailable.
func (h *PlaybackHandler) carrySubtitleFromPlanV3(ctx context.Context, req *playback.StartRequestV3, plan playback.PlanV3, file *models.MediaFile) bool {
	if req.SubtitleTrackIndex != nil || req.SubtitleTrackID != "" {
		return false
	}
	previousIndex, ok := previousSubtitleSelectionIndexV3(plan)
	if !ok || previousIndex < 0 {
		return false
	}
	item, ok := subtitleInventoryItemAtV3(plan.Subtitle.Inventory, previousIndex)
	if !ok {
		return true
	}
	if item.Source == playback.SubtitleSourceDownloadedV3 {
		return h.carryDownloadedSubtitleFromPlanV3(ctx, req, item, file)
	}
	if index, matched := carriedSubtitleIndexV3(subtitleTrackIdentityOfInventoryV3(item), file); matched {
		req.SubtitleTrackIndex = &index
		req.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", index)
		return false
	}
	return true
}

// carryDownloadedSubtitleFromPlanV3 re-resolves a downloaded selection by its
// durable row id: that id is the only stable name for a downloaded track, and a
// same-language sibling must not be substituted. The target's downloaded rows
// follow its own published inventory, so the matched position is published base
// plus row position. Any failure is reported unavailable rather than carried by
// the combined ordinal.
func (h *PlaybackHandler) carryDownloadedSubtitleFromPlanV3(ctx context.Context, req *playback.StartRequestV3, item playback.SubtitleInventoryItemV3, file *models.MediaFile) bool {
	downloadedID := item.DownloadedSubtitleIDV3()
	if downloadedID <= 0 || h.SubtitleRepo == nil || file == nil {
		return true
	}
	downloaded, err := h.SubtitleRepo.ListDownloadedSubtitles(ctx, file.ID)
	if err != nil {
		return true
	}
	base := len(playback.BuildSubtitleInventoryV3(file, nil))
	matched := -1
	for position, candidate := range downloaded {
		if candidate.ID != downloadedID {
			continue
		}
		if matched >= 0 {
			return true
		}
		matched = base + position
	}
	if matched < 0 {
		return true
	}
	req.SubtitleTrackIndex = &matched
	req.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", matched)
	return false
}
func (h *PlaybackHandler) carrySelectionsFromActiveSessionV3(
	ctx context.Context,
	userID int,
	profileID string,
	file *models.MediaFile,
	req *playback.StartRequestV3,
	clientIP string,
	userAgent string,
) (audioCarried, subtitleUnavailable, audioUnavailable bool) {
	if h == nil || h.sessionMgr == nil || file == nil || req == nil {
		return false, false, false
	}
	if strings.TrimSpace(req.PreviousPlaybackAttemptID) != "" {
		return h.carrySelectionsFromPredecessorAttemptV3(ctx, userID, profileID, file, req)
	}
	lister, ok := h.sessionMgr.(interface {
		GetUserSessions(userID int) []*playback.Session
	})
	if !ok {
		return false, false, false
	}
	clientIP = strings.TrimSpace(clientIP)
	userAgent = strings.TrimSpace(userAgent)
	previous := findCarryCandidateSessionV3(lister.GetUserSessions(userID), profileID, file.ID, clientIP, userAgent)
	if previous == nil {
		return false, false, false
	}

	audioCarried, audioUnavailable = h.carryAudioFromSessionV3(ctx, req, previous, file)

	if h.carrySubtitleFromSessionV3(ctx, req, previous, file) {
		subtitleUnavailable = true
	}
	return audioCarried, subtitleUnavailable, audioUnavailable
}

// carrySelectionsFromPredecessorAttemptV3 resolves the named predecessor attempt
// from the durable store and carries its explicit selection by identity. The
// store lookup is what makes this work across replicas and after the predecessor
// session stopped; it also keeps two clients behind one NAT from inheriting
// each other's choices because nothing else is used to identify the predecessor.
func (h *PlaybackHandler) carrySelectionsFromPredecessorAttemptV3(
	ctx context.Context,
	userID int,
	profileID string,
	file *models.MediaFile,
	req *playback.StartRequestV3,
) (audioCarried, subtitleUnavailable, audioUnavailable bool) {
	if h == nil || h.PlanStoreV3 == nil || file == nil || req == nil {
		return false, false, false
	}
	predecessorID := strings.TrimSpace(req.PreviousPlaybackAttemptID)
	if predecessorID == "" {
		return false, false, false
	}
	record, err := h.PlanStoreV3.GetAttemptByPlaybackAttemptID(ctx, predecessorID)
	if err != nil || record == nil {
		return false, false, false
	}
	if record.UserID != userID || record.ProfileID != profileID {
		return false, false, false
	}
	if record.RequestedMediaFileID != file.ID && record.EffectiveMediaFileID != file.ID {
		return false, false, false
	}

	plan := record.CurrentPlan
	audioCarried, audioUnavailable = carryAudioFromPlanV3(req, plan, record.SelectionOrigin == SelectionOriginExplicit, file)

	if h.carrySubtitleFromPlanV3(ctx, req, plan, file) {
		subtitleUnavailable = true
	}
	return audioCarried, subtitleUnavailable, audioUnavailable
}

// resumePositionV3 answers what an omitted `start_position` means: resume where
// this profile left off. It runs before planning rather than after session
// creation because the plan's timeline is cut at the start position — a route
// chosen for zero and then seeked to 40 minutes is a different route.
//
// A client that wants to start over sends an explicit `start_position: 0`; only
// omission asks the server for its resume policy. Multipart progress is stored
// as one item-level position and is translated to a part-local seek only when
// the complete ordered part timeline is available.

func (h *PlaybackHandler) resumePositionV3(ctx context.Context, userID int, profileID string, file *models.MediaFile) (*float64, error) {
	if h.StoreProvider == nil {
		return nil, nil
	}
	targetID, err := h.playbackProgressTarget(ctx, file)
	if err != nil {
		return nil, err
	}
	if targetID == "" {
		return nil, nil
	}
	store, err := h.StoreProvider.ForUser(ctx, userID)
	if err != nil {
		slog.ErrorContext(ctx, "protocol v3 start: resume store lookup failed", "component", "api", "user_id", userID, "error", err)
		return nil, err
	}
	progress, err := store.GetProgress(ctx, profileID, targetID)
	if err != nil {
		slog.ErrorContext(ctx, "protocol v3 start: resume progress lookup failed", "component", "api", "target", targetID, "error", err)
		return nil, err
	}
	if progress == nil || progress.Completed || progress.PositionSeconds <= 0 {
		return nil, nil
	}
	position := progress.PositionSeconds
	return &position, nil
}

// A copy remux starts at the preceding keyframe selected by the demuxer, not
// necessarily at the requested source position. Its player clock therefore
// begins at the resolved stream origin and advances through the copied pre-roll
// before reaching the requested position. Neither progressive nor growing HLS
// copy transports can seek arbitrarily inside their current response.
//
// The pre-roll is deliberate, including the up-to-one-GOP rewind a non-seek
// replan such as an audio switch shows on a rebuilt progressive transport. Do
// not clamp the origin to the requested position to hide it. See
// docs/architecture/playback-protocol-v3.md#copy-video-seek-anchor.
func configureCopyRemuxTimelineV3(plan *playback.PlanV3, origin float64) {
	if plan == nil {
		return
	}
	plan.Timeline.PlayerStartSeconds = max(0, plan.Timeline.SourceStartSeconds-origin)
	plan.Timeline.StreamOriginSeconds = origin
	plan.Timeline.TimelineOffsetSeconds = origin
	plan.Timeline.SeekWindowStartSeconds = &origin
	plan.Timeline.SeekWindowEndSeconds = nil
	plan.Timeline.CanSeekAnywhere = false
	plan.Timeline.SeekRestoration = "source_position"
}

// configureCopyVODTimelineV3 publishes a planned copy session's complete
// playlist as a seekable source-timeline window. The player clock starts at
// the playlist's own origin (the first copied keyframe, normally zero), and
// both ends of the window are known, so a client seeks natively by requesting
// the target segment. The segment layer restarts FFmpeg in place at that
// segment's planned boundary when it has not been produced yet; the transport
// and the playlist stay exactly as they are.
func configureCopyVODTimelineV3(plan *playback.PlanV3, vod playback.CopyVODPlan) {
	if plan == nil || vod.Duration <= 0 {
		return
	}
	origin := max(0, vod.Origin)
	end := origin + vod.Duration
	requested := plan.Timeline.SourceStartSeconds
	if requested < origin {
		requested = origin
	}
	if requested > end {
		requested = end
	}
	windowStart := origin
	windowEnd := end
	plan.Timeline.PlayerStartSeconds = max(0, requested-origin)
	plan.Timeline.StreamOriginSeconds = origin
	plan.Timeline.TimelineOffsetSeconds = origin
	plan.Timeline.SeekWindowStartSeconds = &windowStart
	plan.Timeline.SeekWindowEndSeconds = &windowEnd
	plan.Timeline.CanSeekAnywhere = true
	plan.Timeline.SeekRestoration = "source_position"
}

func appendPlaybackQueryV3(rawURL, key, value string) string {
	separator := "?"
	if strings.ContainsRune(rawURL, '?') {
		separator = "&"
	}
	return rawURL + separator + key + "=" + value
}

// softwareToneMapRetryOptsV3 returns the software executor for a one-shot
// hardware fallback when the recipe is allowed to adapt to live capabilities.
func (h *PlaybackHandler) softwareToneMapRetryOptsV3(ctx context.Context, opts playback.TranscodeOpts, frozenSourceMetadata bool) (playback.TranscodeOpts, bool) {
	// The software executor forces HWAccel=none, so it decodes and encodes on
	// the CPU. gpu_only forbids that fallback even when the frozen tone-map
	// policy would otherwise permit it.
	if !h.softwareFallbackAllowedV3() {
		return opts, false
	}
	if frozenSourceMetadata || opts.ToneMapMode != tonemap.ModeHardware ||
		!opts.ToneMapPolicy.Allows(tonemap.ModeSoftware) {
		return opts, false
	}
	capabilities, err := h.localToneMapCapabilitiesForTransportV3(ctx)
	if err != nil {
		return opts, false
	}
	if !capabilities.Supports(tonemap.ModeSoftware, opts.ToneMapSourceKind) {
		return opts, false
	}
	opts.ToneMapMode = tonemap.ModeSoftware
	opts.ToneMapFilter = capabilities.FilterFor(tonemap.ModeSoftware, opts.ToneMapSourceKind)
	opts.HWAccel = playback.HWAccelNone
	return opts, true
}

type localTransportStartupFailureV3 struct {
	cause         error
	failedToStart bool
	wasRunning    bool
	failedDevice  string
}

// startReadyLocalPlaybackTransportV3 returns only after the first manifest is
// safe to serve. A session that fails readiness is closed before the failure is
// returned so every caller gets the same startup cleanup behavior.
func (h *PlaybackHandler) startReadyLocalPlaybackTransportV3(ctx context.Context, opts playback.TranscodeOpts) (*playback.TranscodeSession, *localTransportStartupFailureV3) {
	startedAt := time.Now()
	// Spawn is recorded net of the virtual upstream reopen and the virtual
	// transport's inline manifest wait, both of which the launch performs
	// before it returns: resolving the session-bound provider URL to a relay
	// registration is the "upstream reopen" phase, and awaiting the first
	// manifest is the "manifest" phase, so counting either twice would make the
	// phases overlap. For a local file both are no-ops and spawn keeps the
	// whole span.
	upstreamBefore := transportPhaseDurationV3(ctx, "transport_upstream")
	manifestBefore := transportPhaseDurationV3(ctx, transportPhaseManifestV3)
	ts, err := h.startLocalPlaybackTransport(ctx, opts)
	spawnFinishedAt := time.Now()
	addTransportPhaseDurationV3(ctx, "transport_spawn",
		spawnFinishedAt.Sub(startedAt)-
			(transportPhaseDurationV3(ctx, "transport_upstream")-upstreamBefore)-
			(transportPhaseDurationV3(ctx, transportPhaseManifestV3)-manifestBefore))
	if err != nil {
		slog.InfoContext(ctx, "playback transport startup timing",
			logComponentKey, playbackLogValueV3,
			requestIDLogKeyV3, chimw.GetReqID(ctx),
			"transport", "local",
			"session", opts.SessionID,
			"spawn_ms", spawnFinishedAt.Sub(startedAt).Milliseconds(),
			"manifest_wait_ms", int64(0),
			"total_ms", time.Since(startedAt).Milliseconds(),
			"outcome", "spawn_failed",
		)
		// A revoked generation surfaces its decode verdict through the
		// transport start: it is a readiness outcome (the process ran and
		// rejected the bytes), not a spawn failure, so the rotation loop
		// must see it as such instead of a generic transcode_start_failed.
		if errors.Is(err, playback.ErrSourceDecodeRejected) {
			return nil, &localTransportStartupFailureV3{cause: err}
		}
		return nil, &localTransportStartupFailureV3{cause: err, failedToStart: true}
	}
	// The local first-manifest wait is this request's readiness phase. Report
	// how many poll iterations it ran too: the duration says how long the
	// client waited, and the count separates a manifest that appeared promptly
	// from one that rode the deadline out. The count is additive log
	// observation; the wait and its result are unchanged.
	readinessWait := virtualStartReadinessWait(ctx, virtualStartReadinessBudget)
	_, manifestPolls, manifestErr := ts.WaitForManifestContextPolls(ctx, readinessWait)
	if manifestErr != nil {
		recordTransportPhaseV3(ctx, "transport_readiness", spawnFinishedAt)
		slog.InfoContext(ctx, "playback transport startup timing",
			logComponentKey, playbackLogValueV3,
			requestIDLogKeyV3, chimw.GetReqID(ctx),
			"transport", "local",
			"session", opts.SessionID,
			"spawn_ms", spawnFinishedAt.Sub(startedAt).Milliseconds(),
			"manifest_wait_ms", time.Since(spawnFinishedAt).Milliseconds(),
			"manifest_polls", manifestPolls,
			"readiness_budget_ms", readinessWait.Milliseconds(),
			"total_ms", time.Since(startedAt).Milliseconds(),
			"outcome", "readiness_failed",
		)
		failure := &localTransportStartupFailureV3{
			cause:        manifestErr,
			wasRunning:   ts.IsRunning(),
			failedDevice: ts.Opts().HWDevice,
		}
		_ = ts.Close()
		return nil, failure
	}
	recordTransportPhaseV3(ctx, "transport_readiness", spawnFinishedAt)
	slog.InfoContext(ctx, "playback transport startup timing",
		logComponentKey, playbackLogValueV3,
		requestIDLogKeyV3, chimw.GetReqID(ctx),
		"transport", "local",
		"session", opts.SessionID,
		"spawn_ms", spawnFinishedAt.Sub(startedAt).Milliseconds(),
		"manifest_wait_ms", time.Since(spawnFinishedAt).Milliseconds(),
		"manifest_polls", manifestPolls,
		"readiness_budget_ms", readinessWait.Milliseconds(),
		"total_ms", time.Since(startedAt).Milliseconds(),
		"outcome", transportStartupReadyV3,
	)
	return ts, nil
}

// newAutoTranscodePipelineV3 prepares the hw_accel=auto fallback order for a
// local start. The pipeline is disabled for every other request.
func (h *PlaybackHandler) newAutoTranscodePipelineV3(ctx context.Context, opts playback.TranscodeOpts) *playback.AutoTranscodePipeline {
	if h.autoTranscodePipelineV3 != nil {
		return h.autoTranscodePipelineV3(ctx, opts)
	}
	return playback.NewAutoTranscodePipeline(ctx, opts)
}

// localAutoStartupPerPathV3 bounds one hw_accel=auto execution path's wait for
// its first manifest. A var so tests can shrink the walk.
var localAutoStartupPerPathV3 = playback.AutoTranscodeStartupPerPathTimeout

// localAutoStartupBudgetV3 bounds a local hw_accel=auto readiness-gated start.
// The remote path budgets TranscodeStartReadyMaxDuration + 5s for the same
// fallback; the local path runs runTranscodeStartup in-process, where each of
// the MaxAutoTranscodeStartupAttempts paths may wait its own
// localAutoStartupPerPathV3. The walk therefore fits inside one first-manifest
// window instead of needing three of them: at the old per-path
// ManifestStartupTimeout it totalled 95 s, which no start phase could admit.
// It is a var so tests can shrink it.
var localAutoStartupBudgetV3 = time.Duration(playback.MaxAutoTranscodeStartupAttempts)*localAutoStartupPerPathV3 + 5*time.Second

// addTransportReadinessNetOfWalkV3 records an enabled hw_accel=auto walk's
// readiness as its wall time net of the spans already named during the walk:
// FFmpeg spawn, the upstream reopen, and any inline first-manifest wait. Each
// delta is the phase total after the walk minus its total before, so a
// multi-attempt walk and a reused recorder both net correctly. The result floors
// at zero in addTransportPhaseDurationV3.
func addTransportReadinessNetOfWalkV3(ctx context.Context, elapsed, spawn, upstream, manifest time.Duration) {
	addTransportPhaseDurationV3(ctx, "transport_readiness", elapsed-spawn-upstream-manifest)
}

// startReadyAutoLocalPlaybackTransportV3 walks an enabled hw_accel=auto
// pipeline until one path produces its first manifest. Failed attempts are
// closed by the pipeline loop, so the failure carries the same cleanup
// guarantee as startReadyLocalPlaybackTransportV3.
//
// The whole walk is bounded by one explicit budget (maxAttempts × per-attempt
// timeout + margin), mirroring remotePlaybackTransportTimeout on the remote
// path, and each path waits only its own per-path timeout so one stalled path
// cannot spend the walk. Without both the walk runs under the bare request
// context, so 3 × 30s of fallback could outlive the handler's own deadline and
// surface as a canceled request instead of a classified readiness failure.
func (h *PlaybackHandler) startReadyAutoLocalPlaybackTransportV3(ctx context.Context, pipeline *playback.AutoTranscodePipeline) (*playback.TranscodeSession, *localTransportStartupFailureV3) {
	startedAt := time.Now()
	attempts := 0
	spawnBefore := transportPhaseDurationV3(ctx, "transport_spawn")
	upstreamBefore := transportPhaseDurationV3(ctx, "transport_upstream")
	manifestBefore := transportPhaseDurationV3(ctx, transportPhaseManifestV3)
	walkBudget := virtualStartReadinessWait(ctx, localAutoStartupBudgetV3)
	ts, err := playback.StartReadyTranscode(ctx, pipeline, playback.TranscodeStartup{
		Timeout: localAutoStartupPerPathV3,
		Budget:  walkBudget,
		Start: func(ctx context.Context, opts playback.TranscodeOpts) (*playback.TranscodeSession, error) {
			attempts++
			upstreamBefore := transportPhaseDurationV3(ctx, "transport_upstream")
			manifestBefore := transportPhaseDurationV3(ctx, transportPhaseManifestV3)
			spawnStart := time.Now()
			session, startErr := h.startLocalPlaybackTransport(ctx, opts)
			upstreamDelta := transportPhaseDurationV3(ctx, "transport_upstream") - upstreamBefore
			manifestDelta := transportPhaseDurationV3(ctx, transportPhaseManifestV3) - manifestBefore
			addTransportPhaseDurationV3(ctx, "transport_spawn", time.Since(spawnStart)-upstreamDelta-manifestDelta)
			return session, startErr
		},
	})
	// The auto walk bounds spawn and per-attempt manifest readiness together.
	// Readiness is the walk's wall time net of every span already named during
	// it: spawn, the upstream reopen, and the virtual transport's inline
	// first-manifest wait. Spawn is recorded net of the last two, so subtracting
	// only spawn would count the manifest wait in both transport_manifest and
	// transport_readiness.
	addTransportReadinessNetOfWalkV3(ctx, time.Since(startedAt),
		transportPhaseDurationV3(ctx, "transport_spawn")-spawnBefore,
		transportPhaseDurationV3(ctx, "transport_upstream")-upstreamBefore,
		transportPhaseDurationV3(ctx, transportPhaseManifestV3)-manifestBefore)
	outcome := transportStartupReadyV3
	var failure *localTransportStartupFailureV3
	var startupErr *playback.TranscodeStartupError
	switch {
	case err == nil:
	case errors.As(err, &startupErr):
		outcome = "readiness_failed"
		failure = &localTransportStartupFailureV3{
			cause:        startupErr.Err,
			wasRunning:   startupErr.WasRunning,
			failedDevice: startupErr.FailedDevice,
		}
	case errors.Is(err, playback.ErrSourceDecodeRejected):
		// Same revoked-generation verdict as the non-auto path below: a
		// readiness outcome for rotation, never a spawn failure.
		outcome = "readiness_failed"
		failure = &localTransportStartupFailureV3{cause: err}
	default:
		outcome = "spawn_failed"
		failure = &localTransportStartupFailureV3{cause: err, failedToStart: true}
	}
	slog.InfoContext(ctx, "playback transport startup timing",
		logComponentKey, playbackLogValueV3,
		requestIDLogKeyV3, chimw.GetReqID(ctx),
		"transport", "local",
		"session", pipeline.Current().SessionID,
		"attempts", attempts,
		"readiness_budget_ms", walkBudget.Milliseconds(),
		"total_ms", time.Since(startedAt).Milliseconds(),
		"outcome", outcome,
	)
	return ts, failure
}

// prepareLocalTransportV3 starts a local HLS generation for the selected plan.
func (h *PlaybackHandler) prepareLocalTransportV3(r *http.Request, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, timeline preparedTimelineV3, mode mediaAuthModeV3) (preparedTransportV3, *transportErrorV3) {
	// A cross-release fallback can only carry the requested audio by identity;
	// when it cannot, the transport records the degradation on the plan it is
	// preparing. Attach the plan so the start path can reach it.
	r = r.WithContext(withCrossReleaseAudioPlanV3(r.Context(), result.Plan))
	cfg := h.playbackConfig()
	if err := os.MkdirAll(cfg.TranscodeDir, 0o755); err != nil {
		return preparedTransportV3{}, &transportErrorV3{reason: "internal_error", message: "Failed to prepare the transcode directory.", cause: err}
	}
	outputSubdir := transportGenerationV3(session.ID, result.Plan.PlanID)
	outputDir := filepath.Join(cfg.TranscodeDir, outputSubdir)
	videoCodec := result.TargetVideoCodec
	if result.Plan.Delivery == playback.DeliveryRemuxHLSV3 {
		videoCodec = "copy"
	}
	sourceMetadata := sourceExecutionMetadataV3(file, result)
	sourceProfile, sourceBitDepth := sourceVideoTranscodeFactsV3(file, result)
	// Queue time is the wait for this session's lifecycle slot. It serializes
	// every path that spawns FFmpeg into one output directory, so measuring it
	// apart from spawn and readiness shows a start queued behind another writer
	// instead of an unexplained delay.
	unlock := h.tm.LockSessionLifecycle(session.ID)
	opts := playback.TranscodeOpts{InputPath: file.FilePath, MediaFileID: file.ID, OutputDir: outputDir, OutputSubdir: outputSubdir, SessionID: session.ID, SourceVideoCodec: sourceMetadata.VideoCodec, SourceVideoProfile: sourceProfile, SourceVideoBitDepth: sourceBitDepth, SourceAudioChannels: result.SourceAudioChannels, SoftwareVideoDecode: sourceMetadata.SoftwareVideoDecode, ToneMapPolicy: result.ToneMapPolicy, ToneMapMode: result.ToneMapMode, ToneMapSourceKind: result.ToneMapSourceKind, ToneMapRecipeVersion: result.ToneMapRecipeVersion, ToneMapPreflightRequired: result.ToneMapPreflightRequired, ToneMapSourceRevision: result.ToneMapSourceRevision, VideoBitstreamFilter: videoBitstreamFilterForPlanV3(result.Plan), VideoSampleEntry: videoSampleEntryForPlanV3(result.Plan), VideoRange: copyHLSVideoRangeV3(file, result, videoCodec), SeekSeconds: timeline.seekSeconds, StreamOriginSeconds: timeline.streamOriginSeconds, CopySeekAnchorResolved: timeline.copySeekAnchorResolved, StartSegmentNumber: timeline.startSegmentNumber, TargetResolution: result.TargetResolution, TargetCodecVideo: videoCodec, TargetCodecAudio: result.TargetAudioCodec, TargetAudioChannels: result.TargetAudioChannels, TargetAudioBitrateKbps: result.TargetAudioBitrateKbps, TargetBitrateKbps: result.TargetBitrateKbps, SegmentDuration: playback.DefaultSegmentDuration, KeyframePlaylist: cfg.KeyframePlaylist && videoCodec == "copy", SegmentRetentionSeconds: cfg.SegmentRetentionSeconds, FFmpegPath: cfg.FFmpegPath, HWAccel: cfg.HWAccel, HWDevice: cfg.HWDevice, AudioTrackIndex: plannedAudioStreamOrdinalV3(file, result, session.AudioTrackIndex), SubtitleTrackIndex: result.SubtitleTransportTrackIndex, SubtitleBurnIn: result.SubtitleBurnIn, SubtitleCodec: result.SubtitleCodec, SubtitleRendition: result.SubtitleRendition, SubtitleRenditionTracks: result.SubtitleRenditionTracks, SubtitleRenditionSource: result.SubtitleRenditionSource, TotalDuration: sourceMetadata.DurationSeconds, FastStart: true, NodeType: playbackNodeIntegratedV3, ExecutionMode: playbackNodeIntegratedV3, FFmpegLogSink: h.FFmpegLogSink}
	opts.CopyVideoMPEGTS = playback.PlanCopiesVideoAsMPEGTSV3(*result.Plan)
	if opts.ToneMapMode != "" {
		opts.ToneMapDVConfigPresent = sourceMetadata.ToneMapDVConfigPresent
		opts.ToneMapDVBLCompatIDPresent = sourceMetadata.ToneMapDVBLCompatIDPresent
		opts.ToneMapDVBLPresent = sourceMetadata.ToneMapDVBLPresent
		opts.ToneMapDVRPUPresent = sourceMetadata.ToneMapDVRPUPresent
		capabilities, capabilityErr := h.localToneMapCapabilitiesForTransportV3(r.Context())
		if capabilityErr != nil {
			unlock()
			return preparedTransportV3{}, &transportErrorV3{reason: transcodeStartFailedReasonV3, message: "Local tone-map capability validation is temporarily unavailable.", retryable: true, cause: capabilityErr}
		}
		opts.ToneMapFilter = capabilities.FilterFor(opts.ToneMapMode, opts.ToneMapSourceKind)
		if opts.ToneMapMode == tonemap.ModeHardware {
			opts.HWAccel = capabilities.BackendFor(opts.ToneMapMode, opts.ToneMapSourceKind)
		} else {
			opts.HWAccel = playback.HWAccelNone
		}
		// The opt-in VPP tone map replaces the OpenCL recipe only on QSV.
		// Every other backend ignores the setting and keeps its validated chain.
		opts.ToneMapFilter = playback.ResolveToneMapFilterV3(opts.ToneMapFilter, opts.ToneMapMode, opts.HWAccel, result.ToneMapVPPEnabled)
	}
	opts.HLSVariableSubstitution = mode.hlsVariableSubstitution
	usedToneMapFallback := false
	var ts *playback.TranscodeSession
	var startupFailure *localTransportStartupFailureV3
	if autoPipeline := h.newAutoTranscodePipelineV3(r.Context(), opts); autoPipeline.Enabled() {
		// hw_accel=auto walks GPU decode and encode, CPU decode with GPU
		// encode, then software, so the legacy retry below never applies. An
		// enabled pipeline never carries a tone-map recipe.
		ts, startupFailure = h.startReadyAutoLocalPlaybackTransportV3(r.Context(), autoPipeline)
		if startupFailure != nil {
			unlock()
			if startupFailure.failedToStart {
				return preparedTransportV3{}, transportStartFailureV3(startupFailure.cause,
					toneMapExecutionTransportErrorV3(startupFailure.cause, "Failed to start the playback transport."))
			}
			return preparedTransportV3{}, manifestStartupTransportErrorV3(startupFailure.wasRunning, startupFailure.cause)
		}
	} else {
		ts, startupFailure = h.startReadyLocalPlaybackTransportV3(r.Context(), opts)
	}
	if startupFailure != nil && !startupFailure.failedToStart && r.Context().Err() != nil {
		// The client left while FFmpeg worked toward its first manifest. A
		// retry would run for nobody while holding the session lifecycle
		// lock, and the failure says nothing about the device.
		unlock()
		return preparedTransportV3{}, manifestStartupTransportErrorV3(startupFailure.wasRunning, startupFailure.cause)
	}
	if startupFailure != nil && startupFailure.failedToStart {
		if softwareOpts, eligible := h.softwareToneMapRetryOptsV3(r.Context(), opts, result.FrozenSourceMetadata != nil); eligible {
			slog.WarnContext(r.Context(), "hardware tone-map failed to start; retrying once in software",
				logComponentKey, playbackLogValueV3, "playback_session_id", session.ID, "error", startupFailure.cause)
			opts = softwareOpts
			usedToneMapFallback = true
			ts, startupFailure = h.startReadyLocalPlaybackTransportV3(r.Context(), opts)
		}
	}
	if startupFailure != nil && startupFailure.failedToStart {
		unlock()
		fallback := &transportErrorV3{reason: transcodeStartFailedReasonV3, message: "Failed to start the playback transport.", retryable: false, cause: startupFailure.cause}
		if opts.ToneMapMode != "" {
			fallback = toneMapExecutionTransportErrorV3(startupFailure.cause, "Failed to start the playback transport.")
		}
		// A provider-outage classify outranks the tone-map/transcode reason: the
		// transport never started because the release could not be re-resolved,
		// so the client should retry the release, not rebuild the recipe.
		return preparedTransportV3{}, transportStartFailureV3(startupFailure.cause, fallback)
	}
	if startupFailure != nil {
		transportErr := localTransportReadinessErrorV3(opts, startupFailure.wasRunning, startupFailure.cause)
		if usedToneMapFallback {
			unlock()
			return preparedTransportV3{}, transportErr
		}

		if errors.Is(startupFailure.cause, playback.ErrSourceDecodeRejected) {
			// A confirmed decoder rejection is terminal for this candidate:
			// rebuilding the same undecodable bytes cannot help, and the
			// candidate rotation loop owns the substitution. Return the typed
			// readiness outcome before the generic one-clean-generation retry
			// below can start an extra transcode on the rejected release.
			unlock()
			return preparedTransportV3{}, localTransportReadinessErrorV3(opts, startupFailure.wasRunning, startupFailure.cause)
		}
		if fallbackOpts, eligible := h.softwareToneMapRetryOptsV3(r.Context(), opts, result.FrozenSourceMetadata != nil); eligible {
			slog.WarnContext(r.Context(), "hardware tone-map failed during startup; retrying once in software",
				logComponentKey, playbackLogValueV3,
				"playback_session_id", session.ID,
				"failed_device", startupFailure.failedDevice,
				"error", startupFailure.cause)
			opts = fallbackOpts
			ts, startupFailure = h.startReadyLocalPlaybackTransportV3(r.Context(), opts)
			if startupFailure != nil && startupFailure.failedToStart {
				unlock()
				return preparedTransportV3{}, toneMapExecutionTransportErrorV3(startupFailure.cause, "Failed to start the software tone-map fallback.")
			}
			if startupFailure != nil {
				unlock()
				return preparedTransportV3{}, localTransportReadinessErrorV3(opts, startupFailure.wasRunning, startupFailure.cause)
			}
		} else if startupFailure.wasRunning {
			unlock()
			return preparedTransportV3{}, transportErr
		} else {
			// FFmpeg and GPU drivers can fail before producing their first segment
			// even though the recipe is valid. Retry one clean generation, preferring
			// another configured render device so a transient device failure does not
			// become an immediate client-visible transport error. gpu_only suppresses
			// the VideoToolbox retry to HWAccelNone, which decodes and encodes on the
			// CPU, and surfaces the startup failure instead.
			retryAccel := playback.StartupRetryHWAccel(opts)
			if !h.startupRetryAllowedV3(opts, retryAccel) {
				unlock()
				return preparedTransportV3{}, transportErr
			}
			retryOpts := opts
			retryOpts.AvoidHWDevice = startupFailure.failedDevice
			retryOpts.HWAccel = retryAccel
			slog.WarnContext(r.Context(), "local transcode crashed during startup; retrying once",
				logComponentKey, playbackLogValueV3,
				"playback_session_id", session.ID,
				"failed_device", startupFailure.failedDevice,
				"configured_devices", retryOpts.HWDevice,
				"error", startupFailure.cause)
			ts, startupFailure = h.startReadyLocalPlaybackTransportV3(r.Context(), retryOpts)
			if startupFailure != nil && startupFailure.failedToStart {
				unlock()
				return preparedTransportV3{}, toneMapExecutionTransportErrorV3(startupFailure.cause, "Failed to start the playback transport.")
			}
			if startupFailure != nil {
				unlock()
				return preparedTransportV3{}, localTransportReadinessErrorV3(retryOpts, startupFailure.wasRunning, startupFailure.cause)
			}
		}
	}
	// Start-commit decode pre-check. A virtual generation whose decoder already
	// rejected the source during startup cannot produce a playable plan, so fail
	// it before it is published and let the start/replan rotation loop substitute
	// another provider candidate. This is a flag read on the candidate we are
	// about to commit, never a wait, and only a video encode has a decode verdict
	// to give up on. It deliberately reads the SUCCESSOR ts, not the registered
	// predecessor: a replan to a healthy sibling must not be aborted by the
	// generation it is replacing. Non-virtual files keep the existing behavior —
	// the start commits and the media routes answer 422 + X-Vio-Decode-Error,
	// which remains the fallback for clients without the recovery path.
	if isVirtualPlaybackFile(file) && result.Plan != nil && planHasVideoEncodeV3(*result.Plan) && ts.IsSourceRejected() {
		// The generation is never published; close its process before the
		// lifecycle lock is released so a rejected candidate does not leak a
		// running ffmpeg.
		_ = ts.Close()
		unlock()
		return preparedTransportV3{}, &transportErrorV3{
			reason:  candidateSourceDecodeRejectedReasonV3,
			message: candidateSourceDecodeRejectedMessageV3,
		}
	}
	// A copy session that planned the whole media as one VOD playlist is
	// genuinely seekable: every segment boundary is known before FFmpeg starts,
	// so the client can request any segment directly. Publish that full window
	// instead of the copy pre-roll window, whose open end forced every seek
	// back through a replan.
	if vod, ok := ts.CopyVODPlan(); ok {
		configureCopyVODTimelineV3(result.Plan, vod)
	}
	cardOpts := ts.Opts()
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(file.FilePath)), "virtual://") {
		cardOpts.CanonicalInputPath = file.FilePath
		cardOpts.VirtualSourceOwnerInstallationID = effectiveVirtualOwner(file.VirtualOwnerInstallationID, session.VirtualSourceOwnerInstallationID)
	}
	url := fmt.Sprintf("/playback/transcode/%s/master.m3u8", session.ID)
	card := playback.NewRecipeCard(session.UserID, session.ProfileID, file.ID, "", cardOpts)
	card.OriginalStartedAt = session.StartedAt
	card.StreamLocation = session.StreamLocation
	card.RoutingNetworkProvider = new(netaccess.PathFromContext(r.Context()).Provider)
	card.RoutingWorkload = string(routingWorkloadV3(result))
	card.RoutingExecution = string(noderouting.ExecutionAPI)
	card.RoutingEgress = string(noderouting.EgressAPI)
	var sessionRecipe *playback.RecipeCard
	if mode.headerAuth {
		sessionRecipe = sessionRecipeCardV3(card, session)
	} else {
		url = appendStreamToken(url, h.signSessionToken(card, mode.headerAuth))
	}
	committed := false
	previousNodeURL, previousTransportID := h.liveTranscodeRouteV3(session)
	return preparedTransportV3{
		sessionRecipe:    sessionRecipe,
		url:              url,
		hwAccel:          ts.Opts().EffectiveEncoderHWAccel(),
		toneMapMode:      ts.Opts().ToneMapMode,
		routingWorkload:  routingWorkloadV3(result),
		routingExecution: noderouting.ExecutionAPI,
		routingEgress:    noderouting.EgressAPI,
		commit: func() *transportErrorV3 {
			if committed {
				return nil
			}
			committed = true
			previous, accepted := h.tm.SwapTranscodeSession(session.ID, ts)
			if !accepted {
				unlock()
				return &transportErrorV3{
					reason:    transcodeStartFailedReasonV3,
					message:   "The playback transport could not be published during shutdown.",
					retryable: true,
				}
			}
			// A local transcode is never proxy-served, so an authorized-origins
			// replan landing here has to revoke the grant it is replacing.
			h.revokeStaleProxyGrantOnCommitV3(r.Context(), session.ID, mode, false)
			h.applyRemoteTransportMarkV3(r.Context(), session.ID, false)
			unlock()
			if previous != nil && previous != ts {
				// Keep the displaced generation's already-produced bytes
				// servable for a bounded overlap so the client's in-flight
				// old playlist (before it adopts the new plan) is not 503'd
				// while the new generation warms. The process is stopped;
				// only its output directory is retained.
				h.tm.RetireTranscodeSessionPredecessor(session.ID, previous, playback.RetainedGenerationRetention)
			}
			if previousNodeURL != "" {
				h.tm.StopRemoteTranscode(previousTransportID, previousNodeURL)
			}
			ts.SetRestartHook(func(ctx context.Context) {
				h.maybeStartThrottler(ctx, ts)
				h.tm.MonitorLocalTranscodeExit(session.ID, ts)
			})
			h.maybeStartThrottler(r.Context(), ts)
			h.tm.MonitorLocalTranscodeExit(session.ID, ts)
			return nil
		},
		rollback: func() {
			if committed {
				return
			}
			committed = true
			_ = ts.Close()
			unlock()
		},
	}, nil
}

func manifestStartupTransportErrorV3(running bool, cause error) *transportErrorV3 {
	message := "The playback transport failed before media became ready."
	if running {
		message = "The playback transport did not become ready in time."
	}
	// A revoked generation surfaces its decode verdict through the readiness
	// wait: without this mapping the rotation loop would see a generic
	// transcode_start_failed and retry the same undecodable bytes instead of
	// substituting another provider candidate.
	if errors.Is(cause, playback.ErrSourceDecodeRejected) {
		return &transportErrorV3{
			reason:  candidateSourceDecodeRejectedReasonV3,
			message: candidateSourceDecodeRejectedMessageV3,
			cause:   cause,
		}
	}
	return &transportErrorV3{reason: transcodeStartFailedReasonV3, message: message, retryable: running, cause: cause}
}

// sourcePreflightRejectedMessageV3 tells the viewer, and an admin reading the
// log, that the file failed the tone-map check, so a retry gets the same
// answer. The check also rejects an executor's bad output, so the message
// names both causes.
const sourcePreflightRejectedMessageV3 = "This file's HDR video failed Silo's conversion check, so trying again won't help. Its HDR metadata may not match the video, or the transcoder may not convert it correctly."

func toneMapExecutionTransportErrorV3(cause error, message string) *transportErrorV3 {
	if errors.Is(cause, tonemap.ErrSourcePreflightRejected) {
		message = sourcePreflightRejectedMessageV3
	}
	return &transportErrorV3{
		reason:    transcodeStartFailedReasonV3,
		message:   message,
		retryable: !errors.Is(cause, tonemap.ErrSourceRevisionChanged) && !errors.Is(cause, tonemap.ErrSourcePreflightRejected),
		cause:     cause,
	}
}

func combineTransportErrorsV3(first, second *transportErrorV3) *transportErrorV3 {
	if first == nil {
		return second
	}
	if second == nil {
		return first
	}
	// A route skipped because its executor can't run the recipe says nothing
	// about the file, so a route that ran keeps its reason, message and
	// retryability. Otherwise any retryable failure makes the result retryable,
	// and the reason and message come from a retryable side, so a final
	// verdict's "won't help" message never rides on a retryable result.
	if first.executorIneligible != second.executorIneligible {
		attempted := first
		if first.executorIneligible {
			attempted = second
		}
		combined := *attempted
		combined.cause = errors.Join(first.cause, second.cause)
		return &combined
	}
	combined := *second
	if first.retryable && !second.retryable {
		combined = *first
	}
	combined.retryable = first.retryable || second.retryable
	combined.cause = errors.Join(first.cause, second.cause)
	return &combined
}

// prepareRemoteTransportV3 starts an HLS generation on the selected transcode node.
func (h *PlaybackHandler) prepareRemoteTransportV3(r *http.Request, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, nodePlan nodepool.Plan, timeline preparedTimelineV3, mode mediaAuthModeV3) (preparedTransportV3, *transportErrorV3) {
	node := nodePlan.TranscodeNode
	transportID := transportGenerationV3(session.ID, result.Plan.PlanID)
	videoCodec := result.TargetVideoCodec
	if result.Plan.Delivery == playback.DeliveryRemuxHLSV3 {
		videoCodec = "copy"
	}
	sourceMetadata := sourceExecutionMetadataV3(file, result)
	sourceProfile, sourceBitDepth := sourceVideoTranscodeFactsV3(file, result)
	// The node's own override wins over this host's cluster-wide setting; every
	// other node gets the cluster value verbatim, "auto" included, so the node
	// still resolves it against live hardware at session start.
	hwAccel := node.EffectiveHWAccel(h.playbackConfig().HWAccel)
	toneMapFilter := ""
	if result.ToneMapMode != "" {
		capabilities, err := h.remoteToneMapCapabilitiesV3(r.Context(), node.URL, false)
		if err != nil || !capabilities.Supports(result.ToneMapMode, result.ToneMapSourceKind) {
			return preparedTransportV3{}, &transportErrorV3{reason: capabilityUnavailableReasonV3, message: "The selected node cannot run the tone-map recipe.", retryable: true, cause: err, executorIneligible: err == nil}
		}
		toneMapFilter = capabilities.FilterFor(result.ToneMapMode, result.ToneMapSourceKind)
		if result.ToneMapMode == tonemap.ModeHardware {
			hwAccel = capabilities.BackendFor(result.ToneMapMode, result.ToneMapSourceKind)
		} else {
			hwAccel = playback.HWAccelNone
		}
	}
	req := transcodenode.TranscodeStartRequest{SessionID: transportID, InputPath: file.FilePath, SourceVideoCodec: sourceMetadata.VideoCodec, SourceVideoProfile: sourceProfile, SourceVideoBitDepth: sourceBitDepth, SourceAudioChannels: result.SourceAudioChannels, SoftwareVideoDecode: sourceMetadata.SoftwareVideoDecode, ToneMapPolicy: result.ToneMapPolicy, ToneMapMode: result.ToneMapMode, ToneMapSourceKind: result.ToneMapSourceKind, ToneMapRecipeVersion: result.ToneMapRecipeVersion, ToneMapPreflightRequired: result.ToneMapPreflightRequired, ToneMapSourceRevision: result.ToneMapSourceRevision, VideoBitstreamFilter: videoBitstreamFilterForPlanV3(result.Plan), VideoSampleEntry: videoSampleEntryForPlanV3(result.Plan), VideoRange: copyHLSVideoRangeV3(file, result, videoCodec), SeekSeconds: timeline.seekSeconds, StreamOriginSeconds: timeline.streamOriginSeconds, CopySeekAnchorResolved: timeline.copySeekAnchorResolved, StartSegmentNumber: timeline.startSegmentNumber, TargetResolution: result.TargetResolution, TargetCodecVideo: videoCodec, TargetCodecAudio: result.TargetAudioCodec, TargetAudioChannels: result.TargetAudioChannels, TargetAudioBitrateKbps: result.TargetAudioBitrateKbps, TargetBitrateKbps: result.TargetBitrateKbps, SegmentDuration: playback.DefaultSegmentDuration, KeyframePlaylist: h.playbackConfig().KeyframePlaylist && videoCodec == "copy", HWAccel: hwAccel, AudioTrackIndex: plannedAudioStreamOrdinalV3(file, result, session.AudioTrackIndex), SubtitleTrackIndex: result.SubtitleTransportTrackIndex, SubtitleBurnIn: result.SubtitleBurnIn, SubtitleCodec: result.SubtitleCodec, SubtitleRendition: result.SubtitleRendition, SubtitleRenditionTracks: result.SubtitleRenditionTracks, SubtitleRenditionSource: result.SubtitleRenditionSource, TotalDuration: sourceMetadata.DurationSeconds, RequireReady: true}
	req.ThrottleSeconds = playback.ConfiguredTranscodeThrottleSeconds(r.Context(), h.SettingsRepo)
	if strings.EqualFold(videoCodec, "copy") {
		req.CopyFMP4RecipeVersion = playback.CopyFMP4RecipeVersion
	}
	req.CopyVideoMPEGTS = playback.PlanCopiesVideoAsMPEGTSV3(*result.Plan)
	if playback.IsAudioToAACStereoDownmixV3(req.SourceAudioChannels, req.TargetCodecAudio, req.TargetAudioChannels) {
		// Remote attestation uses the explicit effective layout even though zero
		// means stereo to the local AAC argument builder.
		req.TargetAudioChannels = 2
		req.AudioRecipeVersion = playback.TransformationAudioToAACRecipeVersionV3
	} else {
		// SourceAudioChannels is a v2 recipe field at the node boundary. Omit it
		// for ordinary encodes so they cannot be mistaken for partial v2 work.
		req.SourceAudioChannels = 0
	}
	if req.ToneMapMode != "" {
		req.ToneMapDVConfigPresent = sourceMetadata.ToneMapDVConfigPresent
		req.ToneMapDVBLCompatIDPresent = sourceMetadata.ToneMapDVBLCompatIDPresent
		req.ToneMapDVBLPresent = sourceMetadata.ToneMapDVBLPresent
		req.ToneMapDVRPUPresent = sourceMetadata.ToneMapDVRPUPresent
	}
	cleanupOnFailure := func() {}
	remoteStartAt := time.Now()
	nodeResp, status, err := h.startRemotePlaybackTransport(r.Context(), node.URL, req)
	recordTransportPhaseV3(r.Context(), "transport_remote_ready", remoteStartAt)
	remoteOutcome := transportStartupReadyV3
	if err != nil || status != http.StatusAccepted {
		remoteOutcome = playbackRemoteOutcomeFailedV3
	}
	slog.InfoContext(r.Context(), "playback transport startup timing",
		logComponentKey, playbackLogValueV3,
		requestIDLogKeyV3, chimw.GetReqID(r.Context()),
		"transport", "remote",
		"session", session.ID,
		"total_ms", time.Since(remoteStartAt).Milliseconds(),
		"status", status,
		"outcome", remoteOutcome,
	)
	if err != nil {
		cleanupOnFailure()
		if req.ToneMapMode != "" && (errors.Is(err, tonemap.ErrSourceRevisionChanged) ||
			errors.Is(err, tonemap.ErrSourcePreflightRejected) ||
			errors.Is(err, playback.ErrToneMapSourceValidationUnavailable) ||
			errors.Is(err, playback.ErrToneMapExecutorUnavailable)) {
			h.tm.StopRemoteTranscode(transportID, node.URL)
			return preparedTransportV3{}, toneMapExecutionTransportErrorV3(err, "The selected transcode node rejected the playback transport.")
		}
		// A timeout can fire after the node actually started the job; the
		// stop is a harmless 404 when it never did, and reaps an orphan
		// full-length transcode when it did.
		h.tm.StopRemoteTranscodeAfterFailure(transportID, node.URL, err)
		return preparedTransportV3{}, &transportErrorV3{reason: "transcode_node_unavailable", message: "The selected transcode node is unavailable.", retryable: true, cause: err}
	}
	if status != http.StatusAccepted {
		cleanupOnFailure()
		h.tm.StopRemoteTranscode(transportID, node.URL)
		return preparedTransportV3{}, &transportErrorV3{reason: transcodeStartFailedReasonV3, message: "The selected transcode node rejected the playback transport.", retryable: true, cause: fmt.Errorf("node returned status %d", status)}
	}
	if err := transcodenode.ValidateAudioRecipeAttestation(req, nodeResp); err != nil {
		cleanupOnFailure()
		h.tm.StopRemoteTranscode(transportID, node.URL)
		// A node that cannot confirm the audio adaptation recipe is an audio
		// failure, not a video one. The distinct reason keeps the start/replan
		// alternate-version hunts from rotating the release over it; the
		// transport loop still tries the same recipe on another node, which is
		// the same-release resolution for a node-local audio condition.
		return preparedTransportV3{}, &transportErrorV3{reason: audioAdaptationFailedReasonV3, message: "The selected transcode node did not confirm the audio recipe.", retryable: true, cause: err}
	}
	if err := transcodenode.ValidateCopyFMP4RecipeAttestation(req, nodeResp); err != nil {
		cleanupOnFailure()
		h.tm.StopRemoteTranscode(transportID, node.URL)
		return preparedTransportV3{}, &transportErrorV3{reason: transcodeStartFailedReasonV3, message: "The selected transcode node did not confirm the copy-video recipe.", retryable: true, cause: err}
	}
	if err := transcodenode.ValidateThrottleAttestation(req, nodeResp); err != nil {
		cleanupOnFailure()
		h.tm.StopRemoteTranscode(transportID, node.URL)
		return preparedTransportV3{}, &transportErrorV3{reason: transcodeStartFailedReasonV3, message: "The selected transcode node did not confirm the throttle policy.", retryable: true, cause: err}
	}
	if err := transcodenode.ValidateKeyframePlaylistAttestation(req, nodeResp); err != nil {
		h.tm.StopRemoteTranscode(transportID, node.URL)
		return preparedTransportV3{}, &transportErrorV3{reason: transcodeStartFailedReasonV3, message: "The selected transcode node did not confirm the keyframe playlist.", retryable: true, cause: err}
	}
	if err := transcodenode.ValidateCopyVideoMPEGTSAttestation(req, nodeResp); err != nil {
		h.tm.StopRemoteTranscode(transportID, node.URL)
		return preparedTransportV3{}, &transportErrorV3{reason: transcodeStartFailedReasonV3, message: "The selected transcode node did not confirm MPEG-TS packaging.", retryable: true, cause: err}
	}
	if err := transcodenode.ValidateSubtitleRenditionAttestation(req, nodeResp); err != nil {
		h.tm.StopRemoteTranscode(transportID, node.URL)
		return preparedTransportV3{}, &transportErrorV3{reason: transcodeStartFailedReasonV3, message: "The selected transcode node did not confirm the subtitle rendition.", retryable: true, cause: err}
	}
	if req.ToneMapMode != "" && nodeResp.ToneMapMode != req.ToneMapMode {
		cleanupOnFailure()
		h.tm.StopRemoteTranscode(transportID, node.URL)
		return preparedTransportV3{}, &transportErrorV3{reason: transcodeStartFailedReasonV3, message: "The selected transcode node did not confirm the tone-map recipe.", retryable: true}
	}
	confirmedToneMapMode := tonemap.Mode("")
	if req.ToneMapMode != "" {
		confirmedToneMapMode = nodeResp.ToneMapMode
	}
	card := remoteTranscodeRecipeCardV3(session, file, node.URL, transportID, req, nodeResp, toneMapFilter)
	card.RoutingWorkload = string(routingWorkloadV3(result))
	card.RoutingNetworkProvider = new(netaccess.PathFromContext(r.Context()).Provider)
	card.RoutingExecutionNodeID = node.ID
	card.RoutingExecution = string(noderouting.ExecutionTranscode)
	card.RoutingEgress = string(noderouting.EgressAPI)
	// The node never sees the token, so the API relaying its playlist reads
	// the negotiated playlist form from the token's claims.
	if nodePlan.ProxyNode != nil {
		card.RoutingEgress = string(noderouting.EgressProxy)
		card.RoutingEgressNodeID = nodePlan.ProxyNode.ID
	}
	confirmedHWAccel := card.EffectiveEncoderHWAccel()
	// Keep the provider-neutral virtual source identity on the stored
	// recipe so a node/central restart resolves the same release.
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(file.FilePath)), "virtual://") {
		card.InputPath = file.FilePath
		card.VirtualSourceOwnerInstallationID = effectiveVirtualOwner(file.VirtualOwnerInstallationID, session.VirtualSourceOwnerInstallationID)
	}
	card.OriginalStartedAt = session.StartedAt
	url := fmt.Sprintf("/playback/transcode/%s/master.m3u8", session.ID)
	// Either URL builder only returns an absolute proxy URL when a proxy was
	// planned and its authority (a signed token, or a stored grant) could
	// actually be established; otherwise the client fetches the manifest from
	// this server and the local liveness path applies.
	servedByProxy := false
	var priorGrant *playback.RecipeCard
	accessPath := netaccess.PathFromContext(r.Context())
	switch {
	case !mode.headerAuth:
		url = h.buildProxyManifestURL(card, nodePlan.ProxyNode, mode.headerAuth, accessPath)
		servedByProxy = nodePlan.ProxyNode != nil && strings.HasPrefix(url, "http")
	case mode.proxyEgress:
		url, servedByProxy, priorGrant = h.grantManifestURLV3(r.Context(), card, nodePlan.ProxyNode, accessPath)
	}
	if mode.headerAuth {
		h.putNodeRecipeV3(r.Context(), transportID, card)
	}
	if nodePlan.ProxyNode != nil && !servedByProxy {
		if releaser, ok := h.NodePlanner.(sessionProxyReservationReleaserV3); ok {
			releaser.ReleaseSessionProxy(session.ID)
		}
	}
	committed := false
	previousNodeURL, previousTransportID := h.liveTranscodeRouteV3(session)
	remoteQueueStart := time.Now()
	unlock := h.tm.LockSessionLifecycle(session.ID)
	recordTransportPhaseV3(r.Context(), "transport_queue", remoteQueueStart)
	routingEgress := noderouting.EgressAPI
	egressNodeID := 0
	egressNodeURL := ""
	if servedByProxy {
		routingEgress = noderouting.EgressProxy
		egressNodeID = nodePlan.ProxyNode.ID
		egressNodeURL = nodePlan.ProxyNode.URL
	}
	adoptSuccessor := func() {
		h.tm.CloseTranscodeSession(session.ID, "")
		if previousNodeURL != "" {
			h.tm.StopRemoteTranscode(previousTransportID, previousNodeURL)
			h.deleteNodeRecipeV3(r.Context(), previousTransportID)
		}
		h.revokeStaleProxyGrantOnCommitV3(r.Context(), session.ID, mode, servedByProxy)
		h.applyRemoteTransportMarkV3(r.Context(), session.ID, servedByProxy)
	}
	rollbackTransport := func(requireCancellation bool) error {
		cleanupOnFailure()
		if committed {
			return nil
		}
		if requireCancellation {
			if err := h.tm.CancelRemoteTranscode(r.Context(), transportID, node.URL); err != nil {
				// Preserve the admitted successor route for a retryable stop. Its
				// live session, authority, and reservation must continue to agree.
				committed = true
				adoptSuccessor()
				unlock()
				return err
			}
		} else {
			h.tm.StopRemoteTranscode(transportID, node.URL)
		}
		committed = true
		// The node job this recipe rebuilds is gone, so the recipe must go too.
		h.deleteNodeRecipeV3(r.Context(), transportID)
		if releaser, ok := h.NodePlanner.(sessionReservationReleaserV3); ok {
			releaser.ReleaseSession(session.ID)
		}
		if servedByProxy {
			h.restoreProxyGrantV3(r.Context(), session.ID, priorGrant)
		}
		unlock()
		return nil
	}
	var rollbackRequired func() error
	if routingWorkloadV3(result) == noderouting.WorkloadRemux {
		rollbackRequired = func() error { return rollbackTransport(true) }
	}
	var sessionRecipe *playback.RecipeCard
	if mode.headerAuth {
		sessionRecipe = sessionRecipeCardV3(card, session)
	}
	return preparedTransportV3{sessionRecipe: sessionRecipe, url: url, nodeURL: node.URL, transportID: transportID, hwAccel: confirmedHWAccel, toneMapMode: confirmedToneMapMode,
		routingWorkload: routingWorkloadV3(result), routingExecution: noderouting.ExecutionTranscode, routingExecutorID: node.ID, routingExecutorURL: node.URL,
		routingEgress: routingEgress, routingEgressID: egressNodeID, routingEgressURL: egressNodeURL, commit: func() *transportErrorV3 {
			if committed {
				return nil
			}
			committed = true
			adoptSuccessor()
			unlock()
			return nil
		}, rollback: func() {
			_ = rollbackTransport(false)
		}, rollbackRequired: rollbackRequired}, nil
}

// liveTranscodeRouteV3 is the transcode route the session runs now, which a
// replacement stops once it commits. Callers read it under the session
// lifecycle lock, not from the snapshot they planned with: a move off a dead
// node commits under that lock, so a route read here is the one the commit
// replaces. A session the manager no longer has falls back to the snapshot.
func (h *PlaybackHandler) liveTranscodeRouteV3(session *playback.Session) (nodeURL, transportID string) {
	if current, err := h.sessionMgr.GetSession(session.ID); err == nil && current != nil {
		return current.TranscodeNodeURL, remoteTransportID(current)
	}
	return session.TranscodeNodeURL, remoteTransportID(session)
}

// remoteTranscodeRecipeCardV3 captures the byte-affecting recipe of a started
// remote transcode. It is what a proxy relays from (grant) or a client carries
// (signed token), and what a restarted node reconstructs from, so it must
// reflect the parameters the node accepted rather than the ones requested: the
// node reports the hardware acceleration it actually used, and whether it
// decoded video on the CPU. Software decode is the request's value OR the
// node's, so an older node that omits the field keeps the requested value.
//
// toneMapFilter is the resolved FFmpeg filter for the confirmed executor; it is
// not part of the node's request/response contract, so the caller supplies it.
func remoteTranscodeRecipeCardV3(session *playback.Session, file *models.MediaFile, nodeURL, transportID string, req transcodenode.TranscodeStartRequest, nodeResp transcodenode.TranscodeStartResponse, toneMapFilter string) playback.RecipeCard {
	hw := firstNonEmptyHandlerV3(strings.TrimSpace(nodeResp.HWAccel), strings.TrimSpace(req.HWAccel))
	card := playback.NewRecipeCard(session.UserID, session.ProfileID, file.ID, nodeURL, playback.TranscodeOpts{InputPath: req.InputPath, SessionID: session.ID, TranscodeTransportID: transportID, SourceVideoCodec: req.SourceVideoCodec, SourceVideoProfile: req.SourceVideoProfile, SourceVideoBitDepth: req.SourceVideoBitDepth, SourceAudioChannels: req.SourceAudioChannels, SoftwareVideoDecode: req.SoftwareVideoDecode || nodeResp.SoftwareVideoDecode, ToneMapPolicy: req.ToneMapPolicy, ToneMapMode: req.ToneMapMode, ToneMapSourceKind: req.ToneMapSourceKind, ToneMapFilter: toneMapFilter, ToneMapRecipeVersion: req.ToneMapRecipeVersion, ToneMapPreflightRequired: req.ToneMapPreflightRequired, ToneMapSourceRevision: req.ToneMapSourceRevision, VideoBitstreamFilter: req.VideoBitstreamFilter, VideoSampleEntry: req.VideoSampleEntry, VideoRange: req.VideoRange, SeekSeconds: req.SeekSeconds, StreamOriginSeconds: req.StreamOriginSeconds, CopySeekAnchorResolved: req.CopySeekAnchorResolved, StartSegmentNumber: req.StartSegmentNumber, TargetResolution: req.TargetResolution, TargetCodecVideo: req.TargetCodecVideo, TargetCodecAudio: req.TargetCodecAudio, TargetAudioChannels: req.TargetAudioChannels, TargetAudioBitrateKbps: req.TargetAudioBitrateKbps, TargetBitrateKbps: req.TargetBitrateKbps, SegmentDuration: req.SegmentDuration, KeyframePlaylist: req.KeyframePlaylist, HWAccel: hw, AudioTrackIndex: req.AudioTrackIndex, RequireAudioTrack: req.RequireAudioTrack, SubtitleTrackIndex: req.SubtitleTrackIndex, SubtitleBurnIn: req.SubtitleBurnIn, SubtitleCodec: req.SubtitleCodec, SubtitleRendition: req.SubtitleRendition, SubtitleRenditionTracks: req.SubtitleRenditionTracks, SubtitleRenditionSource: req.SubtitleRenditionSource, TotalDuration: req.TotalDuration, ThrottleSeconds: req.ThrottleSeconds})
	card.ToneMapDVConfigPresent = req.ToneMapDVConfigPresent
	card.CopyVideoMPEGTS = req.CopyVideoMPEGTS
	if encoderHWAccel := strings.TrimSpace(nodeResp.EncoderHWAccel); encoderHWAccel != "" {
		card.EncoderHWAccel = encoderHWAccel
	}
	card.ToneMapDVBLCompatIDPresent = req.ToneMapDVBLCompatIDPresent
	card.ToneMapDVBLPresent = req.ToneMapDVBLPresent
	card.ToneMapDVRPUPresent = req.ToneMapDVRPUPresent
	// Stamp the immutable session creation time onto every copy of this recipe —
	// client token, proxy grant, and node recipe alike — so telemetry can age the
	// session correctly after any reconstruct.
	card.OriginalStartedAt = session.StartedAt
	card.StreamLocation = session.StreamLocation
	return card
}

// grantManifestURLV3 is buildProxyManifestURL's authorized-origins sibling: it
// stores the session's transcode recipe as a proxy grant and returns the
// credential-free manifest URL on that origin. Segment URIs stay relative to
// the manifest, so the same /stream/v3/{session_id}/... family serves both.
//
// Without a planned proxy — or a proxy the client cannot reach on its access
// path, or when the grant cannot be stored — the client fetches the manifest
// from this server, which relays the same node. The third value is the grant
// this write displaced, for the caller's rollback.
func (h *PlaybackHandler) grantManifestURLV3(ctx context.Context, card playback.RecipeCard, proxyNode *nodepool.Node, path netaccess.Path) (string, bool, *playback.RecipeCard) {
	localURL := fmt.Sprintf("/playback/transcode/%s/master.m3u8", card.SessionID)
	if proxyNode == nil {
		return localURL, false, nil
	}
	base := proxyNode.ClientURLFor(path)
	if base == "" {
		return localURL, false, nil
	}
	card.RoutingEgressNodeID = proxyNode.ID
	prior, stored := h.putProxyGrantV3(ctx, card.SessionID, card)
	if !stored {
		return localURL, false, nil
	}
	return base + "/stream/v3/" + card.SessionID + "/master.m3u8", true, prior
}

// sourceExecutionMetadataV3 freezes the source facts used by a remote executor.
func sourceExecutionMetadataV3(file *models.MediaFile, result playback.PlannerResultV3) playback.SourceExecutionMetadataV3 {
	if result.FrozenSourceMetadata != nil {
		metadata := *result.FrozenSourceMetadata
		if result.Plan != nil && result.Plan.EffectiveRecipe.SoftwareVideoDecode {
			metadata.SoftwareVideoDecode = true
		}
		return scopeToneMapSourceMetadataV3(metadata, result.ToneMapMode)
	}
	if file == nil {
		return playback.SourceExecutionMetadataV3{}
	}
	videoCodec, profile, bitDepth := playback.SourceVideoTranscodeFacts(file)
	track := models.VideoTrack{}
	if len(file.VideoTracks) > 0 {
		track = file.VideoTracks[0]
	}
	softwareDecode := playback.RequiresSoftwareVideoDecode(videoCodec, profile, bitDepth)
	if result.Plan != nil {
		softwareDecode = softwareDecode || result.Plan.EffectiveRecipe.SoftwareVideoDecode
	}
	metadata := playback.SourceExecutionMetadataV3{
		VideoCodec:                 videoCodec,
		VideoProfile:               profile,
		VideoBitDepth:              bitDepth,
		SoftwareVideoDecode:        softwareDecode,
		DurationSeconds:            float64(file.Duration),
		ToneMapSourceKind:          result.ToneMapSourceKind,
		ToneMapPreflightRequired:   result.ToneMapPreflightRequired,
		ToneMapSourceRevision:      result.ToneMapSourceRevision,
		ToneMapDVConfigPresent:     track.DVConfigPresent,
		ToneMapDVBLCompatIDPresent: track.DVBLCompatIDPresent,
		ToneMapDVBLPresent:         track.DVBLPresent,
		ToneMapDVRPUPresent:        track.DVRPUPresent,
	}
	return scopeToneMapSourceMetadataV3(metadata, result.ToneMapMode)
}

// Dolby Vision provenance is part of a frozen tone-map recipe, not a general
// source description. Leaving it attached to a video-copy remux makes the
// execution boundary correctly reject the orphaned fields as a partial recipe.
func scopeToneMapSourceMetadataV3(metadata playback.SourceExecutionMetadataV3, mode tonemap.Mode) playback.SourceExecutionMetadataV3 {
	if mode != "" {
		return metadata
	}
	metadata.ToneMapSourceKind = ""
	metadata.ToneMapPreflightRequired = false
	metadata.ToneMapSourceRevision = tonemap.SourceRevision{}
	metadata.ToneMapDVConfigPresent = false
	metadata.ToneMapDVBLCompatIDPresent = false
	metadata.ToneMapDVBLPresent = false
	metadata.ToneMapDVRPUPresent = false
	return metadata
}

func sourceVideoTranscodeFactsV3(file *models.MediaFile, result playback.PlannerResultV3) (string, int) {
	if result.FrozenSourceMetadata != nil {
		return result.FrozenSourceMetadata.VideoProfile, result.FrozenSourceMetadata.VideoBitDepth
	}
	_, profile, bitDepth := playback.SourceVideoTranscodeFacts(file)
	return profile, bitDepth
}

// bindPlannedVirtualFileV3 pins the effective file to the concrete candidate
// and provider installation the plan actually selected. A first-ever auto
// start can return the catalog's provider-neutral sibling as the effective
// row while the planner resolved a concrete ?result= candidate; the neutral
// row carries owner 0, so storing its path and owner on the session leaves a
// seek reanchor unable to reopen the frozen candidate. The plan's
// EffectiveVirtualURI is the candidate that is actually playing, and
// virtualDecision carries the resolver's installation for it.
func bindPlannedVirtualFileV3(file *models.MediaFile, plan *playback.PlanV3, owner int) *models.MediaFile {
	if file == nil || plan == nil || !isVirtualPlaybackFile(file) {
		return file
	}
	uri := strings.TrimSpace(plan.EffectiveVirtualURI)
	if uri == "" {
		return file
	}
	// Never replace an already-concrete catalog candidate with a neutral plan
	// hint. The plan normally carries the concrete candidate; this guard keeps a
	// caller that built the plan from a neutral row from erasing a concrete
	// binding that is already in hand.
	if virtualResultCandidateID(uri) == "" && virtualResultCandidateID(file.FilePath) != "" {
		return file
	}
	owner = effectiveVirtualOwner(owner, file.VirtualOwnerInstallationID)
	if uri == file.FilePath && owner == file.VirtualOwnerInstallationID {
		return file
	}
	bound := *file
	bound.FilePath = uri
	if owner > 0 {
		bound.VirtualOwnerInstallationID = owner
	}
	return &bound
}

// fileContainerV3 names an effective file's container for the copy-audio
// decision, or "" when there is no file to ask (an older token or a synthetic
// row), which CopiedAudioADTSCodec reads as the historical answer.
func fileContainerV3(file *models.MediaFile) string {
	if file == nil {
		return ""
	}
	return file.Container
}

// v3SessionStreamState builds the durable stream state for a prepared transport.
func (h *PlaybackHandler) v3SessionStreamState(ctx context.Context, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, transport preparedTransportV3, mode mediaAuthModeV3) playback.SessionStreamState {
	state := playback.SessionStreamState{
		PlayMethod:                    result.PlayMethod,
		BasePlayMethod:                result.PlayMethod,
		AudioTrackIndex:               plannedAudioTrackIndexV3(result, session.AudioTrackIndex),
		TranscodeAudio:                result.TranscodeAudio,
		RemuxDVMode:                   remuxDVModeForPlanV3(result.Plan),
		DVProfile:                     planDVProfileV3(result.Plan),
		DVProfilePin:                  playback.PinDVPinV3(playback.DVPinV3{}, file, planDVProfileV3(result.Plan)),
		RemuxResumeLeadingPictureDrop: result.RemuxResumeLeadingPictureDrop,
		TranscodeHWAccel:              transport.hwAccel,
		ToneMapMode:                   transport.toneMapMode,
		TranscodeNodeURL:              transport.nodeURL,
		TranscodeTransportID:          transport.transportID,
		TranscodeRouteSet:             true,
		RoutingNetworkProvider:        new(netaccess.PathFromContext(ctx).Provider),
		RoutingWorkload:               string(transport.routingWorkload),
		RoutingExecution:              string(transport.routingExecution),
		RoutingExecutionNodeID:        transport.routingExecutorID,
		RoutingExecutionNodeURL:       transport.routingExecutorURL,
		RoutingEgress:                 string(transport.routingEgress),
		RoutingEgressNodeID:           transport.routingEgressID,
		RoutingEgressNodeURL:          transport.routingEgressURL,
		RequireMediaAuthorization:     mode.headerAuth,
		MediaAuthorizationSet:         true,
		// A v3 stream state is a complete snapshot that owns the virtual-source
		// binding outright: it may move the binding to a different candidate and
		// must be able to clear carried evidence when the new effective file is
		// not virtual. Legacy partial updates leave these unset.
		VirtualSourceSet:          true,
		VirtualSourceOwnershipSet: true,
		ClientIP:                  clientip.FromContext(ctx),
		ClientName:                session.ClientName,
		ClientVersion:             session.ClientVersion,
		ClientUserAgent:           session.ClientUserAgent,
		StreamBitrateKbps:         result.TargetBitrateKbps,
		TargetVideoCodec:          result.TargetVideoCodec,
		TargetAudioCodec:          result.TargetAudioCodec,
		SourceContainer:           fileContainerV3(file),
		SourceAudioChannels:       result.SourceAudioChannels,
		TargetAudioChannels:       result.TargetAudioChannels,
		TargetAudioBitrateKbps:    result.TargetAudioBitrateKbps,
		TargetResolution:          result.TargetResolution,
		SubtitleTrackIndex:        result.SubtitleTransportTrackIndex,
		SubtitleBurnIn:            result.SubtitleBurnIn,
	}
	if result.Plan != nil && (result.Plan.Delivery == playback.DeliveryTranscodeHLSV3 || result.Plan.Delivery == playback.DeliveryRemuxHLSV3) {
		state.SegmentDuration = playback.DefaultSegmentDuration
		state.OutputProtocol = playback.OutputProtocolHLS
		videoCodec := result.TargetVideoCodec
		if result.Plan.Delivery == playback.DeliveryRemuxHLSV3 {
			videoCodec = "copy"
		}
		state.TargetVideoCodec = videoCodec
		state.OutputContainer = playback.HLSOutputContainer(playback.TranscodeOpts{
			TargetCodecVideo: videoCodec,
			SourceVideoCodec: sourceExecutionMetadataV3(file, result).VideoCodec,
			CopyVideoMPEGTS:  playback.PlanCopiesVideoAsMPEGTSV3(*result.Plan),
		})
	}
	if result.Plan != nil && result.Plan.Delivery == playback.DeliveryRemuxProgressiveV3 {
		state.OutputContainer, state.OutputProtocol = playback.OutputContainerFMP4, playback.OutputProtocolHTTP
	}
	if result.Plan != nil && result.Plan.Delivery == playback.DeliveryOriginalHTTPV3 && file != nil {
		state.OutputContainer, state.OutputProtocol = file.Container, playback.OutputProtocolHTTP
	}
	if state.StreamBitrateKbps <= 0 {
		state.StreamBitrateKbps = result.TargetAudioBitrateKbps
	}
	if state.StreamBitrateKbps <= 0 {
		state.StreamBitrateKbps = fileBitrateKbps(file)
	}
	// Bind the session to the exact virtual URI that was resolved and probed
	// during planning, so later serving resolves the same release rather than
	// re-reading a mutable catalog path. The subtitle inventories travel with
	// it: candidate rotation re-probes the catalog row and can overwrite its
	// tracks between planning and a later subtitle fetch, and the serve path
	// must extract from the same evidence the plan promised. The evidence URI
	// and the source revision anchor that evidence to the exact candidate, so
	// the serve path can discard it when the bound candidate has moved.
	if isVirtualPlaybackFile(file) && strings.HasPrefix(file.FilePath, "virtual://") {
		owner := effectiveVirtualOwner(file.VirtualOwnerInstallationID, session.VirtualSourceOwnerInstallationID)
		// The plan names the concrete candidate it validated. Prefer it over a
		// neutral catalog sibling so the session binding and its evidence anchor
		// both name the bytes that are actually playing.
		virtualURI := strings.TrimSpace(file.FilePath)
		if result.Plan != nil {
			if plannedURI := strings.TrimSpace(result.Plan.EffectiveVirtualURI); plannedURI != "" &&
				(virtualResultCandidateID(plannedURI) != "" || virtualResultCandidateID(file.FilePath) == "") {
				virtualURI = plannedURI
			}
		}
		state.VirtualSourceURI = virtualURI
		state.VirtualSourceOwnerInstallationID = owner
		state.VirtualSourceRevision = ""
		state.VirtualSubtitleTracks = file.SubtitleTracks
		state.VirtualExternalSubtitles = file.ExternalSubtitles
		state.VirtualAudioTracks = file.AudioTracks
		state.VirtualSubtitleEvidenceURI = virtualURI
		// The evidence row id is as important as the URI: duplicate catalog
		// rows for one release share candidate URIs and neutral keys, so a
		// later serve must apply this inventory only to the exact row it was
		// captured from, never to a sibling row the probe rotated onto.
		state.VirtualSubtitleEvidenceFileID = file.ID
		// The evidence owner travels with the URI and row id so a later seek
		// reanchor can restore the frozen candidate's own installation after a
		// cross-installation binding move instead of borrowing the moved
		// binding's owner.
		state.VirtualSubtitleEvidenceOwnerInstallationID = owner
		// The evidence-set flag is normally the row's probe stamp. When the
		// session already retains evidence for this exact candidate — a seek
		// reanchor or a same-candidate carry commits that inventory — the row
		// may carry no stamp (a churned listing row) even though the retained
		// evidence is proven, so the session's own flag is the authority.
		evidenceSet := file.ProbeUpdatedAt != nil
		if !evidenceSet && sameVirtualCandidate(session.VirtualSubtitleEvidenceURI, virtualURI) {
			evidenceSet = session.VirtualSubtitleEvidenceSet
		}
		state.VirtualSubtitleEvidenceSet = evidenceSet
		// Freeze the font-bundle identity with the plan-time file row. A
		// background probe persists provider identity and inventory onto the
		// mutable row after this commit; re-deriving the key at serve time would
		// move every warmed entry out from under its readers. The warm and both
		// serve paths reuse this key while it still names the same file row.
		state.VirtualFontBundleKey = playback.FontBundleKey{
			FileID:          file.ID,
			Virtual:         true,
			ReleaseIdentity: virtualFontReleaseIdentity(file),
			InventoryToken:  playback.SubtitleInventoryToken(file.SubtitleTracks),
			FFmpegPath:      h.playbackConfig().FFmpegPath,
		}
		state.VirtualFontBundleKeySet = true
		// The new evidence generation is committed with this write, so the
		// viewer's subtitle position must be re-keyed onto it in the same
		// commit. A recorded rotation (the replan drift carry, or a candidate
		// move this request did not evaluate itself) either commits the carried
		// ordinal or records the one it could not preserve; a second rotation then
		// re-keys from the carried position.
		state.VirtualSubtitleSelectionIndex = result.SubtitleTrackIndex
		state.VirtualSubtitleSelectionSet = true
		if outcome, ok := subtitleRotationOutcomeFromContextV3(ctx); ok && outcome.ToFileID == file.ID {
			if outcome.Matched {
				if outcome.ToIndex == result.SubtitleTrackIndex {
					state.VirtualSubtitleCarryFromIndex = outcome.FromIndex
					state.VirtualSubtitleCarryToIndex = outcome.ToIndex
					state.VirtualSubtitleCarrySet = true
					if !outcome.Exact {
						appendSubtitleCarryWarningV3(result.Plan)
					}
				}
			} else {
				// A confirmed miss is terminal for that ordinal even when the
				// planner installed some other subtitle in the same commit; the
				// pre-rotation URL must not be reinterpreted positionally.
				state.VirtualSubtitleCarryLostIndex = outcome.FromIndex
				state.VirtualSubtitleCarryLostSet = true
			}
		} else if h.virtualEvidenceMovedV3(session, file, virtualURI) {
			// The request did not evaluate the carry (for example a rehydrated
			// replan that resolved a refreshed candidate directly). Re-derive it
			// from the session's retained evidence at the commit so the new
			// generation still records the carried position or its loss.
			h.commitDerivedSubtitleRotationCarryV3(ctx, session, file, &state, result)
		} else if session.VirtualSubtitleCarryLostSet && result.SubtitleTrackIndex < 0 {
			// The verdict outlives later non-rotation commits while the viewer
			// still has subtitles off. A commit that installs a subtitle is a
			// fresh selection against the current inventory and clears it.
			state.VirtualSubtitleCarryLostIndex = session.VirtualSubtitleCarryLostIndex
			state.VirtualSubtitleCarryLostSet = true
		}
	}
	return state
}

// virtualEvidenceMovedV3 reports whether the session's retained subtitle
// evidence belongs to a different candidate or layout than the file being
// committed. It is the rotation signal the stream-state commit needs when no
// replan drift pass already evaluated the carry.
func (h *PlaybackHandler) virtualEvidenceMovedV3(session *playback.Session, file *models.MediaFile, virtualURI string) bool {
	if session == nil || file == nil || !session.VirtualSubtitleEvidenceSet {
		return false
	}
	if evidenceID := session.VirtualSubtitleEvidenceFileID; evidenceID > 0 && evidenceID != file.ID {
		return true
	}
	evidenceURI := strings.TrimSpace(session.VirtualSubtitleEvidenceURI)
	if evidenceURI != "" && !sameVirtualCandidate(evidenceURI, virtualURI) {
		return true
	}
	return !playback.SubtitleLayoutsEqualIncludingExternal(
		file.SubtitleTracks, file.ExternalSubtitles, session.VirtualSubtitleTracks, session.VirtualExternalSubtitles)
}

// commitDerivedSubtitleRotationCarryV3 carries the session's committed subtitle
// position from the evidence generation it was minted on onto the candidate
// this commit installs. It writes the same carry/loss state the explicit replan
// path records, so both entry points leave an in-flight subtitle request with a
// committed answer.
func (h *PlaybackHandler) commitDerivedSubtitleRotationCarryV3(ctx context.Context, session *playback.Session, file *models.MediaFile, state *playback.SessionStreamState, result playback.PlannerResultV3) {
	if session == nil || file == nil || state == nil || !session.VirtualSubtitleSelectionSet || session.VirtualSubtitleSelectionIndex < 0 {
		return
	}
	evidenceID := session.VirtualSubtitleEvidenceFileID
	if evidenceID <= 0 {
		evidenceID = file.ID
	}
	// Authority is the same as the replan remap source: the durable plan that
	// minted the ordinal first, the session's retained evidence second, and the
	// live row only when neither exists.
	var durable map[int]predecessorPlanInventoryV3
	if h.PlanStoreV3 != nil {
		if record, err := h.PlanStoreV3.GetAttempt(ctx, session.ID); err == nil && record != nil {
			durable = replanDurableInventoryV3(record)
		}
	}
	carry := h.rekeySourceFileWithInventoryV3(ctx, evidenceID, session, durable)
	if carry.subtitleEvidenceErr != nil {
		slog.WarnContext(ctx, "cannot commit the carried subtitle position: the evidence tail is unreadable",
			"component", "playback", "session", session.ID, "file_id", file.ID, "error", carry.subtitleEvidenceErr)
		return
	}
	key, published, exact, matched, err := h.carrySubtitleSelectionAcrossRotationV3(ctx, session, carry, file, session.VirtualSubtitleSelectionIndex)
	if err != nil {
		slog.WarnContext(ctx, "cannot commit the carried subtitle position: the downloaded subtitle list is unreadable",
			"component", "playback", "session", session.ID, "file_id", file.ID, "error", err)
		return
	}
	fromIndex := session.VirtualSubtitleSelectionIndex
	if matched && published == result.SubtitleTrackIndex {
		state.VirtualSubtitleCarryFromIndex = fromIndex
		state.VirtualSubtitleCarryToIndex = published
		state.VirtualSubtitleCarrySet = true
		if !exact {
			appendSubtitleCarryWarningV3(result.Plan)
		}
		return
	}
	if !matched && result.SubtitleTrackIndex < 0 {
		state.VirtualSubtitleCarryLostIndex = fromIndex
		state.VirtualSubtitleCarryLostSet = true
		slog.InfoContext(ctx, "subtitle selection is unavailable after the candidate rotation",
			"component", "playback", "event", "subtitle_unavailable_after_rotation",
			"playback_session_id", session.ID,
			"from_candidate_id", virtualResultCandidateID(session.VirtualSubtitleEvidenceURI),
			"to_candidate_id", virtualResultCandidateID(file.FilePath),
			"carried_index", fromIndex,
			"carried_muxer_class", key.MuxerClass)
	}
}

// appendSubtitleCarryWarningV3 adds the fuzzy-carry degradation warning to a
// plan at most once. The warning is additive: playback keeps the viewer's
// subtitle, and the client learns that the rotated release's track differs on a
// tiebreak rather than being told the selection is gone.
func appendSubtitleCarryWarningV3(plan *playback.PlanV3) {
	if plan == nil {
		return
	}
	for _, warning := range plan.DegradationWarnings {
		if warning.Code == playback.DegradationWarningSubtitleCarryV3 {
			return
		}
	}
	plan.DegradationWarnings = append(plan.DegradationWarnings, playback.SubtitleCarryWarningV3())
}

func (h *PlaybackHandler) updateV3SessionState(ctx context.Context, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, transport preparedTransportV3, mode mediaAuthModeV3) error {
	return h.sessionMgr.UpdateStreamState(session.ID, h.v3SessionStreamState(ctx, session, file, result, transport, mode))
}

// scheduleVirtualWarmAfterTransportV3 starts the virtual font-bundle warm
// detached from the start request, after the transport commit. A font bundle is
// shared by every ASS track in the file, so one extraction serves the whole
// release. Best-effort: a nil cache or file is a no-op.
//
// Text/ASS/PGS subtitle tracks are deliberately not warmed here. A full-track
// warm has to read the entire remote source (30-95 s on the production relay)
// and it starts exactly when the player requests its first bounded subtitle
// window. The two demuxes then compete for the same relay and provider, which
// delays the cues the viewer is waiting for. The windowed serve already caches
// its slice, and the post-window warm fills the whole-track entry once the
// client's first window has completed.
func (h *PlaybackHandler) scheduleVirtualWarmAfterTransportV3(ctx context.Context, session *playback.Session, file *models.MediaFile) {
	if h == nil || h.SubtitleCache == nil || file == nil || session == nil {
		return
	}
	if !isVirtualPlaybackFile(file) {
		// Local files keep their existing serve-path warming; this detour is
		// only for virtual relay sources, which resolve a request-scoped
		// registration the warm must own.
		return
	}
	warmCtx := context.WithoutCancel(ctx)
	// The start path's session copy predates the stream-state commit that froze
	// the font-bundle key, so read the live copy back before the warm derives its
	// key. Without this the warm would key from the plan-time row and the serve
	// path from the frozen identity.
	warmSession := session
	if h.sessionMgr != nil {
		if live, err := h.sessionMgr.GetSession(session.ID); err == nil && live != nil {
			warmSession = live
		}
	}
	go func() {
		h.warmVirtualFontBundleV3(warmCtx, warmSession, file)
	}()
}

// warmVirtualFontBundleV3 pre-warms the font-bundle cache for a virtual
// source's effective file when it carries at least one ASS/SSA embedded track.
// Font bundles are keyed per file (not per track), so exactly one extraction is
// scheduled however many ASS tracks the release has. It resolves its own
// relay registration, runs the extraction detached under the cache's warm
// budget, and holds the relay open until the extraction settles — a relay URL
// released mid-extract would 404
// under ffmpeg. Best-effort by design: resolution failure, warm-slot
// exhaustion, and an already-cached bundle all degrade to the existing
// cold-serve path with no user-visible error. The returned channel closes when
// the warm settled (or was skipped); production ignores it.
func (h *PlaybackHandler) warmVirtualFontBundleV3(ctx context.Context, session *playback.Session, file *models.MediaFile) <-chan struct{} {
	done := make(chan struct{})
	if h == nil || h.SubtitleCache == nil || file == nil || session == nil {
		close(done)
		return done
	}
	if !isVirtualPlaybackFile(file) {
		close(done)
		return done
	}
	// The start path holds a session copy captured before UpdateStreamState
	// wrote VirtualSourceURI, so the effective file's URI is the reliable
	// source; prefer the live session when it has one. Both name the same
	// release the serve path will bind.
	virtualURI := file.FilePath
	if session.VirtualSourceURI != "" {
		virtualURI = session.VirtualSourceURI
	}
	if virtualURI == "" {
		close(done)
		return done
	}
	tracks := session.VirtualSubtitleTracks
	if len(tracks) == 0 {
		tracks = file.SubtitleTracks
	}
	hasASS := false
	for _, track := range tracks {
		if playback.IsASS(track.Codec) {
			hasASS = true
			break
		}
	}
	if !hasASS {
		close(done)
		return done
	}
	ffmpegPath := h.playbackConfig().FFmpegPath
	// The serve path binds the session's captured subtitle evidence before it
	// builds the cache key, so the warm must key the same inventory or it
	// commits under an identity no request will ask for.
	keyFile := file
	if session.VirtualSubtitleEvidenceSet {
		bound := *file
		bound.SubtitleTracks = session.VirtualSubtitleTracks
		keyFile = &bound
	}
	cacheKey := fontBundleCacheKey(keyFile, ffmpegPath)
	if session.VirtualFontBundleKeySet {
		// The plan-time commit froze the identity both this warm and the serve
		// paths must use. Deriving from keyFile again would re-key the warm when
		// the background probe has already persisted provider identity onto the
		// row.
		cacheKey = session.VirtualFontBundleKey
	}
	go func() {
		defer close(done)
		resolved, cleanup, err := h.resolveVirtualInputURI(
			context.WithoutCancel(ctx), file.FilePath, file.VirtualOwnerInstallationID,
			session.UserID, session.ProfileID, false, nil, "",
		)
		if err != nil {
			slog.DebugContext(ctx, "virtual font bundle pre-warm skipped: resolve failed",
				"component", "api", "file_id", file.ID, "error", err)
			return
		}
		if cleanup != nil {
			defer cleanup()
		}
		warmDone := h.SubtitleCache.WarmFontBundleInBackground(cacheKey, func(extractCtx context.Context) ([]byte, error) {
			fonts, extractErr := playback.ExtractAttachedSubtitleFonts(extractCtx, resolved.URL, ffmpegPath)
			if extractErr != nil {
				return nil, extractErr
			}
			return json.Marshal(playback.EncodeSubtitleFontBundle(fonts))
		})
		<-warmDone
	}()
	return done
}

func plannedAudioTrackIndexV3(result playback.PlannerResultV3, fallback int) int {
	if result.Plan != nil && result.Plan.SelectedTracks.Audio != nil && result.Plan.SelectedTracks.Audio.Index != nil {
		return *result.Plan.SelectedTracks.Audio.Index
	}
	return fallback
}

// plannedAudioStreamOrdinalV3 is the audio-only stream ordinal an executor must
// address for the plan's selected audio. The plan's index is an array position
// in its inventory; ffmpeg's 0:a:N and a node's equivalent count audio streams,
// which differs once a re-probe removes or reorders tracks.
func plannedAudioStreamOrdinalV3(file *models.MediaFile, result playback.PlannerResultV3, fallback int) int {
	index := plannedAudioTrackIndexV3(result, fallback)
	if index < 0 {
		return index
	}
	return audioStreamOrdinalV3(file, index)
}

// withoutNativeDeliveryFeaturesV3 drops the /api/v2-only delivery features
// from a request that arrived through the frozen /api/v1 surface, which never
// advertised them.
// withoutNativeDeliveryFieldsV3 drops the delivery fields /api/v1 never had,
// which its decoder ignored as unknown, before validation sees them.
func withoutNativeDeliveryFieldsV3(ctx *playback.ClientPlaybackContextV3) {
	for name, delivery := range ctx.Deliveries {
		if delivery.MaxChannelsByCodec != nil {
			delivery.MaxChannelsByCodec = nil
			ctx.Deliveries[name] = delivery
		}
		// Native embedded limitations came after the freeze: /api/v1 plans
		// an entry as it did before them, and never advertises
		// native_embedded_limitations_v1, without which a client must not
		// send an entry that needs them.
		if slices.ContainsFunc(delivery.Subtitles.NativeEmbedded, func(entry playback.NativeEmbeddedSubtitleCapabilityV3) bool {
			return entry.Limitations != nil
		}) {
			delivery.Subtitles.NativeEmbedded = slices.Clone(delivery.Subtitles.NativeEmbedded)
			for i := range delivery.Subtitles.NativeEmbedded {
				delivery.Subtitles.NativeEmbedded[i].Limitations = nil
			}
			ctx.Deliveries[name] = delivery
		}
	}
}

func withoutNativeDeliveryFeaturesV3(ctx *playback.ClientPlaybackContextV3) {
	hls, ok := ctx.Deliveries[playback.DeliveryClassHLSV3]
	if !ok || !playback.HasFeatureV3(hls.Features, playback.FeatureHLSCopyMPEGTSV3) {
		return
	}
	hls.Features = playback.WithoutFeatureV3(hls.Features, playback.FeatureHLSCopyMPEGTSV3)
	ctx.Deliveries[playback.DeliveryClassHLSV3] = hls
}

func transportGenerationV3(sessionID, planID string) string {
	planSuffix := strings.TrimPrefix(planID, "plan:")
	if len(planSuffix) > 12 {
		planSuffix = planSuffix[:12]
	}
	return sessionID + "-" + planSuffix + "-" + uuid.NewString()[:8]
}

// attachSubtitleArtifactV3 republishes the plan's subtitle inventory with
// session-scoped URLs, then resolves the plan's selected ordinal against it and
// stamps that entry's URL onto the artifact. Publishing and resolution share one
// ordering implementation, so an artifact URL can never point at a different
// track than the inventory entry the client selected.
//
// The inventory is scoped unconditionally, not only when a track is selected:
// spec §8 makes it the authoritative track list and says a sidecar entry carries
// a `url` "once a session exists to scope it to" — which is true here for every
// entry, whatever the current selection is. Gating it on the selection published
// a URL-less menu whenever playback started with subtitles off, so a client
// building its picker from the inventory (the Cast receiver's text tracks, for
// one) had nothing fetchable to offer.
func (h *PlaybackHandler) attachSubtitleArtifactV3(ctx context.Context, sessionID string, file *models.MediaFile, plan *playback.PlanV3, selectedIndex int, recipe *playback.ExecutableRecipeV3, clientFeatures []string) error {
	if plan == nil || file == nil {
		return nil
	}
	var frozenDownloaded *subtitles.DownloadedSubtitle
	if recipe != nil && recipe.SubtitleSource == playback.SubtitleSourceDownloadedV3 {
		if h == nil || h.SubtitleRepo == nil || recipe.DownloadedSubtitleID <= 0 {
			return errors.New("the frozen downloaded subtitle is unavailable")
		}
		selected, err := h.SubtitleRepo.GetDownloadedSubtitle(ctx, recipe.DownloadedSubtitleID)
		if err != nil {
			return wrapSubtitleStoreErrorV3(err)
		}
		if selected == nil || selected.MediaFileID != file.ID {
			return errors.New("the frozen downloaded subtitle is unavailable for the selected media file")
		}
		frozenDownloaded = selected
	}
	inventory := playback.ScopeSubtitleInventoryV3(sessionID, file, plan.Subtitle.Inventory, clientFeatures)
	// A plan restored from JSON no longer carries the server-only downloaded
	// row IDs. Rebuild only in that case; a fresh plan stays on the exact
	// planning snapshot instead of listing a mutable repository twice.
	if playback.SubtitleInventoryNeedsDownloadedIdentityV3(plan.Subtitle.Inventory) {
		if h == nil || h.SubtitleRepo == nil {
			return errors.New("the downloaded subtitle inventory is unavailable")
		}
		downloaded, err := h.SubtitleRepo.ListDownloadedSubtitles(ctx, file.ID)
		if err != nil {
			return wrapSubtitleStoreErrorV3(err)
		}
		inventory = playback.ScopeSubtitleInventoryV3(sessionID, file, playback.BuildSubtitleInventoryV3(file, downloadedSubtitleEntriesV3(file, downloaded)), clientFeatures)
	}
	plan.Subtitle.Inventory = inventory
	// Only render and convert publish a client-fetchable artifact; off and
	// burn_in have none by definition. Clear rather than leave whatever the plan
	// arrived with: a seek reanchor replays record.CurrentPlan verbatim
	// (frozenSeekReanchorResultV3), so a plan that once rendered a sidecar would
	// otherwise keep republishing that artifact after the selection changed —
	// which is exactly the stale `mode: "off"` plus artifact pair observed in
	// the field, and enough for a client to alias the artifact onto the track it
	// is actually playing. An off decision carries no track either, so its
	// track_id goes with the artifact; burn_in keeps the track it burns in.
	if selectedIndex < 0 || (plan.Subtitle.Mode != playback.SubtitleRenderV3 && plan.Subtitle.Mode != playback.SubtitleConvertV3) {
		plan.Subtitle.Artifact = nil
		plan.Subtitle.Embedded = nil
		plan.Subtitle.HLSRendition = nil
		if plan.Subtitle.Mode == playback.SubtitleOffV3 {
			plan.Subtitle.TrackID = ""
		}
		return nil
	}
	item, ok := playback.SubtitleInventoryItemAtV3(inventory, selectedIndex)
	if !ok && frozenDownloaded == nil {
		return errors.New("selected subtitle artifact is absent from the frozen inventory")
	}
	if embedded := plan.Subtitle.Embedded; embedded != nil {
		if plan.Delivery != playback.DeliveryOriginalHTTPV3 || plan.Subtitle.Mode != playback.SubtitleRenderV3 || !ok || item.Source != playback.SubtitleSourceEmbeddedV3 || item.TrackID != plan.Subtitle.TrackID {
			return errors.New("invalid embedded subtitle route")
		}
		// selectedIndex is a published ordinal; the underlying container track
		// lives at the source ordinal the de-duplication assigns it.
		sourceIndex, mapped := playback.SubtitleInventoryOwnSourceIndexV3(file, selectedIndex)
		if !mapped {
			return errors.New("the selected embedded subtitle identity changed")
		}
		ordinal := sourceIndex - len(file.ExternalSubtitles)
		// A frozen route without a container track ID was selected by stream
		// index, so a container ID recorded on the file since (the Matroska
		// track number backfill) does not change the track the client plays.
		if ordinal < 0 || ordinal >= len(file.SubtitleTracks) || file.SubtitleTracks[ordinal].Index != embedded.StreamIndex ||
			(embedded.ContainerTrackID != "" && file.SubtitleTracks[ordinal].ContainerTrackID != embedded.ContainerTrackID) {
			return errors.New("the selected embedded subtitle identity changed")
		}
		plan.Subtitle.Artifact = nil
		return nil
	}
	if plan.Subtitle.HLSRendition != nil {
		// The track is carried by the HLS stream's own playlist; the
		// inventory keeps its sidecar URL for a later selection.
		if (plan.Delivery != playback.DeliveryTranscodeHLSV3 && plan.Delivery != playback.DeliveryRemuxHLSV3) || !ok || item.Source != playback.SubtitleSourceEmbeddedV3 || item.TrackID != plan.Subtitle.TrackID {
			return errors.New("invalid HLS subtitle rendition route")
		}
		plan.Subtitle.Artifact = nil
		return nil
	}
	if frozenDownloaded == nil && item.URL == "" {
		return fmt.Errorf("subtitle track %d is %s and has no fetchable artifact", selectedIndex, item.Delivery)
	}
	format := strings.ToLower(item.Codec)
	source := item.Source
	url := item.URL
	if frozenDownloaded != nil {
		format = strings.ToLower(string(frozenDownloaded.Format))
		source = playback.SubtitleSourceDownloadedV3
		ext := playback.SubtitleSidecarExtV3(format, source, clientFeatures)
		url = playback.DownloadedSubtitleStreamURLV3(sessionID, selectedIndex, ext, file.ID, frozenDownloaded.ID)
		// The plan's selected ordinal must advertise the same opaque URL as the
		// artifact even if another downloaded row was inserted before a seek.
		for index := range plan.Subtitle.Inventory {
			if plan.Subtitle.Inventory[index].CombinedIndex == selectedIndex {
				plan.Subtitle.Inventory[index].URL = url
				plan.Subtitle.Inventory[index].Codec = string(frozenDownloaded.Format)
				break
			}
		}
	}
	// Inventory codec names describe the source; artifact metadata describes
	// the bytes served at its URL (SRT/mov_text are delivered as WebVTT unless
	// the client negotiated original SRT).
	format = strings.TrimPrefix(playback.SubtitleSidecarExtV3(format, source, clientFeatures), ".")
	mime := subtitleMIMEV3(format)
	if plan.Subtitle.Mode == playback.SubtitleConvertV3 {
		format = playback.SubtitleFormatVTTV3
		mime = playback.SubtitleMIMEVTTV3
		url = forceSubtitleExtensionV3(url, playback.SubtitleExtVTTV3)
	}
	plan.Subtitle.Artifact = &playback.SubtitleArtifactV3{URL: url, MIMEType: mime, Format: format, TimingOriginSeconds: 0}
	return nil
}

// downloadedSubtitleInventoryWithErrorV3 lists downloaded/AI tracks and
// propagates any repository read failure so callers can fail closed instead of
// silently serving a partial inventory.
func (h *PlaybackHandler) downloadedSubtitleInventoryWithErrorV3(ctx context.Context, file *models.MediaFile) ([]playback.SubtitleInventoryEntryV3, error) {
	if h == nil || h.SubtitleRepo == nil || file == nil {
		return nil, nil
	}
	downloaded, err := h.SubtitleRepo.ListDownloadedSubtitles(ctx, file.ID)
	if err != nil {
		return nil, err
	}
	return downloadedSubtitleEntriesV3(file, downloaded), nil
}

// downloadedSubtitleInventoryV3 lists the downloaded and AI-generated tracks
// that follow the file's own tracks in the combined-ordinal space. The
// repository orders by created_at, so the ordinals it produces are stable. A
// failed lookup lists nothing, so planning carries on as if the file had no
// such tracks; listDownloadedSubtitlesV3 reports the failure instead.
func (h *PlaybackHandler) downloadedSubtitleInventoryV3(ctx context.Context, file *models.MediaFile) []playback.SubtitleInventoryEntryV3 {
	entries, _ := h.listDownloadedSubtitlesV3(ctx, file)
	return entries
}

// listDownloadedSubtitlesV3 is downloadedSubtitleInventoryV3 that reports a
// failed lookup, wrapped as a subtitle-store outage.
func (h *PlaybackHandler) listDownloadedSubtitlesV3(ctx context.Context, file *models.MediaFile) ([]playback.SubtitleInventoryEntryV3, error) {
	if h == nil || h.SubtitleRepo == nil || file == nil {
		return nil, nil
	}
	downloaded, err := h.SubtitleRepo.ListDownloadedSubtitles(ctx, file.ID)
	if err != nil {
		return nil, wrapSubtitleStoreErrorV3(err)
	}
	return downloadedSubtitleEntriesV3(file, downloaded), nil
}

// selectsDownloadedSubtitleV3 reports whether request selects a subtitle past
// file's own external and embedded tracks, the range downloaded subtitles
// occupy.
func selectsDownloadedSubtitleV3(file *models.MediaFile, request playback.StartRequestV3) bool {
	if file == nil {
		return false
	}
	index := -1
	if request.SubtitleTrackIndex != nil {
		index = *request.SubtitleTrackIndex
	} else if request.SubtitleTrackID != "" {
		if fileID, kind, ordinal, ok := playback.ParseTrackIDV3(request.SubtitleTrackID); ok && kind == subtitleTrackKindV3 && fileID == file.ID {
			index = ordinal
		}
	}
	return index >= len(file.ExternalSubtitles)+len(file.SubtitleTracks)
}

// downloadedSubtitleEntriesV3 converts downloaded rows into inventory entries
// at the ordinals that follow the file's external and embedded tracks.
func downloadedSubtitleEntriesV3(file *models.MediaFile, downloaded []subtitles.DownloadedSubtitle) []playback.SubtitleInventoryEntryV3 {
	if file == nil {
		return nil
	}
	base := len(file.ExternalSubtitles) + len(file.SubtitleTracks)
	result := make([]playback.SubtitleInventoryEntryV3, 0, len(downloaded))
	for index, value := range downloaded {
		result = append(result, playback.SubtitleInventoryEntryV3{
			CombinedIndex:        base + index,
			Codec:                string(value.Format),
			Source:               playback.SubtitleSourceDownloadedV3,
			Language:             value.Language,
			Label:                downloadedSubtitleLabelV3(value),
			HearingImpaired:      value.HearingImpaired,
			DownloadedSubtitleID: value.ID,
		})
	}
	return result
}

func downloadedSubtitleLabelV3(value subtitles.DownloadedSubtitle) string {
	if value.ReleaseName == "" && value.Provider == "" {
		return ""
	}
	return value.ReleaseName + " (" + value.Provider + ")"
}

// stripClientSuppliedAutomatic clears the server-owned `Automatic` provenance
// marker on a decoded client replan request. Reconciliation builds its own
// replan in-process with the marker already set; that path never passes
// through this function. A client that forges the marker would otherwise
// impersonate server reconciliation and suppress its own preference
// persistence, so the inbound boundary drops whatever arrived on the wire.
func stripClientSuppliedAutomatic(req *playback.ReplanRequestV3) {
	if req == nil {
		return
	}
	req.Automatic = ""
}

// HandleReplanPlaybackV3 provides persistent idempotency and preserves the old
// transport until a successor has entered its startup state and the new plan is
// durably committed.
func (h *PlaybackHandler) HandleReplanPlaybackV3(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 || profileID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication and profile are required")
		return
	}
	body, err := readBoundedV3Body(w, r, maxPlaybackV3BodyBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	response, err := h.replanPlaybackApplicationV3(r, chiURLParamV3(r, "session_id"), body)
	if err != nil {
		writePlaybackOperationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, frozenV1DecisionV3(response))
}

// replanAnswerAuditV3 carries the facts the replan entry point needs to account
// for an answer the decision log did not cover: the operation, the durable
// attempt, and whether executeReplanV3 emitted the decision line. Every field
// comes from a read the request already performs, so the answer line costs no
// extra store round trip.
type replanAnswerAuditV3 struct {
	sessionID      string
	operation      playback.ReplanOperationV3
	record         *playback.AttemptRecordV3
	decisionLogged bool
	replayed       bool
}

type replanAnswerAuditContextKeyV3 struct{}

func withReplanAnswerAuditV3(ctx context.Context, audit *replanAnswerAuditV3) context.Context {
	return context.WithValue(ctx, replanAnswerAuditContextKeyV3{}, audit)
}

func markReplanDecisionLoggedV3(ctx context.Context) {
	if audit, ok := ctx.Value(replanAnswerAuditContextKeyV3{}).(*replanAnswerAuditV3); ok && audit != nil {
		audit.decisionLogged = true
	}
}

// Log attribute keys and values for the replan-answered line. They are named
// so the repeated literals stay out of the changed lines (goconst) and every
// occurrence cannot drift from the others.
const (
	replanLogKeyOperationV3 = "operation"
	replanLogKeySessionIDV3 = "session_id"
	replanOutcomeErrorV3    = "error"
)

// logReplanAnsweredUnchangedV3 emits the one line that accounts for a replan
// response the decision log did not cover: an idempotent replay of a committed
// decision, a seek that resolves to the position the plan already serves, or a
// request refused before execution. Production saw 46 of 48 replan calls answer
// 200 with no line at all, which made a client's seek loop impossible to
// reconstruct from server logs alone.
func logReplanAnsweredUnchangedV3(ctx context.Context, audit *replanAnswerAuditV3, response playback.DecisionResponseV3, err error, elapsed time.Duration) {
	if audit == nil {
		return
	}
	outcome := "no_change"
	reason := "no_decision"
	var status int
	switch {
	case err != nil:
		outcome = replanOutcomeErrorV3
		reason = "internal_error"
		var operationErr *PlaybackOperationError
		if errors.As(err, &operationErr) {
			reason = operationErr.Code
			status = operationErr.Status
		}
	case response.Outcome == playback.OutcomeSeekNoMovementV3:
		reason = string(playback.OutcomeSeekNoMovementV3)
	case audit.replayed && response.PlaybackPlan != nil:
		outcome = "plan"
		reason = "idempotent_replay"
	case audit.replayed && response.Terminal != nil:
		outcome = "terminal"
		reason = "idempotent_replay"
	case response.Terminal != nil:
		// A terminal executeReplanV3 chose but did not route through its
		// decision line (the candidate-rotation exhaustion path).
		outcome = "terminal"
		reason = response.Terminal.Reason
	case response.PlaybackPlan != nil:
		outcome = "plan"
		reason = "no_decision_logged"
	}
	decisionReason := ""
	requestedFileID, effectiveFileID := 0, 0
	if response.PlaybackPlan != nil {
		decisionReason = response.PlaybackPlan.DecisionReason
		requestedFileID = response.PlaybackPlan.RequestedMediaFileID
		effectiveFileID = response.PlaybackPlan.EffectiveMediaFileID
	}
	if audit.record != nil {
		if requestedFileID <= 0 {
			requestedFileID = audit.record.RequestedMediaFileID
		}
		if effectiveFileID <= 0 {
			effectiveFileID = audit.record.EffectiveMediaFileID
		}
	}
	attrs := []any{
		logComponentKey, playbackLogValueV3,
		"outcome", outcome,
		"reason", reason,
		"decision_reason", decisionReason,
		replanLogKeyOperationV3, string(audit.operation),
		replanLogKeySessionIDV3, audit.sessionID,
		"requested_file_id", requestedFileID,
		"effective_file_id", effectiveFileID,
		"duration_ms", elapsed.Milliseconds(),
	}
	if status != 0 {
		attrs = append(attrs, "status", status)
	}
	slog.InfoContext(ctx, "playback replan answered unchanged", attrs...)
}

// replanPlaybackApplicationV3 is the replan application shared by the v1
// handler and the v2 service seam: idempotent on replan_request_id + body
// digest, serialized per session, and atomic between the durable plan and the
// live transport. Errors are *PlaybackOperationError.
func (h *PlaybackHandler) replanPlaybackApplicationV3(r *http.Request, sessionID string, body []byte) (response playback.DecisionResponseV3, err error) {
	started := time.Now()
	audit := &replanAnswerAuditV3{sessionID: sessionID}
	r = r.WithContext(withReplanAnswerAuditV3(r.Context(), audit))
	defer func() {
		if audit.decisionLogged {
			return
		}
		logReplanAnsweredUnchangedV3(r.Context(), audit, response, err, time.Since(started))
	}()
	req, err := h.prepareReplanRequestV3(r, sessionID, body, audit)
	if err != nil {
		return playback.DecisionResponseV3{}, err
	}
	releaseSlot, err := h.acquireReplanSlotV3(r.Context())
	if err != nil {
		return playback.DecisionResponseV3{}, playbackOperationError(http.StatusServiceUnavailable, "replan_capacity_exhausted", "The server is replanning too many sessions; retry shortly")
	}
	defer releaseSlot()
	unlockReplan := h.lockReplanV3(sessionID)
	defer unlockReplan()
	unlockStore, err := h.PlanStoreV3.AcquireSessionLock(r.Context(), sessionID)
	if err != nil {
		return playback.DecisionResponseV3{}, playbackOperationError(http.StatusInternalServerError, "internal_error", "Failed to serialize the replan request")
	}
	defer unlockStore()
	record, lease, replay, err := h.reserveReplanLeaseV3(r, sessionID, body, req, audit)
	if err != nil {
		return playback.DecisionResponseV3{}, err
	}
	if record.PlaybackAttemptID != req.PlaybackAttemptID {
		return playback.DecisionResponseV3{}, playbackOperationError(http.StatusConflict, "stale_playback_plan", "The failed plan is no longer current")
	}
	if replay != nil {
		return *replay, nil
	}
	// Replan feature advertisement is optional. Validate transformations against
	// the durable start-time features when the client omits the unchanged list;
	// otherwise a valid replan can be rejected before executeReplanV3 gets the
	// chance to perform the same merge.
	if req.ClientFeatures == nil {
		req.ClientFeatures = append([]string(nil), record.NormalizedRequest.ClientFeatures...)
	}
	// The attempt-sticky features are fixed at start. Neither an omitted/empty
	// feature list nor a later opt-in can add or drop one mid-attempt: media
	// authentication would leave two security contracts alive for one session
	// (a legacy URL from an earlier plan stays usable until its signed recipe
	// expires), and a dropped software-decode opt-in would silently convert a
	// direct route into a transcode and persist that downgrade into the durable
	// normalized request. Stop/start is the explicit boundary for both.
	//
	// This is the single place attempt stickiness is enforced: everything
	// downstream — the durable normalized request, the transport's media-auth
	// mode, the planner's evidence tiers — reads the pinned list.
	req.ClientFeatures = playback.PinAttemptStickyFeaturesV3(req.ClientFeatures, record.NormalizedRequest.ClientFeatures)
	playback.PinHLSVariableSubstitutionV3(&req.ClientPlaybackContext, record.NormalizedRequest)
	if err := req.Validate(); err != nil {
		return playback.DecisionResponseV3{}, playbackOperationError(http.StatusBadRequest, "bad_request", "Invalid replan request")
	}
	if _, err := h.sessionMgr.GetSession(sessionID); err != nil {
		return playback.DecisionResponseV3{}, replanSessionNotFoundV3()
	}
	leaseCompleted := false
	defer func() {
		if leaseCompleted {
			return
		}
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), replanReleaseTimeoutV3)
		defer cancel()
		if err := h.PlanStoreV3.ReleaseReplan(releaseCtx, sessionID, req.ReplanRequestID, lease.LeaseToken); err != nil {
			slog.ErrorContext(r.Context(), "protocol v3 replan lease release failed", "component", "api", "session", sessionID, "replan_request_id", req.ReplanRequestID, "error", err)
		}
	}()
	if record.CurrentPlanID != req.FailedPlanID {
		return playback.DecisionResponseV3{}, playbackOperationError(http.StatusConflict, "stale_playback_plan", "The failed plan is no longer current")
	}
	// The replan budget must cover the declared virtual resolution budgets
	// (60s startup / 15s probe) without allowing hangs; 30s is a bounded value
	// that fits a slow provider's synchronous rehydration while still failing
	// fast on a genuinely stuck provider.
	replanCtx, cancelReplan := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancelReplan()
	// A viewer who re-armed Auto mid-session states it on the replan, so a
	// later dead-source recovery rotates even though the session started
	// explicit. Applying it before execution is what lets executeReplanV3's
	// autoFallbackForSession read the same intent the client is showing; it is
	// also written to the durable normalized request so a reconstructed session
	// keeps the negotiated policy instead of reverting to the start request. A
	// policy the session cannot adopt refuses the replan rather than continuing
	// with a stale intent.
	//
	// The apply is speculative: executeReplanV3 may still fail, and the live
	// flag is read mid-execution, so capture the prior policy first and restore
	// both halves on every failure path below. Only a replacement plan that
	// actually commits keeps the new policy, which is when live and durable
	// already agree on it.
	autoFallbackRollback := h.captureAutoFallbackRollbackV3(sessionID, record)
	autoFallbackCommitted := false
	defer func() {
		if !autoFallbackCommitted {
			autoFallbackRollback.restore()
		}
	}()
	if err := h.renegotiateAutoFallbackV3(sessionID, record, req); err != nil {
		slog.WarnContext(r.Context(), "protocol v3 replan auto-fallback re-negotiation failed",
			"component", "api", "session", sessionID, "error", err)
		if errors.Is(err, playback.ErrSessionNotFound) {
			return playback.DecisionResponseV3{}, replanSessionNotFoundV3()
		}
		return playback.DecisionResponseV3{}, playbackOperationError(http.StatusInternalServerError, "internal_error", "Failed to apply the version fallback policy")
	}
	response, updated, transport, replanErr := h.executeReplanV3(r.WithContext(replanCtx), record, req)
	if replanErr != nil {
		if transport != nil {
			transport.rollback()
		}
		var persisted bool
		response, persisted, err = h.persistReplanTerminalV3(r, sessionID, req, record, lease, replanErr, autoFallbackRollback)
		leaseCompleted = persisted
		return response, err
	}

	var committed bool
	response, committed, err = h.commitReplanApplicationV3(r, sessionID, req, record, lease, response, updated, transport)
	leaseCompleted = committed
	autoFallbackCommitted = committed
	return response, err
}

// prepareReplanRequestV3 validates a replan body and loads the attempt before
// the handler queues on the replan capacity bound and per-session locks. Both
// the preflight and the attempt read are early-outs: the attempt is re-read
// under the locks and CompleteReplan's compare-and-swap stays the final
// authority.
func (h *PlaybackHandler) prepareReplanRequestV3(r *http.Request, sessionID string, body []byte, audit *replanAnswerAuditV3) (playback.ReplanRequestV3, error) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 || profileID == "" {
		return playback.ReplanRequestV3{}, playbackOperationError(http.StatusUnauthorized, "unauthorized", "Authentication and profile are required")
	}
	var req playback.ReplanRequestV3
	if err := json.Unmarshal(body, &req); err != nil {
		return playback.ReplanRequestV3{}, playbackOperationError(http.StatusBadRequest, "bad_request", "Invalid replan request")
	}
	if !isNativeAPIV2(r.Context()) {
		// As on start: /api/v1 ignores the buffering-quality fields and the
		// /api/v2-only delivery fields and features.
		req.BandwidthEstimateSource, req.ObservedThroughputKbps, req.LearnedLimit = "", nil, ""
		withoutNativeDeliveryFieldsV3(&req.ClientPlaybackContext)
		withoutNativeDeliveryFeaturesV3(&req.ClientPlaybackContext)
	}
	audit.operation = req.EffectiveOperation()
	// Automatic marks a server-built reconciliation replan. It is internal
	// provenance: a client that forges it would suppress preference
	// persistence and impersonate reconciliation, so strip whatever the
	// client sent and let the server set it only on its own path.
	stripClientSuppliedAutomatic(&req)
	// Reject malformed identity/bounds before doing any session lookup. When
	// client_features is omitted, temporarily allow the only validation rule
	// that depends on the durable start request; the authoritative merge and a
	// second full validation happen after the attempt is loaded below.
	// Validate is structural only — it neither normalizes nor drops a
	// capability — so this preflight cannot consume the un-negotiated
	// transformations the single post-merge normalization must later report.
	preflightReq := req
	if preflightReq.ClientFeatures == nil {
		preflightReq.ClientFeatures = []string{playback.FeatureClientVideoTransforms}
	}
	if err := preflightReq.Validate(); err != nil {
		return playback.ReplanRequestV3{}, playbackOperationError(http.StatusBadRequest, "bad_request", "Invalid replan request")
	}
	// Resolve the cheap attempt/session lookups before queueing for a replan
	// slot. A reaped in-memory session (or an expired attempt) must surface as
	// a fast 404 instead of waiting behind the replan capacity bound;
	// production saw an ~11s queue wait before the session_not_found verdict.
	// The store read is a single pooled query that releases its connection
	// immediately and holds none of the replan locks, so it cannot invert the
	// slot -> per-session mutex -> advisory-lock order below or starve a lock
	// holder's inner queries.
	record, err := h.PlanStoreV3.GetAttempt(r.Context(), sessionID)
	if err != nil {
		// A store outage must read as retryable, not as the session being
		// gone: clients tear playback down on session_not_found.
		if !errors.Is(err, playback.ErrSessionNotFound) {
			return playback.ReplanRequestV3{}, playbackOperationError(http.StatusInternalServerError, "internal_error", "Failed to load the playback attempt")
		}
		return playback.ReplanRequestV3{}, replanSessionNotFoundV3()
	}
	audit.record = record
	if record.UserID != userID || record.ProfileID != profileID {
		return playback.ReplanRequestV3{}, playbackOperationError(http.StatusForbidden, "forbidden", "Session belongs to another profile")
	}
	if record.StoppedAt != nil {
		// The stop may have landed after this request's fast precheck. An
		// administrator end answers 410 so the player exits; an expiry keeps
		// the 404 the player renews from.
		return playback.ReplanRequestV3{}, stoppedAttemptOperationErrorV3(sessionID, record)
	}
	if err := requireAttemptAPISurfaceV3(r.Context(), record, nil); err != nil {
		return playback.ReplanRequestV3{}, err
	}
	if record.PlaybackAttemptID != req.PlaybackAttemptID {
		return playback.ReplanRequestV3{}, playbackOperationError(http.StatusConflict, "stale_playback_plan", "The failed plan is no longer current")
	}
	// Replan feature advertisement is optional. An omitted list and an
	// explicitly empty list both mean "unchanged"; validate transformations
	// against the durable start-time features in either case, otherwise a valid
	// replan can be rejected before executeReplanV3 gets the chance to perform
	// the same merge.
	if len(req.ClientFeatures) == 0 {
		req.ClientFeatures = append([]string(nil), record.NormalizedRequest.ClientFeatures...)
	}

	// Attempt-sticky features negotiated on the original attempt stay pinned
	// across every replan of that attempt.
	req.ClientFeatures = playback.PinAttemptStickyFeaturesV3(req.ClientFeatures, record.NormalizedRequest.ClientFeatures)
	if err := req.Validate(); err != nil {
		return playback.ReplanRequestV3{}, playbackOperationError(http.StatusBadRequest, "bad_request", "Invalid replan request")
	}
	if _, err := h.sessionMgr.GetSession(sessionID); err != nil {
		return playback.ReplanRequestV3{}, replanSessionNotFoundV3()
	}
	return req, nil
}

// reserveReplanLeaseV3 re-reads the attempt under the replan locks and reserves
// the request's lease. A non-nil replay response means the request repeats a
// completed replan, which answers with the stored decision instead of
// executing again.
func (h *PlaybackHandler) reserveReplanLeaseV3(r *http.Request, sessionID string, body []byte, req playback.ReplanRequestV3, audit *replanAnswerAuditV3) (*playback.AttemptRecordV3, playback.ReplanLeaseV3, *playback.DecisionResponseV3, error) {
	// Re-read the attempt now that the replan locks are held. The fast 404 in
	// prepareReplanRequestV3 deliberately reads before queueing on the capacity
	// bound and the per-session locks, so a duplicate request that waited
	// behind a completing sibling still holds the pre-lock snapshot here.
	// Without this read, BeginReplan can return the sibling's
	// ReplanLeaseCompletedV3 and the stale record makes the handler reject the
	// replay as stale_playback_plan instead of returning the stored response;
	// BeginReplan/CompleteReplan would also build on a stale base revision. The
	// post-lock read is authoritative for both, and CompleteReplan stays the
	// final compare-and-swap.
	record, err := h.PlanStoreV3.GetAttempt(r.Context(), sessionID)
	if err != nil {
		// Match the fast 404: a session that vanished while this request waited
		// reads as gone, any other store failure as retryable.
		if !errors.Is(err, playback.ErrSessionNotFound) {
			return nil, playback.ReplanLeaseV3{}, nil, playbackOperationError(http.StatusInternalServerError, "internal_error", "Failed to load the playback attempt")
		}
		return nil, playback.ReplanLeaseV3{}, nil, replanSessionNotFoundV3()
	}
	audit.record = record
	var lease playback.ReplanLeaseV3
	// The durable attempt can outlive the in-memory session: a stop or an idle
	// reap can land while this request waits on the replan slot and the
	// per-session locks, between the pre-lock check and here. Re-check now so it
	// reads as the fast 404; otherwise the request would reserve a replan lease
	// and persist executeReplanV3's session_expired as a terminal 200, which the
	// web client does not rebuild from. CompleteReplan stays the final
	// compare-and-swap.
	if _, err := h.sessionMgr.GetSession(sessionID); err != nil {
		return nil, playback.ReplanLeaseV3{}, nil, replanSessionNotFoundV3()
	}
	digestBytes := sha256.Sum256(body)
	digest := hex.EncodeToString(digestBytes[:])
	lease, err = h.PlanStoreV3.BeginReplan(
		r.Context(),
		sessionID,
		req.ReplanRequestID,
		digest,
		record.CurrentReplanRequestID,
		time.Now().Add(replanLeaseDurationV3),
	)
	if errors.Is(err, playback.ErrIdempotencyKeyReusedV3) {
		return nil, playback.ReplanLeaseV3{}, nil, playbackOperationError(http.StatusConflict, "idempotency_key_reused", "The replan request ID was reused with different input")
	}
	if errors.Is(err, playback.ErrStaleReplanLeaseV3) {
		return nil, playback.ReplanLeaseV3{}, nil, playbackOperationError(http.StatusConflict, "stale_playback_plan", "A newer replacement plan is already active")
	}
	if err != nil {
		return nil, playback.ReplanLeaseV3{}, nil, playbackOperationError(http.StatusInternalServerError, "internal_error", "Failed to reserve the replan request")
	}
	if lease.State == playback.ReplanLeaseInFlightV3 {
		return nil, playback.ReplanLeaseV3{}, nil, playbackOperationError(http.StatusConflict, "replan_in_progress", "An identical replan is still in progress")
	}
	if lease.State != playback.ReplanLeaseCompletedV3 {
		return record, lease, nil, nil
	}
	if record.CurrentReplanRequestID != req.ReplanRequestID || !completedReplanResponseMatchesAttemptV3(lease.Response, record) {
		return nil, playback.ReplanLeaseV3{}, nil, playbackOperationError(http.StatusConflict, "stale_playback_plan", "A newer replacement plan is already active")
	}
	if _, err := h.sessionMgr.GetSession(sessionID); err != nil {
		return nil, playback.ReplanLeaseV3{}, nil, replanSessionNotFoundV3()
	}
	var replay playback.DecisionResponseV3
	if err := json.Unmarshal(lease.Response, &replay); err != nil {
		return nil, playback.ReplanLeaseV3{}, nil, playbackOperationError(http.StatusInternalServerError, "internal_error", "Failed to decode the completed replan decision")
	}
	// The cached durable decision was minted for the surface that first
	// completed it. A replay through a different surface must not re-emit
	// v2-only contract additions (today delivery_change) on the frozen v1
	// bridge: clear the marker on a response-local plan copy here, at the same
	// response boundary the fresh v1 execution uses.
	if !isNativeAPIV2(r.Context()) && replay.PlaybackPlan != nil {
		replay.PlaybackPlan.DeliveryChange = nil
	}
	audit.replayed = true
	return record, lease, &replay, nil
}

// persistReplanTerminalV3 records the terminal decision executeReplanV3
// produced and answers it. A session that died mid-execution reads as the fast
// 404 instead, and an attempt a concurrent stop fenced keeps the replan's own
// retryable virtual-source terminal. The returned bool reports whether the
// lease was completed, so the caller's deferred release stays off.
func (h *PlaybackHandler) persistReplanTerminalV3(
	r *http.Request,
	sessionID string,
	req playback.ReplanRequestV3,
	record *playback.AttemptRecordV3,
	lease playback.ReplanLeaseV3,
	replanErr *transportErrorV3,
	autoFallbackRollback autoFallbackRollbackV3,
) (playback.DecisionResponseV3, bool, error) {
	// The caller's post-lock live-session check is an early-out only: it holds
	// no lock across BeginReplan or executeReplanV3, so a stop or idle reap can
	// still land before execution reads the session. Persisting that
	// session_expired through CompleteReplan would store it as the terminal
	// decision and answer HTTP 200 — a response the web client does not rebuild
	// from. Translate it to the same fast 404 and leave the lease to the
	// deferred non-terminal ReleaseReplan, so no completed 200 terminal for a
	// dead session is ever persisted. CompleteReplan stays the final
	// compare-and-swap for every other terminal path.
	if replanErr.reason == "session_expired" {
		return playback.DecisionResponseV3{}, false, replanSessionNotFoundV3()
	}
	response := playback.NewTerminalResponseV3(replanErr.reason, replanErr.message, replanErr.retryable)
	encoded := marshalReplanDecisionV3(response)
	// The failed replan did not adopt the stated policy, so the terminal record
	// must persist the policy in force before it, not the one this request
	// tried. Restore the durable half now, before the record is copied into the
	// terminal decision; the deferred restore covers the live half and
	// re-applies harmlessly.
	autoFallbackRollback.restore()
	terminalRecord := *record
	terminalRecord.CurrentReplanRequestID = req.ReplanRequestID
	if err := h.PlanStoreV3.CompleteReplan(r.Context(), sessionID, req.ReplanRequestID, lease.LeaseToken, record.CurrentReplanRequestID, encoded, terminalRecord); err != nil {
		// A stop landed on the attempt while this replan ran. A stop verdict
		// persisted as a 200 terminal is a response the web client does not
		// rebuild from, so the fast 404 is the right answer for a
		// session-expired/stop terminal. A provider stall is not a stop
		// verdict: the replan computed its own retryable virtual-source
		// terminal, and answering 404 collapses that into "session gone",
		// forcing every client to tear down and re-resume. Return the terminal
		// the replan already produced so the retryable virtual-source signal
		// survives the concurrent stop; the durable attempt is fenced, so
		// nothing is persisted.
		if errors.Is(err, playback.ErrAttemptStoppedV3) && virtualSourceStallTerminalV3(replanErr.reason) {
			return response, false, nil
		}
		return playback.DecisionResponseV3{}, false, replanTerminalPersistErrorV3(err, replanErr)
	}
	return response, true, nil
}

// replanTerminalPersistErrorV3 maps a failed terminal CompleteReplan to the
// client answer: a concurrent stop reads as the fast 404, and route-capacity
// exhaustion stays retryable rather than collapsing into an internal error.
func replanTerminalPersistErrorV3(completeErr error, replanErr *transportErrorV3) error {
	switch {
	case errors.Is(completeErr, playback.ErrAttemptStoppedV3):
		return replanSessionNotFoundV3()
	case errors.Is(completeErr, playback.ErrReplanSupersededV3):
		return playbackOperationError(http.StatusConflict, "stale_playback_plan", "A newer replacement plan is already active")
	case isRouteCapacityUnavailableError(replanErr):
		return playbackOperationError(http.StatusServiceUnavailable, string(noderouting.OutcomeCapacityUnavailable), "The playback route capacity is temporarily unavailable; retry shortly")
	default:
		return playbackOperationError(http.StatusInternalServerError, "internal_error", "Failed to persist the terminal replan decision")
	}
}

// marshalReplanDecisionV3 encodes a replan decision for the durable store. The
// decision carries float64 timeline fields, so the encoder can refuse a
// non-finite value; the caller keeps persisting the empty body it did before
// this helper existed rather than inventing a different terminal.
func marshalReplanDecisionV3(response playback.DecisionResponseV3) []byte {
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil
	}
	return encoded
}

// commitReplanApplicationV3 makes a computed replacement durable and installs
// it on the live transport. The returned bool reports whether the durable plan
// committed: the caller then keeps the speculative version-fallback policy and
// skips the deferred lease release, even when the later transport commit fails
// and aborts the live session.
func (h *PlaybackHandler) commitReplanApplicationV3(
	r *http.Request,
	sessionID string,
	req playback.ReplanRequestV3,
	record *playback.AttemptRecordV3,
	lease playback.ReplanLeaseV3,
	response playback.DecisionResponseV3,
	updated playback.AttemptRecordV3,
	transport *preparedTransportV3,
) (playback.DecisionResponseV3, bool, error) {
	updated.CurrentReplanRequestID = req.ReplanRequestID
	encoded := marshalReplanDecisionV3(response)
	var rollbackSession func() error
	if transport != nil && transport.applySession != nil {
		var err error
		rollbackSession, err = transport.applySession()
		if err != nil {
			// With no session to restore, only the transport can fail.
			rollbackErr, _ := rollbackFailedReplanV3(transport, nil)
			if rollbackErr != nil {
				slog.ErrorContext(r.Context(), "protocol v3 unapplied replacement transport cancellation failed", "session", sessionID, "error", rollbackErr)
				return playback.DecisionResponseV3{}, false, playbackOperationError(http.StatusInternalServerError, "internal_error", "Failed to cancel the unapplied replacement transport")
			}
			if isRouteCapacityUnavailableError(err) {
				return playback.DecisionResponseV3{}, false, playbackOperationError(http.StatusServiceUnavailable, string(noderouting.OutcomeCapacityUnavailable), "The playback route capacity is temporarily unavailable; retry shortly")
			}
			return playback.DecisionResponseV3{}, false, playbackOperationError(http.StatusInternalServerError, "internal_error", "Failed to commit the live replacement session")
		}
	}
	if err := h.PlanStoreV3.CompleteReplan(r.Context(), sessionID, req.ReplanRequestID, lease.LeaseToken, record.CurrentReplanRequestID, encoded, updated); err != nil {
		if errors.Is(err, playback.ErrAttemptStoppedV3) {
			// A stop landed on another replica while this replan ran: tear the
			// replacement transport and the local session down and report the
			// session gone; the deny marker refuses the plan anyway. The abort
			// below makes a failed session restore moot.
			transportRollbackErr, _ := rollbackFailedReplanV3(transport, rollbackSession)
			if transportRollbackErr != nil {
				slog.ErrorContext(r.Context(), "protocol v3 replacement transport cancellation failed", "session", sessionID, "error", transportRollbackErr)
			}
			_ = h.abortPlaybackSessionByID(context.WithoutCancel(r.Context()), sessionID)
			return playback.DecisionResponseV3{}, false, replanSessionNotFoundV3()
		}
		transportRollbackErr, sessionRollbackErr := rollbackFailedReplanV3(transport, rollbackSession)
		if transportRollbackErr != nil {
			slog.ErrorContext(r.Context(), "protocol v3 replacement transport cancellation failed", "session", sessionID, "error", transportRollbackErr)
			return playback.DecisionResponseV3{}, false, playbackOperationError(http.StatusInternalServerError, "internal_error", "Failed to cancel the replacement transport")
		}
		if sessionRollbackErr != nil {
			slog.ErrorContext(r.Context(), "protocol v3 replacement rollback failed", "session", sessionID, "error", sessionRollbackErr)
			_ = h.stopPlaybackSessionByID(context.WithoutCancel(r.Context()), sessionID, "")
		}
		if errors.Is(err, playback.ErrReplanSupersededV3) {
			return playback.DecisionResponseV3{}, false, playbackOperationError(http.StatusConflict, "stale_playback_plan", "A newer replacement plan is already active")
		}
		if isRouteCapacityUnavailableError(err) {
			return playback.DecisionResponseV3{}, false, playbackOperationError(http.StatusServiceUnavailable, string(noderouting.OutcomeCapacityUnavailable), "The playback route capacity is temporarily unavailable; retry shortly")
		}
		return playback.DecisionResponseV3{}, false, playbackOperationError(http.StatusInternalServerError, "internal_error", "Failed to commit the replacement plan")
	}
	// The replacement plan is durable, so the speculative policy application is
	// now the adopted policy: live and durable agree and the deferred rollback
	// must not undo it. A later transport commit failure aborts the live session
	// without reverting the durable plan.
	if transport != nil {
		if commitErr := transport.commit(); commitErr != nil {
			_ = h.abortPlaybackSessionByID(context.WithoutCancel(r.Context()), sessionID)
			return playback.DecisionResponseV3{}, true, playbackOperationError(http.StatusServiceUnavailable, commitErr.reason, commitErr.message)
		}
		h.recordSessionRecipeV3(r.Context(), updated.CurrentPlanID, transport)
		if transport.afterDurableCommit != nil {
			transport.afterDurableCommit()
		}
	}
	h.raceCopySafetyV3(updated.EffectiveMediaFileID, response.PlaybackPlan)
	return response, true, nil
}

// replanSessionNotFoundV3 is the v1 playback_session_not_found body; the v2
// adapter maps its status to the not-found problem.
func replanSessionNotFoundV3() *PlaybackOperationError {
	return playbackOperationError(http.StatusNotFound, playbackSessionNotFoundErrorCode, "Playback session not found")
}

// virtualSourceStallTerminalV3 reports whether a replan failure reason is a
// retryable virtual-source stall rather than a verdict on the session. These
// reasons are the resolver's honest "the provider did not answer / the pinned
// candidate is absent" outcome; a client rebuilds a plan or retries from them.
// They must not be collapsed into playback_session_not_found by a concurrent
// stop, because the stall — not the stop — is what the replan observed.
func virtualSourceStallTerminalV3(reason string) bool {
	switch reason {
	case virtualSourceUnavailableReasonV3, replanVirtualInputTimeoutReasonV3, seekReanchorEvidenceUnavailableReasonV3:
		return true
	default:
		return false
	}
}

// isRouteCapacityUnavailableError reports whether an error names route-capacity
// exhaustion so the replan handler keeps the client-visible code retryable (503)
// instead of collapsing it into a 500 internal_error. It matches the structured
// transport reason on the error or its cause, and falls back to the sentinel
// string the candidate-exhaustion join carries.
func isRouteCapacityUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	var transportErr *transportErrorV3
	if errors.As(err, &transportErr) {
		if transportErr.reason == string(noderouting.OutcomeCapacityUnavailable) {
			return true
		}
		if transportErr.cause != nil && strings.Contains(transportErr.cause.Error(), string(noderouting.OutcomeCapacityUnavailable)) {
			return true
		}
	}
	return strings.Contains(err.Error(), string(noderouting.OutcomeCapacityUnavailable))
}

// rollbackFailedReplanV3 cancels a remotely admitted replacement before it
// restores the predecessor session. If cancellation cannot be confirmed, the
// successor remains live and authoritative so a later stop can retry it.
func rollbackFailedReplanV3(transport *preparedTransportV3, rollbackSession func() error) (transportErr, sessionErr error) {
	if transport != nil { //nolint:nestif // rollbackFailedReplanV3 exceeds Vio's nestif complexity-4 limit; refactor tracked separately
		if transport.rollbackRequired != nil {
			if err := transport.rollbackRequired(); err != nil {
				return err, nil
			}
		} else {
			transport.rollback()
		}
	}
	if rollbackSession != nil {
		return nil, rollbackSession()
	}
	return nil, nil
}

// raceCopySafetyV3 resolves an unknown H.264 copy-safety verdict behind a plan
// that stream-copies video. It is called after the durable commit on both the
// start and replan paths, so the scan only ever chases a route a client was
// actually handed, and it returns immediately — no response waits on it.
func (h *PlaybackHandler) raceCopySafetyV3(fileID int, plan *playback.PlanV3) {
	if h == nil || h.CopySafetyRacer == nil || fileID <= 0 || plan == nil {
		return
	}
	h.CopySafetyRacer.RaceScanForPlan(fileID, plan)
}

// replanPlanningV3 is what every planning pass of one replan shares: the
// requested edition (loaded, and as the planner names it), the version playing
// and its request, for an output change to fall back to, and the lower
// version, settings and failed routes the planner works from.
type replanPlanningV3 struct {
	operation                    playback.ReplanOperationV3
	requestedFile                *models.MediaFile
	plannerRequestedFile         *models.MediaFile
	currentEffectiveFile         *models.MediaFile
	currentEffectiveStart        playback.StartRequestV3
	lowerVersion                 *playback.LowerVersionV3
	settings                     playback.PlannerSettingsV3
	attemptedKeys                []string
	keptActiveEditionForSubtitle bool
	// desired is the attempt's track intent; nil remaps from the effective
	// file's selection instead.
	desired *playback.DesiredTracksV3
	// renditionTracks answers which tracks of the file playing the source's
	// index reads, from the on-demand renditions its stream already serves,
	// so a track change plans the same renditions without reading the index
	// again and can keep the stream. Nil reads the index.
	renditionTracks *pinnedRenditionTracksV3
	// audioShapeRetry rebuilds the failed delivery's audio as stereo after an
	// audio-output initialization failure.
	audioShapeRetry bool
	// audioInitFailureDetail names that failure so the planner can report the
	// specific terminal when every route is exhausted.
	audioInitFailureDetail string
}

// pinnedRenditionTracksV3 is the set of on-demand rendition tracks the
// running HLS stream of file fileID serves, which may be none.
type pinnedRenditionTracksV3 struct {
	fileID   int
	indexed  *playback.CueIndexedSubtitles
	onDemand bool
}

// pinnedRenditionTracksForRecordV3 pins the on-demand renditions the attempt's
// current HLS stream serves, or returns nil when its plan isn't one.
func pinnedRenditionTracksForRecordV3(record *playback.AttemptRecordV3) *pinnedRenditionTracksV3 {
	if record == nil || !isHLSDeliveryV3(record.CurrentPlan.Delivery) || !record.FrozenRecipe.ValidFor(record.CurrentPlan) {
		return nil
	}
	frozen := record.FrozenRecipe
	tracks := make(map[uint64]bool, len(frozen.SubtitleRenditionTracks))
	for _, track := range frozen.SubtitleRenditionTracks {
		tracks[track.TrackNumber] = true
	}
	return &pinnedRenditionTracksV3{
		fileID:   record.EffectiveMediaFileID,
		indexed:  &playback.CueIndexedSubtitles{Tracks: tracks, Source: frozen.SubtitleRenditionSource},
		onDemand: len(tracks) > 0,
	}
}

// cueIndexedSubtitleTracks is the planner seam for a replan of start on file.
// While the replan stays on the file playing, it answers the renditions the
// stream serves without reading the index: a stream serving none then keeps
// serving none, so turning subtitles off or choosing a sidecar keeps it. A
// stream without on-demand renditions whose replan selects an embedded text
// track restarts anyway, for its single rendition, so that replan reads the
// index and may move to on-demand renditions. Nil reads the index.
func (pinned *pinnedRenditionTracksV3) cueIndexedSubtitleTracks(file *models.MediaFile, start playback.StartRequestV3) func() *playback.CueIndexedSubtitles {
	if pinned == nil || file == nil || file.ID != pinned.fileID ||
		!pinned.onDemand && selectsEmbeddedTextSubtitleV3(file, start) {
		return nil
	}
	return func() *playback.CueIndexedSubtitles { return pinned.indexed }
}

// selectsEmbeddedTextSubtitleV3 reports whether start selects an embedded
// track of file that an HLS rendition can carry.
func selectsEmbeddedTextSubtitleV3(file *models.MediaFile, start playback.StartRequestV3) bool {
	index := -1
	switch {
	case start.SubtitleTrackIndex != nil:
		index = *start.SubtitleTrackIndex
	case start.SubtitleTrackID != "":
		if fileID, kind, ordinal, ok := playback.ParseTrackIDV3(start.SubtitleTrackID); ok && kind == subtitleTrackKindV3 && fileID == file.ID {
			index = ordinal
		}
	}
	ordinal := index - len(file.ExternalSubtitles)
	return index >= 0 && ordinal >= 0 && ordinal < len(file.SubtitleTracks) &&
		playback.SubtitleRenditionCodecSupported(file.SubtitleTracks[ordinal].Codec)
}

// onDemandSubtitleSwitchV3 reports a failure_recovery classified
// subtitle_track_changed on a stream serving on-demand renditions.
func onDemandSubtitleSwitchV3(record *playback.AttemptRecordV3, req playback.ReplanRequestV3) bool {
	return record != nil && req.EffectiveOperation() == playback.ReplanOperationFailureRecoveryV3 &&
		req.Failure.Classification == failureSubtitleTrackChangedV3 && len(record.FrozenRecipe.SubtitleRenditionTracks) > 0
}

// replanPassV3 is a replan's plan and how it degraded the viewer's audio and
// subtitle selection.
type replanPassV3 struct {
	alternateCandidateV3
	audioDegraded, subtitleDegraded, subtitleNotShown bool
}

// planReplanV3 plans a replan of effectiveFile for start. An output change the
// planner refuses falls back to the version playing, and a refusal another
// version can avoid falls back to another version (planReplanAlternateV3).
// audioDegraded is the audio selection's state before planning.
func (h *PlaybackHandler) planReplanV3(r *http.Request, p replanPlanningV3, start playback.StartRequestV3, effectiveFile *models.MediaFile, audioIndex int, audioDegraded bool) (replanPassV3, *transportErrorV3) {
	// An output change that falls back to the playing edition plans under the
	// same estimate, and so the same limit.
	currentEffectiveStart := p.currentEffectiveStart
	currentEffectiveStart.BandwidthEstimateKbps = start.BandwidthEstimateKbps
	currentEffectiveStart.BandwidthEstimateSource = start.BandwidthEstimateSource
	additional, inventoryErr := h.listDownloadedSubtitlesV3(r.Context(), effectiveFile)
	if inventoryErr != nil && (p.operation == playback.ReplanOperationTrackChangeV3 || p.operation == playback.ReplanOperationQualityChangeV3) && selectsDownloadedSubtitleV3(effectiveFile, start) {
		// A failed lookup says nothing about whether the selected track
		// exists. Planning without it would turn the subtitle off, and
		// later replans start from that plan's tracks, so it would stay
		// off for the rest of the session. The playing plan carries on
		// and the viewer can repeat the change.
		return replanPassV3{}, subtitleArtifactErrorV3("Downloaded subtitles are temporarily unavailable.", inventoryErr)
	}
	result, toneMapCapabilityErr := h.planPlaybackWithCapabilitiesV3(r.Context(), playback.PlannerInputV3{Request: start, RequestedFile: p.plannerRequestedFile, EffectiveFile: effectiveFile, LowerVersion: lowerVersionForFileV3(p.lowerVersion, effectiveFile), ServerBitrateCapKbps: serverBitrateCapV3(r.Context()), AudioTrackIndex: audioIndex, Settings: p.settings, Registry: h.transformationRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), effectiveFile), Now: time.Now(), AttemptedKeys: p.attemptedKeys, AdditionalSubtitles: additional, AudioShapeRetry: p.audioShapeRetry, AudioInitFailureDetail: p.audioInitFailureDetail, CueIndexedSubtitleTracks: p.renditionTracks.cueIndexedSubtitleTracks(effectiveFile, start)})
	if p.operation == playback.ReplanOperationOutputChangeV3 && result.Terminal != nil && effectiveFile.ID != p.currentEffectiveFile.ID {
		// Returning to the requested edition is speculative during an output
		// refresh. Any terminal from that probe must fall back to the edition
		// already playing, not only HDR/alternate-selection terminals: its audio,
		// subtitle, or delivery constraints may still differ from the active file.
		start = currentEffectiveStart
		start.FileID = p.currentEffectiveFile.ID
		effectiveFile = p.currentEffectiveFile
		var err error
		audioIndex, audioDegraded, err = resolveV3AudioIndex(effectiveFile, start.AudioTrackID, start.AudioTrackIndex)
		if err != nil {
			return replanPassV3{}, &transportErrorV3{reason: trackUnavailableReasonV3, message: err.Error()}
		}
		result, toneMapCapabilityErr = h.planPlaybackWithCapabilitiesV3(r.Context(), playback.PlannerInputV3{Request: start, RequestedFile: p.plannerRequestedFile, EffectiveFile: effectiveFile, LowerVersion: lowerVersionForFileV3(p.lowerVersion, effectiveFile), ServerBitrateCapKbps: serverBitrateCapV3(r.Context()), AudioTrackIndex: audioIndex, Settings: p.settings, Registry: h.transformationRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), effectiveFile), Now: time.Now(), AttemptedKeys: p.attemptedKeys, AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), effectiveFile), AudioShapeRetry: p.audioShapeRetry, AudioInitFailureDetail: p.audioInitFailureDetail, CueIndexedSubtitleTracks: p.renditionTracks.cueIndexedSubtitleTracks(effectiveFile, start)})
	}
	planned := replanPassV3{alternateCandidateV3: alternateCandidateV3{file: effectiveFile, request: start, audioIndex: audioIndex, result: result, toneMapErr: toneMapCapabilityErr}, audioDegraded: audioDegraded}
	if start.AllowsAlternateVersions() && terminalAllowsAlternateFileV3(result.Terminal) && replanAllowsAlternateFileV3(p.operation, start.QualityPreference) {
		planned.alternateCandidateV3, planned.subtitleDegraded, planned.subtitleNotShown = h.planReplanAlternateV3(r, p, planned.alternateCandidateV3)
	}
	return planned, nil
}

// planReplanAlternateV3 replaces planned, which the planner refused for a
// reason another version can avoid, with the first authorized version that
// plans. A version that drops the viewer's subtitle is held back in case none
// keeps it; when every version that plays drops it, planned's own file without
// the subtitle comes first, and when none plays, the first refusal stands.
func (h *PlaybackHandler) planReplanAlternateV3(r *http.Request, p replanPlanningV3, planned alternateCandidateV3) (chosen alternateCandidateV3, subtitleDegraded, subtitleNotShown bool) { //nolint:funlen // planReplanAlternateV3 exceeds Vio's 100-line limit; refactor tracked separately
	start, effectiveFile, audioIndex, result, toneMapCapabilityErr := planned.request, planned.file, planned.audioIndex, planned.result, planned.toneMapErr
	accessFilter := requestAccessFilter(r)
	alternates, alternateErr := h.findAlternateFiles(r.Context(), p.requestedFile, accessFilter)
	if p.keptActiveEditionForSubtitle {
		// The requested edition was passed over only to keep the subtitle;
		// it can still play without it if nothing else can, whether or not
		// sibling discovery succeeded.
		alternates = append(alternates, p.requestedFile)
		alternateErr = nil
	}
	if alternateErr != nil {
		if result.Terminal != nil {
			if fallback := h.planWithoutLowerVersionV3(r.Context(), h.replanInputV3(r, p, start, effectiveFile, audioIndex)); fallback != nil {
				return *fallback, false, false
			}
		}
		return planned, false, false
	}
	baseStart := start
	baseEffectiveFile := effectiveFile
	baseAudioIndex := audioIndex
	var firstFailureResult playback.PlannerResultV3
	var firstFailureToneMapErr error
	var firstFailureFile *models.MediaFile
	var firstFailureStart playback.StartRequestV3
	firstFailureAudioIndex := 0
	var droppedSubtitle *alternateCandidateV3
	for _, alternate := range alternates {
		if alternate.ID == baseEffectiveFile.ID {
			continue
		}
		candidateFile := h.ensurePlaybackProbe(r.Context(), alternate)
		// The sibling was authorized on its stored resolution, which
		// its first probe can fill in above the viewer's ceiling.
		if !catalog.FileAllowedByAccess(candidateFile, accessFilter) {
			continue
		}
		candidateStart := baseStart
		var candidateAudioIndex int
		var candidateResult playback.PlannerResultV3
		var candidateToneMapErr error
		var subtitleDropped bool
		var err error
		if p.desired != nil {
			subtitleDropped, err = h.applyDesiredTracksV3(r.Context(), p.desired, candidateFile, &candidateStart)
			candidateAudioIndex = requestAudioIndexV3(candidateFile, candidateStart)
		} else {
			candidateAudioIndex = remapAudioIndexV3(baseEffectiveFile, candidateFile, baseAudioIndex)
			subtitleDropped, err = h.remapSubtitleSelectionV3(r.Context(), baseEffectiveFile, candidateFile, &candidateStart)
		}
		if err != nil {
			candidateResult = playback.PlannerResultV3{Terminal: &playback.TerminalV3{
				Reason:    terminalSubtitleUnavailableInVersionV3,
				Message:   "The selected subtitle track is unavailable in the fallback media version.",
				Retryable: false,
			}}
		} else {
			candidateStart.FileID = candidateFile.ID
			if err := preflightPlaybackFile(r.Context(), candidateFile, h.MissingMarker, h.EventsHub); err != nil {
				continue
			}
			candidateResult, candidateToneMapErr = h.planPlaybackWithCapabilitiesV3(r.Context(), playback.PlannerInputV3{Request: candidateStart, RequestedFile: p.plannerRequestedFile, EffectiveFile: candidateFile, LowerVersion: lowerVersionForFileV3(p.lowerVersion, candidateFile), ServerBitrateCapKbps: serverBitrateCapV3(r.Context()), AudioTrackIndex: candidateAudioIndex, Settings: p.settings, Registry: h.transformationRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), candidateFile), Now: time.Now(), AttemptedKeys: p.attemptedKeys, AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), candidateFile)})
		}
		if candidateResult.Terminal == nil && subtitleDropped {
			// Prefer a version that keeps the viewer's subtitle; hold
			// this one back in case none does. The requested edition,
			// listed last when it was passed over only for the
			// subtitle, outranks any other version held back.
			if droppedSubtitle == nil || candidateFile.ID == p.requestedFile.ID {
				droppedSubtitle = &alternateCandidateV3{file: candidateFile, request: candidateStart, audioIndex: candidateAudioIndex, result: candidateResult, toneMapErr: candidateToneMapErr}
			}
			continue
		}
		if candidateResult.Terminal == nil {
			start = candidateStart
			effectiveFile = candidateFile
			audioIndex = candidateAudioIndex
			result = candidateResult
			toneMapCapabilityErr = candidateToneMapErr
			break
		}
		if firstFailureFile == nil {
			firstFailureResult = candidateResult
			firstFailureToneMapErr = candidateToneMapErr
			firstFailureFile = candidateFile
			firstFailureStart = candidateStart
			firstFailureAudioIndex = candidateAudioIndex
		}
	}
	// A held-back requested edition is already the picture the
	// viewer asked for, so it keeps its place.
	if result.Terminal != nil && droppedSubtitle != nil && droppedSubtitle.file.ID != p.requestedFile.ID && subtitleRefusalV3(result.Terminal) {
		// The planner pinned the effective file's refusal on the
		// subtitle, and every sibling that plays drops the subtitle
		// too. Playing the effective file without it keeps its
		// picture, when that plan can run.
		if candidate := h.planWithoutSubtitleV3(r.Context(), playback.PlannerInputV3{Request: baseStart, RequestedFile: p.plannerRequestedFile, EffectiveFile: baseEffectiveFile, LowerVersion: lowerVersionForFileV3(p.lowerVersion, baseEffectiveFile), ServerBitrateCapKbps: serverBitrateCapV3(r.Context()), AudioTrackIndex: baseAudioIndex, Settings: p.settings, Registry: h.transformationRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), baseEffectiveFile), Now: time.Now(), AttemptedKeys: p.attemptedKeys, AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), baseEffectiveFile)}); candidate != nil {
			droppedSubtitle = candidate
			subtitleNotShown = true
		}
	}
	if result.Terminal != nil && droppedSubtitle != nil {
		start = droppedSubtitle.request
		effectiveFile = droppedSubtitle.file
		audioIndex = droppedSubtitle.audioIndex
		result = droppedSubtitle.result
		toneMapCapabilityErr = droppedSubtitle.toneMapErr
		subtitleDegraded = true
	} else if result.Terminal != nil && firstFailureFile != nil {
		start = firstFailureStart
		effectiveFile = firstFailureFile
		audioIndex = firstFailureAudioIndex
		result = firstFailureResult
		toneMapCapabilityErr = firstFailureToneMapErr
	}
	if result.Terminal != nil {
		if fallback := h.planWithoutLowerVersionV3(r.Context(), h.replanInputV3(r, p, baseStart, baseEffectiveFile, baseAudioIndex)); fallback != nil {
			return *fallback, false, false
		}
	}
	return alternateCandidateV3{file: effectiveFile, request: start, audioIndex: audioIndex, result: result, toneMapErr: toneMapCapabilityErr}, subtitleDegraded, subtitleNotShown
}

// replanInputV3 is the planner input of a replan of file.
func (h *PlaybackHandler) replanInputV3(r *http.Request, p replanPlanningV3, start playback.StartRequestV3, file *models.MediaFile, audioIndex int) playback.PlannerInputV3 {
	return playback.PlannerInputV3{
		Request: start, RequestedFile: p.plannerRequestedFile, EffectiveFile: file,
		LowerVersion:         lowerVersionForFileV3(p.lowerVersion, file),
		ServerBitrateCapKbps: serverBitrateCapV3(r.Context()),
		AudioTrackIndex:      audioIndex, Settings: p.settings,
		Registry: h.transformationRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), file), Now: time.Now(),
		AttemptedKeys: p.attemptedKeys, AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), file),
		AudioShapeRetry: p.audioShapeRetry, AudioInitFailureDetail: p.audioInitFailureDetail,
	}
}

// executeReplanV3 prepares an atomic replacement for a failed playback route.
// degradeStartAudioInPlaceV3 re-plans a fresh start's effective file with a
// different audio track when the selected one cannot be adapted. It is the
// audio counterpart of degradeStartSubtitleInPlaceV3: an audio problem must be
// resolved on the release already mounted, never by rotating the video
// candidate. The effective file is planned verbatim, so a virtual release is
// never re-resolved or substituted. An explicit audio pick is left untouched: a
// viewer's track choice surfaces its failure instead of being silently
// overridden. It reports false when no alternative track yields a playable plan.
func (h *PlaybackHandler) degradeStartAudioInPlaceV3(
	r *http.Request,
	req playback.StartRequestV3,
	requestedFile, effectiveFile *models.MediaFile,
	currentAudioIndex int,
	settings playback.PlannerSettingsV3,
	provenance ProbeProvenance,
) (playback.StartRequestV3, int, playback.PlannerResultV3, error, bool) {
	if req.AudioTrackID != "" || req.AudioTrackIndex != nil {
		return req, currentAudioIndex, playback.PlannerResultV3{}, nil, false
	}
	for _, index := range inPlaceAudioCandidateOrderV3(req, effectiveFile, currentAudioIndex) {
		degradedReq := audioDegradedStartV3(req, effectiveFile, index)
		degradedResult, degradedToneMapErr := h.planPlaybackWithCapabilitiesV3(r.Context(), playback.PlannerInputV3{
			Request: degradedReq, RequestedFile: requestedFile, EffectiveFile: effectiveFile,
			AudioTrackIndex: index, Settings: settings,
			Registry:            h.transformationRegistryV3(r.Context()),
			DVRPUStrippable:     h.lazyDVRPUStrippableV3(r.Context(), effectiveFile),
			Now:                 time.Now(),
			AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), effectiveFile),
			// The degraded plan serves the same release with another audio
			// track, so it keeps the selected source's provenance.
			InventoryProvenance: string(provenance),
		})
		clampPlannerTargetResolution(&degradedResult, effectiveFile)
		if degradedResult.Terminal == nil {
			annotateAudioTrackSubstitutedV3(&degradedResult, index)
			return degradedReq, index, degradedResult, degradedToneMapErr, true
		}
	}
	return req, currentAudioIndex, playback.PlannerResultV3{}, nil, false
}

func (h *PlaybackHandler) evaluateAudioDegradeInPlaceV3(
	r *http.Request,
	session *playback.Session,
	record *playback.AttemptRecordV3,
	req playback.ReplanRequestV3,
	baseStart playback.StartRequestV3,
	file *models.MediaFile,
	provenance ProbeProvenance,
	plannerRequestedFile *models.MediaFile,
	plannerSettings playback.PlannerSettingsV3,
	plannerSettingsErr error,
	attemptedKeys []string,
) (*candidateEvaluationV3, *candidateV3Error, bool) {
	if file == nil {
		return nil, &candidateV3Error{Stage: candidateStageResolve, Message: "nil same-release audio degrade target"}, false
	}
	if req.SelectedTracks.Audio != nil {
		return nil, &candidateV3Error{Stage: candidateStageAudioRemap, Message: "explicit audio selection is not auto-substituted"}, false
	}
	current := 0
	if baseStart.AudioTrackIndex != nil {
		current = *baseStart.AudioTrackIndex
	}
	for _, index := range inPlaceAudioCandidateOrderV3(baseStart, file, current) {
		degradedStart := audioDegradedStartV3(baseStart, file, index)
		eval, evalErr := h.evaluatePreparedReplanCandidateV3(r, session, record, req, degradedStart, file, file, provenance, plannerRequestedFile, plannerSettings, plannerSettingsErr, attemptedKeys, false, nil)
		if evalErr == nil && eval != nil && eval.result.Terminal == nil {
			annotateAudioTrackSubstitutedV3(&eval.result, index)
			return eval, nil, true
		}
	}
	return nil, &candidateV3Error{Stage: candidateStageAudioRemap, Message: "no alternative audio track is playable on this release"}, false
}

func audioDegradedStartV3(baseStart playback.StartRequestV3, file *models.MediaFile, index int) playback.StartRequestV3 {
	degradedStart := baseStart
	indexCopy := index
	degradedStart.AudioTrackIndex = &indexCopy
	if file != nil {
		degradedStart.AudioTrackID = playback.TrackIDV3(file.ID, "audio", index)
	}
	return degradedStart
}

func inPlaceAudioCandidateOrderV3(req playback.StartRequestV3, file *models.MediaFile, current int) []int {
	if file == nil || len(file.AudioTracks) < 2 {
		return nil
	}
	playable := playback.AudioTrackPlayableFuncV3(req)
	order := make([]int, 0, len(file.AudioTracks))
	for index := range file.AudioTracks {
		if index != current && playable(file.AudioTracks[index]) {
			order = append(order, index)
		}
	}
	for index := range file.AudioTracks {
		if index != current && !playable(file.AudioTracks[index]) {
			order = append(order, index)
		}
	}
	if len(order) > maxInPlaceAudioCandidatesV3 {
		order = order[:maxInPlaceAudioCandidatesV3]
	}
	return order
}

const maxInPlaceAudioCandidatesV3 = 4

func annotateAudioTrackSubstitutedV3(result *playback.PlannerResultV3, index int) {
	if result == nil || result.Terminal != nil || result.Plan == nil {
		return
	}
	result.Plan.DegradationWarnings = append(result.Plan.DegradationWarnings, playback.DegradationWarningV3{
		Code:    "audio_track_substituted",
		Message: fmt.Sprintf("The selected audio track could not be played on this release; audio track %d in the same file is used instead.", index),
	})
}

// syncSessionsAfterReplanDetached persists the post-replan session snapshot on
// a context that outlives the replan request. The durable commit (and its
// afterDurableCommit callback) can run after executeReplanV3 returns, at which
// point the request context is canceled; a sync bound to it failed with
// context.Canceled on every replan, leaving the shared admin snapshot stale.
// WithoutCancel keeps the request's logging/trace values while dropping that
// cancellation, and the bound keeps a wedged syncer from holding the response.
func (h *PlaybackHandler) syncSessionsAfterReplanDetached(ctx context.Context) {
	syncCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	h.syncSessionsNow(syncCtx, "v3_replan")
}

func (h *PlaybackHandler) executeReplanV3(r *http.Request, record *playback.AttemptRecordV3, req playback.ReplanRequestV3) (playback.DecisionResponseV3, playback.AttemptRecordV3, *preparedTransportV3, *transportErrorV3) {
	r = r.WithContext(withPlaybackRoutingPolicySnapshotV3(r.Context(), h.playbackRoutingPolicyV3()))
	r = r.WithContext(withServerBitrateCapV3(r.Context(), record.ServerBitrateCapKbps))
	r = r.WithContext(withCueIndexReadsV3(r.Context()))
	reservationHeld := false
	reservationHandedOff := false
	cancelReservation := func() {
		if reservationHeld {
			if canceller, ok := h.sessionMgr.(replacementReservationCancellerV3); ok {
				canceller.CancelReplacementReservation(record.SessionID)
			}
			reservationHeld = false
		}
	}
	defer func() {
		if !reservationHandedOff {
			cancelReservation()
		}
	}()
	start := record.NormalizedRequest
	operation := req.EffectiveOperation()
	if onDemandSubtitleSwitchV3(record, req) {
		// A client that switched among on-demand renditions in its player may
		// report it as a subtitle_track_changed recovery. That is a track
		// change: it stays on the file playing, selects the track it names,
		// and turns subtitles off when it names none.
		operation = playback.ReplanOperationTrackChangeV3
	}
	seekReanchor := operation == playback.ReplanOperationSeekReanchorV3
	// seekNoMovement marks a frozen seek that resolves to the timeline the plan
	// and the live session already serve. It suppresses the decision line so the
	// entry point can answer it as an unchanged replan instead.
	seekNoMovement := false
	// seekSubtitleDegraded marks a seek reanchor whose carried subtitle no
	// longer binds to the effective file. The seek still moves: a requested
	// position outranks subtitle fidelity, so the replan drops the selection
	// once and keeps video and audio intact.
	seekSubtitleDegraded := false
	seekFailureRecovery := operation == playback.ReplanOperationSeekFailureRecoveryV3
	seekScopedRecovery := seekReanchor || seekFailureRecovery
	// A segment-generation fence (the segment route's 412 stale_generation)
	// reports that FFmpeg's generation moved, not that the recipe is bad. It
	// must never be spent as a failed attempt: retiring the recipe's key is
	// what terminals a healthy session at adaptation_exhausted even though the
	// same route can serve the rebuilt generation.
	generationFence := failureRecoveryOperationV3(operation) && generationFenceFailureV3(req.Failure.Classification)
	// A failure recovery on a transcode session that answered the playlist and
	// never served a single segment request is a client that walked away after
	// fetching the manifest: the encode was live, the bytes were ready, and
	// nobody ever collected one. Nothing here is evidence against the plan, so
	// stopping the abandoned session and answering "gone" is the honest reply.
	// Rotating instead would indict a healthy recipe, spend the adaptation
	// budget, and persist a failure for a route that never delivered a byte to
	// the player.
	if failureRecoveryOperationV3(operation) && h.abandonedBeforeFirstSegmentV3(record.SessionID) {
		if session, err := h.sessionMgr.GetSession(record.SessionID); err == nil {
			slog.InfoContext(r.Context(), "playback failure recovery abandoned before the first segment",
				logComponentKey, playbackLogValueV3,
				"session_id", record.SessionID,
				"operation", string(operation),
				"failure_classification", req.Failure.Classification,
			)
			_ = h.abortPlaybackSession(context.WithoutCancel(r.Context()), session)
		}
		// Answered as a gone session, which is what it now is: the durable
		// attempt is stopped, no plan is indicted, and the deferred lease
		// release leaves no terminal decision behind.
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
			reason:  "session_expired",
			message: "The playback session has ended.",
		}
	}
	// A seek reanchor must keep its route; every other replan first finds out
	// whether the session's nodes still answer, so neither the plan nor the
	// route is built on a node that went away.
	var unreachableNodes map[string]struct{}
	if !seekReanchor {
		if current, err := h.sessionMgr.GetSession(record.SessionID); err == nil {
			unreachableNodes = h.unreachableSessionNodesV3(r.Context(), current)
			r = r.WithContext(withUnreachableRouteNodesV3(r.Context(), unreachableNodes))
		}
	}
	trackChange := operation == playback.ReplanOperationTrackChangeV3 || req.Failure.Classification == failureSubtitleTrackChangedV3 || req.Failure.Classification == failureAudioTrackChangedV3
	// A subtitle switch the client already made on the playing stream, and
	// reports as a failure recovery, is expressed in the mounted file's
	// inventory like a track_change, and its answer must be the same media:
	// it stays on the effective file as a track_change does.
	pinnedTrackChange := trackChange ||
		(operation == playback.ReplanOperationFailureRecoveryV3 && req.Failure.Classification == failureSubtitleTrackChangedV3)
	qualityChange := operation == playback.ReplanOperationQualityChangeV3
	outputChange := operation == playback.ReplanOperationOutputChangeV3
	// A same-output replan plans again for the output context the durable plan
	// was built for, so a recipe that already failed there is a recipe for this
	// same question. That is what lets the durable recipe-exclusion chain below
	// apply whatever the operation: a track_change re-reads the client's
	// still-advertised claims and would otherwise steer straight back to a
	// recipe the client already abandoned. An output change re-targets the plan
	// at a different output context, which PlanAttemptKeyV3 folds into every
	// key, so the previous recipe is a different question there. A seek reanchor
	// replays a frozen recipe by design and never consults the ladder.
	sameOutputReplan := !seekReanchor && !outputChange
	// A failure recovery that abandoned a delivery for a transport reason used
	// to demote that delivery on the durable request, so every later replan in
	// this attempt planned with it disabled. The demotion is what kept a
	// track_change replan from re-reading the client's still-advertised claims
	// and steering straight back to a route that just failed, thrashing the
	// player between direct play and remux (each flip reloading the stream and
	// orphaning the client's subtitle track state — the field symptom where a
	// menu shows a selection that is not actually playing).
	//
	// That demotion was too broad. Delivery classes pool recipes that are not
	// interchangeable — server_remux_hls and server_transcode_hls are both
	// DeliveryClassHLSV3 — so one aborted copy recipe retired a transcode
	// fallback the device never tried. An abort also cannot be evidence about a
	// class: the client parsed the plan and started fetching it, which proves
	// the class reached the device, and a user backing out or a transient fetch
	// error produces the same report. Only classifications that name a recipe
	// property (decode, parser) retire the class
	// (abandonmentDoesNotIndictDeliveryClassV3). The aborted recipe itself is
	// retired by excluding its plan key from this replan and from same-output
	// replans after it, which keeps the thrash guard without spending a class
	// the ladder has not finished with.
	//
	// The demotion is a server-side evidence write on the durable attempt, not
	// a client-authority change: a fresh start of the same file still negotiates
	// normally, and an explicit user retry can re-enable the route by going
	// through a new start.
	// A decode failure defers the demotion while the next hop can still recover
	// the same server-transcode delivery: either an untried software-decode
	// variant of the same source, or a provider candidate rotation under the
	// same virtual release. Demoting first would strand the session on a route
	// shape that rotation needs. Every other transport failure demotes exactly
	// as before.
	//
	// virtualDecodeRotation is the rotation predicate for a non-explicit
	// virtual failure: candidate substitution is the recovery, so the delivery
	// stays eligible until rotation exhausts.
	virtualDecodeRotation := h.virtualCandidateRotationPendingV3(record, req)
	// A player whose audio output failed to initialize is not evidence against
	// the video route. Keep the delivery and spend its one audio-shape retry on
	// a stereo recipe before the ordinary demotion path can retire the class.
	// The failing code comes from the replan's own failure evidence, never from
	// the asynchronous route-event telemetry.
	audioInitFailureDetail := ""
	audioShapeRetry := false
	if failureRecoveryOperationV3(operation) {
		audioInitFailureDetail = playback.AudioInitFailureDetailV3(req.Failure)
	}
	if audioInitFailureDetail != "" && len(unreachableNodes) == 0 && audioShapeRetryApplicableV3(record) {
		audioShapeRetry = true
		markAudioShapeRetryV3(&record.NormalizedRequest, record.CurrentPlan.Delivery)
		markAudioShapeRetryV3(&start, record.CurrentPlan.Delivery)
		slog.InfoContext(r.Context(), "playback delivery kept for a stereo audio-shape retry",
			logComponentKey, playbackLogValueV3,
			"session_id", record.SessionID,
			"delivery_class", record.CurrentPlan.Delivery,
			"audio_channels", planAudioChannelsV3(record.CurrentPlan),
			"failure_detail", audioInitFailureDetail,
		)
	}
	// A node that stopped answering indicts the node, not the delivery: keep
	// the delivery class eligible so the same recipe can run on another node.
	// An abandonment (player_failure, http_failure) indicts only the recipe the
	// client already reached: the class stays eligible, and the abandoned
	// recipe key is what keeps a later replan from replaying it.
	abandonedRecipe := false
	if !generationFence &&
		!audioShapeRetry &&
		failureRecoveryAbandonedDeliveryV3(operation, req.Failure.Classification) &&
		len(unreachableNodes) == 0 &&
		(!decodeFailureClassificationV3(req.Failure.Classification) ||
			(!h.softwareDecodeRetryPendingV3(record, req) && !virtualDecodeRotation)) {
		// An audio-output init failure is evidence about the audio shape this
		// delivery's recipe produced, not about the client walking away: once
		// the one stereo retry is spent, the class retires as it always did.
		if audioInitFailureDetail != "" || !abandonmentDoesNotIndictDeliveryClassV3(req.Failure.Classification) {
			// Demote on both copies: the record (the durable attempt this replan
			// may still terminal-persist) and the seeded start, whose payload the
			// success commit writes back via updated.NormalizedRequest. Demoting
			// only the record would be wiped by that write-back — the seed was
			// copied before the demotion and the overlay re-enables the client's
			// still-advertised claim.
			demoteDeliveryCapabilityV3(&record.NormalizedRequest, record.CurrentPlan.Delivery)
			demoteDeliveryCapabilityV3(&start, record.CurrentPlan.Delivery)
		} else {
			abandonedRecipe = true
		}
	}
	// User-intent operations replace the legacy audio PATCH and client-recipe
	// transcode start. Nothing failed, so their previous route stays eligible:
	// neither attempted-key history nor the failed-plan exclusion applies.
	userIntentOperation := trackChange || qualityChange || outputChange
	// A proxy-origin source refusal means the media route may be sound while
	// the selected egress is not. Retry the same plan through the API origin
	// before excluding its plan key and considering another source or route.
	proxyOriginRecovery := !userIntentOperation &&
		req.Failure.Classification == "sourceRefused" &&
		strings.HasPrefix(record.CurrentPlan.Stream.URL, "http")
	// A recovery whose node stopped answering failed on the node, not on its
	// recipe: keep the failed plan eligible so the replacement can run the same
	// recipe on another node instead of stepping down or running out of plans.
	nodeRecovery := !userIntentOperation && len(unreachableNodes) > 0
	intentChange := false
	if seekScopedRecovery {
		if err := validateSeekRecoveryRequestV3(record, req); err != nil {
			reason := "seek_reanchor_intent_mismatch"
			if seekFailureRecovery {
				reason = "seek_failure_recovery_intent_mismatch"
			}
			return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
				reason:  reason,
				message: err.Error(),
			}
		}
		// Reconstruct the complete route intent from the durable current attempt.
		// A seek request is not an authority boundary for replacing capability or
		// device evidence: accepting those fields here could make the same file
		// select a materially different route based on request-only claims.
		start.FileID = record.EffectiveMediaFileID
		start.StartPosition = &req.PositionSeconds
		applySelectedTracksToStartV3(&start, record.CurrentPlan.SelectedTracks)
	} else {
		// Failure replans may omit unchanged tracks. The durable current plan
		// holds the authoritative effective-file selections; the normalized
		// request can still carry requested-edition identities after an
		// alternate-version fallback, and validating those against the
		// effective file would reject an otherwise valid replan. Seed from
		// the plan first, then overlay the request's explicit changes.
		applySelectedTracksToStartV3(&start, record.CurrentPlan.SelectedTracks)
		switch {
		case trackChange:
			intentChange = audioSelectionDiffersFromStartV3(req.SelectedTracks, start) ||
				subtitleSelectionDiffersFromStartV3(req.SelectedTracks, start)
		case qualityChange:
			intentChange = qualityIntentChangedV3(req, start, record.EffectiveMediaFileID, record.RequestedMediaFileID)
		case outputChange:
			intentChange = true
		default:
			switch req.Failure.Classification {
			case "quality_changed":
				intentChange = qualityIntentChangedV3(req, start, record.EffectiveMediaFileID, record.RequestedMediaFileID)
			case failureAudioTrackChangedV3:
				intentChange = audioSelectionDiffersFromStartV3(req.SelectedTracks, start)
			case failureSubtitleTrackChangedV3:
				intentChange = subtitleSelectionDiffersFromStartV3(req.SelectedTracks, start)
			case "output_route_changed":
				intentChange = req.ClientPlaybackContext.Output.OutputContextID != start.ClientPlaybackContext.Output.OutputContextID
			}
		}
		// Failure replans use the current effective file. Quality/output intent may
		// restart source selection from the requested edition, but a track change
		// is expressed in the mounted alternate's inventory and must stay pinned to
		// that file or its combined ordinals can select unrelated tracks.
		start.FileID = record.EffectiveMediaFileID
		if intentChange && !trackChange {
			start.FileID = record.RequestedMediaFileID
		}
		if strings.TrimSpace(req.QualityPreference) != "" {
			// Replans may omit unchanged intent. Normalizing an absent quality
			// would silently reset a fixed rung to "auto".
			start.QualityPreference = req.QualityPreference
		}
		// A non-seek replan (track_change, quality_change, output_change) keeps
		// the playhead where the viewer is. The client's position_seconds is a
		// snapshot from before its replan round trip, and a client that
		// rebuilds its media element for a subtitle switch can report the old
		// element's position or a zero it has not filled in yet. Echoing that
		// re-anchors the new route at the start (or a stale point) instead of
		// where the viewer is. The live session position is the authoritative
		// value, so prefer it and fall back to the client's number only when
		// the session has not reported one. Seek operations above keep the
		// client's target: there the requested position IS the intent.
		start.StartPosition = h.nonSeekReplanPositionV3(record.SessionID, req.PositionSeconds)
		start.Metered = req.Metered
		start.BandwidthEstimateKbps = copyOptionalIntV3(req.BandwidthEstimateKbps)
		start.BandwidthEstimateSource = req.BandwidthEstimateSource
		start.ObservedThroughputKbps = copyOptionalIntV3(req.ObservedThroughputKbps)
		start.LearnedLimit = req.LearnedLimit
		start.BandwidthCapKbps = copyOptionalIntV3(req.BandwidthCapKbps)
		start.Capabilities = req.Capabilities
		start.ClientPlaybackContext = req.ClientPlaybackContext
		// The overlay above aliases the replan body's deliveries map, which
		// every shallow copy made since decode — the preflight request
		// included — also points at. Deep-clone it here so the single
		// normalization the merged start request runs cannot write through to
		// req, and a later preflight mutation cannot leak into the durable
		// request stored on the attempt.
		start.ClientPlaybackContext.Deliveries = playback.CloneDeliveryCapabilitiesV3(req.ClientPlaybackContext.Deliveries)
		// The client's capability payload just replaced the seeded one.
		// Re-apply the durable record's server-side delivery demotions so a
		// route a previous failure recovery abandoned stays disabled — the
		// client cannot advertise itself back into a route that failed.
		reapplyDeliveryDemotionsV3(&start, record.NormalizedRequest)
		// The audio-shape retry marker is the same kind of server-owned
		// evidence: the overlay must not let the client's still-surround
		// advertisement spend a second retry.
		reapplyAudioShapeRetriesV3(&start, record.NormalizedRequest)
		if req.ClientFeatures != nil {
			// Feature advertisement is single-location (top-level); a replan
			// that sends it refreshes the durable request's copy alongside the
			// capability payloads. Omission keeps the start-time features.
			start.ClientFeatures = req.ClientFeatures
		}
		// A replan that answers a default-audio reconciliation withdrawal
		// carries the correction the settled decision recorded: the client
		// replans off the withdrawn plan, which still names the pre-reorder
		// stream, so without this the correction never reaches the transport.
		// This must run BEFORE the selection is applied onto start: audio
		// resolution reads start.AudioTrackID/AudioTrackIndex, so a correction
		// layered on afterwards would never reach the plan or the executor's
		// audio map.
		h.pendingAudioReconciliationReplan(record, &req)
		if trackChange {
			// A track_change is the only operation where an omitted subtitle
			// means "subtitles off". Failure, seek, and quality replans may omit
			// unchanged identities and must not erase the durable selection.
			applySelectedTracksToStartV3(&start, req.SelectedTracks)
		} else {
			applySelectedTrackOverridesToStartV3(&start, req.SelectedTracks)
		}
	}
	start.ClientFeatures = withoutFailedSubtitleRoutesV3(start.ClientFeatures, record.NormalizedRequest.ClientFeatures, req.Failure.Classification)
	// A failure recovery after the current transport showed frames and suffered
	// a network connectivity loss (e.g. transport_stall) is a lost connection,
	// not a failed recipe: the transport already delivered frames, so the same
	// delivery and recipe may be reissued once instead of retiring the route (§6.2).
	// Pre-first-frame failures keep the ordinary ladder because the route itself
	// never proved it could reach this client. Capability/engine rejections
	// (such as browser_engine_unsupported_source), decode errors, and delivery
	// demotions are not connectivity losses and must continue through route retirement.
	currentTransportRendered := record.CurrentTransportRendered
	subtitleDegraded, subtitleNotShown := false, false
	audioDegraded := false
	progressReissue := operation == playback.ReplanOperationFailureRecoveryV3 &&
		!intentChange &&
		!proxyOriginRecovery &&
		!nodeRecovery &&
		!virtualDecodeRotation &&
		connectivityFailureClassificationV3(req.Failure.Classification) &&
		currentTransportRendered
	// A direct-play plan is disqualified by any recognized client-reported
	// playback failure, not only the decode classes that indict a
	// server-produced recipe. original_http ships the source container
	// untouched, so unlike a packaged route there is no server-side executor
	// that can transform the bytes around what the device could not render:
	// the client has just told us it cannot play this file, and handing the
	// same bytes back is what stalled the browser that over-claimed mkv.
	// Demoting the class on both copies of the request keeps every later replan
	// in this attempt off direct play, exactly as a decode failure retires the
	// class that produced it. The packaged ladder (codec-copy remux, then
	// transcode) and the existing attempt-key/attempt-count guards bound the
	// chain; no classification here can re-enable direct play for the attempt.
	routeDowngrade := ""
	if failureRecoveryOperationV3(operation) &&
		record.CurrentPlan.Delivery == playback.DeliveryOriginalHTTPV3 &&
		!nodeRecovery &&
		!progressReissue &&
		!audioShapeRetry &&
		directPlayFailureClassificationV3(req.Failure.Classification) {
		demoteDeliveryCapabilityV3(&record.NormalizedRequest, playback.DeliveryClassOriginalHTTPV3)
		demoteDeliveryCapabilityV3(&start, playback.DeliveryClassOriginalHTTPV3)
		routeDowngrade = routeDowngradeDirectPlayFailedV3
		slog.InfoContext(r.Context(), "playback direct route retired after a client-reported failure",
			logComponentKey, playbackLogValueV3,
			routeDowngradeLogKeyV3, routeDowngrade,
			"session_id", record.SessionID,
			"operation", string(operation),
			"failed_delivery", record.CurrentPlan.Delivery,
			"failure_classification", req.Failure.Classification,
			"attempt_count", req.AttemptCount,
		)
	}
	requestedFallbackID := record.EffectiveMediaFileID
	effectiveFallbackID := record.RequestedMediaFileID
	if seekScopedRecovery {
		// Edition fallback is useful for ordinary failure replans, but never for
		// a seek operation: the caller asked to move within the currently mounted
		// source, not to select another version when that source disappears.
		requestedFallbackID = 0
		effectiveFallbackID = 0
	}
	requestedFile, err := h.loadFileByPreferredID(r.Context(), record.RequestedMediaFileID, requestedFallbackID)
	requestedEditionResolved := err == nil && requestedFile != nil && requestedFile.ID == record.RequestedMediaFileID
	if err != nil || requestedFile == nil {
		if !seekScopedRecovery {
			return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: sourceUnavailableReason, message: "The requested media source is unavailable."}
		}
		// The requested edition is identity-only once another effective edition
		// is mounted. Seeking must depend on that effective file remaining
		// available, not on an inactive original edition still resolving.
		requestedFile = &models.MediaFile{ID: record.RequestedMediaFileID}
	} else {
		// Mirror the start path's on-demand probe repair: a session outlives the
		// scan that produced its file row, and a replan re-reads that row from
		// the catalog. Without this, a replan refuses a route as
		// source_metadata_incomplete for metadata the start path would have
		// healed before planning.
		requestedFile = h.ensurePlaybackProbe(r.Context(), requestedFile)
	}
	plannerRequestedFile := requestedFile
	if requestedFile.ID != record.RequestedMediaFileID {
		// The live loader may fall back to the current effective file when the
		// original edition is gone. Keep that file for metadata/remapping while
		// preserving the durable requested-edition identity in every new plan.
		plannerRequestedFile = &models.MediaFile{ID: record.RequestedMediaFileID}
	}
	currentEffectiveFile, err := h.loadFileByPreferredID(r.Context(), record.EffectiveMediaFileID, effectiveFallbackID)
	if err != nil || currentEffectiveFile == nil {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: sourceUnavailableReason, message: effectiveSourceUnavailableMessageV3}
	}
	// Mirror the start path's on-demand probe repair before either planning pass
	// reads the row: a rescan can leave the effective row without the probe
	// metadata the running plan validated, and the provisional plan built below
	// would otherwise terminal source_metadata_incomplete before the local heal
	// later in this function could repair it.
	if !isVirtualPlaybackFile(currentEffectiveFile) {
		currentEffectiveFile = h.ensurePlaybackProbe(r.Context(), currentEffectiveFile)
	}
	effectiveFile := currentEffectiveFile
	currentEffectiveStart := start
	keptActiveEditionForSubtitle := false
	// What the viewer asked for, which a fallback version may not carry. Any
	// move to another file resolves it afresh rather than remapping whatever
	// the current plan could deliver.
	desired := h.replanDesiredTracksV3(r.Context(), record, currentEffectiveFile)
	// Replans the viewer asked for can carry a choice: a track change, and a
	// quality or output change echoing a track the client switched to locally
	// without telling the server. A failure recovery may send a track the
	// client fell back to, so only its track-change classifications count.
	choosesTracks := userIntentOperation || req.Failure.Classification == failureAudioTrackChangedV3 || req.Failure.Classification == failureSubtitleTrackChangedV3
	if choosesTracks && !seekScopedRecovery {
		var intentErr error
		desired, intentErr = h.updateDesiredTracksV3(r.Context(), desired, record.CurrentPlan.SelectedTracks, req.SelectedTracks, trackChange, currentEffectiveFile)
		if intentErr != nil {
			// The pick cannot be recorded while downloaded subtitles are
			// unreadable; the playing plan keeps its tracks and the change
			// can be repeated.
			return playback.DecisionResponseV3{}, *record, nil, subtitleArtifactErrorV3("Downloaded subtitles are temporarily unavailable.", intentErr)
		}
	}
	if intentChange && !trackChange { //nolint:nestif // executeReplanV3 exceeds Vio's nestif complexity-4 limit; refactor tracked separately
		// Prefer returning to the requested edition, but a quality/output/track
		// change must not abandon a healthy active alternate merely because the
		// inactive original has gone missing since playback started.
		if requestedEditionResolved && preflightPlaybackFile(r.Context(), requestedFile, h.MissingMarker, h.EventsHub) == nil {
			effectiveFile = requestedFile
		}
		// Track identities only need remapping when the effective edition
		// actually changes. Remapping within the same file would degrade an
		// exact selection to a best-match lookup — e.g. moving a listener
		// from an eng/ac3 commentary track to the identically-shaped main
		// track on a quality change.
		if currentEffectiveFile.ID != effectiveFile.ID {
			candidateStart := start
			var remapErr error
			subtitleDropped := false
			if desired != nil {
				subtitleDropped, remapErr = h.applyDesiredTracksV3(r.Context(), desired, effectiveFile, &candidateStart)
			} else {
				remapErr = remapAudioSelectionV3(currentEffectiveFile, effectiveFile, &candidateStart)
				if remapErr == nil && (candidateStart.SubtitleTrackIndex != nil || candidateStart.SubtitleTrackID != "") {
					subtitleDropped, remapErr = h.remapSubtitleSelectionV3(r.Context(), currentEffectiveFile, effectiveFile, &candidateStart)
				}
			}
			// An output refresh may make the requested edition viable again,
			// but it must not retire a healthy active alternate merely because
			// the viewer selected a track unique to that alternate. Other intent
			// changes must not drop the viewer's subtitle to get back there either.
			// Staying keeps the selection, so a fallback can still prefer a
			// version that has it.
			// Staying only helps when the version playing shows the subtitle;
			// when it already lost it, leaving costs nothing.
			keepForSubtitle := subtitleDropped && record.CurrentPlan.SelectedTracks.Subtitle != nil
			keepActiveEdition := (outputChange && remapErr != nil) || keepForSubtitle
			if keepActiveEdition {
				effectiveFile = currentEffectiveFile
				keptActiveEditionForSubtitle = keepForSubtitle
			} else if errors.Is(remapErr, errSubtitleStoreUnavailableV3) {
				// The playing plan keeps its subtitle; the change can be
				// repeated once the downloaded subtitles can be read.
				return playback.DecisionResponseV3{}, *record, nil, subtitleArtifactErrorV3("Downloaded subtitles are temporarily unavailable.", remapErr)
			} else if remapErr != nil {
				return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: trackUnavailableReasonV3, message: remapErr.Error()}
			} else {
				start = candidateStart
				subtitleDegraded = subtitleDegraded || subtitleDropped
			}
		}
	}
	start.FileID = effectiveFile.ID
	if err := preflightPlaybackFile(r.Context(), effectiveFile, h.MissingMarker, h.EventsHub); err != nil {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
			reason:  sourceUnavailableReason,
			message: effectiveSourceUnavailableMessageV3,
			cause:   err,
		}
	}
	seekDuration := float64(effectiveFile.Duration)
	if seekReanchor && record.FrozenRecipe.ValidFor(record.CurrentPlan) {
		seekDuration = record.FrozenRecipe.SourceDurationSeconds
	}
	if seekScopedRecovery && seekDuration > 0 && req.PositionSeconds > seekDuration {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
			reason:  "invalid_seek_position",
			message: "The requested seek position is beyond the end of the selected media source.",
		}
	}
	// A stop or idle reap can land after the caller's early-out and while the
	// replan reads the catalog (the source load is the slow step). Re-check the
	// live session before validating the rebuilt start: a session that died
	// mid-execution must answer the fast 404, not persist a terminal for a
	// session nobody owns any more.
	if _, liveErr := h.sessionMgr.GetSession(record.SessionID); liveErr != nil {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "session_expired", message: "The playback session is no longer live.", cause: liveErr}
	}
	audioIndex := 0
	if !seekReanchor {
		// A carried identity was minted on the row the previous plan selected
		// (the requested row). The replan validates against the effective row,
		// which a serve rotation may have moved; drop the foreign identity
		// before the file-bound resolve so it degrades to the default track
		// instead of failing as a malformed client selection.
		if dropStaleAudioTrackIdentityV3(r.Context(), effectiveFile, start.AudioTrackID) {
			start.AudioTrackID = ""
			start.AudioTrackIndex = nil
		}
		audioIndex, audioDegraded, err = resolveV3AudioIndex(effectiveFile, start.AudioTrackID, start.AudioTrackIndex)
		if err != nil {
			return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: trackUnavailableReasonV3, message: err.Error()}
		}
	}
	// A recovery whose node stopped answering failed on the node, not on its
	// recipe: keep the failed plan eligible so the replacement can run the same
	// recipe on another node instead of stepping down or running out of plans.
	nodeRecovery = !userIntentOperation && len(unreachableNodes) > 0
	currentKey := playback.PlanAttemptKeyV3(record.CurrentPlan, record.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID, req.LocalMutations)
	unmutatedKey := playback.PlanAttemptKeyV3(record.CurrentPlan, record.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	if abandonedRecipe {
		// Retire the recipe durably rather than only for this replan: a later
		// track_change or quality pick echoes no attempted-key history, so the
		// attempt's own chain is the only thing that keeps the abandoned recipe
		// from coming back (§6.2).
		if err := h.appendRecipeExclusionsV3(r.Context(), record, abandonedRecipeAttemptKeysV3(record, req)); err != nil {
			slog.WarnContext(r.Context(), "playback abandoned recipe was not recorded",
				logComponentKey, playbackLogValueV3,
				"session_id", record.SessionID,
				"delivery", record.CurrentPlan.Delivery,
				"error", err,
			)
		}
	}
	attemptedKeys := []string(nil)
	// The durable recipe-exclusion chain carries the keys an abandonment
	// retired. A same-output replan re-asks the question that recipe already
	// answered badly, and it usually brings no history of its own, so the chain
	// is what keeps an abandoned recipe from being planned again. An output
	// change plans for a different output context, whose keys the chain cannot
	// contain, and a seek reanchor never consults the ladder.
	if sameOutputReplan {
		for _, key := range record.RecoveryState.RecipeExclusions {
			if !containsStringExactV3(attemptedKeys, key) {
				attemptedKeys = append(attemptedKeys, key)
			}
		}
	}
	if !intentChange && !seekReanchor && !userIntentOperation {
		failedKey := strings.TrimSpace(req.PlanAttemptKey)
		for _, key := range req.AttemptedPlanKeys {
			trimmed := strings.TrimSpace(key)
			if (nodeRecovery || generationFence) && (trimmed == failedKey || trimmed == currentKey) {
				continue
			}
			// A progress reissue keeps the recipe it just played out of the loop
			// guard for its one retry. The client echoes it in its history and
			// the server computes the same key; both copies must be dropped or
			// the planner cannot select the route it just played.
			if progressReissue && (trimmed == failedKey || trimmed == currentKey || trimmed == unmutatedKey) {
				continue
			}
			attemptedKeys = append(attemptedKeys, key)
		}
		if !proxyOriginRecovery && !nodeRecovery && !generationFence && !progressReissue && !containsStringExactV3(attemptedKeys, req.PlanAttemptKey) {
			attemptedKeys = append(attemptedKeys, req.PlanAttemptKey)
		}
	}
	if !seekReanchor && !userIntentOperation && (!intentChange || seekFailureRecovery) { //nolint:nestif // executeReplanV3 exceeds Vio's nestif complexity-4 limit; refactor tracked separately
		// Always exclude the durable server recipe so stale or malformed client
		// history cannot immediately re-select the route that just failed and
		// ping-pong the session. A client-reported local mutation (for example a
		// PCM recovery route) is folded into the failed plan's key here — the
		// server owns the hash; clients only echo opaque keys.
		if !proxyOriginRecovery && !nodeRecovery && !generationFence && !progressReissue && !containsStringExactV3(attemptedKeys, currentKey) {
			attemptedKeys = append(attemptedKeys, currentKey)
		}
		if len(req.LocalMutations) > 0 && !proxyOriginRecovery && !nodeRecovery && !generationFence && !progressReissue {
			// The unmutated recipe already failed before the client mutated it
			// locally; exclude it as well.
			if !containsStringExactV3(attemptedKeys, unmutatedKey) {
				attemptedKeys = append(attemptedKeys, unmutatedKey)
			}
		}
	}
	// A decode rejection substitutes the provider candidate, not the route. The
	// attempt key hashes the recipe and delivery, not the provider bytes, so the
	// replacement candidate legitimately produces the same key as the rejected
	// one. Keeping those keys in the history would make every rotation plan
	// adaptation_exhausted before it is ever tried; the durable exclusion chain
	// — not this guard — is what keeps the undecodable release from returning.
	if virtualDecodeRotation {
		attemptedKeys = withoutRotatedPlanKeysV3(attemptedKeys, req.PlanAttemptKey, currentKey)
	}
	var result playback.PlannerResultV3
	var toneMapCapabilityErr error
	var plannerSettings playback.PlannerSettingsV3
	var plannerSettingsErr error
	var lowerVersion *playback.LowerVersionV3
	var learnedLimit *playback.BandwidthLimitV3
	if !seekScopedRecovery {
		if err := h.applyConnectionReading(r, &start); err != nil {
			return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "internal_error", message: "Connection observations are temporarily unavailable.", retryable: true, cause: err}
		}
	}
	// The replan without the learned limit, planned again only when the plan
	// would carry the limit's label, to check that the limit lowered it. A
	// stall's estimate goes with the limit; the connection's reading then
	// stands in for it.
	unlimitedStart, unlimitedFile, unlimitedAudioIndex := withoutObservedEstimateV3(start), effectiveFile, audioIndex
	// A quality change, and an output change that went back to the requested
	// edition, choose a version again. Under a buffering budget that must not
	// be the source that stalled. A track change stays on the mounted version.
	choosesVersion := qualityChange || (intentChange && !trackChange)
	// bufferedLoweredFile remembers the lower version an Auto budget picked, so
	// the requested-edition preference and the row reset below cannot silently
	// return the plan to the row the budget just ruled out.
	var bufferedLoweredFile *models.MediaFile
	if !seekReanchor { //nolint:nestif // executeReplanV3 exceeds Vio's nestif complexity-4 limit; refactor tracked separately
		var limitErr error
		// The playing transcode is still mounted here: whether the client
		// was waiting on it tells an encoder-paced stall from a slow link.
		outputBound := func() bool { return h.transcodeOutputBoundV3(r.Context(), record.SessionID, unreachableNodes) }
		learnedLimit, limitErr = h.applyLearnedLimit(r, &start, !seekScopedRecovery, outputBound)
		if limitErr != nil {
			return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "internal_error", message: "Connection observations are temporarily unavailable.", retryable: true, cause: limitErr}
		}
		plannerSettings, plannerSettingsErr = h.plannerSettingsV3Result(r.Context())
		if choosesVersion {
			effectiveFile, start, audioIndex = h.lowerBufferedVersionForIntent(r, start, effectiveFile, audioIndex, plannerSettings, desired)
			if start.BandwidthEstimateKbps != nil && start.QualityPreference == playback.QualityAutoV3 {
				// Under an Auto budget the lower-version pick is the answer,
				// whether or not it happens to be the row already playing: the
				// requested-edition preference below would return a row the
				// budget just ruled out.
				bufferedLoweredFile = effectiveFile
			}
		}
		if requestedEditionResolved {
			lowerVersion = h.lowerVersionV3(r.Context(), start, requestedFile, plannerSettings, requestAccessFilter(r))
		}
	}
	planning := replanPlanningV3{
		operation: operation, requestedFile: requestedFile, plannerRequestedFile: plannerRequestedFile,
		currentEffectiveFile: currentEffectiveFile, currentEffectiveStart: currentEffectiveStart,
		lowerVersion: lowerVersion, settings: plannerSettings, attemptedKeys: attemptedKeys,
		keptActiveEditionForSubtitle: keptActiveEditionForSubtitle, desired: desired,
		renditionTracks:        pinnedRenditionTracksForRecordV3(record),
		audioShapeRetry:        audioShapeRetry,
		audioInitFailureDetail: audioInitFailureDetail,
	}
	if seekReanchor { //nolint:nestif // executeReplanV3 exceeds Vio's nestif complexity-4 limit; refactor tracked separately
		var frozenErr error
		result, frozenErr = frozenSeekReanchorResultV3(record, req.PositionSeconds, time.Now())
		if frozenErr != nil {
			return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
				reason:    "seek_reanchor_recipe_unavailable",
				message:   "The active playback recipe cannot be reopened; start a new playback attempt.",
				retryable: true,
			}
		}
		// A carried subtitle that no longer binds to the effective file is not
		// a reason to refuse the position: drop it for this replan and keep
		// moving. Only a recipe that cannot be replayed at all stays terminal.
		if err := h.validateFrozenSubtitleIdentityV3(r.Context(), effectiveFile, record.FrozenRecipe); err != nil {
			// The authoritative seek block below records the drop once; this
			// pass only decides that the frozen recipe may be replayed without
			// its subtitle.
			seekSubtitleDegraded = true
		}
	} else if userIntentOperation || (virtualDecodeRotation && isVirtualPlaybackFile(currentEffectiveFile)) {
		// A track/quality/output change replaces the plan with one built from
		// the viewer's pick after the version and edition selection below. A
		// provisional pass here would run the planner twice for a plan that is
		// discarded, duplicating its side effects (the subtitle-degrade log a
		// repeated pick must record once). A virtual decode rejection
		// substitutes the provider candidate before a plan can be built: the
		// rehydration block below excludes the rejected candidate and resolves
		// its replacement, and the authoritative plan is built once from that
		// row further down. Planning here would either re-select the rejected
		// release or terminal adaptation_exhausted before the rotation runs.
	} else {
		planned, planErr := h.planReplanV3(r, planning, start, effectiveFile, audioIndex, audioDegraded)
		if planErr != nil {
			return playback.DecisionResponseV3{}, *record, nil, planErr
		}
		start, effectiveFile, audioIndex, result, toneMapCapabilityErr = planned.request, planned.file, planned.audioIndex, planned.result, planned.toneMapErr
		audioDegraded, subtitleNotShown = planned.audioDegraded, planned.subtitleNotShown
		subtitleDegraded = subtitleDegraded || planned.subtitleDegraded
	}
	mode := withHLSVariableSubstitutionV3(r, headerAuthenticatedMediaV3(start.ClientFeatures), start)
	result = retryIncompleteToneMapPlanningV3(result, toneMapCapabilityErr)
	result = retryIncompletePlaybackSettingsV3(result, plannerSettingsErr)
	if !seekReanchor {
		// A freshly planned replan can land on the same refused progressive
		// remux a start would have; escalate it identically. A seek reanchor
		// replays the frozen recipe verbatim and must not change route identity,
		// so it is excluded — its route was escalated when the attempt started.
		escalated, escalateErr := h.escalateRefusedProgressiveRemuxV3(r.Context(), mode,
			func() playback.PlannerInputV3 {
				input := h.plannerInputV3(r.Context(), start, plannerRequestedFile, effectiveFile, audioIndex, attemptedKeys, ProbeProvenance(""))
				input.LowerVersion = lowerVersionForFileV3(lowerVersion, effectiveFile)
				return input
			}, result)
		if escalateErr != nil {
			return playback.DecisionResponseV3{}, *record, nil, escalateErr
		}
		result = escalated
	}
	if result.Terminal != nil && !isVirtualPlaybackFile(currentEffectiveFile) &&
		(operation != playback.ReplanOperationFailureRecoveryV3 ||
			!decodeFailureClassificationV3(req.Failure.Classification) ||
			record.CurrentPlan.Delivery != playback.DeliveryTranscodeHLSV3) {
		// A virtual source can still be healed or replaced by the rehydration
		// below: a rescan-rewritten row, a moved pin, or a provider candidate
		// rotation all plan against evidence this provisional pass did not have.
		// Returning its terminal here would refuse the replan before the
		// authoritative plan is built; the terminal is answered after that plan
		// (or by the rehydration failure classifier) instead.
		//
		// A decode failure on a video-encoding plan is the same case: the
		// provisional pass never knew a software-decode variant of the same
		// delivery is untried, so its terminal must wait for the authoritative
		// plan built with that variant forced.
		return playback.NewTerminalResponseFromTerminalV3(retryableAfterDirectPlayV3(result.Terminal, directPlayRetiredByServerV3(record))), *record, nil, nil
	}
	// Replans degrade the same two ways starts do, and clients read the same
	// contract: silently swapping audio to the default or turning subtitles
	// off mid-session is worse than the refusal this replaced.
	if result.Plan != nil && audioDegraded {
		result.Plan.DegradationWarnings = append(result.Plan.DegradationWarnings, playback.AudioTrackUnavailableWarningV3())
	}
	if result.Plan != nil && subtitleDegraded {
		warning := playback.SubtitleTrackUnavailableWarningV3()
		if subtitleNotShown {
			warning = playback.SubtitleNotShownWarningV3()
		}
		result.Plan.DegradationWarnings = appendWarningOnceV3(result.Plan.DegradationWarnings, warning)
	}
	session, err := h.sessionMgr.GetSession(record.SessionID)
	if err != nil {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: terminalSessionExpiredV3, message: "The playback session has expired.", retryable: true}
	}
	replacementManager, ok := h.sessionMgr.(replacementStateManagerV3)
	if !ok {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "internal_error", message: "The live session manager does not support atomic replacement."}
	}
	// A transport cancel is evidence about one request, not a verdict on the
	// session: the viewer may have navigated off, or hls.js may have given up
	// and retried. It must never fence a live client's failure_recovery or
	// answer it as a missing session — at this layer a live recovery is
	// indistinguishable from a stale one, and running a superfluous recovery
	// costs far less than retiring a healthy session. Keep the mark
	// route-scoped so a predecessor's cancel cannot outlive its route, log it
	// for diagnostics, and drop a superseded one. A genuine stop stays fenced
	// by the session lookup above, which answers session_expired when the
	// session is gone or stopped.
	if failureRecoveryOperationV3(operation) &&
		sessionClientCanceledRecentlyV3(session.ClientCanceled.At, time.Now()) {
		currentRoute := session.ClientCancelNamesCurrentRoute()
		outcome := "zombie_recovery_candidate"
		if !currentRoute {
			outcome = "zombie_recovery_stale"
		}
		slog.InfoContext(r.Context(), "playback failure recovery ran after a canceled transport",
			logComponentKey, playbackLogValueV3,
			"outcome", outcome,
			"session_id", record.SessionID,
			"operation", string(operation),
			"failure_classification", req.Failure.Classification,
			"canceled_at", session.ClientCanceled.At.UTC().Format(time.RFC3339Nano),
			"canceled_revision", session.ClientCanceled.StreamRevision,
			"canceled_binding_generation", session.ClientCanceled.BindingGeneration,
		)
		if !currentRoute {
			// The mark names a route this session has already replaced. Drop it
			// so a predecessor's cancel cannot color a later read.
			_ = h.sessionMgr.ClearClientCanceled(record.SessionID)
		}
	}
	// An explicit virtual version pin is never substituted. A decode rejection
	// on it terminalls immediately with the version-list hint so the viewer can
	// pick another release; rotation is reserved for auto selections.
	if strings.TrimSpace(session.VirtualSourceURI) != "" &&
		operation == playback.ReplanOperationFailureRecoveryV3 &&
		decodeFailureClassificationV3(req.Failure.Classification) &&
		record.CurrentPlan.Delivery == playback.DeliveryTranscodeHLSV3 &&
		planHasVideoEncodeV3(record.CurrentPlan) &&
		record.NormalizedRequest.FileSelection == playback.FileSelectionExplicitV3 {
		terminal := &playback.TerminalV3{
			Reason:    sourceDecodeFailedReasonV3,
			Message:   "The selected media version could not be decoded.",
			Retryable: false,
		}
		hintExplicitSelectionAlternateAvailableV3(terminal, record.NormalizedRequest.FileSelection)
		return playback.NewTerminalResponseFromTerminalV3(terminal), *record, nil, nil
	}
	// Reactive software-decode recovery. A hardware decoder can reject a source
	// the planner believed it could decode (invalid-bitstream failures from the
	// first frame). The planner has no way to express a software-decode
	// retry, so when the live transcode session reports the decoder gave up,
	// force the next server-transcode plan onto the software decode path. This
	// stays client-driven: the retry happens inside the existing
	// failure_recovery replan, with no server->client command.
	forceSoftwareDecode := false
	decodeFailureSample := ""
	decodeFailureCount := 0
	decodeAttemptDetail := ""
	// The frame counter is what separates a decoder that is producing pictures
	// from one that is only muxing audio: the decode counters alone cannot say
	// whether anything was ever decoded.
	var decodeVideoFrames, decodeOutTimeUS int64
	if operation == playback.ReplanOperationFailureRecoveryV3 &&
		record.CurrentPlan.Delivery == playback.DeliveryTranscodeHLSV3 &&
		!record.CurrentPlan.EffectiveRecipe.SoftwareVideoDecode &&
		planHasVideoEncodeV3(record.CurrentPlan) {
		executedHWAccel := strings.TrimSpace(session.TranscodeHWAccel)
		if executedHWAccel != "" && !strings.EqualFold(executedHWAccel, playback.HWAccelNone) &&
			!playback.RequiresSoftwareVideoDecode(record.CurrentPlan.Source.VideoCodec, record.CurrentPlan.Source.VideoProfile, record.CurrentPlan.Source.BitDepth) {
			if ts := h.tm.GetTranscodeSession(record.SessionID); ts != nil && ts.IsDecodeFailed() {
				// gpu_only forbids the reactive CPU-decode retry: the operator
				// asked for GPU playback, so a hardware decoder rejection is a
				// terminal verdict for this candidate, not a prompt to decode
				// on the CPU. The hardware plan key is already in the attempted
				// set, so the planner surfaces adaptation_exhausted (or the
				// virtual recovery loop re-grabs a different candidate with the
				// failed one excluded). allow keeps the existing retry.
				forceSoftwareDecode = h.softwareFallbackAllowedV3() && softwareDecodeVariantPendingV3(record, req)
				if forceSoftwareDecode {
					decodeFailureSample, decodeFailureCount = ts.DecodeFailureEvidence()
				}
			}
		}
	} else if operation == playback.ReplanOperationFailureRecoveryV3 &&
		record.CurrentPlan.Delivery == playback.DeliveryTranscodeHLSV3 &&
		record.CurrentPlan.EffectiveRecipe.SoftwareVideoDecode &&
		planHasVideoEncodeV3(record.CurrentPlan) {
		// The software variant was the plan that just failed, so both decoder
		// modes are exhausted. Name them on the terminal detail so it is not
		// empty when the planner returns adaptation_exhausted.
		decodeAttemptDetail = fmt.Sprintf("hardware and software video decode both attempted for %s (hw_accel=%s)", record.CurrentPlan.Delivery, strings.TrimSpace(session.TranscodeHWAccel))
	}
	// Read the frame counter for every video encode, not only the software
	// retry. The counter is the only evidence that separates a decoder
	// producing pictures from one muxing audio alone, and reading it only
	// inside the forceSoftwareDecode branch logged video_frames=0
	// out_time_us=0 for ordinary hardware encodes whose segments each carried
	// frames. A zero here has to mean zero decoded pictures.
	if !forceSoftwareDecode &&
		record.CurrentPlan.Delivery == playback.DeliveryTranscodeHLSV3 &&
		planHasVideoEncodeV3(record.CurrentPlan) {
		if ts := h.tm.GetTranscodeSession(record.SessionID); ts != nil {
			decodeVideoFrames, decodeOutTimeUS = ts.VideoFrameProgress()
		}
	}
	// Rotation is candidate substitution, never a decode-mode change: an
	// explicit pin is not substituted, and for a virtual non-explicit decode
	// failure the recovery is another provider release under the same delivery,
	// not a forced CPU decode of the rejected one. The operator's gpu_only /
	// software-fallback policy therefore stays untouched for local sources.
	if virtualDecodeRotation {
		forceSoftwareDecode = false
	}
	virtualRehydrationFailed := false
	var virtualRehydrationErr error
	// replanVirtualProvenance is the resolver's provenance for the rehydrated
	// effective source, published additively on the replan's plan. Empty when
	// the source is not virtual or the rehydration did not resolve.
	replanVirtualProvenance := ProbeProvenance("")
	if isVirtualPlaybackFile(currentEffectiveFile) {
		if session.VirtualSourceURI == "" {
			slog.WarnContext(r.Context(), "virtual playback rehydration has no pinned source", "component", "api", "session_id", record.SessionID, "file_id", currentEffectiveFile.ID)
			virtualRehydrationFailed = true
		} else {
			pinnedFile := *currentEffectiveFile
			pinnedFile.FilePath = session.VirtualSourceURI
			pinnedFile.VirtualOwnerInstallationID = session.VirtualSourceOwnerInstallationID
			// When the catalog row already points at the exact session-bound
			// candidate and carries complete probed evidence, rehydrating it
			// again would force a synchronous provider probe (up to 15s) for
			// metadata the planner already has. Skip the round-trip entirely
			// and use the loaded row directly.
			// Widened reuse guard: a same-release identity (same provider result
			// id under the same provider-neutral path) counts as unchanged even
			// when the catalog row string differs, so a same-file re-plan reuses
			// the probed row and lets the attempted-key guard advance the
			// transformation rung instead of short-circuiting into a re-list.
			candidateUnchanged := currentEffectiveFile.FilePath == session.VirtualSourceURI ||
				(virtualResultCandidateID(currentEffectiveFile.FilePath) != "" &&
					virtualResultCandidateID(currentEffectiveFile.FilePath) == virtualResultCandidateID(session.VirtualSourceURI) &&
					virtualPlaybackNeutralKey(currentEffectiveFile.FilePath) == virtualPlaybackNeutralKey(session.VirtualSourceURI))
			evidenceComplete := completeVirtualVideoEvidenceV3(currentEffectiveFile) &&
				completeVirtualAudioEvidenceV3(currentEffectiveFile) &&
				completeVirtualContainerEvidenceV3(currentEffectiveFile)
			// A decode-classified failure on a non-explicit selection always
			// takes the exclusion path, even when the catalog row still names
			// the session-bound candidate and its evidence is complete: that
			// short-circuit would re-mount the exact release the decoder just
			// rejected. Rotation must happen on the provider result id.
			// A user selection operation — a track change or a seek reanchor —
			// re-selects within the source the session is already serving and
			// never swaps the release. When the session holds a bound virtual
			// candidate that the catalog row still names, or whose plan-time
			// inventory was captured on the session, carry that binding forward
			// instead of re-listing. The listing is keyed by per-listing
			// provider result ids that churn for bytes that never changed, so a
			// mid-session re-resolve pays a multi-second round-trip and can hand
			// back a token the identity guard then refuses to adopt: the
			// same-row track pick that silently does nothing, and the seek that
			// stalls or reloads.
			//
			// A row whose only relationship to the pin is a matching release
			// under a different token, with no session evidence to describe the
			// pinned bytes, still re-resolves: an offline pin then surfaces as
			// source_unavailable through the existing rotation semantics
			// instead of the replan planning against stale metadata. The
			// release is never swapped silently.
			//
			// pinnedRowProvenance is the one evaluation of "the session's
			// retained evidence describes the pinned row". Every admission path
			// reads it, including the candidateUnchanged shortcut: a same-row
			// binding move can leave the reloaded row naming the new pin while
			// the running plan's descriptor still describes the previous
			// candidate, and row-id equality alone cannot tell that apart from a
			// same-release re-plan. sessionProvenanceMatchesPinnedRowV3 is the
			// authority for that judgement.
			pinnedRowProvenance := sessionProvenanceMatchesPinnedRowV3(r.Context(), session, currentEffectiveFile, h.fileResolver)
			// A carry tops the carried row up with the running plan's validated
			// source descriptor, so the descriptor must describe the candidate
			// being carried. Session provenance alone cannot prove that: serving
			// or probing can advance the session's evidence to another candidate
			// while the durable plan still describes the previous one, and the
			// carry would then pair the old descriptor with the new bytes.
			planCarriesPin := sameVirtualCandidate(strings.TrimSpace(record.CurrentPlan.EffectiveVirtualURI), strings.TrimSpace(session.VirtualSourceURI))
			carryPinnedCandidate := (trackChange || seekReanchor) && !virtualDecodeRotation &&
				strings.TrimSpace(session.VirtualSourceURI) != "" &&
				pinnedRowProvenance &&
				planCarriesPin &&
				// A user-intent replan keeps the session's pinned candidate when
				// the reloaded row is the pin's own row under a churned
				// per-listing token (the probe-evidence flag) or that profile's
				// provider-neutral row (virtualPinnedProfileRowV3). The provider
				// churns the per-listing result id for bytes that never changed,
				// so a re-listing pays a multi-second round-trip to hand back a
				// token the identity guard then refuses to adopt. Admission is
				// provenance-checked, not presence-checked: a stop, a binding
				// move, or a rotation can leave the anchor naming the previous
				// candidate, and trusting it would bypass resolution while the
				// binder, which rejects the mismatch, cannot supply trustworthy
				// inventory. A row whose only relationship to the pin is a
				// different release keeps re-resolving, so an offline pin
				// surfaces as source_unavailable through the existing rotation
				// semantics instead of planning against a stub. The release is
				// never swapped silently.
				(candidateUnchanged ||
					session.VirtualSubtitleEvidenceSet ||
					virtualPinnedProfileRowV3(session, currentEffectiveFile))
			// A seek reanchor replays the frozen recipe verbatim, so the bytes
			// it continues are the running plan's candidate, not the session's
			// pin. When the pin has moved since the plan was committed, binding
			// the pin would pair the replayed descriptor and recipe with a
			// different release's bytes — the franken-evidence the carry gate
			// exists to refuse. Bind the plan's own candidate instead, and do it
			// ahead of the same-row shortcuts so the effective file, the
			// replayed plan, and the transport all describe one release.
			seekReanchorCandidate := ""
			if seekReanchor && record.FrozenRecipe.ValidFor(record.CurrentPlan) {
				seekReanchorCandidate = strings.TrimSpace(record.CurrentPlan.EffectiveVirtualURI)
			}
			if seekReanchorCandidate != "" {
				// Rebuild the effective file for the frozen candidate from
				// evidence that demonstrably belongs to it. The reloaded row may
				// already describe another release, and the session's live pin
				// and owner may have moved: replaying the frozen recipe through
				// either would pair the running plan's descriptor and recipe with
				// foreign bytes or another installation's credentials.
				frozenCarrier, frozenErr := h.frozenSeekCarrierV3(r.Context(), session, record, currentEffectiveFile)
				if frozenErr != nil {
					return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
						reason:    seekReanchorEvidenceUnavailableReasonV3,
						message:   "The frozen playback source could not be reopened; start a new playback attempt.",
						retryable: true,
						cause:     frozenErr,
					}
				}
				currentEffectiveFile = frozenCarrier
			} else if candidateUnchanged && evidenceComplete && !virtualDecodeRotation {
				currentEffectiveFile.FilePath = session.VirtualSourceURI
				currentEffectiveFile.VirtualOwnerInstallationID = session.VirtualSourceOwnerInstallationID
			} else if carryPinnedCandidate {
				// Bind the session's pinned candidate and overlay the plan-time
				// inventory captured with it, so the requested selection
				// resolves against the release the viewer is actually watching
				// rather than a churned listing token. The row id and every
				// transport field stay exactly as the plan named them.
				carried := bindSessionVirtualSourceWithTracks(r.Context(), &pinnedFile, session, h.fileResolver)
				carried.FilePath = session.VirtualSourceURI
				carried.VirtualOwnerInstallationID = session.VirtualSourceOwnerInstallationID
				// bindSessionVirtualSourceWithTracks overlays the plan-time
				// subtitle inventory; audio evidence travels on the session
				// too, and a track change must remap against the pinned
				// release's audio list, not the churned row's.
				if session.VirtualSubtitleEvidenceSet && len(session.VirtualAudioTracks) > 0 &&
					virtualEvidenceMatchesBoundFile(r.Context(), carried, session, h.fileResolver) {
					carriedCopy := *carried
					carriedCopy.AudioTracks = session.VirtualAudioTracks
					carried = &carriedCopy
				}
				// The catalog row may be a fresh listing row the provider has
				// never let the probe stamp: it carries no bit depth, frame
				// rate, or bitrate, and planning against it would terminal
				// source_metadata_incomplete on a user-intent replan that
				// re-selects within the release already playing. The running
				// plan's validated source descriptor is the carried probe
				// evidence for exactly these bytes, so prefer it over the
				// unprobed row. Only gaps are filled. The provenance gate that
				// admitted carryPinnedCandidate is what makes the descriptor
				// current for these bytes; the carry itself can only compare
				// row ids, never bytes.
				carried = carryValidatedVirtualVideoEvidenceV3(carried, record.CurrentPlan.Source)
				currentEffectiveFile = carried
			} else {
				// The session-bound release is preferred so re-ranking cannot
				// drift to a different provider candidate.
				preferredCandidateID := virtualResultCandidateID(session.VirtualSourceURI)
				var excludedCandidateIDs []string
				// Only a verdict that indicts the release may exclude the
				// session-bound candidate. `virtualDecodeRotation` is the
				// server-confirmed decode rejection; a dead or unavailable
				// provider surfaces as the pinned id being absent from the
				// provider list, which the resolver still substitutes. Every
				// other failure — display-driven fallback, route error, stale
				// credential — keeps the same file so the attempted-key guard
				// advances to the next transformation rung on it (for example
				// the server DV7->HDR10 strip) instead of swapping the release.
				failedID := virtualResultCandidateID(session.VirtualSourceURI)
				if failedID == "" {
					// The session holds no binding (e.g. a fresh start that
					// failed before committing one); fall back to the catalog
					// row the plan was built against.
					failedID = virtualResultCandidateID(currentEffectiveFile.FilePath)
				}
				providerSource := virtualAttemptProviderSourceV3(session.VirtualSourceURI)
				if providerSource == "" {
					providerSource = virtualAttemptProviderSourceV3(currentEffectiveFile.FilePath)
				}
				if failedID != "" && virtualDecodeRotation {
					// Persist the confirmed verdict on the attempt before
					// rotating. The durable chain is what makes a later replan
					// — on this replica or another after a reload — skip every
					// candidate already indicted, instead of relying on the
					// asynchronous failed_at marker landing in time. A failed
					// write must surface as a controlled error rather than let
					// the rotation publish an untracked candidate the next hop
					// could re-select.
					exclusion := playback.RecoveryExclusionV3{
						ProviderSource: providerSource,
						CandidateID:    failedID,
						ReleaseID:      virtualCandidateReleaseIdentityV3(currentEffectiveFile),
						FileID:         record.EffectiveMediaFileID,
						ConfirmedAt:    time.Now().UTC(),
					}
					chain, persistErr := h.appendRecoveryExclusionsV3(r.Context(), record, []playback.RecoveryExclusionV3{exclusion})
					if persistErr != nil {
						slog.ErrorContext(r.Context(), "virtual decode rejection exclusions could not be persisted", "component", "api", "session_id", record.SessionID, "candidate_id", failedID, "error", persistErr)
						return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
							reason:    "recovery_state_unavailable",
							message:   "The server could not durably record the failed source candidate; retry the replay.",
							retryable: true,
							cause:     persistErr,
						}
					}
					record.RecoveryState = chain
					// Union every confirmed exclusion in this provider scope,
					// not just the candidate that just failed: a prior hop the
					// durable chain already recorded must stay excluded even
					// when its failed_at marker never landed or was cleared.
					excludedCandidateIDs = playback.RecoveryExcludedCandidateIDsV3(chain, providerSource)
					if !containsStringExactV3(excludedCandidateIDs, failedID) {
						excludedCandidateIDs = append(excludedCandidateIDs, failedID)
					}
					// A suspected-but-unconfirmed sibling is still eligible:
					// only confirmed verdicts enter the chain, so no extra
					// filtering is applied here.
				}
				// Only a confirmed rotation may re-select a candidate the
				// catalog marked failed (for example one this serve layer just
				// stamped after it produced no bytes). Every other replan must
				// honor the known-bad stamp so the resolver skips it and either
				// finds a live sibling or fails with a retryable terminal,
				// instead of looping back onto the dead pin.
				//
				// An absent/renumbered session anchor is its own rotation cause:
				// resolveRehydratedVirtualSourceV3 retries with rotation declared
				// when the resolver reports the pinned candidate is no longer
				// listed. Session-bound stays true; only the anchor rotates.
				// Rotation honors active failed_at stamps: allowFailed stays
				// false so a sibling the marker already stamped (a prior hop
				// in this chain) is skipped rather than re-mounted. The
				// durable chain travels explicitly in excludedCandidateIDs,
				// so nothing needs the bypass; bypassing stamps here is what
				// would let sequential rotations cycle A→B→C→A instead of
				// terminating when every sibling is known-bad.
				//
				// One rotation policy for the session. The retry's same-release
				// assertion is the single arbiter of what an explicit pin may
				// accept: a renumbered or dropped anchor is re-identified by its
				// durable provider identity and allowed through (the same
				// recovery the transport anchor and serve-layer doors allow),
				// while a genuinely different release, or one with no durable
				// identity to prove the match, is refused and keeps the original
				// absent/marked-failed cause. An explicit pick is therefore never
				// substituted with a different release on any door, and an auto
				// selection keeps its documented renumbered/dead-pin recovery.
				// The terminal-driven alternate-file hunt below is separately
				// gated by the negotiated fallback policy, so a failed
				// rehydration never silently swaps in a sibling version.
				resolved, resolveErr := h.resolveRehydratedVirtualSourceWithAttemptV3(r, &pinnedFile, record.ProfileID, excludedCandidateIDs, preferredCandidateID, start.QualityPreference, intOrZeroHandlerV3(start.BandwidthCapKbps), virtualResolveOptionsV3{allowFailedCandidate: false, rotateCandidates: virtualDecodeRotation, sessionBound: true, sessionAnchorURI: session.VirtualSourceURI, bypassProviderFloor: virtualReplanNeedsFreshProviderListingV3(operation, req.Failure.Classification, virtualDecodeRotation)}, record)
				if resolveErr != nil {
					slog.WarnContext(r.Context(), "virtual playback rehydration failed", "component", "api", "session_id", record.SessionID, "file_id", currentEffectiveFile.ID, "owner_installation_id", session.VirtualSourceOwnerInstallationID, "error", logredact.SanitizeURLError(resolveErr))
					virtualRehydrationFailed = true
					virtualRehydrationErr = resolveErr
				} else if resolved.File == nil {
					slog.WarnContext(r.Context(), "virtual playback rehydration returned no file", "component", "api", "session_id", record.SessionID, "file_id", currentEffectiveFile.ID, "owner_installation_id", session.VirtualSourceOwnerInstallationID)
					virtualRehydrationFailed = true
				} else {
					resolvedFile := *resolved.File
					// The effective file is the candidate the resolver actually
					// served; keep its own catalog row id (the substituted
					// candidate) instead of overwriting it with the record's
					// previous effective id, so the replan plan can name the
					// bytes it will play. The plan's requested id stays the
					// record's requested row.
					resolvedFile.FilePath = resolved.URI
					resolvedFile.VirtualOwnerInstallationID = resolved.OwnerID
					// A same-candidate failure recovery re-resolves the release
					// the running plan validated, and the provider may serve a
					// catalog row that never received the probe's video
					// metadata. Carry the running plan's validated descriptor
					// when identity, installation, and session provenance all
					// still describe the same bytes, exactly as the
					// user-intent carry does. Without it the planner terminals
					// source_metadata_incomplete on a route that just played.
					carryProvenance := sessionProvenanceMatchesPinnedRowV3(r.Context(), session, resolved.File, h.fileResolver)
					if sameCandidateFailureDescriptorCarryV3(operation, session, record, carryProvenance, resolved, virtualDecodeRotation) {
						resolvedFile = *carryValidatedVirtualVideoEvidenceV3(&resolvedFile, record.CurrentPlan.Source)
					}
					currentEffectiveFile = &resolvedFile
					replanVirtualProvenance = resolved.Provenance
				}
			}
		}
	} else {
		currentEffectiveFile = h.ensurePlaybackProbe(r.Context(), currentEffectiveFile)
	}
	// Freeze the session's first-verified Dolby Vision profile onto the freshly
	// read effective file before planning. A replan re-reads the catalog row and
	// can see a drifted dv_profile on bytes that never changed; the pin keeps
	// route selection stable for the session without touching catalog state.
	applyDVPinToEffectiveFileV3(session, currentEffectiveFile)
	if virtualRehydrationFailed && operation != playback.ReplanOperationFailureRecoveryV3 {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
			reason: virtualSourceUnavailableReasonV3, message: "The virtual source could not be refreshed for playback.", retryable: true, cause: virtualRehydrationErr,
		}
	}
	failedEffectiveFile := currentEffectiveFile
	if virtualRehydrationFailed && errors.Is(virtualRehydrationErr, context.DeadlineExceeded) {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
			reason: replanVirtualInputTimeoutReasonV3, message: replanVirtualInputTimeoutMessageV3, retryable: true, cause: virtualRehydrationErr,
		}
	}
	// A canceled virtual re-resolve is not a verdict about the release: the
	// provider call was interrupted (most often the client or the replan
	// deadline gave up). Continuing into the alternate-version hunt would fold
	// that cancellation into a candidate-exhaustion verdict and can surface as
	// a 500 after a later route-capacity decision. Stop here with the same
	// retryable timeout reason so the client retries the whole replan instead
	// of being told its plan is broken. The check also catches a request whose
	// context is already done even when the inner error is not wrapped.
	if virtualRehydrationFailed &&
		(errors.Is(virtualRehydrationErr, context.Canceled) || r.Context().Err() != nil) {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
			reason: replanVirtualInputTimeoutReasonV3, message: "Virtual stream input resolution was interrupted during replan.", retryable: true, cause: virtualRehydrationErr,
		}
	}
	// The virtual resolver has already walked every provider candidate for this
	// release (and its stale-source fallback) without a usable source, or the
	// provider itself is down. The sibling-edition walk below would resolve each
	// alternate through the same provider, so it can only re-pay the resolve and
	// probe budgets before reaching this verdict again. Answer with the same
	// classified terminal now; only the time to reach it changes.
	if virtualRehydrationFailed && !virtualDecodeRotation && virtualReplanRotationExhaustedV3(virtualRehydrationErr) {
		return playback.DecisionResponseV3{}, *record, nil, classifyVirtualReplanExhaustionV3(virtualRehydrationErr, nil)
	}

	var preparedTransport *preparedTransportV3
	var artifactRecipe playback.ExecutableRecipeV3
	// recordSubtitleBreaker arms the repeated-unhonorable-subtitle no-op after
	// the rejection plan commits. A failed replan must not leave a rejection
	// recorded for a plan the client never received.
	var recordSubtitleBreaker func()
	// replanCapabilityWarnings holds the degradation warnings produced by the
	// one normalization of the merged start request, so they can be surfaced on
	// whichever plan this replan ultimately returns.
	var replanCapabilityWarnings []playback.DegradationWarningV3
	transportPrepared := false
	// One version-fallback policy for the whole replan. Both alternate-version
	// hunts — the rehydration-failure recovery below and the terminal-driven one
	// further down — read this single value, so an explicit version pick is never
	// substituted on either door and a viewer's mid-session re-arm of Auto is
	// honored consistently. resolveReplanAutoFallbackV3 prefers the live session
	// flag over the durable start request, so a reconstructed session keeps the
	// negotiated intent. Without this gate the rehydration path silently swapped
	// an explicit pin's release whenever its rehydration failed but a sibling
	// edition was still available.
	replanFallbackAllowed := resolveReplanAutoFallbackV3(h.sessionMgr, session.ID, record) &&
		(replanAllowsAlternateFileV3(operation, start.QualityPreference) ||
			(isVirtualPlaybackFile(requestedFile) && operation == playback.ReplanOperationFailureRecoveryV3))
	// An output change keeps the active edition only because the requested
	// edition lacks the selected subtitle. Once the subtitle is dropped, the
	// requested edition is the picture the viewer asked for and is tried first.
	outputChangeKeptActiveForSubtitle := false

	// detourDroppedSubtitle remembers a selection a speculative detour
	// through the requested edition could not honor; validation would then
	// drop anything still naming another file. A plan that plays on without
	// the viewer's subtitle reports it instead of going quiet.
	detourDroppedSubtitle := false
	// driftSubtitleUnavailable remembers a selection the active row itself lost
	// when it was re-probed in place. There is no fallback version holding it,
	// so the drop is reported unconditionally.
	driftSubtitleUnavailable := false
	// reportDetourSubtitleDrop appends the dropped-subtitle warning when
	// a speculative detour cost the viewer a selection no played version
	// honors. It runs on the final plan, so a fallback that keeps the
	// subtitle stays quiet while one that plays muted reports it.
	reportDetourSubtitleDrop := func() {
		if !detourDroppedSubtitle || result.Terminal != nil || result.Plan == nil || result.Plan.SelectedTracks.Subtitle != nil {
			return
		}
		for _, warning := range result.Plan.DegradationWarnings {
			if warning.Code == playback.DegradationWarningSubtitleTrackUnavailableV3 {
				return
			}
		}
		result.Plan.DegradationWarnings = append(result.Plan.DegradationWarnings, playback.SubtitleTrackUnavailableWarningV3())
	}

	if virtualRehydrationFailed {
		// Gate the alternate hunt on the same negotiated fallback policy the
		// non-rehydration path applies. An explicit pin whose release fails to
		// rehydrate must not be substituted: the resolver refused the sibling
		// above, and hunting for one here would contradict that refusal. When the
		// policy withholds fallback no alternate is tried, and the honest
		// refresh cause is returned through the exhaustion classifier below.
		var alternates []*models.MediaFile
		if replanFallbackAllowed {
			alternateOrder := alternateOrderingForClient(req.Capabilities)
			found, alternateErr := h.findAlternateFilesOrdered(r.Context(), requestedFile, requestAccessFilter(r), alternateOrder)
			if alternateErr != nil {
				virtualRehydrationErr = errors.Join(virtualRehydrationErr, alternateErr)
			}
			alternates = found
		}
		plannerSettings, plannerSettingsErr = h.plannerSettingsV3Result(r.Context())
		attemptedKeys := append([]string(nil), req.AttemptedPlanKeys...)
		if !generationFence && !containsStringExactV3(attemptedKeys, req.PlanAttemptKey) {
			attemptedKeys = append(attemptedKeys, req.PlanAttemptKey)
		}
		currentKey := playback.PlanAttemptKeyV3(record.CurrentPlan, record.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID, req.LocalMutations)
		if !generationFence && !containsStringExactV3(attemptedKeys, currentKey) {
			attemptedKeys = append(attemptedKeys, currentKey)
		}
		if virtualDecodeRotation {
			attemptedKeys = withoutRotatedPlanKeysV3(attemptedKeys, req.PlanAttemptKey, currentKey)
		}

		var candidateErrs []*candidateV3Error
		virtualRecoverySucceeded := false
		var subtitleMissAlternate *models.MediaFile
		for _, alternate := range alternates {
			if alternate.ID == failedEffectiveFile.ID {
				continue
			}
			eval, err := h.evaluateReplanCandidateV3(r, session, record, req, start, failedEffectiveFile, alternate, plannerRequestedFile, plannerSettings, plannerSettingsErr, attemptedKeys, lowerVersion)
			if err != nil {
				candidateErrs = append(candidateErrs, err)
				if candidateSubtitleLocalFailureV3(err) && subtitleMissAlternate == nil {
					subtitleMissAlternate = alternate
				}
				slog.WarnContext(r.Context(), "virtual replan candidate alternate failed",
					"component", "api", "session_id", record.SessionID,
					"requested_file_id", record.RequestedMediaFileID, "failed_file_id", failedEffectiveFile.ID,
					"candidate_file_id", alternate.ID, "stage", string(err.Stage), "error", logredact.SanitizeURLError(err),
				)
			}
			if eval != nil && eval.result.Terminal == nil {
				start = eval.start
				effectiveFile = eval.file
				result = eval.result
				toneMapCapabilityErr = eval.toneMapErr
				artifactRecipe = eval.frozenRecipe
				preparedTransport = &eval.transport
				transportPrepared = true
				reservationHeld = eval.reservationHeld
				virtualRecoverySucceeded = true
				break
			}
		}
		if !virtualRecoverySucceeded && subtitleMissAlternate != nil {
			// No candidate had an equivalent track. Degrade the first such
			// candidate to subtitles-off rather than terminalling playback.
			if eval, evalErr := h.evaluateSubtitleDegradedCandidateV3(r, session, record, req, start, failedEffectiveFile, subtitleMissAlternate, plannerRequestedFile, plannerSettings, plannerSettingsErr, attemptedKeys, lowerVersion); evalErr == nil && eval != nil && eval.result.Terminal == nil {
				start = eval.start
				effectiveFile = eval.file
				result = eval.result
				toneMapCapabilityErr = eval.toneMapErr
				artifactRecipe = eval.frozenRecipe
				preparedTransport = &eval.transport
				transportPrepared = true
				reservationHeld = eval.reservationHeld
				virtualRecoverySucceeded = true
			}
		}
		if !seekReanchor && plannerSettingsErr == nil {
			if requestedEditionResolved {
				lowerVersion = h.lowerVersionV3(r.Context(), start, requestedFile, plannerSettings, requestAccessFilter(r))
			}
		}
		if !virtualRecoverySucceeded {
			if virtualDecodeRotation {
				// The rejected release had no untried sibling (and no sibling
				// edition recovered it). Retire the delivery and terminal with
				// the decode reason so the viewer can pick another release.
				demoteDeliveryCapabilityV3(&record.NormalizedRequest, record.CurrentPlan.Delivery)
				demoteDeliveryCapabilityV3(&start, record.CurrentPlan.Delivery)
				terminal := &playback.TerminalV3{
					Reason:    sourceDecodeFailedReasonV3,
					Message:   sourceDecodeFailedMessageV3,
					Retryable: false,
				}
				hintExplicitSelectionAlternateAvailableV3(terminal, record.NormalizedRequest.FileSelection)
				return playback.NewTerminalResponseFromTerminalV3(terminal), *record, nil, nil
			}
			return playback.DecisionResponseV3{}, *record, nil, classifyVirtualReplanExhaustionV3(virtualRehydrationErr, candidateErrs)
		}
	} else {
		effectiveFile = currentEffectiveFile
		if bufferedLoweredFile != nil {
			// The Auto budget already chose a lower version; the row reset above
			// must not undo it.
			effectiveFile = bufferedLoweredFile
		}
		// A replan's carried selection was minted against the plan-time
		// inventory. The plan being replanned is the strongest evidence (the
		// request names that exact attempt); otherwise a virtual session's
		// captured evidence stands in. When the catalog row was re-probed in
		// place (same id, different release) the freshly loaded effective
		// file's tracks no longer describe what the ordinal names, so remap
		// from the evidence rather than the row.
		durableInventory := replanDurableInventoryV3(record)
		remapCarry := h.replanRemapSourceV3(r.Context(), currentEffectiveFile, session, durableInventory)
		remapSource := remapCarry.file
		// A track_change replaces the seeded selection with the viewer's choice
		// from the current menu, so its ordinals already address the live
		// inventory. Every other replan carries the plan-time ordinal and relies
		// on the drift remap below to reconcile it into the effective row's
		// inventory. Track both so the re-key step applies predecessor inventory
		// to a carried ordinal exactly once, never to a fresh or already
		// reconciled one.
		subtitleSelectionCurrent := trackChange
		currentEffectiveStart := start
		if intentChange && !trackChange {
			// Prefer returning to the requested edition, but a quality/output/track
			// change must not abandon a healthy active alternate merely because the
			// inactive original has gone missing since playback started. For virtual
			// sources, the requested file is a catalog placeholder without probed streams;
			// preserve the probed candidate stream. A lower version an Auto budget
			// already picked outranks the requested edition: the budget is why the
			// requested row cannot play as it is.
			if bufferedLoweredFile == nil && !isVirtualPlaybackFile(currentEffectiveFile) && requestedEditionResolved && preflightPlaybackFile(r.Context(), requestedFile, h.MissingMarker, h.EventsHub) == nil {
				effectiveFile = requestedFile
			}
		}
		// Track identities only need remapping when the effective inventory
		// actually changes. Remapping within an unchanged inventory would
		// degrade an exact selection to a best-match lookup — e.g. moving a
		// listener from an eng/ac3 commentary track to the identically-shaped
		// main track on a quality change. A same-row candidate rotation keeps
		// the id but replaces the tracks, so the guard is the union of an id
		// change (a real edition switch, which always remaps), an inventory
		// fingerprint change under a fixed id, and a recorded candidate
		// rotation (a moved revision) whose tracks happen to compare equal.
		//
		// The fingerprint and rotation guard run for every replan that keeps
		// the carried selection, not only an intent change: a seek-only or
		// recovery replan after an in-place rotation was reusing its ordinal
		// positionally. A track change is excluded because the client's new
		// selection was made against the current inventory, not the plan-time
		// one.
		// A recorded candidate rotation is the one case where the carried
		// subtitle ordinal must be re-identified with the strict release matcher
		// at the commit, not with the generic edition-switch remap below. The
		// verdict travels with the request so the stream-state commit can record
		// the new position and answer pre-rotation subtitle URLs.
		rotationRecorded := !trackChange && virtualCandidateRotationRecordedV3(r.Context(), session, currentEffectiveFile, h.fileResolver)
		subtitleRotationCarry := subtitleRotationOutcomeV3{}
		// The carried ordinal was minted against the durable plan inventory when
		// this replica has it; otherwise the session's retained evidence (with
		// the downloaded tail recovered from its attempt plan) is the authority.
		// The live row speaks only when neither exists, which a recorded rotation
		// never allows.
		rotationCarryID := currentEffectiveFile.ID
		if _, durableKnown := durableInventory[rotationCarryID]; !durableKnown && session != nil && session.VirtualSubtitleEvidenceFileID > 0 {
			rotationCarryID = session.VirtualSubtitleEvidenceFileID
		}
		rotationCarry := h.rekeySourceFileWithInventoryV3(r.Context(), rotationCarryID, session, durableInventory)
		if !trackChange && (intentChange && currentEffectiveFile.ID != effectiveFile.ID ||
			!sameMediaInventoryV3(remapSource, effectiveFile) ||
			rotationRecorded) {
			candidateStart := start
			subtitleReconciled := false
			// An edition switch carries the viewer's intent, not the current
			// plan's tracks. The plan may have dropped a subtitle the target
			// still has; desired remembers it, so restore it here instead of
			// remapping whatever the playing edition could deliver.
			desiredApplied := intentChange && currentEffectiveFile.ID != effectiveFile.ID && desired != nil
			var remapErr error
			if desiredApplied {
				var subtitleDropped bool
				subtitleDropped, remapErr = h.applyDesiredTracksV3(r.Context(), desired, effectiveFile, &candidateStart)
				if subtitleDropped && outputChange && record.CurrentPlan.SelectedTracks.Subtitle != nil {
					// An output refresh is not a request to leave the edition that
					// still shows the selected subtitle. Stay on it and apply the
					// viewer's intent there instead of playing the requested
					// edition with the subtitle dropped.
					effectiveFile = currentEffectiveFile
					outputChangeKeptActiveForSubtitle = true
					candidateStart = start
					subtitleDropped, remapErr = h.applyDesiredTracksV3(r.Context(), desired, currentEffectiveFile, &candidateStart)
				}
				detourDroppedSubtitle = detourDroppedSubtitle || subtitleDropped
				subtitleReconciled = remapErr == nil
			} else {
				remapErr = remapAudioSelectionV3(remapSource, effectiveFile, &candidateStart)
			}
			// carrySubtitleSelectionV3 reconciles the carried ordinal against the
			// candidate this attempt will serve. It returns the outcome so the
			// commit can record it; a fuzzy match keeps the selection and reports
			// one warning, a miss is reported through the existing unavailable
			// sentinel and degrade paths.
			carrySubtitleSelectionV3 := func(target *models.MediaFile, request *playback.StartRequestV3) (bool, error) {
				from, to, exact, matched, attempted, carryErr := h.reconcileRotationSubtitleSelectionV3(r.Context(), session, rotationCarry, target, request)
				if !attempted {
					// The ordinal does not classify against the evidence (a
					// captured-empty inventory, or a downloaded ordinal without
					// its captured tail). Leave it to the historical remap,
					// whose captured-empty and no-tail rules already fail closed.
					return false, nil
				}
				subtitleRotationCarry = subtitleRotationOutcomeV3{
					Recorded:         true,
					FromIndex:        from,
					ToIndex:          to,
					Matched:          matched,
					Exact:            exact,
					ToFileID:         target.ID,
					ToCandidateURI:   strings.TrimSpace(target.FilePath),
					FromCandidateURI: strings.TrimSpace(session.VirtualSubtitleEvidenceURI),
				}
				if carryErr != nil {
					return true, carryErr
				}
				if matched {
					return true, nil
				}
				slog.InfoContext(r.Context(), "subtitle selection is unavailable after the candidate rotation",
					"component", "playback", "event", "subtitle_unavailable_after_rotation",
					"playback_session_id", session.ID,
					"from_candidate_id", virtualResultCandidateID(session.VirtualSubtitleEvidenceURI),
					"to_candidate_id", virtualResultCandidateID(target.FilePath),
					"carried_index", from)
				return true, errSubtitleUnavailableInTargetV3
			}
			if remapErr == nil && !desiredApplied && (candidateStart.SubtitleTrackIndex != nil || candidateStart.SubtitleTrackID != "") {
				// The same-inventory fast path still runs first: when the rotated
				// release publishes exactly the evidence layout, the carried
				// ordinal already names the same position and no identity carry
				// (which could fail closed on an otherwise harmless duplicate) is
				// needed. Only a genuinely different inventory is re-identified
				// with the strict rotation matcher.
				if rotationRecorded && (remapCarry.file == nil || !sameSubtitleInventoryV3(remapCarry.file, effectiveFile)) {
					attempted, carryErr := carrySubtitleSelectionV3(effectiveFile, &candidateStart)
					if attempted {
						remapErr = carryErr
					} else {
						_, remapErr = h.remapSubtitleSelectionWithCarryV3(r.Context(), remapCarry, effectiveFile, &candidateStart)
					}
					// A successful carry leaves the ordinal addressing the
					// effective row's current inventory; the re-key step below must
					// not read it through the plan-time tail a second time.
					subtitleReconciled = remapErr == nil
				} else {
					_, remapErr = h.remapSubtitleSelectionWithCarryV3(r.Context(), remapCarry, effectiveFile, &candidateStart)
					subtitleReconciled = remapErr == nil
				}
			}
			if remapErr != nil && outputChange {
				// An output refresh may make the requested edition viable again,
				// but it must not retire a healthy active alternate merely because
				// the viewer selected a track unique to that alternate.
				outputChangeKeptActiveForSubtitle = requestedFile != nil && requestedFile.ID != currentEffectiveFile.ID
				effectiveFile = currentEffectiveFile
				// The row being kept is not necessarily the inventory the carried
				// ordinal was minted against: an in-place re-probe replaces its
				// tracks under the same id. Re-check the selection against the row
				// actually kept, or the fallback would play whatever now occupies
				// the old ordinal.
				if !trackChange && (!sameMediaInventoryV3(remapSource, effectiveFile) || rotationRecorded) {
					fallbackStart := start
					fallbackErr := remapAudioSelectionV3(remapSource, effectiveFile, &fallbackStart)
					fallbackSubtitleReconciled := false
					if fallbackErr == nil && (fallbackStart.SubtitleTrackIndex != nil || fallbackStart.SubtitleTrackID != "") {
						if rotationRecorded && (remapCarry.file == nil || !sameSubtitleInventoryV3(remapCarry.file, effectiveFile)) {
							if attempted, carryErr := carrySubtitleSelectionV3(effectiveFile, &fallbackStart); attempted {
								fallbackErr = carryErr
							} else {
								_, fallbackErr = h.remapSubtitleSelectionWithCarryV3(r.Context(), remapCarry, effectiveFile, &fallbackStart)
							}
						} else {
							_, fallbackErr = h.remapSubtitleSelectionWithCarryV3(r.Context(), remapCarry, effectiveFile, &fallbackStart)
						}
						fallbackSubtitleReconciled = fallbackErr == nil
					}
					switch {
					case fallbackErr == nil:
						start = fallbackStart
						subtitleSelectionCurrent = fallbackSubtitleReconciled
					case errors.Is(fallbackErr, errSubtitleUnavailableInTargetV3):
						// The kept row lost the selected subtitle in the same
						// reprobe. Clearing both fields keeps the fallback off
						// that ordinal and reports the loss below.
						fallbackStart.SubtitleTrackIndex = nil
						fallbackStart.SubtitleTrackID = ""
						start = fallbackStart
						driftSubtitleUnavailable = true
					case errors.Is(fallbackErr, errSubtitleStoreUnavailableV3):
						return playback.DecisionResponseV3{}, *record, nil, subtitleArtifactErrorV3("Downloaded subtitles are temporarily unavailable.", fallbackErr)
					default:
						return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: trackUnavailableReasonV3, message: fallbackErr.Error()}
					}
				}
			} else if errors.Is(remapErr, errSubtitleUnavailableInTargetV3) {
				// The target has no equivalent track. Playability wins: plan it
				// with subtitles off instead of terminalling.
				candidateStart.SubtitleTrackIndex = nil
				candidateStart.SubtitleTrackID = ""
				if effectiveFile.ID != currentEffectiveFile.ID {
					// A speculative detour to another edition keeps the carried
					// selection (with the remapped audio) so a failed detour
					// falls back with the viewer's pick intact; the detour plan
					// itself degrades the stale selection through the subtitle
					// policy and reports the drop below.
					detourDroppedSubtitle = true
					candidateStart.SubtitleTrackIndex = start.SubtitleTrackIndex
					candidateStart.SubtitleTrackID = start.SubtitleTrackID
				} else {
					// The active row itself was re-probed in place. Its old
					// ordinal now names whatever track occupies it, and
					// validation would accept it because the file id still
					// matches, so the selection must be dropped and reported
					// instead of restored.
					driftSubtitleUnavailable = true
				}
				start = candidateStart
			} else if errors.Is(remapErr, errSubtitleStoreUnavailableV3) {
				// A failed lookup says nothing about whether the selected track
				// exists on the target edition, so it is not a track miss. The
				// playing plan keeps its subtitle; the change can be repeated
				// once the downloaded subtitles can be read.
				return playback.DecisionResponseV3{}, *record, nil, subtitleArtifactErrorV3("Downloaded subtitles are temporarily unavailable.", remapErr)
			} else if remapErr != nil {
				return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: trackUnavailableReasonV3, message: remapErr.Error()}
			} else {
				start = candidateStart
				subtitleSelectionCurrent = subtitleReconciled
			}
		}
		// Only a verdict that belongs to the candidate this replan actually
		// commits may travel to the stream state; a later fallback that kept a
		// different row supersedes it and records its own.
		if subtitleRotationCarry.Recorded && subtitleRotationCarry.ToFileID == effectiveFile.ID {
			r = r.WithContext(withSubtitleRotationOutcomeV3(r.Context(), subtitleRotationCarry))
		}
		start.FileID = effectiveFile.ID
		if err := preflightPlaybackFile(r.Context(), effectiveFile, h.MissingMarker, h.EventsHub); err != nil {
			return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
				reason:  sourceUnavailableReason,
				message: effectiveSourceUnavailableMessageV3,
				cause:   err,
			}
		}
		seekDuration := float64(effectiveFile.Duration)
		if seekReanchor && record.FrozenRecipe.ValidFor(record.CurrentPlan) {
			seekDuration = record.FrozenRecipe.SourceDurationSeconds
		}
		if seekScopedRecovery && seekDuration > 0 && req.PositionSeconds > seekDuration {
			return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
				reason:  "invalid_seek_position",
				message: "The requested seek position is beyond the end of the selected media source.",
			}
		}
		// The client echoes identities minted by the running plan, which can
		// name the session's current effective row after a serve association
		// moved it without rewriting the durable record. Normalize those onto
		// the row this replan validates against; a foreign row is still dropped.
		replanAudioUnavailable, replanSubtitleUnavailable, rekeyErr := h.rekeyReplanTrackIdentitiesWithRotationV3(r.Context(), &start, effectiveFile, session, durableInventory, subtitleSelectionCurrent, rotationRecorded && !subtitleRotationCarry.Recorded)
		if rekeyErr != nil {
			// A subtitle-store failure cannot prove the carried track is absent.
			// Keep the playing plan authoritative and let the viewer retry
			// instead of reinterpreting the old ordinal on unverified evidence.
			if errors.Is(rekeyErr, errSubtitleStoreUnavailableV3) {
				if !seekReanchor {
					return playback.DecisionResponseV3{}, *record, nil, subtitleArtifactErrorV3("Downloaded subtitles are temporarily unavailable.", rekeyErr)
				}
				// A seek must not terminal on a subtitle it cannot verify while
				// the store is down. Drop the carried selection and keep moving
				// at the requested position; the plan records the loss once.
				start.SubtitleTrackID = ""
				start.SubtitleTrackIndex = nil
				replanSubtitleUnavailable = true
				slog.InfoContext(r.Context(), "seek reanchor subtitle inventory is temporarily unavailable; continuing with subtitles off",
					logComponentKey, "playback",
					"session_id", record.SessionID,
					"effective_file_id", effectiveFile.ID,
					"error", logredact.SanitizeURLError(rekeyErr),
				)
			} else {
				return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: trackUnavailableReasonV3, message: rekeyErr.Error()}
			}
		}
		warnings, err := start.NormalizeAndValidate()
		if err != nil {
			return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "invalid_replan", message: err.Error()}
		}
		// A re-keyed identity whose ordinal could not be proven against the
		// effective row's inventory was cleared above; report the degradation
		// instead of letting it disappear as an omitted selection.
		if replanAudioUnavailable {
			warnings = append(warnings, playback.AudioTrackUnavailableWarningV3())
		}
		if replanSubtitleUnavailable || driftSubtitleUnavailable {
			warnings = append(warnings, playback.SubtitleTrackUnavailableWarningV3())
		}
		// This is the single normalization of the merged start request. Keep
		// its degradation warnings, including the drop of a client
		// transformation the merged feature list did not negotiate, and attach
		// them to the plan once it is selected.
		replanCapabilityWarnings = warnings
		audioIndex = 0
		if !seekReanchor {
			if dropStaleAudioTrackIdentityV3(r.Context(), effectiveFile, start.AudioTrackID) {
				start.AudioTrackID = ""
				start.AudioTrackIndex = nil
			}
			audioIndex, _, err = resolveV3AudioIndex(effectiveFile, start.AudioTrackID, start.AudioTrackIndex)
			if err != nil {
				return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: trackUnavailableReasonV3, message: err.Error()}
			}
		}
		attemptedKeys = []string(nil)
		// The durable recipe-exclusion chain applies to this pass exactly as it
		// does to the first one: an abandoned recipe stays retired whatever
		// operation asks for the next plan.
		if sameOutputReplan {
			for _, key := range record.RecoveryState.RecipeExclusions {
				if !containsStringExactV3(attemptedKeys, key) {
					attemptedKeys = append(attemptedKeys, key)
				}
			}
		}
		currentKey := playback.PlanAttemptKeyV3(record.CurrentPlan, record.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID, req.LocalMutations)
		unmutatedKey := playback.PlanAttemptKeyV3(record.CurrentPlan, record.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID, nil)
		if !intentChange && !seekReanchor && !userIntentOperation {
			failedKey := strings.TrimSpace(req.PlanAttemptKey)
			for _, key := range req.AttemptedPlanKeys {
				trimmed := strings.TrimSpace(key)
				if (nodeRecovery || generationFence) && (trimmed == failedKey || trimmed == currentKey) {
					continue
				}
				// Keep the current recipe out of the loop guard for its one
				// reissue. The client echoes it in its history and the server
				// computes the same key; both copies must be dropped or the
				// planner cannot select the route it just played.
				if progressReissue && (trimmed == failedKey || trimmed == currentKey || trimmed == unmutatedKey) {
					continue
				}
				attemptedKeys = append(attemptedKeys, key)
			}
			if !proxyOriginRecovery && !nodeRecovery && !generationFence && !progressReissue && !containsStringExactV3(attemptedKeys, req.PlanAttemptKey) {
				attemptedKeys = append(attemptedKeys, req.PlanAttemptKey)
			}
		}
		if !seekReanchor && !userIntentOperation && (!intentChange || seekFailureRecovery) {
			// Always exclude the durable server recipe so stale or malformed client
			// history cannot immediately re-select the route that just failed and
			// ping-pong the session. A client-reported local mutation (for example a
			// PCM recovery route) is folded into the failed plan's key here — the
			// server owns the hash; clients only echo opaque keys.
			if !proxyOriginRecovery && !nodeRecovery && !generationFence && !progressReissue && !containsStringExactV3(attemptedKeys, currentKey) {
				attemptedKeys = append(attemptedKeys, currentKey)
			}
			if len(req.LocalMutations) > 0 && !proxyOriginRecovery && !nodeRecovery && !generationFence && !progressReissue {
				// The unmutated recipe already failed before the client mutated it
				// locally; exclude it as well.
				if !containsStringExactV3(attemptedKeys, unmutatedKey) {
					attemptedKeys = append(attemptedKeys, unmutatedKey)
				}
			}
		}
		if virtualDecodeRotation {
			// The plan attempt key does not encode the provider result= URI, so
			// the decode-rejected candidate and its rotated sibling share a key.
			// Excluding the failed key would make the planner report
			// adaptation_exhausted for the sibling before it is ever tried.
			// Drop the failed candidate's key (and the client-echoed copies)
			// from the loop guard for this rotation only; plan identity itself is
			// unchanged, and distinct media-version keys stay excluded.
			failedKey := playback.PlanAttemptKeyV3(record.CurrentPlan, record.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID, nil)
			mutatedKey := playback.PlanAttemptKeyV3(record.CurrentPlan, record.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID, req.LocalMutations)
			attemptedKeys = slices.DeleteFunc(attemptedKeys, func(key string) bool {
				trimmed := strings.TrimSpace(key)
				return trimmed == failedKey || trimmed == mutatedKey
			})
		}
		if !seekReanchor {
			plannerSettings, plannerSettingsErr = h.plannerSettingsV3Result(r.Context())
		}
		if seekReanchor {
			var frozenErr error
			result, frozenErr = frozenSeekReanchorResultV3(record, req.PositionSeconds, time.Now())
			if frozenErr != nil {
				return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
					reason:    "seek_reanchor_recipe_unavailable",
					message:   "The active playback recipe cannot be reopened; start a new playback attempt.",
					retryable: true,
				}
			}
			// A carried subtitle that no longer binds to the effective file is
			// not a reason to refuse the position: the file was substituted, the
			// pick went stale, or its artifact moved. Drop the subtitle for this
			// replan and keep moving with video and audio untouched. Only a
			// recipe that cannot be replayed at all (the check above) stays
			// terminal, because the stream itself cannot be reopened.
			if replanSubtitleUnavailable || driftSubtitleUnavailable {
				seekSubtitleDegraded = true
			} else if err := h.validateFrozenSubtitleIdentityV3(r.Context(), effectiveFile, record.FrozenRecipe); err != nil {
				seekSubtitleDegraded = true
				slog.InfoContext(r.Context(), "seek reanchor subtitle no longer binds to the effective file; continuing with subtitles off",
					logComponentKey, "playback",
					"session_id", record.SessionID,
					"requested_file_id", record.RequestedMediaFileID,
					"effective_file_id", effectiveFile.ID,
					"error", logredact.SanitizeURLError(err),
				)
			}
			seekNoMovement = seekReanchorNoMovementV3(record, session, req.PositionSeconds)
		} else {
			// Delivery stickiness exists to avoid thrashing the pipeline while
			// the same release keeps serving. Switching editions re-derives the
			// route from scratch: the current delivery's bytes are not what the
			// viewer asked to play, so a sticky class can only force a needless
			// remux of the target edition.
			var stickyDelivery playback.DeliveryV3
			if effectiveFile != nil && currentEffectiveFile != nil && effectiveFile.ID == currentEffectiveFile.ID {
				stickyDelivery = replanStickyDeliveryV3(operation, record.CurrentPlan.Delivery)
			}
			additional, inventoryErr := h.listDownloadedSubtitlesV3(r.Context(), effectiveFile)
			if inventoryErr != nil && (trackChange || qualityChange) && selectsDownloadedSubtitleV3(effectiveFile, start) {
				// A failed lookup says nothing about whether the selected track
				// exists. Planning without it would turn the subtitle off, and
				// later replans start from that plan's tracks, so it would stay
				// off for the rest of the session. The playing plan carries on
				// and the viewer can repeat the change.
				return playback.DecisionResponseV3{}, *record, nil, subtitleArtifactErrorV3("Downloaded subtitles are temporarily unavailable.", inventoryErr)
			}
			result, toneMapCapabilityErr = h.planPlaybackWithCapabilitiesV3(r.Context(), playback.PlannerInputV3{Request: start, RequestedFile: plannerRequestedFile, EffectiveFile: effectiveFile, LowerVersion: lowerVersionForFileV3(lowerVersion, effectiveFile), ServerBitrateCapKbps: serverBitrateCapV3(r.Context()), AudioTrackIndex: audioIndex, Settings: plannerSettings, Registry: h.transformationRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), effectiveFile), Now: time.Now(), AttemptedKeys: attemptedKeys, AdditionalSubtitles: additional, ForceSoftwareVideoDecode: forceSoftwareDecode, DecodeAttemptDetail: decodeAttemptDetail, StickyDelivery: stickyDelivery, InventoryProvenance: string(replanVirtualProvenance), PreviousSubtitleEvaluation: previousSubtitleEvaluationV3(record), AudioShapeRetry: audioShapeRetry, AudioInitFailureDetail: audioInitFailureDetail, CueIndexedSubtitleTracks: planning.renditionTracks.cueIndexedSubtitleTracks(effectiveFile, start)})
			clampPlannerTargetResolution(&result, effectiveFile)
			reportDetourSubtitleDrop()
		}
		if outputChange && result.Terminal != nil && effectiveFile.ID != currentEffectiveFile.ID {
			// Returning to the requested edition is speculative during an output
			// refresh. Any terminal from that probe must fall back to the edition
			// already playing, not only HDR/alternate-selection terminals: its audio,
			// subtitle, or delivery constraints may still differ from the active file.
			start = currentEffectiveStart
			start.FileID = currentEffectiveFile.ID
			effectiveFile = currentEffectiveFile
			if dropStaleAudioTrackIdentityV3(r.Context(), effectiveFile, start.AudioTrackID) {
				start.AudioTrackID = ""
				start.AudioTrackIndex = nil
			}
			audioIndex, _, err = resolveV3AudioIndex(effectiveFile, start.AudioTrackID, start.AudioTrackIndex)
			if err != nil {
				return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: trackUnavailableReasonV3, message: err.Error()}
			}
			result, toneMapCapabilityErr = h.planPlaybackWithCapabilitiesV3(r.Context(), playback.PlannerInputV3{Request: start, RequestedFile: plannerRequestedFile, EffectiveFile: effectiveFile, LowerVersion: lowerVersionForFileV3(lowerVersion, effectiveFile), ServerBitrateCapKbps: serverBitrateCapV3(r.Context()), AudioTrackIndex: audioIndex, Settings: plannerSettings, Registry: h.transformationRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), effectiveFile), Now: time.Now(), AttemptedKeys: attemptedKeys, AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), effectiveFile), ForceSoftwareVideoDecode: forceSoftwareDecode, DecodeAttemptDetail: decodeAttemptDetail, StickyDelivery: replanStickyDeliveryV3(operation, record.CurrentPlan.Delivery), AudioShapeRetry: audioShapeRetry, AudioInitFailureDetail: audioInitFailureDetail, CueIndexedSubtitleTracks: planning.renditionTracks.cueIndexedSubtitleTracks(effectiveFile, start)})
			clampPlannerTargetResolution(&result, effectiveFile)

		}
		// A subtitle-only refusal and a video/policy refusal take different
		// recovery routes. The in-place subtitle degrade is gated on the same
		// operation/quality/selection rules the alternate hunt uses, but not on
		// terminalAllowsAlternateFileV3: subtitle_conversion_unsupported is no
		// longer in that set precisely so a subtitle problem cannot reach the
		// sibling hunt.
		// replanFallbackAllowed (computed once above) is the session's negotiated
		// auto-fallback policy: an explicit start turns it off so a replan must
		// not silently substitute another version, and the viewer re-selecting
		// Auto turns it back on even for a session that started explicit. An
		// unset flag (a reconstruction) falls back to the durable normalized
		// request, which a mid-session re-arm updates, so the reconstructed
		// policy still matches the viewer's intent instead of the original start
		// request.
		// A same-release degrade never substitutes the version, so it does not
		// depend on the version-fallback policy: an explicit pick and a
		// track_change both keep the mounted release and only drop the
		// selected track. Seek reanchors replay a frozen recipe and stay out.
		inPlaceDegradeAllowed := operation != playback.ReplanOperationSeekReanchorV3 &&
			operation != playback.ReplanOperationSeekFailureRecoveryV3
		subtitleOnlyAllowed := inPlaceDegradeAllowed && subtitleOnlyTerminalV3(result.Terminal)
		subtitleResolved := false
		if subtitleOnlyAllowed && outputChangeKeptActiveForSubtitle && requestedFile != nil && requestedFile.ID != effectiveFile.ID {
			// The viewer asked to return to this edition; the active one was
			// kept only for a subtitle that cannot be shown. Try the requested
			// edition with the subtitle off before falling back to the active
			// edition without it.
			degradedStart := subtitleDegradedStartV3(start)
			if eval, evalErr := h.evaluateReplanCandidateV3(r, session, record, req, degradedStart, effectiveFile, requestedFile, plannerRequestedFile, plannerSettings, plannerSettingsErr, attemptedKeys, lowerVersion); evalErr == nil && eval != nil && eval.result.Terminal == nil {
				start = eval.start
				effectiveFile = eval.file
				result = eval.result
				toneMapCapabilityErr = eval.toneMapErr
				artifactRecipe = eval.frozenRecipe
				preparedTransport = &eval.transport
				transportPrepared = true
				reservationHeld = eval.reservationHeld
				if result.Plan != nil {
					result.Plan.DegradationWarnings = append(result.Plan.DegradationWarnings, playback.SubtitleTrackUnavailableWarningV3())
				}
				subtitleResolved = true
			}
		}
		// one: keep the mounted release and re-plan it with another audio
		// track. audio_conversion_unsupported is not in
		// terminalAllowsAlternateFileV3, so an audio problem never reaches the
		// sibling hunt. On a replan the mounted release is known to be
		// playable, so a failed in-place resolution leaves the terminal in
		// place rather than substituting a sibling.
		audioOnlyAllowed := inPlaceDegradeAllowed && audioOnlyTerminalV3(result.Terminal)
		alternateFileAllowed := replanFallbackAllowed && terminalAllowsAlternateFileV3(result.Terminal)
		// versionPickFallback remembers whether the hunt below runs for an
		// explicitly picked but unplannable release, so its success publishes
		// the substitution notice (the shared hunt is silent by default).
		versionPickFallback := false
		if !alternateFileAllowed && trackChange && result.Terminal != nil &&
			result.Terminal.Reason == sourceMetadataIncompleteReasonV3 &&
			requestedFile != nil && effectiveFile != nil &&
			requestedFile.ID != 0 && requestedFile.ID != effectiveFile.ID &&
			playback.VirtualRouteVideoMetadataGapsV3(requestedFile) != "" &&
			playback.VirtualRouteVideoMetadataGapsV3(effectiveFile) == "" {
			// A track_change naming a different file than mounted is a
			// version pick, not a subtitle/audio pick (those name the
			// mounted file and never reach this branch). When planning the
			// pick terminals on missing metadata while the mounted release
			// is fully characterized, fall back through the same alternate
			// hunt (with substitution notice) instead of stranding on a
			// terminal for a row that was never probed. Subtitle/audio
			// terminals keep their in-place degrade above; this branch
			// deliberately bypasses the auto-fallback flag because an
			// explicitly picked but unplannable release with a playable
			// bound sibling is exactly what the notice exists for.
			alternateFileAllowed = true
			versionPickFallback = true
		}
		if subtitleOnlyAllowed && !subtitleResolved {
			// A subtitle-only refusal must not move the release. Re-plan the
			// file already mounted with the subtitle dropped; the in-place
			// helper uses the resolved file verbatim, so a virtual release is
			// never re-listed or substituted. If the same release cannot play
			// without the subtitle either, the original subtitle terminal
			// stands and no sibling is tried.
			eval, evalErr := h.evaluateSubtitleDegradeInPlaceV3(r, session, record, req, start, effectiveFile, replanVirtualProvenance, plannerRequestedFile, plannerSettings, plannerSettingsErr, attemptedKeys)
			if evalErr == nil && eval != nil && eval.result.Terminal == nil {
				start = eval.start
				effectiveFile = eval.file
				result = eval.result
				toneMapCapabilityErr = eval.toneMapErr
				artifactRecipe = eval.frozenRecipe
				if effectiveFile.ID != record.EffectiveMediaFileID {
					preparedTransport = &eval.transport
					transportPrepared = true
				}
				// Same release: dropping the subtitle leaves the A/V recipe
				// byte-identical, so the replan reuses the active transport.
				reservationHeld = eval.reservationHeld
			} else if outputChange && replanFallbackAllowed {
				// The release cannot play without the subtitle either, so an
				// output change may still move to a playable sibling.
				alternateFileAllowed = true
			}
		}
		if audioOnlyAllowed {
			// An audio-only refusal must not move the release. Re-plan the file
			// already mounted with another audio track; the helper uses the
			// resolved file verbatim, so a virtual release is never re-listed or
			// substituted. If no track is playable the original audio terminal
			// stands and no sibling is tried.
			if eval, evalErr, ok := h.evaluateAudioDegradeInPlaceV3(r, session, record, req, start, effectiveFile, replanVirtualProvenance, plannerRequestedFile, plannerSettings, plannerSettingsErr, attemptedKeys); ok && evalErr == nil && eval != nil && eval.result.Terminal == nil {
				start = eval.start
				effectiveFile = eval.file
				result = eval.result
				toneMapCapabilityErr = eval.toneMapErr
				artifactRecipe = eval.frozenRecipe
				preparedTransport = &eval.transport
				transportPrepared = true
				reservationHeld = eval.reservationHeld
			}
		}
		if alternateFileAllowed && result.Terminal != nil {
			alternateOrder := alternateOrderingForClient(req.Capabilities)
			if alternates, alternateErr := h.findAlternateFilesOrdered(r.Context(), requestedFile, requestAccessFilter(r), alternateOrder); alternateErr == nil {
				baseStart := start
				// A speculative detour through the requested edition (above)
				// may have dropped a selection its inventory cannot honor,
				// and validation drops anything still naming another file.
				// The durable plan still carries the viewer's pick: restore
				// it for the hunt so a sibling that CAN honor it keeps
				// subtitles instead of starting muted. Restricted to
				// non-virtual mounts: virtual rows rotate releases under a
				// fixed id, so their selections re-resolve from plan-time
				// evidence, never from a bare carried ordinal.
				if baseStart.SubtitleTrackID == "" && baseStart.SubtitleTrackIndex == nil && !isVirtualPlaybackFile(currentEffectiveFile) {
					if sel := record.CurrentPlan.SelectedTracks.Subtitle; sel != nil {
						baseStart.SubtitleTrackID = sel.ID
						baseStart.SubtitleTrackIndex = copyOptionalIntV3(sel.Index)
					}
				}

				baseEffectiveFile := effectiveFile
				var firstFailureEval *candidateEvaluationV3
				var subtitleMissAlternate *models.MediaFile
				// A terminal that names a server-wide condition (no tone-map
				// recipe, transcoding disabled) is not per-candidate: every
				// remaining sibling would plan to the same verdict, so paying
				// a resolve+probe+plan round-trip per file only delays the
				// replan the client is already waiting on. Stop at the first.
				var capabilityBlocked bool
				for _, alternate := range alternates {
					if capabilityBlocked {
						break
					}
					if alternate.ID == baseEffectiveFile.ID {
						continue
					}
					if virtualCandidateVerdictActive(alternate.FailedAt, time.Now()) {
						// AltMount indicted this version; never attempt it,
						// however the pinned release failed. This mirrors the
						// start-path fallback's authority rule.
						continue
					}
					eval, err := h.evaluateReplanCandidateV3(r, session, record, req, baseStart, baseEffectiveFile, alternate, plannerRequestedFile, plannerSettings, plannerSettingsErr, attemptedKeys, lowerVersion)
					if err != nil {
						slog.WarnContext(r.Context(), "replan alternate candidate rejected",
							"component", "api", "session_id", record.SessionID,
							"requested_file_id", record.RequestedMediaFileID, "base_file_id", baseEffectiveFile.ID,
							"candidate_file_id", alternate.ID, "error", logredact.SanitizeURLError(err),
						)
						if candidateSubtitleLocalFailureV3(err) && subtitleMissAlternate == nil {
							subtitleMissAlternate = alternate
						}
						// A planner terminal of transcode_start_failed here is
						// the tone-map discovery retry: it is a server-wide
						// capability condition, so later candidates cannot
						// succeed either.
						if err.Stage == candidateStagePlan && err.TerminalReason == transcodeStartFailedReasonV3 {
							capabilityBlocked = true
						}
					}
					if eval != nil && eval.result.Terminal == nil {
						start = eval.start
						effectiveFile = eval.file
						result = eval.result
						toneMapCapabilityErr = eval.toneMapErr
						artifactRecipe = eval.frozenRecipe
						preparedTransport = &eval.transport
						transportPrepared = true
						reservationHeld = eval.reservationHeld
						if versionPickFallback {
							// The hunt ran for an explicitly picked but
							// unplannable release: name the substitution so
							// the client shows the notice instead of playing
							// a different release silently. Unknown reason:
							// the pick was unplannable (missing metadata),
							// not confirmed dead.
							publishSubstitutionFieldsV3(result.Plan, requestedFile.ID, effectiveFile.ID, substitutionReasonUnknownV3)
						}
						break
					}
					if firstFailureEval == nil && eval != nil {
						firstFailureEval = eval
					}
				}
				if result.Terminal != nil && subtitleMissAlternate != nil {
					// Every candidate with an equivalent track is exhausted.
					// Degrade to subtitles-off rather than terminalling, but only
					// after the honoring candidates above have all been tried.
					if eval, evalErr := h.evaluateSubtitleDegradedCandidateV3(r, session, record, req, baseStart, baseEffectiveFile, subtitleMissAlternate, plannerRequestedFile, plannerSettings, plannerSettingsErr, attemptedKeys, lowerVersion); evalErr == nil && eval != nil && eval.result.Terminal == nil {
						start = eval.start
						effectiveFile = eval.file
						result = eval.result
						toneMapCapabilityErr = eval.toneMapErr
						artifactRecipe = eval.frozenRecipe
						preparedTransport = &eval.transport
						transportPrepared = true
						reservationHeld = eval.reservationHeld
					}
				}
				if result.Terminal != nil && firstFailureEval != nil {
					start = firstFailureEval.start
					effectiveFile = firstFailureEval.file
					result = firstFailureEval.result
					toneMapCapabilityErr = firstFailureEval.toneMapErr
				}
			}
		}
	}
	result = retryIncompleteToneMapPlanningV3(result, toneMapCapabilityErr)
	result = retryIncompletePlaybackSettingsV3(result, plannerSettingsErr)
	reportDetourSubtitleDrop()
	h.clarifyOriginalQuality4KTerminalV3(r.Context(), requestAccessFilter(r), result.Terminal, requestedFile, replanAlternateFilePinnedByOriginalQualityV3(operation, start.QualityPreference))
	// A planner terminal on a rotated sibling is surfaced as-is: it names the
	// actual blocker (capability, capacity, recipe) rather than being rewritten
	// as a decode rejection the sibling never produced. The decode reason is
	// reserved for the paths that have per-candidate decode evidence: the
	// no-sibling exhaustion below and a successor the transport actually
	// rejected.
	if decodeAttemptDetail != "" && result.Terminal != nil && strings.TrimSpace(result.Terminal.Detail) == "" {
		// The software decode mode was the plan that just failed. Name both
		// attempted decode modes on any terminal so an exhausted route never
		// reports an empty detail.
		result.Terminal.Detail = decodeAttemptDetail
	}
	if forceSoftwareDecode {
		// The software retry is a deliberate quality trade, not a silent
		// fallback: record it on the plan so clients can surface it, and emit
		// one classifier line carrying the decoder evidence that triggered it.
		if result.Plan != nil {
			result.Plan.DegradationWarnings = append(result.Plan.DegradationWarnings, playback.DegradationWarningV3{
				Code:    degradationSoftwareDecodeFallbackV3,
				Message: "The hardware video decoder could not decode this source; retrying with software decoding.",
			})
		}
		slog.WarnContext(r.Context(), "playback software decode fallback",
			logComponentKey, "playback",
			"session_id", record.SessionID,
			"operation", string(operation),
			"delivery", record.CurrentPlan.Delivery,
			"hw_accel", strings.TrimSpace(session.TranscodeHWAccel),
			"source_video_codec", record.CurrentPlan.Source.VideoCodec,
			"source_video_profile", record.CurrentPlan.Source.VideoProfile,
			"source_video_bit_depth", record.CurrentPlan.Source.BitDepth,
			"decode_failure_sample", decodeFailureSample,
			"decode_failure_count", decodeFailureCount,
			videoFramesLogKeyV3, decodeVideoFrames,
			outTimeUSLogKeyV3, decodeOutTimeUS,
		)
	}
	// Media authentication is attempt-sticky (pinned in HandleReplanPlaybackV3),
	// so this mode always equals the one the attempt started under: a reused
	// transport cannot change the session's media security contract.
	mode = headerAuthenticatedMediaV3(start.ClientFeatures)
	if !seekReanchor {
		// A freshly planned replan can land on the same refused progressive
		// remux a start would have; escalate it identically. A seek reanchor
		// replays the frozen recipe verbatim and must not change route identity,
		// so it is excluded — its route was escalated when the attempt started.
		escalated, escalateErr := h.escalateRefusedProgressiveRemuxV3(r.Context(), mode,
			func() playback.PlannerInputV3 {
				input := h.plannerInputV3(r.Context(), start, plannerRequestedFile, effectiveFile, audioIndex, attemptedKeys, replanVirtualProvenance)
				input.LowerVersion = lowerVersionForFileV3(lowerVersion, effectiveFile)
				// An escalation replans the same selection against the same
				// inventory, so it reuses the predecessor's degraded subtitle
				// outcome exactly as the primary plan did.
				input.PreviousSubtitleEvaluation = previousSubtitleEvaluationV3(record)
				return input
			}, result)
		if escalateErr != nil {
			return playback.DecisionResponseV3{}, *record, nil, escalateErr
		}
		result = escalated
	}
	// Marked on the plan that will run, after every fallback that can replace it.
	// The authoritative replan plan is built below the candidate rehydration and
	// version selection, so the learned-limit label must be applied here rather
	// than to the earlier provisional plan it would overwrite.
	markLearnedLimit(&result, learnedLimit, requestedFile, start, serverBitrateCapV3(r.Context()))
	// A replan that starts from the version playing and stays on it (a track
	// change, a recovery, Auto again) keeps the version an earlier plan chose:
	// when the limit lowered that plan, it still lowers this one. Any other
	// label stands only if the same replan without the limit plays more, as
	// bitrates alone cannot tell whether the requested file could play.
	staysLimited := (!intentChange || pinnedTrackChange) && effectiveFile.ID == record.EffectiveMediaFileID && record.CurrentPlan.BandwidthLimit != nil
	if result.Plan != nil && result.Plan.BandwidthLimit != nil && !staysLimited {
		var readingErr error
		if !seekScopedRecovery {
			readingErr = h.applyConnectionReading(r, &unlimitedStart)
		}
		if choosesVersion {
			unlimitedFile, unlimitedStart, unlimitedAudioIndex = h.lowerBufferedVersionForIntent(r, unlimitedStart, unlimitedFile, unlimitedAudioIndex, plannerSettings, desired)
		}
		unlimited, unlimitedErr := h.planReplanV3(r, planning, unlimitedStart, unlimitedFile, unlimitedAudioIndex, false)
		if readingErr != nil || unlimitedErr != nil || unlimited.result.Plan == nil || deliveredKbpsV3(result.Plan) >= deliveredKbpsV3(unlimited.result.Plan) {
			result.Plan.BandwidthLimit = nil
		}
	}
	// One decision line per replan, mirroring the start endpoint's record:
	// replans choose routes (and terminals) just as consequential, and an
	// unlogged terminal made client reports impossible to reconstruct from
	// server logs alone.
	clientInfo := playbackClientInfoForStartV3(r, req.ClientPlaybackContext)
	if result.Terminal != nil {
		slog.InfoContext(r.Context(), "playback replan decided", append([]any{
			logComponentKey, "playback",
			"outcome", "terminal",
			"reason", result.Terminal.Reason,
			"detail", result.Terminal.Detail,
			"operation", string(operation),
			routeDowngradeLogKeyV3, routeDowngradeV3(routeDowngrade, result.RouteDowngrade),
			"session_id", record.SessionID,
			"requested_file_id", record.RequestedMediaFileID,
			"effective_file_id", effectiveFile.ID,
			"quality_preference", start.QualityPreference,
			"software_video_decode", forceSoftwareDecode,
			"decode_failure_count", decodeFailureCount,
			videoFramesLogKeyV3, decodeVideoFrames,
			outTimeUSLogKeyV3, decodeOutTimeUS,
		}, clientInfo.LogAttrs()...)...)
		markReplanDecisionLoggedV3(r.Context())
		return playback.NewTerminalResponseFromTerminalV3(retryableAfterDirectPlayV3(result.Terminal, directPlayRetiredByServerV3(record))), *record, nil, nil
	}
	// Surface the warnings from the merged start request's single
	// normalization on the selected plan. A seek reanchor replays the durable
	// plan, which may already carry the same warning from its own start, so the
	// helper skips duplicates.
	appendStartWarningsV3(&result, replanCapabilityWarnings)
	// Explicit mid-session delivery-swap signaling. A failure-recovery replan
	// may legitimately change the serving route (for example transcode-HLS to
	// remux-progressive). Without a marker the client only sees the new plan
	// body and cannot distinguish a planned route change from a silent mid-play
	// swap. Record the change additively on the plan and name old->new on the one
	// decision line below. A seek reanchor replays the durable route verbatim and
	// cannot change it, so it never produces a marker. The marker is UI-only and
	// excluded from plan identity hashing (the identity was finalized in the
	// planner before this point).
	var previousPlayMethod playback.PlayMethod
	if session != nil {
		previousPlayMethod = session.PlayMethod
	}
	var deliveryChange *playback.DeliveryChangeV3
	if !seekReanchor {
		deliveryChange = deliveryChangeV3(record.CurrentPlan.Delivery, previousPlayMethod, result.Plan.Delivery, result.PlayMethod)
	}
	// The marker is scoped to the native v2 surface, like the deferred
	// track-inventory hint (tracks_pending): the frozen /api/v1 bridge must not
	// grow a new field, so a v1 response never exposes it. It is assigned — to
	// nil on v1 and on a seek reanchor — unconditionally, never only when
	// non-nil: a seek replays the durable route verbatim via
	// frozenSeekReanchorResultV3, which copies record.CurrentPlan wholesale and
	// therefore already carries any marker stamped on the recovery plan that
	// produced the current route, so an omitted assignment would re-emit a stale
	// A->B swap. A nil assignment clears it on both surfaces.
	if isNativeAPIV2(r.Context()) {
		result.Plan.DeliveryChange = deliveryChange
	} else {
		result.Plan.DeliveryChange = nil
	}
	// A client that ignores selection_unavailable and replays the exact same
	// unhonorable subtitle pick would otherwise receive a brand-new playable
	// plan every time, minting a new plan id and rebuilding the transport at
	// the live position. Once that pick has already been rejected for this
	// session and file, answer the identical replan with the durable plan
	// unchanged. The first rejection still returns the explicit plan below, so
	// a capable client learns the reason; this is only the server-side guarantee
	// for clients that do not.
	loopResponse, recordBreaker, handled := h.replanSubtitleLoopBreakerV3(
		r, record, req, operation, trackChange, effectiveFile, result.Plan, clientInfo,
	)
	if handled {
		return loopResponse, *record, nil, nil
	}
	recordSubtitleBreaker = recordBreaker
	replanLogAttrs := []any{
		logComponentKey, "playback",
		"outcome", "plan",
		"decision_reason", result.Plan.DecisionReason,
		routeDowngradeLogKeyV3, routeDowngradeV3(routeDowngrade, result.RouteDowngrade),
		"delivery", result.Plan.Delivery,
		"play_method", string(result.PlayMethod),
		"operation", string(operation),
		"session_id", record.SessionID,
		"requested_file_id", record.RequestedMediaFileID,
		"effective_file_id", effectiveFile.ID,
		"dv_profile", result.Plan.Source.DVProfile,
		"dynamic_range", result.Plan.Source.DynamicRange,
		"target_resolution", result.TargetResolution,
		"target_bitrate_kbps", result.TargetBitrateKbps,
		"quality_preference", start.QualityPreference,
		"bandwidth_estimate_kbps", intOrZeroHandlerV3(start.BandwidthEstimateKbps),
		"software_video_decode", forceSoftwareDecode,
		"decode_failure_count", decodeFailureCount,
		"decode_failure_sample", decodeFailureSample,
		videoFramesLogKeyV3, decodeVideoFrames,
		outTimeUSLogKeyV3, decodeOutTimeUS,
	}
	if deliveryChange != nil {
		replanLogAttrs = append(replanLogAttrs,
			"previous_delivery", deliveryChange.PreviousDelivery,
			"new_delivery", deliveryChange.Delivery,
			"previous_play_method", string(deliveryChange.PreviousPlayMethod),
			"new_play_method", string(deliveryChange.PlayMethod),
			"delivery_changed", deliveryChange.DeliveryChanged,
			"play_method_changed", deliveryChange.PlayMethodChanged,
		)
	}
	// Record the delivery-stickiness decision beside the swap marker: a replan
	// with no failure behind it prefers the running pipeline (see
	// replanStickyDeliveryV3), so operators can tell a deliberate keep from a
	// swap that never happened to occur. delivery_sticky is true only when the
	// planner had to retry to keep the class; sticky_delivery names the class.
	replanLogAttrs = append(replanLogAttrs, "delivery_sticky", result.StickyDeliveryKept)
	if result.StickyDeliveryKept {
		replanLogAttrs = append(replanLogAttrs, "sticky_delivery", result.Plan.Delivery)
	}
	if !seekNoMovement {
		slog.InfoContext(r.Context(), "playback replan decided", append(replanLogAttrs, clientInfo.LogAttrs()...)...)
		markReplanDecisionLoggedV3(r.Context())
	}
	mode = headerAuthenticatedMediaV3(start.ClientFeatures)
	_, reservationHeld = h.sessionMgr.(replacementReservationCancellerV3)
	result.Plan.SessionID = session.ID
	result.Plan.InventoryURL = "/api/v2/playback/" + session.ID + "/inventory"
	artifactRecipe = record.FrozenRecipe
	if seekReanchor && seekSubtitleDegraded {
		// Replay the same route with the subtitle selection cleared. Clearing
		// the subtitle identity off the frozen recipe too is what stops later
		// seeks from re-checking a selection this attempt already dropped, so
		// the warning and its log line are recorded once, not once per seek.
		artifactRecipe = degradeSeekReanchorSubtitleV3(record.FrozenRecipe, &result)
	}
	if !seekReanchor {
		frozenRecipe, frozenErr := h.freezeExecutableRecipeV3(r.Context(), effectiveFile, result)
		if frozenErr != nil {
			return playback.DecisionResponseV3{}, *record, nil, subtitleArtifactErrorV3("Failed to freeze the selected subtitle identity.", frozenErr)
		}
		artifactRecipe = frozenRecipe
	}
	// Resolve every fallible subtitle and frozen-route check before transport
	// preparation publishes a stable proxy grant. An existing client can issue
	// a GET against that grant immediately, even though this replan response has
	// not returned yet, so errors after publication would require canceling an
	// already admitted successor.
	// Every replan keeps the SRT representation the attempt already published;
	// a seek reanchor must also reproduce its frozen artifact exactly. An
	// attempt started by a server that did not know subrip_sidecar_v1 may carry
	// the feature beside WebVTT URLs.
	artifactFeatures := replanSubtitleFeaturesV3(record, start.ClientFeatures)
	if err := h.attachSubtitleArtifactV3(r.Context(), session.ID, effectiveFile, result.Plan, result.SubtitleTrackIndex, &artifactRecipe, artifactFeatures); err != nil {
		if !seekReanchor {
			return playback.DecisionResponseV3{}, *record, nil, subtitleArtifactErrorV3("Failed to prepare the selected subtitle artifact.", err)
		}
		// The subtitle artifact itself is missing, moved, or unreadable. A seek
		// must still reach the requested position without it; only the
		// selection is lost, and the plan reports that once.
		slog.InfoContext(r.Context(), "seek reanchor subtitle artifact is unavailable; continuing with subtitles off",
			logComponentKey, "playback",
			"session_id", record.SessionID,
			"requested_file_id", record.RequestedMediaFileID,
			"effective_file_id", effectiveFile.ID,
			"error", logredact.SanitizeURLError(err),
		)
		seekSubtitleDegraded = true
		artifactRecipe = degradeSeekReanchorSubtitleV3(artifactRecipe, &result)
		if retryErr := h.attachSubtitleArtifactV3(r.Context(), session.ID, effectiveFile, result.Plan, result.SubtitleTrackIndex, &artifactRecipe, artifactFeatures); retryErr != nil {
			// The menu could not be republished from the current store. The
			// plan is subtitles-off either way, so serve the position instead
			// of failing the seek over an advisory inventory.
			slog.WarnContext(r.Context(), "seek reanchor subtitle inventory could not be republished; continuing with subtitles off",
				logComponentKey, "playback",
				"session_id", record.SessionID,
				"effective_file_id", effectiveFile.ID,
				"error", logredact.SanitizeURLError(retryErr),
			)
		}
	}
	if seekSubtitleDegraded {
		// The durable request must not keep a selection this seek dropped:
		// later replans seed their carried tracks from it.
		start.SubtitleTrackID = ""
		start.SubtitleTrackIndex = nil
	}
	if seekReanchor {
		// The route-identity guard proves the frozen route did not drift. A
		// subtitle degrade is this replan's own, deliberate outcome, so compare
		// against the same subtitles-off baseline on both sides: every other
		// identity field must still match the running plan exactly.
		validationRecord := record
		if seekSubtitleDegraded {
			baseline := *record
			baselinePlan := record.CurrentPlan
			clearPlanSubtitleV3(&baselinePlan)
			baseline.CurrentPlan = baselinePlan
			validationRecord = &baseline
		}
		if err := validateSeekReanchorPlanV3(validationRecord, result.Plan); err != nil {
			changedFields := seekReanchorIdentityChangesV3(record, result.Plan)
			slog.ErrorContext(r.Context(), "protocol v3 seek reanchor changed route identity",
				"session", record.SessionID,
				"playback_attempt_id", record.PlaybackAttemptID,
				"changed_fields", changedFields,
			)
			return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
				reason:  "seek_reanchor_route_changed",
				message: err.Error(),
			}
		}
	}
	transportReused := false
	// A track change or a seek reanchor that keeps a byte-identical A/V recipe
	// can keep the active transport. The segment layer serves the new position
	// from the same growing HLS generation — restarting FFmpeg in place when
	// the target is past the produced head — so rebuilding the transport would
	// only add a teardown/respawn and, for virtual sources, a provider
	// re-resolution that turns an in-session seek into a fresh-start stall.
	// validateSeekReanchorPlanV3 has already rejected any route-identity drift,
	// and a target before the active window's start still rebuilds because the
	// reused generation cannot produce bytes before its origin.
	//
	// A track change also requires that every route node still answers: the
	// transport may be active on one that went away, and the replacement then
	// has to rebuild rather than keep a route it cannot serve. A seek reanchor
	// stays on the mounted source by definition and is not subject to that.
	// Direct play serves from the mounted source without a transcode session;
	// an in-place seek reanchor stays on the same source by definition and keeps
	// its transport URL (/stream/{sessionID}).
	if seekReanchor && record.CurrentPlan.Delivery == playback.DeliveryOriginalHTTPV3 && result.Plan.Delivery == playback.DeliveryOriginalHTTPV3 {
		transportReused = true
	} else if ((trackChange && len(unreachableNodes) == 0) ||
		(seekReanchor && h.seekReanchorWithinActiveWindowV3(session, record.CurrentPlan, req.PositionSeconds))) &&
		h.hasActiveReusableTransportV3(session, record.CurrentPlan.Delivery) {
		proxyAllowed := mode.proxyEgress || (!mode.headerAuth && h.JWTSecret != "")
		policy := h.playbackRoutingPolicyForContextV3(r.Context())
		if reusedRecipe, ok := sidecarOnlyReuseReplanV3(record, result.Plan, artifactRecipe, req.ClientPlaybackContext.Output.OutputContextID); ok &&
			reusedHLSRouteAllowedV3(session, result, policy, proxyAllowed) {
			artifactRecipe = reusedRecipe
			result.ToneMapMode = reusedRecipe.ToneMapMode
			transportReused = true
		}
	}
	var transport preparedTransportV3
	if transportPrepared && preparedTransport != nil {
		transport = *preparedTransport
	} else {
		reusedStream := record.CurrentPlan.Stream
		credentialRefreshed := false
		if transportReused {
			// Preserving the prior credential is only safe while it still
			// authorizes the live session for this resource. The old plan is
			// about to be retired, and a signed stream token is session- and
			// file-bound rather than plan-bound, so retirement alone does not
			// invalidate it — but an expired, mismatched, or near-expiry
			// credential is re-minted in place, keeping the session and
			// transport identity so playback is not restarted.
			credentialRefreshed, transportReused = h.authorizeReusedStreamCredentialV3(session, &reusedStream)
		}
		if transportReused {
			// A sidecar selection changes the plan and subtitle artifact, but it
			// does not change the bytes FFmpeg produces. Keep the active
			// generation and its transport window so a client remount cannot
			// strand itself between the killed old window and a replacement
			// window that starts elsewhere. The requested source position still
			// belongs to this replan: translate it onto the reused window
			// instead of rewinding to the previous plan's start.
			result.Plan.Stream = reusedStream
			reusedTimeline := record.CurrentPlan.Timeline
			reusedTimeline.SourceStartSeconds = result.Plan.Timeline.SourceStartSeconds
			reusedTimeline.PlayerStartSeconds = max(0, reusedTimeline.SourceStartSeconds-reusedTimeline.StreamOriginSeconds)
			result.Plan.Timeline = reusedTimeline
			result.Plan.ExpiresAt = record.CurrentPlan.ExpiresAt
			if credentialRefreshed {
				// The credential now carries a full lifetime; the published plan
				// expiry must not outlive it.
				result.Plan.ExpiresAt = playback.NewPlanExpiryV3(time.Now())
			}
			transport = reusedHLSTransportV3(session, reusedStream.URL)
			if mode.headerAuth {
				transport.sessionRecipe = h.reusedSessionRecipeV3(r.Context(), session.ID, record.CurrentPlanID)
			}
			slog.InfoContext(r.Context(), "protocol v3 replan reused active A/V transport",
				logComponentKey, playbackLogValueV3,
				"playback_session_id", session.ID,
				"previous_plan_id", record.CurrentPlanID,
				"plan_id", result.Plan.PlanID,
				"delivery", result.Plan.Delivery,
				"credential_refreshed", credentialRefreshed,
			)
		} else {
			var transportErr *transportErrorV3
			transportRequest := r
			if proxyOriginRecovery {
				policy := h.playbackRoutingPolicyForContextV3(r.Context())
				policy.DirectPlayEgress = config.PlaybackEgressAPIOnly
				policy.RemuxEgress = config.PlaybackEgressAPIOnly
				policy.VideoTranscodeEgress = config.PlaybackEgressAPIOnly
				transportRequest = r.WithContext(withPlaybackRoutingPolicySnapshotV3(r.Context(), policy))
			}
			// A decode-driven candidate rotation tells transport preparation to
			// serve the plan's replacement candidate rather than the session's
			// still-rejected binding. The intent travels in the request context
			// so the live session is not mutated before the durable session
			// replacement commits; the local transport's session clamp reads it
			// in preference to session.VirtualSourceURI.
			if virtualDecodeRotation && isVirtualPlaybackFile(effectiveFile) &&
				strings.TrimSpace(session.VirtualSourceURI) != "" &&
				effectiveFile.FilePath != session.VirtualSourceURI {
				transportRequest = transportRequest.WithContext(withVirtualSourceRotationV3(
					transportRequest.Context(), effectiveFile.FilePath, effectiveFile.VirtualOwnerInstallationID,
				))
			}
			transport, transportErr = h.prepareTransportV3(transportRequest, session, effectiveFile, result, mode)
			if transportErr != nil {
				if transportErr.reason == candidateSourceDecodeRejectedReasonV3 {
					// The candidate this replan prepared was rejected by its
					// decoder during startup. That is a genuine decode rejection
					// with per-candidate evidence, so report the client-facing
					// reason instead of leaking the internal transport token as a
					// terminal. A rotation retires the exhausted delivery; any
					// other replan just surfaces the decode verdict. (A bounded
					// multi-candidate replan retry is a follow-up; the start path
					// already loops.)
					if virtualDecodeRotation {
						demoteDeliveryCapabilityV3(&record.NormalizedRequest, record.CurrentPlan.Delivery)
						demoteDeliveryCapabilityV3(&start, record.CurrentPlan.Delivery)
					}
					return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
						reason:    sourceDecodeFailedReasonV3,
						message:   sourceDecodeFailedMessageV3,
						retryable: false,
						cause:     transportErr.cause,
					}
				}
				return playback.DecisionResponseV3{}, *record, nil, transportErr
			}
			applyTransportToneMapModeV3(&result, transport)
			// Transport preparation can only attest the executor's tone-map
			// mode; every other frozen identity field was validated above. Copy
			// that one receipt into the already validated recipe instead of
			// rerunning a fallible subtitle-identity freeze after authority
			// publication.
			artifactRecipe.ToneMapMode = result.ToneMapMode
		}
	}
	result.Plan.Stream.URL = transport.url
	if result.Plan != nil && !transportReused {
		// Give each newly installed replan transport a unique plan ID that
		// cannot be confused with an earlier installation, while preserving
		// the canonical recipe attempt key for loop retirement.
		finalizeReplanPlanIdentityV3(result.Plan, record.PlaybackAttemptID, req.ReplanRequestID, start.ClientPlaybackContext.Output.OutputContextID)
		artifactRecipe.PlanID = result.Plan.PlanID
	}
	// Every replan that commits a release other than the row the viewer selected
	// reports it, not only the explicit version-pick hunt: a failure-recovery
	// rotation, a decode rotation and a rehydration after a dead source all move
	// the session onto different bytes, and a client that cannot see that can
	// only guess. The fields are set after the plan identity is finalized, so
	// they never change the plan hash, and the helper is a no-op on a replan that
	// keeps the requested row.
	if result.Plan != nil && requestedFile != nil && effectiveFile != nil {
		publishSubstitutionFieldsV3(result.Plan, requestedFile.ID, effectiveFile.ID,
			replanSubstitutionReasonV3(result.Plan, req, virtualDecodeRotation))
	}
	response := playback.DecisionResponseV3{ProtocolVersion: playback.ProtocolV3, ServerFeatures: serverFeaturesForRequestV3(r.Context()), Outcome: playback.OutcomePlayableV3, SessionID: session.ID, PlaybackPlan: result.Plan}
	if seekNoMovement {
		// The frozen rebuild is byte-identical to the route already playing:
		// answer it explicitly so the client does not treat the same timeline as
		// a fresh seek and snap back to it.
		response.Outcome = playback.OutcomeSeekNoMovementV3
	}
	updated := *record
	updated.CurrentPlanID = result.Plan.PlanID
	updated.CurrentPlan = *result.Plan
	// A seek reanchor that reuses the transport replays the durable recipe
	// verbatim; a rebuilt seek or newly planned replan binds the frozen
	// recipe to the final installed plan ID so ValidFor remains true. A seek
	// that dropped its subtitle must rewrite the recipe even when the
	// transport is reused, or the next seek re-validates the selection this
	// attempt already dropped and warns again.
	if !seekReanchor || !transportReused || seekSubtitleDegraded {
		updated.FrozenRecipe = artifactRecipe
	}
	updated.NormalizedRequest = start
	updated.DesiredTracks = nil
	if desired != nil {
		desired.PlanID = result.Plan.PlanID
		updated.DesiredTracks = desired
	}
	updated.EffectiveMediaFileID = effectiveFile.ID
	updated.ExpiresAt = time.Now().Add(playback.MaxTokenTTL)
	updated.CurrentTransportRendered = transportReused && record.CurrentTransportRendered
	if transportReused {
		if expiresAt, parseErr := time.Parse(time.RFC3339, result.Plan.ExpiresAt); parseErr == nil {
			updated.ExpiresAt = expiresAt
		}
	}
	originalRollback := transport.rollback
	originalRollbackRequired := transport.rollbackRequired
	replacement := playback.SessionReplacement{
		EffectiveMediaFileID: effectiveFile.ID,
		StreamState:          h.v3SessionStreamState(r.Context(), session, effectiveFile, result, transport, headerAuthenticatedMediaV3(req.ClientFeatures)),
	}
	if seekScopedRecovery {
		replacement.PositionSeconds = &req.PositionSeconds
		replacement.PositionSequence = record.LastSequence
		replacement.PreservePaused = true
	}
	transport.applySession = func() (func() error, error) {
		rollback, err := replacementManager.ApplyReplacement(session.ID, replacement)
		if err != nil {
			return nil, err
		}
		return func() error {
			return replacementManager.RollbackReplacement(session.ID, rollback)
		}, nil
	}
	transport.afterDurableCommit = func() {
		cancelReservation()
		if trackChange && !isNativeAPIV2(r.Context()) && strings.TrimSpace(req.Automatic) == "" {
			// The frozen /api/v1 bridge still saves a track change's audio. A
			// server-built reconciliation correction is not a viewer choice and
			// must not become a stored preference.
			h.persistLegacyAudioPreferenceV3(r.Context(), session.ID, session.UserID, session.ProfileID, effectiveFile, plannedAudioTrackIndexV3(result, session.AudioTrackIndex))
		}
		// afterDurableCommit runs after executeReplanV3 returns and its request
		// context may already be canceled. A sync bound to that context always
		// failed with context.Canceled, so the replanned session was never
		// persisted to the shared admin snapshot. Detach it while keeping the
		// request's logging/trace values and bound it so a wedged syncer cannot
		// hold the response open forever.
		h.syncSessionsAfterReplanDetached(r.Context())
		event := playback.RouteEventPlanSelectedV3
		clientModel := req.ClientPlaybackContext.Device.Model
		if seekReanchor {
			event = playback.RouteEventRuntimeCorrectionSucceededV3
			clientModel = start.ClientPlaybackContext.Device.Model
		}
		h.enqueueRouteEventV3(playback.RouteEventRecordV3{RouteEventV3: playback.RouteEventV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: req.PlaybackAttemptID, SessionID: session.ID, PlanID: result.Plan.PlanID, PlanAttemptID: req.PlanAttemptID, PlanAttemptKey: playback.PlanAttemptKeyV3(*result.Plan, start.ClientPlaybackContext.Output.OutputContextID, nil), Event: event, FallbackReason: req.Failure.Classification, AppliedQuirkIDs: appliedQuirkIDsV3(result.Plan), QuirkRegistryRevision: appliedQuirkRevisionV3(result.Plan), OutputContextID: start.ClientPlaybackContext.Output.OutputContextID, Diagnostics: softwareDecodeRouteDiagnosticsV3(forceSoftwareDecode, session, decodeFailureSample, decodeFailureCount, decodeVideoFrames, decodeOutTimeUS)}, UserID: session.UserID, ProfileID: session.ProfileID, ClientName: session.ClientName, ClientVersion: session.ClientVersion, ClientBuild: session.ClientBuild, ClientChannel: session.ClientChannel, ClientModel: clientModel})
	}
	transport.rollback = func() {
		originalRollback()
		cancelReservation()
	}
	if originalRollbackRequired != nil {
		transport.rollbackRequired = func() error {
			if err := originalRollbackRequired(); err != nil {
				return err
			}
			cancelReservation()
			return nil
		}
	}
	if recordSubtitleBreaker != nil {
		// Arm the breaker only after the rejection plan is durable. The
		// existing after-commit work runs first so its side effects are never
		// skipped by a panic in the tracker.
		previousAfterCommit := transport.afterDurableCommit
		transport.afterDurableCommit = func() {
			if previousAfterCommit != nil {
				previousAfterCommit()
			}
			recordSubtitleBreaker()
		}
	}
	reservationHandedOff = true
	return response, updated, &transport, nil
}

// replanStickyDeliveryV3 decides whether a replan should prefer to keep the
// delivery the session is already serving. Stickiness is a user-intent backstop,
// not a failure-recovery policy: a failure recovery (or its seek-scoped variant)
// carries an explicit verdict about the current route and must remain free to
// move. Every other operation — track, quality, output, seek reanchor, and the
// automatic audio correction — has no failure behind it, so re-deriving the
// delivery from scratch can only thrash the pipeline. The returned value is the
// current delivery for those operations; the planner keeps it only when it
// genuinely can still serve the request, and a principled switch (burn-in,
// forced encode, quality ladder) still wins.
func replanStickyDeliveryV3(operation playback.ReplanOperationV3, current playback.DeliveryV3) playback.DeliveryV3 {
	switch operation {
	case playback.ReplanOperationFailureRecoveryV3, playback.ReplanOperationSeekFailureRecoveryV3:
		return ""
	default:
		return current
	}
}

// deliveryChangeV3 builds the additive mid-session delivery-swap marker, or
// returns nil when the serving route did not change. A delivery change is always
// detectable; a play-method change is only reported when the previous method is
// known, so a reconstructed session with no cached method does not emit a
// spurious marker for an unchanged route.
func deliveryChangeV3(previousDelivery playback.DeliveryV3, previousPlayMethod playback.PlayMethod, nextDelivery playback.DeliveryV3, nextPlayMethod playback.PlayMethod) *playback.DeliveryChangeV3 {
	deliveryChanged := previousDelivery != "" && previousDelivery != nextDelivery
	playMethodChanged := previousPlayMethod != "" && previousPlayMethod != nextPlayMethod
	if !deliveryChanged && !playMethodChanged {
		return nil
	}
	return &playback.DeliveryChangeV3{
		PreviousDelivery:   previousDelivery,
		Delivery:           nextDelivery,
		PreviousPlayMethod: previousPlayMethod,
		PlayMethod:         nextPlayMethod,
		DeliveryChanged:    deliveryChanged,
		PlayMethodChanged:  playMethodChanged,
	}
}

// finalizeReplanPlanIdentityV3 mints a generation-scoped plan id for each newly
// installed replan transport, while preserving the canonical recipe attempt key
// for retirement in attempted_plan_keys. The replan request id distinguishes
// this transport generation so a delayed first-frame event from an earlier
// installation cannot falsely mark a reopened transport as rendered.
func finalizeReplanPlanIdentityV3(plan *playback.PlanV3, attemptID, replanRequestID, outputContextID string) {
	if plan == nil {
		return
	}
	canonicalPlan := *plan
	canonicalPlan.PlanID = playback.DeterministicPlanIDV3(attemptID, plan.RequestedMediaFileID, plan.EffectiveMediaFileID, *plan)
	plan.CanonicalPlanID = canonicalPlan.PlanID

	replanAttemptID := strings.TrimSpace(attemptID) + "|replan:" + strings.TrimSpace(replanRequestID)
	plan.PlanID = playback.DeterministicPlanIDV3(replanAttemptID, plan.RequestedMediaFileID, plan.EffectiveMediaFileID, *plan)
	plan.PlanAttemptKey = playback.PlanAttemptKeyV3(*plan, outputContextID, nil)
}

// softwareDecodeRouteDiagnosticsV3 records the reactive software-decode
// fallback and the decoder evidence on the durable plan-selected route event.
// It returns nil for an ordinary replan so the event shape is unchanged.
func softwareDecodeRouteDiagnosticsV3(force bool, session *playback.Session, sample string, count int, videoFrames, outTimeUS int64) map[string]string {
	if !force {
		return nil
	}
	diagnostics := map[string]string{
		"software_video_decode": "true",
		"decode_failure_count":  strconv.Itoa(count),
		// The frame counter is the evidence that distinguishes a decoder that
		// produced nothing from one that produced pictures: a hardware plan
		// that muxed segments with zero frames is the case the software retry
		// exists for.
		videoFramesLogKeyV3: strconv.FormatInt(videoFrames, 10),
		outTimeUSLogKeyV3:   strconv.FormatInt(outTimeUS, 10),
	}
	if session != nil {
		if hw := strings.TrimSpace(session.TranscodeHWAccel); hw != "" {
			diagnostics["hw_accel"] = hw
		}
	}
	if strings.TrimSpace(sample) != "" {
		diagnostics["decode_failure_sample"] = sample
	}
	return diagnostics
}

// applyTransportToneMapModeV3 records an executor fallback in the result before
// the durable recipe and session state are committed.
func applyTransportToneMapModeV3(result *playback.PlannerResultV3, transport preparedTransportV3) {
	if result != nil && transport.toneMapMode != "" {
		result.ToneMapMode = transport.toneMapMode
	}
}

// seekNoMovementToleranceSecondsV3 is the slack a seek target may differ from
// the anchored position and still count as "already there". Sub-quarter-second
// seeks are not visible to a viewer, and the client echoes the plan's own float.
const seekNoMovementToleranceSecondsV3 = 0.25

// seekReanchorNoMovementV3 reports whether a seek reanchor asks for the
// position the durable plan and the live session are already serving, so the
// frozen rebuild would hand back the same timeline and nothing would move.
//
// The plan anchor alone cannot answer this: after a viewer has watched past the
// anchor, a deliberate rewind to that anchor is a real move. Requiring the live
// position to match too keeps that rewind working while catching a repeated
// seek to where playback already is.
func seekReanchorNoMovementV3(record *playback.AttemptRecordV3, session *playback.Session, position float64) bool {
	if record == nil {
		return false
	}
	if math.Abs(record.CurrentPlan.Timeline.SourceStartSeconds-position) > seekNoMovementToleranceSecondsV3 {
		return false
	}
	return session == nil || math.Abs(session.Position-position) <= seekNoMovementToleranceSecondsV3
}

// nonSeekReplanPositionV3 is where a non-seek replan resumes: the client's live
// position when it is at least as far along as the session's last report, else
// the session position.
//
// A track change never moves the playhead, so the replacement must start where
// the viewer is now. The session position advances only with progress reports
// (about every 10s), so preferring it silently rewinds an audio switch by the
// time since the last report — production measured 66.92s -> 56.97s on one
// switch. At the click the client's position is the element's current content
// position, which is fresher than the last report whenever playback advanced.
//
// A client value behind the session is still treated as a stale snapshot (a
// rebuilt element that reported the route's start, or a position read before
// the previous progress report). The session has advanced past it, and starting
// the replacement there would rewind content that already played.
//
// On a progressive copy-video route this is only the requested start. The
// anchor resolver moves the transport origin back to the preceding keyframe, so
// the first presented frame can be up to one GOP earlier. That pre-roll is
// expected; do not compensate for it by choosing a later position here. See
// docs/architecture/playback-protocol-v3.md#copy-video-seek-anchor.
func (h *PlaybackHandler) nonSeekReplanPositionV3(sessionID string, requested float64) *float64 {
	position := requested
	if sessionID != "" {
		if current, err := h.sessionMgr.GetSession(sessionID); err == nil && current != nil &&
			current.Position > 0 && (!math.IsNaN(current.Position) && !math.IsInf(current.Position, 0)) {
			if position <= 0 || position < current.Position {
				position = current.Position
			}
		}
	}
	if math.IsNaN(position) || math.IsInf(position, 0) || position < 0 {
		position = 0
	}
	return &position
}

func frozenSeekReanchorResultV3(record *playback.AttemptRecordV3, position float64, now time.Time) (playback.PlannerResultV3, error) {
	if record == nil || !record.FrozenRecipe.ValidFor(record.CurrentPlan) {
		return playback.PlannerResultV3{}, errors.New("the active playback recipe is unavailable")
	}
	plan := record.CurrentPlan
	plan.ExpiresAt = playback.NewPlanExpiryV3(now)
	plan.Timeline = playback.TimelineV3{
		SourceStartSeconds: position,
		PlayerStartSeconds: position,
		CanSeekAnywhere:    true,
		SeekRestoration:    seekRestorationPlayerV3,
	}
	return record.FrozenRecipe.PlannerResult(&plan), nil
}

type subtitleIndexLocationV3 struct {
	source string
	offset int
}

// classifySubtitleIndexV3 maps the dense published combined subtitle index used
// by buildSubtitleURLs to its inventory segment and segment-local offset. The
// published space suppresses de-duplicated tracks, so the file's own range is
// translated back to its source ordinal before the external/embedded split; an
// index past the file's own published count is a downloaded entry whose offset
// follows that same published base.
func classifySubtitleIndexV3(file *models.MediaFile, index int) (subtitleIndexLocationV3, bool) {
	if file == nil || index < 0 {
		return subtitleIndexLocationV3{}, false
	}
	ownPublished := len(playback.BuildSubtitleInventoryV3(file, nil))
	if index < ownPublished {
		sourceIndex, ok := playback.SubtitleInventoryOwnSourceIndexV3(file, index)
		if !ok {
			return subtitleIndexLocationV3{}, false
		}
		if sourceIndex < len(file.ExternalSubtitles) {
			return subtitleIndexLocationV3{source: playback.SubtitleSourceExternalV3, offset: sourceIndex}, true
		}
		return subtitleIndexLocationV3{source: playback.SubtitleSourceEmbeddedV3, offset: sourceIndex - len(file.ExternalSubtitles)}, true
	}
	return subtitleIndexLocationV3{
		source: playback.SubtitleSourceDownloadedV3,
		offset: index - ownPublished,
	}, true
}

// freezeExecutableRecipeV3 extends the pure planner freeze with the identity
// of the selected sidecar subtitle. The combined subtitle index space
// (externals, then embedded, then downloaded — see buildSubtitleURLs) is not
// stable across inventory changes, so the index alone cannot anchor a durable
// selection. A downloaded selection whose identity cannot be established is
// an error: silently omitting it would disable drift detection for exactly
// the seeks this recipe exists to protect.
func (h *PlaybackHandler) freezeExecutableRecipeV3(_ context.Context, file *models.MediaFile, result playback.PlannerResultV3) (playback.ExecutableRecipeV3, error) {
	recipe := playback.FreezeExecutableRecipeV3(result)
	if file != nil {
		sourceMetadata := sourceExecutionMetadataV3(file, playback.PlannerResultV3{})
		recipe.SourceVideoCodec = sourceMetadata.VideoCodec
		recipe.SourceVideoProfile = sourceMetadata.VideoProfile
		recipe.SourceVideoBitDepth = sourceMetadata.VideoBitDepth
		recipe.SoftwareVideoDecode = sourceMetadata.SoftwareVideoDecode
		if result.Plan != nil && result.Plan.EffectiveRecipe.SoftwareVideoDecode {
			// The plan, not the source facts, is authoritative for a reactive
			// software-decode retry: freeze the decode mode so a reconstruct
			// cannot re-enable the hardware decoder that just failed.
			recipe.SoftwareVideoDecode = true
		}
		recipe.SourceDurationSeconds = sourceMetadata.DurationSeconds
		recipe.ToneMapDVConfigPresent = sourceMetadata.ToneMapDVConfigPresent
		recipe.ToneMapDVBLCompatIDPresent = sourceMetadata.ToneMapDVBLCompatIDPresent
		recipe.ToneMapDVBLPresent = sourceMetadata.ToneMapDVBLPresent
		recipe.ToneMapDVRPUPresent = sourceMetadata.ToneMapDVRPUPresent
		if recipe.ToneMapDVConfigPresent || recipe.ToneMapDVBLCompatIDPresent || recipe.ToneMapDVBLPresent || recipe.ToneMapDVRPUPresent {
			recipe.Version = 2
		}
	}
	if file == nil || result.SubtitleTrackIndex < 0 {
		return recipe, nil
	}
	// A downloaded row ID was selected from the planner's inventory snapshot.
	// Treat it as authoritative before consulting the mutable combined-index
	// segments: an external or embedded subtitle added after planning must not
	// make this downloaded selection look like a different source.
	if recipe.DownloadedSubtitleID > 0 {
		recipe.SubtitleSource = playback.SubtitleSourceDownloadedV3
		return recipe, nil
	}
	location, ok := classifySubtitleIndexV3(file, result.SubtitleTrackIndex)
	if !ok {
		return recipe, nil
	}
	switch location.source {
	case playback.SubtitleSourceExternalV3:
		recipe.SubtitleSource = playback.SubtitleSourceExternalV3
		recipe.ExternalSubtitlePath = file.ExternalSubtitles[location.offset].Path
	case playback.SubtitleSourceEmbeddedV3:
		recipe.SubtitleSource = playback.SubtitleSourceEmbeddedV3
		recipe.EmbeddedStreamIndex = file.SubtitleTracks[location.offset].Index
	case playback.SubtitleSourceDownloadedV3:
		if recipe.DownloadedSubtitleID <= 0 {
			return playback.ExecutableRecipeV3{}, errors.New("the selected downloaded subtitle has no stable identity")
		}
	}
	return recipe, nil
}

// validateFrozenSubtitleIdentityV3 confirms the frozen combined subtitle
// index still resolves to the identical inventory entry it was frozen
// against. It mirrors the segment layout of buildSubtitleURLs so a change in
// any earlier segment's size — which shifts every later index — is detected
// as an identity mismatch rather than silently re-resolved.
func (h *PlaybackHandler) validateFrozenSubtitleIdentityV3(ctx context.Context, file *models.MediaFile, recipe playback.ExecutableRecipeV3) error {
	if recipe.SubtitleSource == "" {
		return nil
	}
	if file == nil || recipe.SubtitleTrackIndex < 0 {
		return errors.New("the frozen subtitle selection is unavailable")
	}
	if recipe.SubtitleSource == playback.SubtitleSourceDownloadedV3 {
		if h == nil || h.SubtitleRepo == nil || recipe.DownloadedSubtitleID <= 0 {
			return errors.New("the downloaded subtitle inventory is unavailable")
		}
		downloaded, err := h.SubtitleRepo.GetDownloadedSubtitle(ctx, recipe.DownloadedSubtitleID)
		if err != nil {
			return wrapSubtitleStoreErrorV3(err)
		}
		if downloaded == nil || downloaded.MediaFileID != file.ID {
			return errors.New("the frozen downloaded subtitle identity changed")
		}
		return nil
	}
	location, ok := classifySubtitleIndexV3(file, recipe.SubtitleTrackIndex)
	if !ok || location.source != recipe.SubtitleSource {
		return errors.New("the frozen subtitle inventory segment changed")
	}
	switch recipe.SubtitleSource {
	case playback.SubtitleSourceExternalV3:
		if file.ExternalSubtitles[location.offset].Path != recipe.ExternalSubtitlePath {
			return errors.New("the frozen external subtitle identity changed")
		}
	case playback.SubtitleSourceEmbeddedV3:
		if file.SubtitleTracks[location.offset].Index != recipe.EmbeddedStreamIndex {
			return errors.New("the frozen embedded subtitle identity changed")
		}
	default:
		return errors.New("the frozen subtitle identity is unrecognized")
	}
	return nil
}

// subtitleBurnInDecisionReasonV3 marks a plan encoded only because the selected
// subtitle had to be rendered into the video. A seek that drops that subtitle
// must not keep advertising the burn-in.
const subtitleBurnInDecisionReasonV3 = "subtitle_burn_in_required"

// subtitleBurnInWarningCodeV3 is the degradation warning a burn-in plan carries.
const subtitleBurnInWarningCodeV3 = "subtitle_burn_in"

// clearPlanSubtitleV3 publishes a plan as subtitles-off while keeping the
// inventory it already carries, so the client menu survives the degrade. It
// clears every part of the selection together — the decision, the selected
// track identity, the subtitle claims, and burn-in evidence — because a plan
// that says "off" next to a selected track or a rendered overlay is exactly the
// stale pairing clients alias wrong.
func clearPlanSubtitleV3(plan *playback.PlanV3) {
	if plan == nil {
		return
	}
	inventory := plan.Subtitle.Inventory
	plan.Subtitle = playback.SubtitleDecisionV3{Mode: playback.SubtitleOffV3, Inventory: inventory}
	plan.SelectedTracks.Subtitle = nil
	plan.Claims.Subtitles = playback.SubtitleClaimsV3{}
	// Rebuild rather than filter in place: the plan is a shallow copy of the
	// durable record's plan, so an in-place delete would rewrite the record's
	// warning slice through the shared backing array.
	warnings := make([]playback.DegradationWarningV3, 0, len(plan.DegradationWarnings))
	for _, warning := range plan.DegradationWarnings {
		if warning.Code != subtitleBurnInWarningCodeV3 {
			warnings = append(warnings, warning)
		}
	}
	plan.DegradationWarnings = warnings
	if plan.DecisionReason == subtitleBurnInDecisionReasonV3 {
		plan.DecisionReason = "subtitle_unavailable"
	}
}

// degradeSeekReanchorSubtitleV3 drops a seek reanchor's subtitle from the
// replayed plan and its frozen recipe. A seek's requested position outranks
// subtitle fidelity: when the carried selection cannot bind to the effective
// file (a substituted release, a stale pick, or a missing artifact) the replan
// continues with video and audio intact and records the existing
// subtitle-unavailable warning once. The recipe's subtitle identity is cleared
// with the plan so the same failure is not re-discovered on the next seek.
func degradeSeekReanchorSubtitleV3(recipe playback.ExecutableRecipeV3, result *playback.PlannerResultV3) playback.ExecutableRecipeV3 {
	recipe.SubtitleTrackIndex = -1
	recipe.SubtitleTransportTrackIndex = -1
	recipe.SubtitleBurnIn = false
	recipe.SubtitleCodec = ""
	recipe.SubtitleRendition = false
	recipe.SubtitleSource = ""
	recipe.ExternalSubtitlePath = ""
	recipe.EmbeddedStreamIndex = 0
	recipe.DownloadedSubtitleID = 0
	if result == nil {
		return recipe
	}
	result.SubtitleTrackIndex = -1
	result.SubtitleTransportTrackIndex = -1
	result.SubtitleBurnIn = false
	result.SubtitleCodec = ""
	result.SubtitleRendition = false
	result.DownloadedSubtitleID = 0
	if result.Plan == nil {
		return recipe
	}
	clearPlanSubtitleV3(result.Plan)
	appendStartWarningsV3(result, []playback.DegradationWarningV3{playback.SubtitleTrackUnavailableWarningV3()})
	return recipe
}

func validateSeekRecoveryRequestV3(record *playback.AttemptRecordV3, req playback.ReplanRequestV3) error {
	if record == nil {
		return errors.New("the current playback attempt is unavailable")
	}
	wantedQuality, _ := playback.NormalizeQualityV3(record.NormalizedRequest.QualityPreference)
	requestedQuality, _ := playback.NormalizeQualityV3(req.QualityPreference)
	if requestedQuality != wantedQuality {
		return errors.New("seek recovery cannot change playback quality")
	}
	if req.ClientPlaybackContext.Output.OutputContextID != record.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID {
		return errors.New("seek recovery cannot change the output route")
	}
	if !sameSelectedTracksV3(req.SelectedTracks, record.CurrentPlan.SelectedTracks) {
		return errors.New("seek recovery cannot change selected tracks")
	}
	return nil
}

// seekReanchorIdentityChangesV3 returns only bounded, non-secret field names.
// It is safe for structured logs: values, URLs, headers, tokens, and subtitle
// artifact locations are deliberately excluded.
func seekReanchorIdentityChangesV3(record *playback.AttemptRecordV3, candidate *playback.PlanV3) []string {
	if record == nil || candidate == nil {
		return []string{"route"}
	}
	current := record.CurrentPlan
	changed := make([]string, 0, 16)
	add := func(name string, differs bool) {
		if differs {
			changed = append(changed, name)
		}
	}
	// Generation-scoped plan IDs rotate per transport; the seek-reanchor guard
	// compares the canonical recipe identity so a new generation of the same
	// recipe stays eligible. Plans without a canonical ID keep the exact
	// generation comparison.
	candidateIdentity := candidate.PlanID
	if candidate.CanonicalPlanID != "" {
		candidateIdentity = candidate.CanonicalPlanID
	}
	currentIdentity := current.PlanID
	committedIdentity := record.CurrentPlanID
	if current.CanonicalPlanID != "" {
		currentIdentity = current.CanonicalPlanID
		committedIdentity = current.CanonicalPlanID
	}
	add("plan_id", candidateIdentity != currentIdentity || candidateIdentity != committedIdentity)
	add("requested_file_id", candidate.RequestedMediaFileID != record.RequestedMediaFileID)
	add("effective_file_id", candidate.EffectiveMediaFileID != record.EffectiveMediaFileID)
	add("delivery", candidate.Delivery != current.Delivery)
	add("protocol", candidate.Stream.Protocol != current.Stream.Protocol)
	add("container", candidate.Stream.Container != current.Stream.Container)
	add("mime_type", candidate.Stream.MIMEType != current.Stream.MIMEType)
	add("header_refresh", candidate.Stream.HeaderRefresh != current.Stream.HeaderRefresh)
	add("video_codec", candidate.EffectiveRecipe.VideoCodec != current.EffectiveRecipe.VideoCodec)
	add("video_sample_entry", candidate.EffectiveRecipe.VideoSampleEntry != current.EffectiveRecipe.VideoSampleEntry)
	add("segment_container", candidate.EffectiveRecipe.SegmentContainer != current.EffectiveRecipe.SegmentContainer)
	add("audio_codec", candidate.EffectiveRecipe.AudioCodec != current.EffectiveRecipe.AudioCodec)
	add("resolution", !optionalIntEqualV3(candidate.EffectiveRecipe.Width, current.EffectiveRecipe.Width) || !optionalIntEqualV3(candidate.EffectiveRecipe.Height, current.EffectiveRecipe.Height))
	add("frame_rate", !optionalFloatEqualV3(candidate.EffectiveRecipe.FrameRate, current.EffectiveRecipe.FrameRate))
	add("bitrate", !optionalIntEqualV3(candidate.EffectiveRecipe.BitrateKbps, current.EffectiveRecipe.BitrateKbps))
	add("dynamic_range", candidate.EffectiveRecipe.DynamicRange != current.EffectiveRecipe.DynamicRange)
	add("audio_channels", !optionalIntEqualV3(candidate.EffectiveRecipe.AudioChannels, current.EffectiveRecipe.AudioChannels) || candidate.EffectiveRecipe.AudioLayout != current.EffectiveRecipe.AudioLayout)
	add("selected_audio", !sameTrackIdentityV3(candidate.SelectedTracks.Audio, current.SelectedTracks.Audio))
	add("selected_subtitle", !sameTrackIdentityV3(candidate.SelectedTracks.Subtitle, current.SelectedTracks.Subtitle))
	add("subtitle_mode", candidate.Subtitle.Mode != current.Subtitle.Mode || candidate.Subtitle.TrackID != current.Subtitle.TrackID)
	add("subtitle_embedded_route", !sameEmbeddedSubtitleRouteV3(candidate.Subtitle.Embedded, current.Subtitle.Embedded))
	add("subtitle_hls_rendition", !sameHLSSubtitleRenditionV3(candidate.Subtitle.HLSRendition, current.Subtitle.HLSRendition))
	add("subtitle_artifact_route", !sameSubtitleArtifactRouteV3(candidate.Subtitle.Artifact, current.Subtitle.Artifact))
	add("subtitle_fidelity", candidate.SubtitleFidelityPolicy != current.SubtitleFidelityPolicy)
	add("transformations", !sameTransformationsV3(candidate.Transformations, current.Transformations))
	add("quirks", !sameAppliedQuirksV3(candidate.AppliedQuirks, current.AppliedQuirks))
	add("runtime_corrections", !sameStringMultisetV3(candidate.RuntimeCorrections, current.RuntimeCorrections))
	add("claims", candidate.Claims != current.Claims)
	// A seek reanchor replays the running plan's source binding, so the
	// candidate may not name a different candidate URI or source descriptor. The
	// frozen rebuild copies both from record.CurrentPlan, so this only fires on a
	// future caller that plans a seek from live state instead; it is the source
	// half of the route-identity guard the other fields already cover.
	add("effective_virtual_uri", candidate.EffectiveVirtualURI != current.EffectiveVirtualURI)
	add("source", candidate.Source != current.Source)
	return changed
}

func validateSeekReanchorPlanV3(record *playback.AttemptRecordV3, candidate *playback.PlanV3) error {
	if record == nil || candidate == nil {
		return errors.New("seek reanchor produced no playback route")
	}
	changedFields := seekReanchorIdentityChangesV3(record, candidate)
	if len(changedFields) == 0 {
		return nil
	}
	if containsStringExactV3(changedFields, "plan_id") {
		return errors.New("seek reanchor changed the playback plan identity")
	}
	if containsStringExactV3(changedFields, "requested_file_id") || containsStringExactV3(changedFields, "effective_file_id") {
		return errors.New("seek reanchor changed the selected media version")
	}
	if containsStringExactV3(changedFields, "selected_audio") || containsStringExactV3(changedFields, "selected_subtitle") {
		return errors.New("seek reanchor changed selected tracks")
	}
	return errors.New("seek reanchor changed the playback route semantics")
}

func sameEmbeddedSubtitleRouteV3(left, right *playback.EmbeddedSubtitleV3) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// withoutFailedSubtitleRoutesV3 removes the subtitle routes this attempt may
// no longer use from a replan's client features.
//
// Native selection is negotiated at start and can only be disabled during an
// attempt: a confirmed failure stays disabled even when a later replan resends
// the full feature advertisement. A subtitle rendition the player could not
// select would fail the same way on the next intent replan, which forgets the
// attempted plan keys, so it is dropped from the durable request too; the
// attempt-sticky pinning then keeps it dropped.
func withoutFailedSubtitleRoutesV3(features, negotiated []string, failureClassification string) []string {
	if !playback.HasFeatureV3(negotiated, playback.FeatureEmbeddedSubtitlesV3) || failureClassification == playback.FailureSubtitleEmbeddedFailedV3 {
		features = playback.WithoutFeatureV3(features, playback.FeatureEmbeddedSubtitlesV3)
	}
	if failureClassification == playback.FailureSubtitleHLSRenditionFailedV3 {
		features = playback.WithoutFeatureV3(features, playback.FeatureHLSSubtitleRenditionsV3)
	}
	return features
}

func sameHLSSubtitleRenditionV3(left, right *playback.HLSSubtitleRenditionV3) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameSubtitleArtifactRouteV3(left, right *playback.SubtitleArtifactV3) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	// Signed URLs and timing origins are allowed to rotate when a transport is
	// reopened; the player-facing artifact representation is not.
	return left.MIMEType == right.MIMEType && left.Format == right.Format
}

func sameTransformationsV3(left, right []playback.TransformationV3) bool {
	if len(left) != len(right) {
		return false
	}
	matched := make([]bool, len(right))
	for _, candidate := range left {
		found := false
		for index, current := range right {
			if !matched[index] && candidate.Name == current.Name && candidate.Executor == current.Executor &&
				candidate.RecipeVersion == current.RecipeVersion && sameStringMultisetV3(candidate.ValidatedClaims, current.ValidatedClaims) {
				matched[index] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func sameAppliedQuirksV3(left, right []playback.AppliedQuirkV3) bool {
	if len(left) != len(right) {
		return false
	}
	matched := make([]bool, len(right))
	for _, candidate := range left {
		found := false
		for index, current := range right {
			if !matched[index] && candidate == current {
				matched[index] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func sameStringMultisetV3(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	return true
}

// sidecarOnlyReuseReplanV3 proves that a track-change plan can keep the active
// A/V generation. Subtitle identity and claims are intentionally excluded:
// those are the point of the replan and are delivered by the independently
// addressed sidecar artifact. Every field that can change FFmpeg's audio/video
// output remains part of the comparison.
func sidecarOnlyHLSReplanV3(record *playback.AttemptRecordV3, candidate *playback.PlanV3, candidateRecipe playback.ExecutableRecipeV3, outputContextID string) (playback.ExecutableRecipeV3, bool) {
	return sidecarOnlyReuseReplanV3(record, candidate, candidateRecipe, outputContextID)
}

func sidecarOnlyReuseReplanV3(record *playback.AttemptRecordV3, candidate *playback.PlanV3, candidateRecipe playback.ExecutableRecipeV3, outputContextID string) (playback.ExecutableRecipeV3, bool) {
	if record == nil || candidate == nil || record.CurrentPlan.Stream.URL == "" ||
		!record.FrozenRecipe.ValidFor(record.CurrentPlan) || !candidateRecipe.ValidFor(*candidate) ||
		record.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID != outputContextID ||
		record.EffectiveMediaFileID != candidate.EffectiveMediaFileID ||
		record.CurrentPlan.RequestedMediaFileID != candidate.RequestedMediaFileID ||
		record.CurrentPlan.EffectiveMediaFileID != candidate.EffectiveMediaFileID ||
		!reuseEligibleDeliveryV3(record.CurrentPlan.Delivery) || record.CurrentPlan.Delivery != candidate.Delivery ||
		record.CurrentPlan.Subtitle.Mode == playback.SubtitleBurnInV3 || candidate.Subtitle.Mode == playback.SubtitleBurnInV3 ||
		// On-demand renditions are the stream's own playlists: the player picks
		// one, so a selection change among the same set keeps the transport. A
		// different set, or a move to or from the pipeline rendition, needs a new
		// stream. Dropping every on-demand playlist is fine (the client stops
		// selecting one) unless a pipeline rendition takes over.
		!sidecarOnlyReplanRenditionsCompatibleV3(record, candidate, candidateRecipe) ||
		!sameTrackIdentityV3(record.CurrentPlan.SelectedTracks.Audio, candidate.SelectedTracks.Audio) {
		return candidateRecipe, false
	}
	// The planner prefers hardware whenever both executors are currently
	// available, but the active transport may have fallen back to software after
	// its plan was accepted. A sidecar-only replan must compare against and carry
	// that effective mode or it will replace byte-identical running output merely
	// because planning preferred hardware again.
	effectiveCandidateRecipe := candidateRecipe
	if effectiveMode := record.FrozenRecipe.ToneMapMode; effectiveMode != "" && candidateRecipe.ToneMapPolicy.Allows(effectiveMode) {
		effectiveCandidateRecipe.ToneMapMode = effectiveMode
	}
	if record.CurrentPlan.Stream.Protocol != candidate.Stream.Protocol ||
		record.CurrentPlan.Stream.Container != candidate.Stream.Container ||
		record.CurrentPlan.Stream.MIMEType != candidate.Stream.MIMEType ||
		record.CurrentPlan.Stream.HeaderRefresh != candidate.Stream.HeaderRefresh ||
		!sameExecutableAVRecipeV3(record.FrozenRecipe, effectiveCandidateRecipe) ||
		!sameEffectiveAVRecipeV3(record.CurrentPlan.EffectiveRecipe, candidate.EffectiveRecipe) ||
		record.CurrentPlan.Claims.Video != candidate.Claims.Video ||
		record.CurrentPlan.Claims.Audio != candidate.Claims.Audio ||
		!sameTransformationsV3(record.CurrentPlan.Transformations, candidate.Transformations) ||
		!sameAppliedQuirksV3(record.CurrentPlan.AppliedQuirks, candidate.AppliedQuirks) ||
		!sameStringMultisetV3(record.CurrentPlan.RuntimeCorrections, candidate.RuntimeCorrections) {
		return candidateRecipe, false
	}
	return effectiveCandidateRecipe, true
}

// sidecarOnlyReplanRenditionsCompatibleV3 reports whether a sidecar-only replan
// may keep the running HLS transport's subtitle rendition output. On-demand
// renditions are per-track playlists the player selects between, so a change
// among the same set keeps the transport. A different set, or a move to or from
// the pipeline rendition, changes the emitted playlists and needs a new stream;
// dropping every rendition is safe because the client simply stops selecting
// one.
func sidecarOnlyReplanRenditionsCompatibleV3(record *playback.AttemptRecordV3, candidate *playback.PlanV3, candidateRecipe playback.ExecutableRecipeV3) bool {
	currentTracks := record.FrozenRecipe.SubtitleRenditionTracks
	candidateTracks := candidateRecipe.SubtitleRenditionTracks
	switch {
	case len(candidateTracks) > 0 && len(currentTracks) == 0:
		return false
	case len(currentTracks) > 0 && len(candidateTracks) == 0:
		// Dropping the on-demand set is fine only when no pipeline rendition
		// replaces it.
		return candidate.Subtitle.HLSRendition == nil
	case len(candidateTracks) > 0:
		if !sameSubtitleRenditionTrackSetV3(currentTracks, candidateTracks) {
			return false
		}
	}
	if len(candidateTracks) > 0 {
		// The selected rendition is a player-side playlist choice within the
		// same set; it does not alter the emitted output.
		return true
	}
	return candidate.Subtitle.HLSRendition == nil ||
		(record.CurrentPlan.Subtitle.HLSRendition != nil &&
			record.FrozenRecipe.SubtitleRendition &&
			record.FrozenRecipe.SubtitleTransportTrackIndex == candidateRecipe.SubtitleTransportTrackIndex)
}

// sameSubtitleRenditionTrackSetV3 compares the on-demand track numbers as a
// set: ordering is not part of the emitted playlist identity.
func sameSubtitleRenditionTrackSetV3(left, right []playback.SubtitleRenditionTrack) bool {
	if len(left) != len(right) {
		return false
	}
	numbers := make(map[uint64]int, len(left))
	for _, track := range left {
		numbers[track.TrackNumber]++
	}
	for _, track := range right {
		numbers[track.TrackNumber]--
		if numbers[track.TrackNumber] < 0 {
			return false
		}
	}
	return true
}

func isHLSDeliveryV3(delivery playback.DeliveryV3) bool {
	return delivery == playback.DeliveryRemuxHLSV3 || delivery == playback.DeliveryTranscodeHLSV3
}

// reuseEligibleDeliveryV3 reports whether a delivery keeps a single addressable
// stream window whose bytes do not change when a sidecar-only track switch
// replans. HLS generations are keyed to the session, and both a local
// progressive remux and a direct HTTP play are served lazily from
// /stream/{sessionID}, so they survive a subtitle switch without a client
// remount. Direct HTTP is included because its URL is transport-stable while
// the route is unchanged: the only per-plan variation was the signed token,
// which the reuse path preserves verbatim. The reused token still carries the
// active plan's claims.
func reuseEligibleDeliveryV3(delivery playback.DeliveryV3) bool {
	return delivery == playback.DeliveryOriginalHTTPV3 ||
		delivery == playback.DeliveryRemuxProgressiveV3 ||
		delivery == playback.DeliveryRemuxHLSV3 ||
		delivery == playback.DeliveryTranscodeHLSV3
}

// seekReanchorWithinActiveWindowV3 reports whether a seek target can be served
// in place by the active transport's window. Only a segment-addressable HLS
// transport can: its manifest resolves an arbitrary position against the
// generation's timeline. A progressive remux response is a single continuous
// byte stream from its original start, so reusing it for a seek hands the
// client a URL whose bytes already (or never) cover the target; a progressive
// reanchor must rebuild instead.
//
// A target before an HLS window's start needs a fresh generation (the reused
// one cannot produce bytes before its origin). A closed window end is honored
// so reuse never claims a range the transport no longer serves. A growing
// playlist publishes no end, so this check alone cannot tell whether the target
// has been produced; seekReanchorWithinActiveWindowV3 on the handler adds that
// comparison against the live session's produced head.
//
// A can_seek_anywhere plan publishes no window start, but the reused generation
// still begins at a known stream origin: the segment layer can only serve
// bytes at or after that origin, and a target before it makes
// copyForwardJumpSeekTarget decline with ErrSegmentNotFound, turning the seek
// into a 404 -> failure-recovery rebuild. Reject it here so the caller rebuilds
// directly. An unknown origin (zero) keeps the previous permissive behavior.
func seekReanchorWithinActiveWindowV3(plan playback.PlanV3, position float64) bool {
	if !isHLSDeliveryV3(plan.Delivery) {
		return false
	}
	if plan.Timeline.SeekWindowStartSeconds != nil && position < *plan.Timeline.SeekWindowStartSeconds {
		return false
	}
	if plan.Timeline.StreamOriginSeconds > 0 && position < plan.Timeline.StreamOriginSeconds {
		return false
	}
	if plan.Timeline.SeekWindowEndSeconds != nil && position > *plan.Timeline.SeekWindowEndSeconds {
		return false
	}
	return true
}

// seekReanchorWithinActiveWindowV3 is the live-session form of the check
// above. A transport that has published a closed or can_seek_anywhere window
// can serve any target inside it from its segment layer, so reuse is safe. A
// growing manifest has no published end: a target beyond the segments the
// session has actually produced cannot be served by the reused generation
// without an encoder restart that leaves the client clamped to the live edge,
// so it must rebuild the transport at the target. The produced head comes from
// the live transcode session, not from the plan, because only the session
// knows how far FFmpeg has actually written.
func (h *PlaybackHandler) seekReanchorWithinActiveWindowV3(session *playback.Session, plan playback.PlanV3, position float64) bool {
	if !seekReanchorWithinActiveWindowV3(plan, position) {
		return false
	}
	// The plan's window is the window the transport had when the plan was
	// built. An in-place seek restart moves the live generation forward, and
	// the next backward seek still names this plan: its window still starts at
	// the old origin even though the generation now begins at the restart
	// segment. Reusing that generation would hand the client a playlist whose
	// first segment is past the target, which clamps the playhead forward
	// (production: a viewer asked for 328 s and ended at 931 s). A target
	// before the live generation's own start is a new window, not a position
	// this generation can produce, so rebuild the transport at the target.
	if generationStart, ok := liveGenerationStartSecondsV3(h, session); ok && position < generationStart {
		return false
	}
	if plan.Timeline.CanSeekAnywhere || plan.Timeline.SeekWindowEndSeconds != nil {
		return true
	}
	headEnd, ok := h.producedHeadEndSecondsV3(session)
	return seekReanchorWithinActiveWindowAtHeadV3(plan, position, headEnd, ok)
}

// liveGenerationStartSecondsV3 reports the source-timeline start of the
// generation the session's FFmpeg process currently produces, as its absolute
// HLS segment number times the segment duration. An in-place restart moves it
// forward while the plan the client holds keeps the older window. A session
// that never started a process, or whose generation begins at segment zero,
// reports false and keeps the pre-existing plan-window behavior.
func liveGenerationStartSecondsV3(h *PlaybackHandler, session *playback.Session) (float64, bool) {
	if h == nil || h.tm == nil || session == nil {
		return 0, false
	}
	live := h.tm.GetTranscodeSession(session.ID)
	if live == nil {
		return 0, false
	}
	progress := live.SegmentProgress(time.Now())
	if progress.StartSegmentNumber <= 0 {
		return 0, false
	}
	segmentSeconds := progress.SegmentDuration
	if segmentSeconds <= 0 {
		segmentSeconds = playback.DefaultSegmentDuration
	}
	return float64(progress.StartSegmentNumber) * float64(segmentSeconds), true
}

// seekReanchorWithinActiveWindowAtHeadV3 reports whether a growing HLS
// transport, whose produced content ends at producedHeadEnd, can answer
// position in place. Without a session that reports a head, the previous
// permissive behavior is kept: only an in-place restart is then possible, and
// rebuilding would lose the only stream that can serve it.
func seekReanchorWithinActiveWindowAtHeadV3(plan playback.PlanV3, position, producedHeadEnd float64, haveHead bool) bool {
	if !seekReanchorWithinActiveWindowV3(plan, position) {
		return false
	}
	if plan.Timeline.CanSeekAnywhere || plan.Timeline.SeekWindowEndSeconds != nil {
		return true
	}
	if !haveHead {
		return true
	}
	return position <= producedHeadEnd
}

// producedHeadEndSecondsV3 reports the source-timeline end of the last segment
// the live session has fully produced. The head's start comes from the
// session's real manifest and its duration from the configured segment length;
// a missing session or an unreadable timeline reports no head, which keeps the
// caller on the reuse path.
func (h *PlaybackHandler) producedHeadEndSecondsV3(session *playback.Session) (float64, bool) {
	if h == nil || h.tm == nil || session == nil {
		return 0, false
	}
	transcodeSession := h.tm.GetTranscodeSession(session.ID)
	if transcodeSession == nil {
		return 0, false
	}
	progress := transcodeSession.SegmentProgress(time.Now())
	if progress.ProducedHead < 0 {
		return 0, false
	}
	headStart, ok, err := transcodeSession.SegmentStartTime(progress.ProducedHead)
	if err != nil || !ok {
		return 0, false
	}
	segmentSeconds := float64(progress.SegmentDuration)
	if segmentSeconds <= 0 {
		segmentSeconds = playback.DefaultSegmentDuration
	}
	return headStart + segmentSeconds, true
}

// sameExecutableAVRecipeV3 reports whether two frozen A/V recipes are equivalent.
func sameExecutableAVRecipeV3(left, right playback.ExecutableRecipeV3) bool {
	return left.PlayMethod == right.PlayMethod &&
		left.TranscodeAudio == right.TranscodeAudio &&
		left.TargetVideoCodec == right.TargetVideoCodec &&
		left.TargetAudioCodec == right.TargetAudioCodec &&
		left.SourceAudioChannels == right.SourceAudioChannels &&
		left.TargetAudioChannels == right.TargetAudioChannels &&
		left.TargetAudioBitrateKbps == right.TargetAudioBitrateKbps &&
		left.TargetResolution == right.TargetResolution &&
		left.TargetBitrateKbps == right.TargetBitrateKbps &&
		left.SourceVideoCodec == right.SourceVideoCodec &&
		left.SourceVideoProfile == right.SourceVideoProfile &&
		left.SourceVideoBitDepth == right.SourceVideoBitDepth &&
		left.SoftwareVideoDecode == right.SoftwareVideoDecode &&
		left.SourceDurationSeconds == right.SourceDurationSeconds &&
		left.ToneMapPolicy == right.ToneMapPolicy &&
		left.ToneMapMode == right.ToneMapMode &&
		left.ToneMapSourceKind == right.ToneMapSourceKind &&
		left.ToneMapRecipeVersion == right.ToneMapRecipeVersion &&
		left.ToneMapPreflightRequired == right.ToneMapPreflightRequired &&
		left.ToneMapSourceRevision == right.ToneMapSourceRevision &&
		left.ToneMapDVConfigPresent == right.ToneMapDVConfigPresent &&
		left.ToneMapDVBLCompatIDPresent == right.ToneMapDVBLCompatIDPresent &&
		left.ToneMapDVBLPresent == right.ToneMapDVBLPresent &&
		left.ToneMapDVRPUPresent == right.ToneMapDVRPUPresent &&
		left.CopyVideoMPEGTS == right.CopyVideoMPEGTS
}

func sameEffectiveAVRecipeV3(left, right playback.EffectiveRecipeV3) bool {
	return left.VideoCodec == right.VideoCodec && left.AudioCodec == right.AudioCodec &&
		left.SegmentContainer == right.SegmentContainer &&
		optionalIntEqualV3(left.Width, right.Width) && optionalIntEqualV3(left.Height, right.Height) &&
		optionalFloatEqualV3(left.FrameRate, right.FrameRate) &&
		optionalIntEqualV3(left.BitrateKbps, right.BitrateKbps) &&
		left.DynamicRange == right.DynamicRange &&
		optionalIntEqualV3(left.AudioChannels, right.AudioChannels) && left.AudioLayout == right.AudioLayout
}

// reusedStreamCredentialRefreshMarginV3 is how much of a reused signed stream
// credential's lifetime must remain before a transport may reuse it verbatim.
// A credential closer to expiry is re-minted in place.
const reusedStreamCredentialRefreshMarginV3 = time.Hour

// authorizeReusedStreamCredentialV3 proves that the signed credential carried by
// a reused stream URL still authorizes the live session and its resource, and
// re-mints it in place when it is missing, mismatched, or close to expiry. It
// exists because preserving record.CurrentPlan.Stream verbatim is only safe
// while that credential remains valid: retiring the plan does not retire a
// session-bound token, so the credential is bound to the session and the media
// file, not to the plan. A credential that no longer proves that binding — or
// that is about to expire — must be re-minted rather than reused.
//
// Only the st query parameter is re-signed; the URL path, its other query
// parameters, and the session identity are untouched, so a sidecar-only replan
// cannot force a remount. A URL with no st credential (header-authenticated
// attempts, signing disabled, or a proxy path token) is returned unchanged:
// there is no plan-scoped credential here to validate.
//
// The returned bool is whether the credential was re-minted. ok is false when
// the URL's credential cannot be proven to authorize the resource and could not
// be refreshed, in which case the caller must rebuild the transport.
func (h *PlaybackHandler) authorizeReusedStreamCredentialV3(session *playback.Session, stream *playback.StreamV3) (refreshed bool, ok bool) {
	if h == nil || session == nil || stream == nil {
		return false, false
	}
	token := streamTokenFromURLV3(stream.URL)
	if token == "" || h.JWTSecret == "" {
		// No signed credential to validate. Header-authenticated attempts
		// deliberately publish a credential-free URL, and an empty signing
		// secret cannot mint or verify one; both preserve the URL byte-for-byte.
		return false, true
	}
	if claims, err := streamtoken.Verify(token, h.JWTSecret); err == nil && claims.SessionID == session.ID &&
		(session.MediaFileID == 0 || claims.MediaFileID == session.MediaFileID) {
		if claims.ExpiresAt != nil && time.Until(claims.ExpiresAt.Time) > reusedStreamCredentialRefreshMarginV3 {
			return false, true
		}
		// A verified, correctly bound credential that is close to expiry is
		// re-signed from its own claims, so a transcode recipe is preserved
		// exactly rather than rebuilt from session identity.
		if fresh, signErr := streamtoken.Sign(*claims, h.JWTSecret, playback.MaxTokenTTL); signErr == nil && fresh != "" {
			stream.URL = setStreamTokenV3(stream.URL, fresh)
			return true, true
		}
	}
	// The credential could not be verified, was bound to another resource, or
	// could not be re-signed from its own claims. A session whose identity
	// recipe the server can reconstruct without the token gets a fresh
	// credential; anything else must rebuild.
	card, identityReconstructable := h.identityReconstructableCardV3(session)
	if !identityReconstructable {
		return false, false
	}
	fresh := h.signSessionToken(card, false)
	if fresh == "" {
		return false, false
	}
	stream.URL = setStreamTokenV3(stream.URL, fresh)
	return true, true
}

// identityReconstructableCardV3 reports whether a session's stream credential
// can be re-minted from its own identity, and returns that card. Only direct
// and remux sessions can: a transcode session's recipe lives in the token, not
// in the Session, so it must keep the verified claims it was issued with.
func (h *PlaybackHandler) identityReconstructableCardV3(session *playback.Session) (playback.RecipeCard, bool) {
	if session == nil {
		return playback.RecipeCard{}, false
	}
	switch session.PlayMethod {
	case playback.PlayDirect, playback.PlayRemux:
		return identityRecipeCard(session), true
	default:
		return playback.RecipeCard{}, false
	}
}

// streamTokenFromURLV3 extracts the native signed-credential query parameter
// from a stream URL. An absent or unparseable URL yields "".
func streamTokenFromURLV3(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.Query().Get(streamTokenParam)
}

// setStreamTokenV3 replaces a stream URL's signed-credential query parameter,
// preserving every other query parameter and the URL path.
func setStreamTokenV3(rawURL, token string) string {
	if token == "" {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return appendStreamToken(rawURL, token)
	}
	query := parsed.Query()
	query.Set(streamTokenParam, token)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// reusedHLSTransportV3 reconstructs transport facts for an existing HLS or
// identity (direct-play/progressive-remux) session.
func reusedHLSTransportV3(session *playback.Session, streamURL string) preparedTransportV3 {
	transport := preparedTransportV3{url: streamURL}
	if session != nil {
		transport.routingWorkload = noderouting.Workload(session.RoutingWorkload)
		transport.routingExecution = noderouting.Execution(session.RoutingExecution)
		transport.routingExecutorID = session.RoutingExecutionNodeID
		transport.routingExecutorURL = session.RoutingExecutionNodeURL
		transport.routingEgress = noderouting.Egress(session.RoutingEgress)
		transport.routingEgressID = session.RoutingEgressNodeID
		transport.routingEgressURL = session.RoutingEgressNodeURL
		transport.nodeURL = session.TranscodeNodeURL
		transport.transportID = session.TranscodeTransportID
		transport.hwAccel = session.TranscodeHWAccel
		transport.toneMapMode = session.ToneMapMode
	}
	transport.commit = func() *transportErrorV3 { return nil }
	transport.rollback = func() {}
	return transport
}

// reusedHLSRouteAllowedV3 checks the running transport against the routing
// snapshot for this replan. Soft preferences do not interrupt healthy bytes,
// but a hard execution/egress boundary or client-incompatible proxy route must
// force normal replacement preparation.
func reusedHLSRouteAllowedV3(session *playback.Session, result playback.PlannerResultV3, policy config.PlaybackRoutingPolicy, proxyAllowed bool) bool {
	workload, delivery, ok := routingClassV3(result)
	if !ok || session == nil {
		return false
	}
	compiled, err := noderouting.Candidates(noderouting.Request{
		Workload: workload, Delivery: delivery, Policy: policy, ProxyAllowed: proxyAllowed,
	})
	if err != nil {
		return false
	}
	for _, shape := range compiled.Candidates {
		if shape.Workload == noderouting.Workload(session.RoutingWorkload) &&
			shape.Execution == noderouting.Execution(session.RoutingExecution) &&
			shape.Egress == noderouting.Egress(session.RoutingEgress) {
			return true
		}
	}
	return false
}

func (h *PlaybackHandler) hasActiveReusableTransportV3(session *playback.Session, delivery playback.DeliveryV3) bool {
	if h == nil || session == nil {
		return false
	}
	if session.TranscodeNodeURL != "" {
		return true
	}
	if h.tm.GetTranscodeSession(session.ID) != nil {
		return true
	}
	if delivery == playback.DeliveryRemuxProgressiveV3 {
		// Local progressive remux is served lazily from /stream/{sessionID}
		// and never holds a transcode-manager session. The committed routing
		// facts are its active-transport evidence; remote/proxy progressive
		// already returned above via TranscodeNodeURL.
		return session.RoutingWorkload == string(noderouting.WorkloadRemux) && session.RoutingExecution != ""
	}
	if delivery == playback.DeliveryOriginalHTTPV3 {
		// Direct HTTP is likewise served from the live session by
		// /stream/{sessionID} and holds no transcode-manager session, so the
		// checks above do not cover it: direct play stores any egress proxy in
		// RoutingEgressNodeURL, never TranscodeNodeURL. Committed routing facts
		// are the active-transport evidence. An uncommitted or reconstructed-old
		// session has an empty execution and must rebuild.
		return session.RoutingWorkload == string(noderouting.WorkloadDirectPlay) && session.RoutingExecution != ""
	}
	return false
}

func applySelectedTracksToStartV3(start *playback.StartRequestV3, selected playback.SelectedTracksV3) {
	if start == nil {
		return
	}
	if selected.Audio != nil {
		start.AudioTrackID = selected.Audio.ID
		start.AudioTrackIndex = copyOptionalIntV3(selected.Audio.Index)
	}
	if selected.Subtitle != nil {
		start.SubtitleTrackID = selected.Subtitle.ID
		start.SubtitleTrackIndex = copyOptionalIntV3(selected.Subtitle.Index)
	} else {
		start.SubtitleTrackID = ""
		start.SubtitleTrackIndex = nil
	}
}

// applySelectedTrackOverridesToStartV3 overlays only identities the caller
// actually sent. Replan bodies are intentionally sparse for every operation
// except track_change, so an omitted subtitle here means "unchanged", not
// "off". The exact-replacement helper above remains the authority for an
// explicit track_change and for reconstructing a durable plan selection.
func applySelectedTrackOverridesToStartV3(start *playback.StartRequestV3, selected playback.SelectedTracksV3) {
	if start == nil {
		return
	}
	if selected.Audio != nil {
		start.AudioTrackID = selected.Audio.ID
		start.AudioTrackIndex = copyOptionalIntV3(selected.Audio.Index)
	}
	if selected.Subtitle != nil {
		start.SubtitleTrackID = selected.Subtitle.ID
		start.SubtitleTrackIndex = copyOptionalIntV3(selected.Subtitle.Index)
	}
}

// audioSelectionDiffersFromStartV3 reports whether the replan's audio
// selection names a track other than the start request's. An omitted audio
// identity means "unchanged" — clients may not resend the current track.
func audioSelectionDiffersFromStartV3(selected playback.SelectedTracksV3, start playback.StartRequestV3) bool {
	return selected.Audio != nil &&
		(selected.Audio.ID != start.AudioTrackID || !optionalIntEqualV3(selected.Audio.Index, start.AudioTrackIndex))
}

// subtitleSelectionDiffersFromStartV3 reports whether the replan's subtitle
// selection differs from the start request's. Unlike audio, a nil subtitle is
// an explicit "subtitles off" and counts as a change when one was selected.
func subtitleSelectionDiffersFromStartV3(selected playback.SelectedTracksV3, start playback.StartRequestV3) bool {
	if selected.Subtitle == nil {
		return start.SubtitleTrackIndex != nil
	}
	return selected.Subtitle.ID != start.SubtitleTrackID || !optionalIntEqualV3(selected.Subtitle.Index, start.SubtitleTrackIndex)
}

func sameSelectedTracksV3(left, right playback.SelectedTracksV3) bool {
	return sameTrackIdentityV3(left.Audio, right.Audio) && sameTrackIdentityV3(left.Subtitle, right.Subtitle)
}

func sameTrackIdentityV3(left, right *playback.TrackIdentityV3) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.ID == right.ID && optionalIntEqualV3(left.Index, right.Index)
}

func copyOptionalIntV3(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func optionalPositiveIntV3(value *int) int {
	if value == nil || *value <= 0 {
		return 0
	}
	return *value
}

func bandwidthEstimateRelaxedV3(previous, next *int) bool {
	prev := optionalPositiveIntV3(previous)
	if prev == 0 {
		return false
	}
	curr := optionalPositiveIntV3(next)
	return curr == 0 || curr > prev
}

func qualityIntentChangedV3(req playback.ReplanRequestV3, start playback.StartRequestV3, effectiveFileID, requestedFileID int) bool {
	nextQuality, _ := playback.NormalizeQualityV3(req.QualityPreference)
	if nextQuality != start.QualityPreference ||
		optionalPositiveIntV3(req.BandwidthCapKbps) != optionalPositiveIntV3(start.BandwidthCapKbps) ||
		req.LearnedLimit == playback.LearnedLimitClearV3 {
		return true
	}
	if bandwidthEstimateRelaxedV3(start.BandwidthEstimateKbps, req.BandwidthEstimateKbps) {
		return true
	}
	return nextQuality == playback.QualityAutoV3 &&
		optionalPositiveIntV3(req.BandwidthEstimateKbps) == 0 &&
		effectiveFileID != requestedFileID
}

// lowerVersionV3 finds the version that serves lower qualities of a 4K
// version when 4K transcoding is disabled or the server prefers transcoding a
// lower version: the first non-4K candidate in the alternate-version fallback
// order, the one that fallback adopts when a lower quality refuses the 4K
// source. It reads stored metadata only; the menu is built on every start and
// must not pay for probes. Nil when the policy transcodes the 4K source, the
// request pins its version, or the item has no lower version.
func (h *PlaybackHandler) lowerVersionV3(ctx context.Context, req playback.StartRequestV3, base *models.MediaFile, settings playback.PlannerSettingsV3, filter catalog.AccessFilter) *playback.LowerVersionV3 {
	transcodes4K := settings.Allow4KTranscode && !settings.PreferLowerVersionTranscode
	if base == nil || transcodes4K || !settings.TranscodeEnabled || !req.AllowsAlternateVersions() || !playback.Is4KMediaFileV3(base) {
		return nil
	}
	alternates, err := h.findAlternateFiles(ctx, base, filter)
	if err != nil {
		return nil
	}
	for _, alternate := range alternates {
		// A part of a multi-part version falls back only to the same part.
		if base.PresentationPartTotal > 1 && alternate.PresentationPartIndex != base.PresentationPartIndex {
			continue
		}
		if playback.Is4KMediaFileV3(alternate) {
			// Candidates are ordered non-4K first; the rest are 4K too.
			return nil
		}
		return &playback.LowerVersionV3{
			Requested: playback.SourceDescriptorFromFileV3(base, 0),
			Lower:     playback.SourceDescriptorFromFileV3(alternate, 0),
		}
	}
	return nil
}

// lowerVersionForFileV3 is the lower version for a plan of file. When the
// alternate-version fallback plays a lower version other than the first one,
// that plan's menu describes the version it plays, with the probed metadata
// the fallback just read.
func lowerVersionForFileV3(lower *playback.LowerVersionV3, file *models.MediaFile) *playback.LowerVersionV3 {
	if lower == nil || file == nil || playback.Is4KMediaFileV3(file) {
		return lower
	}
	return &playback.LowerVersionV3{Requested: lower.Requested, Lower: playback.SourceDescriptorFromFileV3(file, 0)}
}

func shouldTryAlternateFileV3(qualityPreference string) bool {
	return !strings.EqualFold(strings.TrimSpace(qualityPreference), "original")
}

const (
	terminalNoAlternateVersionV3            = "no_alternate_version"
	terminalHDRTranscodeUnsupportedV3       = playback.TerminalHDRTranscodeUnsupportedV3
	terminalSubtitleConversionUnsupportedV3 = "subtitle_conversion_unsupported"
	terminalSubtitleUnavailableInVersionV3  = "subtitle_unavailable_in_version"
	terminalBitratePolicyUnavailableV3      = playback.TerminalBitratePolicyUnavailableV3
	// sourceMetadataIncompleteReasonV3 is the planner terminal for a source
	// missing video metadata. A version pick (not a subtitle/audio pick)
	// terminaling on it with a fully-characterized bound sibling falls back
	// through the alternate hunt instead of stranding on terminal.
	sourceMetadataIncompleteReasonV3 = "source_metadata_incomplete"
)

// terminalAllowsAlternateFileV3 reports whether a refusal is the kind another
// version of the same item could satisfy.
//
// subtitle_conversion_unsupported is deliberately absent as a direct trigger.
// It is emitted when a subtitle burn-in is the sole trigger of a video
// adaptation the source cannot take (HDR re-encode, 4K policy, transcoding
// disabled) or when a subtitle simply cannot be delivered, and deselecting the
// subtitle restores playback on the same release. A subtitle problem must never
// move the video candidate, so the reason is routed to the in-place subtitle
// degrade first; only when that same-release degrade also fails does the start
// path re-admit the alternate-version failover, because the failure proves the
// release itself is blocked and the refusal is a video/policy one in disguise.
// The genuine video/policy refusals (HDR pipeline, 4K policy without a subtitle
// trigger, a corrupt source) keep their alternate-version failover directly.
//
// audio_conversion_unsupported is deliberately absent for the same reason as the
// subtitle reason: it names an audio codec or track the available executors
// cannot adapt, not a video stream they cannot play. It is routed to the
// in-place audio resolution (another track in the same file, or the planner's
// own transcode/downmix) before any sibling hunt. The decode reason is only
// video-scoped evidence (videoStreamEvidenceV3); an audio decoder/demux failure
// can no longer produce it.
//
// bitrate_policy_unavailable belongs here for the same reason: it replaces the
// 4K and HDR refusals of a version that exceeds the stream's bitrate limit, and
// a lower-bitrate version may fit that limit.
//
// source_unreadable belongs here because the refusal is about one damaged
// file, not the item: a readable version of the same item still plays.
func terminalAllowsAlternateFileV3(terminal *playback.TerminalV3) bool {
	if terminal == nil {
		return false
	}
	switch terminal.Reason {
	case playback.TerminalAudioConversionUnsupportedV3:
		return false
	case terminalNoAlternateVersionV3, terminalHDRTranscodeUnsupportedV3, terminalBitratePolicyUnavailableV3,
		sourceDecodeFailedReasonV3, playback.TerminalSourceUnreadableV3:
		return true
	default:
		return false
	}
}

// rather than a video/policy one. Such a refusal is first resolved on the
// release already mounted by dropping the subtitle (with the drop stated on the
// plan). On a replan the subtitle terminal then stands unchanged, because the
// playing release is known to be playable. On a fresh start, a same-release
// degrade that also fails proves the release itself is blocked, so the start
// path may still fall back to another version as a video/policy refusal. The
// video-failure reasons keep their existing alternate-file failover.
func subtitleOnlyTerminalV3(terminal *playback.TerminalV3) bool {
	if terminal == nil {
		return false
	}
	return terminal.Reason == terminalSubtitleConversionUnsupportedV3
}

// audioOnlyTerminalV3 reports whether a refusal names an audio problem rather
// than a video/policy one. The planner emits audio_conversion_unsupported when
// the selected audio track needs an adaptation no eligible executor can run.
// That is a statement about the audio stream, not the picture, so the refusal
// is first resolved on the release already mounted by selecting another audio
// track in the same file (the planner already chooses an audio transcode or
// downmix during planning). A track change is the viewer's call, so an explicit
// audio pick is never silently replaced. The video/policy reasons keep their
// alternate-file failover. On a fresh start a same-release resolution that also
// fails proves the release itself is blocked, and only then may the start path
// re-admit the version failover.
func audioOnlyTerminalV3(terminal *playback.TerminalV3) bool {
	if terminal == nil {
		return false
	}
	return terminal.Reason == playback.TerminalAudioConversionUnsupportedV3
}

// audioOnlyTransportReasonV3 reports whether a transport failure names an audio
// condition. Such a failure must stay on the release already mounted: it is not
// evidence the video candidate is bad. The audio recipe a node declined to
// confirm is the audio counterpart of the subtitle-local readiness timeout, and
// audio_transcoding_disabled is a policy statement about the audio adaptation,
// never about the picture.
func audioOnlyTransportReasonV3(reason string) bool {
	switch reason {
	case audioAdaptationFailedReasonV3, "audio_transcoding_disabled":
		return true
	default:
		return false
	}
}

// autoFallbackForSession reads a session's negotiated auto-fallback flag. ok is
// false when the manager does not expose it or the session never set it, so the
// caller keeps its own default.
func autoFallbackForSession(sessionMgr SessionManagerInterface, sessionID string) (enabled bool, ok bool) {
	getter, ok := sessionMgr.(interface {
		AutoFallback(sessionID string) (bool, bool)
	})
	if !ok {
		return false, false
	}
	return getter.AutoFallback(sessionID)
}

// resolveReplanAutoFallbackV3 resolves the attempt's effective version-fallback
// policy for a replan. The live session flag wins while it is set (it carries a
// mid-session re-arm); otherwise the durable normalized request is
// authoritative, so a reconstructed session whose in-memory flag was lost with
// the process keeps the negotiated policy instead of reverting to the start
// request.
func resolveReplanAutoFallbackV3(sessionMgr SessionManagerInterface, sessionID string, record *playback.AttemptRecordV3) bool {
	autoFallback := false
	if record != nil {
		autoFallback = record.NormalizedRequest.AllowsAlternateVersions() &&
			record.NormalizedRequest.FileSelection != playback.FileSelectionExplicitV3
	}
	if negotiated, ok := autoFallbackForSession(sessionMgr, sessionID); ok {
		autoFallback = negotiated
	}
	return autoFallback
}

// errAutoFallbackUnsupportedV3 reports a session manager that cannot negotiate
// auto-fallback. The replan refuses rather than applying a policy the session
// would not honor.
var errAutoFallbackUnsupportedV3 = errors.New("session manager does not expose auto-fallback negotiation")

// renegotiateAutoFallbackV3 applies a replan's auto-fallback intent to the live
// session and the durable attempt record. Clients that start on an explicit
// pick and later re-select Auto from the version menu have no start request to
// carry the intent, so the next replan states it here. Recording it on the
// durable normalized request as well is what lets a reconstructed session keep
// the negotiated policy instead of reverting to the start request. A set
// failure is returned to the caller so the replan fails closed: running with a
// policy the session will not honor, or one that will not survive a
// reconstruction, is worse than a retryable failure. An absent field leaves the
// negotiated intent untouched.
func (h *PlaybackHandler) renegotiateAutoFallbackV3(sessionID string, record *playback.AttemptRecordV3, req playback.ReplanRequestV3) error {
	if req.AutoFallback == nil {
		return nil
	}
	setter, ok := h.sessionMgr.(interface {
		SetAutoFallback(sessionID string, enabled bool) error
	})
	if !ok {
		return errAutoFallbackUnsupportedV3
	}
	if err := setter.SetAutoFallback(sessionID, *req.AutoFallback); err != nil {
		return err
	}
	persistAutoFallbackPolicyV3(&record.NormalizedRequest, *req.AutoFallback)
	return nil
}

// autoFallbackRollbackV3 captures the auto-fallback policy in force before a
// replan speculatively applies a new one. The handler applies the new intent to
// the live session before executeReplanV3 so execution reads the same policy the
// client is showing; if execution then fails, the speculative write must be
// undone. Both halves are restored: the live session's boolean and set-bit (an
// unset session must go back to reporting ok=false, not a spurious explicit
// "off"), and the durable normalized request the record carries, so a terminal
// failure does not persist the policy the failed replan never adopted.
type autoFallbackRollbackV3 struct {
	handler   *PlaybackHandler
	sessionID string
	record    *playback.AttemptRecordV3

	enabled bool
	set     bool

	allowAlternate *bool
	fileSelection  playback.FileSelectionV3
}

// captureAutoFallbackRollbackV3 snapshots the live and durable policy so a
// later failure can restore it. Call it before renegotiateAutoFallbackV3.
func (h *PlaybackHandler) captureAutoFallbackRollbackV3(sessionID string, record *playback.AttemptRecordV3) autoFallbackRollbackV3 {
	rollback := autoFallbackRollbackV3{handler: h, sessionID: sessionID, record: record}
	rollback.enabled, rollback.set = autoFallbackForSession(h.sessionMgr, sessionID)
	if record != nil {
		if record.NormalizedRequest.AllowAlternateVersions != nil {
			allow := *record.NormalizedRequest.AllowAlternateVersions
			rollback.allowAlternate = &allow
		}
		rollback.fileSelection = record.NormalizedRequest.FileSelection
	}
	return rollback
}

// restore puts the captured policy back. The live restore tolerates a session
// that vanished while the replan ran (session_expired) — there is nothing left
// to restore on in that case. The durable restore always applies, because the
// record is a local snapshot the caller may still persist.
func (rb autoFallbackRollbackV3) restore() {
	if rb.record != nil {
		rb.record.NormalizedRequest.AllowAlternateVersions = rb.allowAlternate
		rb.record.NormalizedRequest.FileSelection = rb.fileSelection
	}
	restorer, ok := rb.handler.sessionMgr.(interface {
		RestoreAutoFallback(sessionID string, enabled bool, set bool) error
	})
	if !ok {
		return
	}
	if err := restorer.RestoreAutoFallback(rb.sessionID, rb.enabled, rb.set); err != nil && !errors.Is(err, playback.ErrSessionNotFound) {
		slog.Warn("protocol v3 replan auto-fallback rollback failed", "component", "api", "session", rb.sessionID, "error", err)
	}
}

// persistAutoFallbackPolicyV3 materializes a negotiated version-fallback policy
// into the durable normalized request. The reconstruction read derives fallback
// from the request's selection intent, so enabling clears an explicit pin and
// disabling records one; the explicit boolean is written too, so a reader does
// not have to infer it from the selection when a client sends an explicit
// allow_alternate_versions.
func persistAutoFallbackPolicyV3(req *playback.StartRequestV3, enabled bool) {
	if req == nil {
		return
	}
	allow := enabled
	req.AllowAlternateVersions = &allow
	if enabled {
		req.FileSelection = playback.FileSelectionAutoV3
	} else {
		req.FileSelection = playback.FileSelectionExplicitV3
	}
}

// subtitleRefusalV3 reports a refusal the planner pinned on the selected
// subtitle: a burn-in that was the only reason for an adaptation the file
// cannot take, or a subtitle the fidelity policy cannot serve. The file may
// then play with the subtitle cleared; callers re-plan it to find out.
func subtitleRefusalV3(terminal *playback.TerminalV3) bool {
	return terminal != nil && terminal.Reason == terminalSubtitleConversionUnsupportedV3
}

// planWithoutSubtitleV3 plans input's file again with the selected subtitle
// cleared. It returns nil unless that plan can run: a progressive remux this
// attempt cannot reach must escalate here, because the caller drops a sibling
// that already plays in favor of the result.
func (h *PlaybackHandler) planWithoutSubtitleV3(ctx context.Context, input playback.PlannerInputV3) *alternateCandidateV3 {
	input.Request.SubtitleTrackID, input.Request.SubtitleTrackIndex = "", nil
	result, toneMapErr := h.planPlaybackWithCapabilitiesV3(ctx, input)
	if result.Terminal != nil {
		return nil
	}
	result, escalateErr := h.escalateRefusedProgressiveRemuxV3(ctx, headerAuthenticatedMediaV3(input.Request.ClientFeatures), func() playback.PlannerInputV3 { return input }, result)
	if escalateErr != nil {
		return nil
	}
	return &alternateCandidateV3{file: input.EffectiveFile, request: input.Request, audioIndex: input.AudioTrackIndex, result: result, toneMapErr: toneMapErr}
}

// alternateCandidateV3 is a planned alternate version held back while the
// fallback loop looks for a better one.
type alternateCandidateV3 struct {
	file       *models.MediaFile
	request    playback.StartRequestV3
	audioIndex int
	result     playback.PlannerResultV3
	toneMapErr error
	// substitutionReason names why the returned file is not the one this plan
	// started with, when the alternate-version hunt replaced it. Empty means the
	// file is the entry file (or a caller-supplied substitution whose cause the
	// caller already carries).
	substitutionReason string
}

func replanAllowsAlternateFileV3(operation playback.ReplanOperationV3, qualityPreference string) bool {
	switch operation {
	case playback.ReplanOperationFailureRecoveryV3, playback.ReplanOperationQualityChangeV3, playback.ReplanOperationOutputChangeV3:
		// Quality and output changes can make another version the only viable
		// route. In particular, a bitmap subtitle can require video burn-in
		// that an HDR source cannot support while an SDR alternate can. The
		//
		// A track_change deliberately does NOT fall back to another version:
		// the viewer chose a subtitle or audio track from THIS version's
		// dropdown, which is scoped to the currently playing file. If that
		// track cannot be delivered on this version, the replan refuses with a
		// terminal instead of silently swapping the video underneath the
		// selection — a track that "doesn't work" must not change which
		// version is playing. Only a video that itself failed to load
		// (unreadable/corrupted, surfaced as a failure recovery) can move the
		// version. Seek operations stay pinned to the mounted source.
		return shouldTryAlternateFileV3(qualityPreference)
	default:
		return false
	}
}

func replanAlternateFilePinnedByOriginalQualityV3(operation playback.ReplanOperationV3, qualityPreference string) bool {
	if shouldTryAlternateFileV3(qualityPreference) {
		return false
	}
	return operation == playback.ReplanOperationFailureRecoveryV3 || operation == playback.ReplanOperationQualityChangeV3 || operation == playback.ReplanOperationOutputChangeV3
}

func (h *PlaybackHandler) clarifyOriginalQuality4KTerminalV3(ctx context.Context, filter catalog.AccessFilter, terminal *playback.TerminalV3, requestedFile *models.MediaFile, alternateFilePinned bool) {
	if !alternateFilePinned || terminal == nil || terminal.Reason != terminalNoAlternateVersionV3 || terminal.Message != playback.TerminalMessage4KTranscodeDisabledV3 {
		return
	}
	if alternate, err := h.findAlternateFile(ctx, requestedFile, filter); err == nil && alternate != nil && !playback.Is4KMediaFileV3(alternate) {
		terminal.Message = "4K transcoding is disabled and quality 'original' pins the 4K version; a compatible lower-resolution version of this title is available."
	}
}

// hintExplicitSelectionAlternateAvailableV3 appends a version-list hint to a
// terminal when the viewer explicitly picked a version that cannot be delivered
// but another version of the same title could. The explicit pick is never
// silently swapped (the start-path fallback is gated off), so the client is
// pointed at the version list instead. The terminal reason and retryable flag
// are left untouched.
func hintExplicitSelectionAlternateAvailableV3(terminal *playback.TerminalV3, fileSelection playback.FileSelectionV3) {
	if terminal == nil || fileSelection != playback.FileSelectionExplicitV3 || !terminalAllowsAlternateFileV3(terminal) {
		return
	}
	if terminal.Message != "" {
		terminal.Message += " "
	}
	terminal.Message += "A compatible version of this title is available; choose it from the version list."
}

func (h *PlaybackHandler) lockReplanV3(sessionID string) func() {
	h.v3ReplanMu.Lock()
	if h.v3ReplanLocks == nil {
		h.v3ReplanLocks = make(map[string]*v3ReplanLock)
	}
	entry := h.v3ReplanLocks[sessionID]
	if entry == nil {
		entry = &v3ReplanLock{}
		h.v3ReplanLocks[sessionID] = entry
	}
	entry.refs++
	h.v3ReplanMu.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		h.v3ReplanMu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(h.v3ReplanLocks, sessionID)
		}
		h.v3ReplanMu.Unlock()
	}
}

// maxConcurrentReplansV3 bounds simultaneous replan executions. Each replan
// pins one pooled DB connection for its advisory session lock while issuing
// further store queries from the same pool; without a bound, a recovery storm
// (a transcode node dying with dozens of active sessions) turns every pool
// connection into a lock holder and the inner queries deadlock against them.
const maxConcurrentReplansV3 = 8

// sessionLockCapacityAdvisorV3 lets a plan store cap replan concurrency below
// the fixed default when its own connection budget is smaller; a pool sized at
// or below the default would otherwise let lock holders starve the inner
// store queries that must complete before any lock is released.
type sessionLockCapacityAdvisorV3 interface {
	SessionLockCapacity() int
}

// acquireReplanSlotV3 blocks until a replan slot frees or the request context
// is cancelled; excess replans queue here holding no DB resources at all.
func (h *PlaybackHandler) acquireReplanSlotV3(ctx context.Context) (func(), error) {
	h.v3ReplanSlotsOnce.Do(func() {
		capacity := maxConcurrentReplansV3
		if advisor, ok := h.PlanStoreV3.(sessionLockCapacityAdvisorV3); ok {
			if advised := advisor.SessionLockCapacity(); advised > 0 && advised < capacity {
				capacity = advised
			}
		}
		h.v3ReplanSlots = make(chan struct{}, capacity)
	})
	select {
	case h.v3ReplanSlots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-h.v3ReplanSlots }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (h *PlaybackHandler) HandlePlaybackRouteEventV3(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 || profileID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication and profile are required")
		return
	}
	body, err := readBoundedV3Body(w, r, maxPlaybackV3EventBodyBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid event body")
		return
	}
	var event playback.RouteEventV3
	// Plan summaries are a native /api/v2 feature; the frozen v1 route keeps
	// rejecting them as it did before they existed.
	if err := json.Unmarshal(body, &event); err != nil || !validRouteEventV3(event) || event.Event == playback.RouteEventPlanSummaryV3 {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid route event")
		return
	}
	// The rate limiter runs before the ownership lookup so the per-minute
	// budget bounds the store reads as well as the writes.
	if !h.allowRouteEventV3(userID, event.PlaybackAttemptID) {
		writeError(w, http.StatusTooManyRequests, "event_rate_limited", "Playback route event rate exceeded")
		return
	}
	var identity *playback.AttemptIdentityV3
	var identityErr error
	if event.SessionID != "" {
		identity, identityErr = h.PlanStoreV3.GetAttemptIdentity(r.Context(), event.SessionID)
	} else {
		identity, identityErr = h.PlanStoreV3.GetAttemptIdentityByPlaybackAttemptID(r.Context(), event.PlaybackAttemptID)
	}
	if identityErr != nil {
		// A store outage is not an ownership violation; keep 403 for genuine
		// mismatches so clients stop sending events for foreign sessions.
		if !errors.Is(identityErr, playback.ErrSessionNotFound) {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to authorize the route event")
			return
		}
		writeError(w, http.StatusForbidden, "forbidden", "Route event does not belong to this profile")
		return
	}
	if identity.UserID != userID || identity.ProfileID != profileID ||
		(event.SessionID != "" && identity.PlaybackAttemptID != event.PlaybackAttemptID) ||
		(identity.SessionID == "" && !terminalStartRouteEventV3(event)) {
		writeError(w, http.StatusForbidden, "forbidden", "Route event does not belong to this profile")
		return
	}
	event.Diagnostics = sanitizeDiagnosticsV3(event.Diagnostics)
	client := h.playbackClientInfoWithSessionFallbackV3(firstNonEmptyValue(event.SessionID, identity.SessionID), playbackClientInfoFromRequest(r))
	h.enqueueRouteEventV3(playback.RouteEventRecordV3{RouteEventV3: event, UserID: userID, ProfileID: profileID, ClientName: client.Name, ClientVersion: client.Version, ClientBuild: client.Build, ClientChannel: client.Channel, ClientModel: event.Diagnostics["device_model"]})
	w.WriteHeader(http.StatusAccepted)
}

func terminalStartRouteEventV3(event playback.RouteEventV3) bool {
	return event.Event == playback.RouteEventTerminalV3 &&
		event.SessionID == "" && event.PlanID == "" &&
		event.PlanAttemptID == "" && event.PlanAttemptKey == ""
}

// StartV3Maintenance expires cached signed responses, old telemetry and
// connection observations past their retention on the application lifecycle
// rather than on latency-sensitive playback requests.
func (h *PlaybackHandler) StartV3Maintenance(ctx context.Context) {
	if h == nil || h.PlanStoreV3 == nil || ctx == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				if _, err := h.PlanStoreV3.CleanupExpired(cleanupCtx, now); err != nil {
					slog.Warn("playback v3 cleanup failed", "error", err)
				}
				if err := h.Connections.Cleanup(cleanupCtx); err != nil {
					slog.WarnContext(cleanupCtx, "playback connection retention failed", "error", err)
				}
				cancel()
			}
		}
	}()
}

func (h *PlaybackHandler) allowRouteEventV3(userID int, attemptID string) bool {
	attemptKey := fmt.Sprintf("attempt:%d:%s", userID, attemptID)
	userKey := fmt.Sprintf("user:%d", userID)
	now := time.Now()
	h.v3EventRateMu.Lock()
	defer h.v3EventRateMu.Unlock()
	if h.v3EventRates == nil {
		h.v3EventRates = make(map[string]v3EventRate)
	}
	attemptEntry := h.v3EventRates[attemptKey]
	if attemptEntry.windowStart.IsZero() || now.Sub(attemptEntry.windowStart) >= time.Minute {
		attemptEntry = v3EventRate{windowStart: now}
	}
	userEntry := h.v3EventRates[userKey]
	if userEntry.windowStart.IsZero() || now.Sub(userEntry.windowStart) >= time.Minute {
		userEntry = v3EventRate{windowStart: now}
	}
	if attemptEntry.count >= 120 || userEntry.count >= 600 {
		return false
	}
	attemptEntry.count++
	userEntry.count++
	h.v3EventRates[attemptKey] = attemptEntry
	h.v3EventRates[userKey] = userEntry
	if len(h.v3EventRates) > 10_000 {
		for candidate, value := range h.v3EventRates {
			if now.Sub(value.windowStart) > 2*time.Minute {
				delete(h.v3EventRates, candidate)
			}
		}
	}
	return true
}

func (h *PlaybackHandler) enqueueRouteEventV3(event playback.RouteEventRecordV3) {
	if h == nil || h.PlanStoreV3 == nil {
		return
	}
	h.v3EventOnce.Do(func() {
		h.v3EventQueue = make(chan playback.RouteEventRecordV3, 512)
		// The store is captured with the queue rather than read per event. The
		// goroutine below outlives the request that started it, so re-reading
		// the field would be an unsynchronized read of handler state — harmless
		// in production, where the router wires PlanStoreV3 once before serving,
		// and a real data race against any caller that replaces it afterwards.
		// Capturing also matches what the queue is: work batched for the store
		// that existed when it was created.
		store := h.PlanStoreV3
		connections := h.Connections
		go func() {
			for value := range h.v3EventQueue {
				recordRouteEventV3(store, value, connections)
			}
		}()
	})
	select {
	case h.v3EventQueue <- event:
	default:
		slog.Warn("playback route event dropped", "event", event.Event, "playback_attempt_id", event.PlaybackAttemptID)
	}
}

// planDeliveryLookupTimeout bounds the lookup that labels a plan summary's
// metrics, inside the route event write's own two seconds.
const planDeliveryLookupTimeout = 500 * time.Millisecond

// recordRouteEventV3 writes one queued route event and feeds the metrics
// derived from it; a plan summary also becomes its connection's latest. Only a
// newly inserted row is observed: a v2 retry after a lost 202 reuses its
// event_id and must not be counted twice.
func recordRouteEventV3(store playback.PlanStoreV3, event playback.RouteEventRecordV3, connections *connectionstore.Store) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	inserted, err := store.RecordRouteEvent(ctx, event)
	if err != nil {
		slog.Warn("playback route event write failed", "error", err, "event", event.Event)
		return
	}
	if inserted {
		if lookup, ok := store.(interface {
			PlanDelivery(context.Context, string, string) (string, error)
		}); ok && event.Event == playback.RouteEventPlanSummaryV3 {
			// The delivery only labels the summary's metrics; without it they
			// count under "unknown". It gets a short budget of its own, so a
			// slow lookup cannot starve the summary and sample writes below.
			lookupCtx, cancelLookup := context.WithTimeout(ctx, planDeliveryLookupTimeout)
			event.Delivery, err = lookup.PlanDelivery(lookupCtx, event.PlaybackAttemptID, event.PlanID)
			cancelLookup()
			if err != nil {
				slog.Warn("playback plan delivery lookup failed", "error", err)
			}
		}
		playback.ObserveStoredRouteEvent(event)
		if event.Event == playback.RouteEventPlanSummaryV3 {
			summary := connectionstore.PlanSummary{PlanID: event.PlanID, ReportedAt: event.ReceivedAt, Diagnostics: event.Diagnostics}
			if err := connections.ReportSummary(ctx, event.UserID, event.ConnectionDeviceID, event.ConnectionKey, summary); err != nil {
				slog.Warn("playback connection summary write failed", "error", err)
			}
			if kbps, ok := playbackThroughputKbpsV3(event.Diagnostics); ok {
				if err := connections.ReportSample(ctx, event.UserID, event.ConnectionDeviceID, event.ConnectionKey, kbps, event.SampleLifetime); err != nil {
					slog.Warn("playback connection sample write failed", "error", err)
				}
			}
		}
	}
}

// plannerSettingsV3 reads the live settings used by capability discovery,
// where failures intentionally degrade to an omitted capability in a 200.
func (h *PlaybackHandler) plannerSettingsV3(ctx context.Context) playback.PlannerSettingsV3 {
	settings, _ := h.plannerSettingsV3Result(ctx)
	return settings
}

// plannerSettingsV3Result reads the live settings used for an actual planning
// decision. Callers must not persist a policy terminal when the store is down.
func (h *PlaybackHandler) plannerSettingsV3Result(ctx context.Context) (settings playback.PlannerSettingsV3, err error) {
	cfg := h.playbackConfig()
	settings.TranscodeEnabled = cfg.TranscodeEnabled
	settings.HWAccel = cfg.HWAccel
	viewerTranscodeDisabled := h.viewerTranscodeDisabledV3(ctx)
	defer func() { settings.ViewerTranscodeDisabled = <-viewerTranscodeDisabled }()
	if h.SettingsRepo != nil {
		var values [6]string
		var errs [6]error
		keys := [...]string{
			config.Allow4KTranscodeSettingKey,
			config.PlaybackTranscodeHardwareToneMapSettingKey,
			config.PlaybackTranscodeSoftwareToneMapSettingKey,
			config.PlaybackTranscodeVPPToneMapSettingKey,
			config.PlaybackAllowHEVCEncodingSettingKey,
			config.PlaybackPreferLowerVersionTranscodeSettingKey,
		}
		var group sync.WaitGroup
		group.Add(len(keys))
		for index, key := range keys {
			go func() {
				defer group.Done()
				values[index], errs[index] = h.SettingsRepo.Get(ctx, key)
			}()
		}
		group.Wait()
		if errs[0] != nil {
			return settings, fmt.Errorf("load 4K transcode setting: %w", errs[0])
		}
		if errs[1] != nil {
			return settings, fmt.Errorf("load hardware tone-map setting: %w", errs[1])
		}
		if errs[2] != nil {
			return settings, fmt.Errorf("load software tone-map setting: %w", errs[2])
		}
		if errs[3] != nil {
			return settings, fmt.Errorf("load VPP tone-map setting: %w", errs[3])
		}
		if errs[4] != nil {
			return settings, fmt.Errorf("load HEVC encoding setting: %w", errs[4])
		}
		if errs[4] != nil {
			return settings, fmt.Errorf("load HEVC encoding setting: %w", errs[4])
		}
		if errs[5] != nil {
			return settings, fmt.Errorf("load lower-version transcode setting: %w", errs[5])
		}
		settings.Allow4KTranscode = config.AdminSettingEnabled(keys[0], values[0])
		settings.HardwareToneMapEnabled = config.AdminSettingEnabled(keys[1], values[1])
		settings.SoftwareToneMapEnabled = config.AdminSettingEnabled(keys[2], values[2])
		settings.VPPToneMapEnabled = config.AdminSettingEnabled(keys[3], values[3])
		settings.AllowHEVCEncoding = config.AdminSettingEnabled(keys[4], values[4])
		settings.PreferLowerVersionTranscode = config.AdminSettingEnabled(keys[5], values[5])
	}
	return settings, nil
}

// softwareFallbackAllowedV3 reports whether the live playback policy permits
// an automated software-decode fallback. The value is read live from the
// playback config, so an operator's gpu_only choice takes effect without a
// restart; the empty string is the default "allow".
func (h *PlaybackHandler) softwareFallbackAllowedV3() bool {
	return playback.SoftwareFallbackAllowed(h.playbackConfig().SoftwareFallback)
}

// startupRetryAllowedV3 reports whether the local startup retry may run with
// retryAccel. retryAccel is a CPU decode+encode fallback when it is
// playback.HWAccelNone while the configured accelerator still is a hardware
// one; gpu_only forbids that. A recipe already on HWAccelNone is not falling
// back and keeps its retry. Only VideoToolbox produces the downgrade, and only
// for a non-hardware-tone-map recipe (see playback.StartupRetryHWAccel).
func (h *PlaybackHandler) startupRetryAllowedV3(opts playback.TranscodeOpts, retryAccel string) bool {
	if retryAccel == playback.HWAccelNone && !strings.EqualFold(opts.HWAccel, playback.HWAccelNone) {
		return h.softwareFallbackAllowedV3()
	}
	return true
}

// dropStaleTrackIdentityV3 reports whether trackID is a well-formed selected
// identity of kind bound to a file other than fileID, and logs the discard.
// Virtual candidate rotation replaces media_files rows whenever the provider
// surfaces a new release, so a client replaying a cached selection would
// otherwise fail playback outright; callers drop the selection and fall back to
// the default/preferred-track pipeline instead. A malformed identity or one
// bound to fileID itself is not stale.
func dropStaleTrackIdentityV3(ctx context.Context, kind string, fileID int, trackID string) bool {
	if !playback.StaleTrackIdentityV3(kind, fileID, trackID) {
		return false
	}
	slog.WarnContext(ctx, "stale track identity; discarding selection",
		"component", "playback",
		"kind", kind,
		"sent_track_id", trackID,
		"current_file_id", fileID)
	return true
}

type viewerLimitsReaderV3 interface {
	LimitsForUser(ctx context.Context, userID int) (playback.SessionLimits, error)
}

// viewerTranscodeDisabledV3 looks up, concurrently with the server settings,
// whether the requesting account may start a video transcode. Admission is the
// authority; a failed lookup only leaves the quality ladder advertised.
func (h *PlaybackHandler) viewerTranscodeDisabledV3(ctx context.Context) <-chan bool {
	result := make(chan bool, 1)
	userID := apimw.GetUserID(ctx)
	reader, ok := h.sessionMgr.(viewerLimitsReaderV3)
	if userID <= 0 || !ok {
		result <- false
		return result
	}
	go func() {
		limits, err := reader.LimitsForUser(ctx, userID)
		if err != nil {
			slog.WarnContext(ctx, "load viewer playback limits for planning", "component", "playback", "user_id", userID, "error", err)
		}
		result <- err == nil && limits.TranscodingDisabled
	}()
	return result
}

// dropStaleAudioTrackIdentityV3 reports whether the request's audio track ID
// embeds a file identity that no longer matches file. Callers drop the
// selection and fall back to the preferred-track pipeline instead.
func dropStaleAudioTrackIdentityV3(ctx context.Context, file *models.MediaFile, trackID string) bool {
	if file == nil {
		return false
	}
	return dropStaleTrackIdentityV3(ctx, "audio", file.ID, trackID)
}

// dropStaleRequestTrackIdentitiesV3 drops the start request's selected audio
// and subtitle identities when they name a file other than the requested file,
// logging each discard. It runs before structural validation so a rotated-out
// selection degrades instead of failing the start.
func dropStaleRequestTrackIdentitiesV3(ctx context.Context, req *playback.StartRequestV3) {
	if req == nil {
		return
	}
	if dropStaleTrackIdentityV3(ctx, "audio", req.FileID, req.AudioTrackID) {
		req.AudioTrackID = ""
		req.AudioTrackIndex = nil
	}
	if dropStaleTrackIdentityV3(ctx, "subtitle", req.FileID, req.SubtitleTrackID) {
		req.SubtitleTrackID = ""
		req.SubtitleTrackIndex = nil
	}
}

// resolveV3AudioIndex returns the audio index to play, whether the requested
// selection had to be degraded to get there, and an error only when the request
// itself is malformed.
func resolveV3AudioIndex(file *models.MediaFile, trackID string, fallback *int) (int, bool, error) {
	index := 0
	if trackID != "" {
		fileID, kind, ordinal, ok := playback.ParseTrackIDV3(trackID)
		if !ok || kind != "audio" || file == nil {
			// A malformed identity is a client bug, not a content mismatch.
			// A well-formed identity bound to a different file was already
			// discarded as stale by dropStaleAudioTrackIdentityV3 at the request
			// boundary; anything still mismatched here is a genuine client bug.
			return 0, false, errors.New("selected audio track identity is invalid")
		}
		if fileID != file.ID {
			return 0, false, errors.New("selected audio track identity is invalid")
		}
		index = ordinal
	} else if fallback != nil {
		index = *fallback
	}
	if index < 0 {
		// Request validation rejects this first; a negative index is a client
		// bug, not a track the file lacks.
		return 0, false, errors.New("selected audio track index is invalid")
	}
	if file == nil || len(file.AudioTracks) == 0 {
		return 0, index != 0, nil
	}
	if index < 0 || index >= len(file.AudioTracks) {
		// A carried/restored ordinal can outrun the effective file's track list
		// after a version switch or candidate rotation, and an explicit pick can
		// name a track the effective edition does not have. Both degrade to the
		// file's default track instead of terminalling: playability wins, and the
		// viewer can re-pick from the audio menu.
		fallbackIndex := normalizeAudioTrackIndex(file, index)
		slog.Info("audio selection out of range for effective file; using default track",
			"component", "playback", "file_id", file.ID, "requested_index", index, "resolved_index", fallbackIndex)
		return fallbackIndex, true, nil
	}
	return index, false, nil
}

// remapAudioIndexV3 rebinds an audio ordinal chosen on source to the closest
// track on target. An ordinal means nothing on another file: the same
// position can hold another language once codecs, layouts or order differ.
func remapAudioIndexV3(source, target *models.MediaFile, index int) int {
	if source == nil || target == nil {
		return normalizeAudioTrackIndex(target, index)
	}
	return playback.MatchAudioTrackAcrossVersions(source.AudioTracks, target.AudioTracks, index)
}

// remapAudioSelectionV3 rebinds the request's audio selection when the
// effective media file changes. ID-only selections are equally file-bound:
// the stale ID would be rejected against the new file's track list
// downstream, so derive the source index from it and remap like any other.
//
// Identity alone is not enough. A virtual candidate rotation can re-probe the
// same catalog row in place (same id, different underlying release), so the
// guard also compares the audio inventory fingerprint: when it differs, the
// carried ordinal names a different track and must be remapped by
// codec/language family exactly like an edition switch.
func remapAudioSelectionV3(source, target *models.MediaFile, request *playback.StartRequestV3) error {
	if request == nil || source == nil || target == nil {
		return nil
	}
	if source.ID == target.ID && playback.AudioLayoutsEqual(source.AudioTracks, target.AudioTracks) {
		return nil
	}
	if request.AudioTrackIndex == nil {
		if request.AudioTrackID == "" {
			return nil
		}
		_, kind, ordinal, ok := playback.ParseTrackIDV3(request.AudioTrackID)
		if !ok || kind != "audio" {
			return errors.New("selected audio track identity is invalid for the source file")
		}
		// A stale file identity (virtual candidate rotation) still carries a
		// meaningful ordinal; the semantic remap below finds the equivalent
		// track on the target regardless of which row the ID referenced.
		request.AudioTrackIndex = &ordinal
	}
	remapped := remapAudioIndexV3(source, target, *request.AudioTrackIndex)
	request.AudioTrackIndex = &remapped
	request.AudioTrackID = playback.TrackIDV3(target.ID, "audio", remapped)
	return nil
}

// carriedAudioSourceV3 loads the inventory the carried audio identity was
// minted against. The identity is file-bound: its embedded row is the effective
// row the previous plan selected on, which is the inventory the ordinal names.
// Evidence is therefore keyed by that source row, not by the target, because a
// version switch has a different target by definition and keying on it would
// never find the evidence that makes the carry safe. A predecessor attempt
// names the evidence durably; otherwise the uniquely matching live session on
// this replica carries it. Evidence from either source is what lets a same-row
// id be remapped when enrichment re-probed that row in place (same id, different
// release) instead of trusting a stale ordinal.
func (h *PlaybackHandler) carriedAudioSourceV3(ctx context.Context, req *playback.StartRequestV3, target *models.MediaFile, userID int, profileID, clientIP, userAgent string) *models.MediaFile {
	if req == nil || target == nil {
		return nil
	}
	srcID, kind, _, ok := playback.ParseTrackIDV3(strings.TrimSpace(req.CarriedAudioTrackID))
	if !ok || kind != "audio" || srcID <= 0 {
		return nil
	}
	if predecessorID := strings.TrimSpace(req.PreviousPlaybackAttemptID); predecessorID != "" && h.PlanStoreV3 != nil {
		record, err := h.PlanStoreV3.GetAttemptByPlaybackAttemptID(ctx, predecessorID)
		if err == nil && record != nil && record.UserID == userID && record.ProfileID == profileID &&
			record.EffectiveMediaFileID == srcID {
			source := &models.MediaFile{ID: srcID, AudioTracks: audioTracksFromInventoryV3(record.CurrentPlan.AudioTracks)}
			if len(source.AudioTracks) > 0 {
				return source
			}
		}
	}
	lister, ok := h.sessionMgr.(interface {
		GetUserSessions(userID int) []*playback.Session
	})
	if !ok {
		return nil
	}
	previous := findCarryCandidateSessionV3(lister.GetUserSessions(userID), profileID, srcID, clientIP, userAgent)
	if previous == nil || previous.MediaFileID != srcID {
		// A session matched only through its requested row carries the effective
		// candidate's tracks, which are not the inventory srcID names. Leave the
		// catalog row to resolve it rather than mislabeling another layout.
		return nil
	}
	source := &models.MediaFile{ID: srcID, AudioTracks: h.previousAudioTracksV3(ctx, previous)}
	if len(source.AudioTracks) == 0 {
		return nil
	}
	return source
}

// resolveCarriedAudioTrackV3 re-resolves the viewer's audio selection from a
// previous version onto the requested file. The carried identity is file-bound,
// so its ordinal is remapped by codec/language family (MatchAudioTrackAcrossVersions),
// not by raw position, which is unstable across encodes. A same-row id is only
// reused directly when the source evidence still describes the target's layout:
// enrichment can re-probe the row in place, and row-id equality alone would then
// apply the old ordinal to a different release. A remapped index is only a hit
// when the matched track still carries a concrete language the selection named.
// MatchAudioTrackAcrossVersions deliberately falls back to the target's default
// when no equivalent exists; reporting that as a successful carry would play
// another language under the viewer's explicit choice with no degradation
// warning. Falls back (ok=false) to the server's preference when the source
// version is gone, the identity cannot be parsed, or no equivalent exists; the
// caller keeps its already-resolved index, so a stale carried selection degrades
// rather than terminating.
func (h *PlaybackHandler) resolveCarriedAudioTrackV3(ctx context.Context, carriedID string, target *models.MediaFile, source *models.MediaFile) (int, bool) {
	srcID, kind, ordinal, ok := playback.ParseTrackIDV3(carriedID)
	if !ok || kind != "audio" || target == nil {
		slog.InfoContext(ctx, "carried audio selection is invalid for the effective file; using resolved track",
			"component", "playback", "carried_track_id", carriedID)
		return 0, false
	}
	if source != nil && source.ID == srcID && len(source.AudioTracks) > 0 {
		if srcID == target.ID && playback.AudioLayoutsEqual(source.AudioTracks, target.AudioTracks) {
			return normalizeAudioTrackIndex(target, ordinal), true
		}
		return carriedAudioMatchV3(ctx, source.AudioTracks, target, ordinal, srcID)
	}
	if srcID == target.ID {
		// No generation evidence: keep the legacy ordinal reuse.
		return normalizeAudioTrackIndex(target, ordinal), true
	}
	if h.fileResolver == nil {
		slog.InfoContext(ctx, "carried audio source version is unavailable; using resolved track",
			"component", "playback", "carried_file_id", srcID, "target_file_id", target.ID)
		return 0, false
	}
	sourceRow, err := h.fileResolver.GetByID(ctx, srcID)
	if err != nil || sourceRow == nil {
		slog.InfoContext(ctx, "carried audio source version could not be loaded; using resolved track",
			"component", "playback", "carried_file_id", srcID, "target_file_id", target.ID)
		return 0, false
	}
	return carriedAudioMatchV3(ctx, sourceRow.AudioTracks, target, ordinal, srcID)
}

// carriedAudioMatchV3 maps a carried ordinal onto target and verifies the result
// still names the selection's concrete language. An out-of-range ordinal or an
// unverifiable target inventory is a miss, not a silent default.
func carriedAudioMatchV3(ctx context.Context, source []models.AudioTrack, target *models.MediaFile, ordinal, srcID int) (int, bool) {
	if ordinal < 0 || ordinal >= len(source) || target == nil || len(target.AudioTracks) == 0 {
		slog.InfoContext(ctx, "carried audio selection has no equivalent on the effective file; using resolved track",
			"component", "playback", "carried_file_id", srcID, "target_file_id", target.ID, "carried_audio_index", ordinal)
		return 0, false
	}
	index := playback.MatchAudioTrackAcrossVersions(source, target.AudioTracks, ordinal)
	if index < 0 || index >= len(target.AudioTracks) ||
		!fallbackAudioCarryMatchesV3(source[ordinal], target.AudioTracks[index]) {
		slog.InfoContext(ctx, "carried audio selection has no equivalent on the effective file; using resolved track",
			"component", "playback", "carried_file_id", srcID, "target_file_id", target.ID,
			"carried_audio_index", ordinal, "matched_audio_index", index)
		return 0, false
	}
	return index, true
}

// errSubtitleUnavailableInTargetV3 reports that the target media file has no
// equivalent of the selected subtitle track. It is deliberately non-terminal:
// a caller iterating candidates keeps hunting for one that honors the
// selection and only degrades to subtitles-off once every candidate has been
// tried, so a stale carried selection never terminates playback. Malformed
// subtitle identities use a plain error and still terminate.
var errSubtitleUnavailableInTargetV3 = errors.New("no equivalent subtitle track in the target file version")

func (h *PlaybackHandler) remapSubtitleSelectionV3(ctx context.Context, source, target *models.MediaFile, request *playback.StartRequestV3) (bool, error) {
	return h.remapSubtitleSelectionWithCarryV3(ctx, carriedInventorySourceV3{file: source}, target, request)
}

// downloadedSelectionCarryErrorV3 fails closed for a carried downloaded
// ordinal the carry cannot place. A dependency failure while recovering the
// captured tail surfaces as a store error so the caller can refuse the replan
// as retryable; a historical carry with no tail reports the track unavailable
// instead of reusing the ordinal positionally against whatever row now occupies
// it.
func downloadedSelectionCarryErrorV3(carry carriedInventorySourceV3, request *playback.StartRequestV3) error {
	if !carry.namesDownloadedSubtitleV3(request) {
		return nil
	}
	if carry.subtitleEvidenceErr != nil {
		return fmt.Errorf("load downloaded subtitles: %w", carry.subtitleEvidenceErr)
	}
	if carry.subtitleHistorical && !carry.downloadedKnown {
		return errSubtitleUnavailableInTargetV3
	}
	return nil
}

// sameInventorySubtitleSelectionSettledV3 reports whether a selection needs no
// remap because the source and target are the same row with identical
// file-shaped subtitle inventories. A carried downloaded ordinal is re-checked
// against the captured rows first: that tail is invisible to the file-shaped
// comparison, so a changed tail still needs the remap below.
func (h *PlaybackHandler) sameInventorySubtitleSelectionSettledV3(ctx context.Context, carry carriedInventorySourceV3, target *models.MediaFile, request *playback.StartRequestV3) (bool, error) {
	if carry.file == nil || target == nil || carry.file.ID != target.ID || !sameSubtitleInventoryV3(carry.file, target) {
		return false, nil
	}
	if !carry.namesDownloadedSubtitleV3(request) {
		return true, nil
	}
	changed, err := h.downloadedTailChangedV3(ctx, carry, target)
	if err != nil {
		return false, fmt.Errorf("load downloaded subtitles: %w", err)
	}
	return !changed, nil
}

// remapSubtitleSelectionWithCarryV3 is remapSubtitleSelectionV3 with the
// inventory's captured downloaded tail. That segment has no models.MediaFile
// representation, so a file-shaped source cannot prove an ordinal in it still
// names the same downloaded row; the carry can.
func (h *PlaybackHandler) remapSubtitleSelectionWithCarryV3(ctx context.Context, carry carriedInventorySourceV3, target *models.MediaFile, request *playback.StartRequestV3) (bool, error) {
	source := carry.file
	if request == nil || source == nil || target == nil {
		return false, nil
	}
	// Explicit off (subtitle_track_index = -1) names no track, so there is
	// nothing to remap and the intent must survive a version change: a client
	// that asked for subtitles off stays off rather than having the negative
	// sentinel reinterpreted against the target's inventory.
	if request.SubtitleTrackIndex != nil && *request.SubtitleTrackIndex == playback.SubtitleTrackOffIndexV3 {
		return false, nil
	}
	// A carried downloaded ordinal is only meaningful with the tail it was
	// minted against. A historical carry that never recovered one cannot prove
	// which row the ordinal names today, and a dependency failure while
	// recovering it says nothing about whether the track exists. Both must fail
	// closed here, before the same-id fast path can reuse the position.
	if err := downloadedSelectionCarryErrorV3(carry, request); err != nil {
		return false, err
	}
	// A same-id pairing is only a no-op when the subtitle inventory is
	// unchanged. A virtual candidate rotation re-probes the catalog row in
	// place (same id, different release), so a fingerprint match — not id
	// equality — decides whether the carried ordinal still names the same
	// track. When the inventory differs, the selection remaps by
	// language/format/forced/HI exactly like an edition switch. The downloaded
	// tail is not part of that fingerprint, so a carried downloaded ordinal is
	// re-checked against the captured rows even when the file-shaped
	// inventories compare equal.
	if settled, err := h.sameInventorySubtitleSelectionSettledV3(ctx, carry, target, request); err != nil {
		return false, err
	} else if settled {
		return false, nil
	}
	// A source with no subtitle inventory has nothing to remap FROM. That is
	// either a virtual parent row (virtual://movie/ttNNNN with no result= param)
	// — a catalog placeholder with no probed streams — or captured-empty
	// plan-time evidence, where the candidate genuinely had no subtitles. In
	// both cases any selection the client made names no track on this
	// inventory: clear the stale selection and let the target file's own
	// default apply, rather than reinterpreting the ordinal against the
	// replacement and inventing a selection the viewer never made. Mirrors
	// resolveV3AudioIndex's graceful empty-tracks fallback.
	if len(source.ExternalSubtitles) == 0 && len(source.SubtitleTracks) == 0 {
		// Without a selection there is nothing to reinterpret or drop: a
		// version change must stay a no-op for subtitles-off playback.
		if request.SubtitleTrackIndex == nil && strings.TrimSpace(request.SubtitleTrackID) == "" {
			return false, nil
		}
		noDownloaded := h.SubtitleRepo == nil
		if carry.downloadedKnown {
			// The captured tail is the authority for an inventory the file
			// cannot describe; the live list may have changed since it was
			// published.
			noDownloaded = len(carry.downloaded) == 0
		} else if !noDownloaded {
			downloaded, err := h.SubtitleRepo.ListDownloadedSubtitles(ctx, source.ID)
			if err != nil {
				// A failed lookup says nothing about whether the track exists;
				// dropping the selection here would store subtitles-off for the
				// rest of the session over a transient error.
				return false, fmt.Errorf("load downloaded subtitles: %w", wrapSubtitleStoreErrorV3(err))
			}
			noDownloaded = len(downloaded) == 0
		}
		if noDownloaded {
			request.SubtitleTrackIndex = nil
			request.SubtitleTrackID = ""
			return true, nil
		}
	}
	if request.SubtitleTrackIndex == nil {
		// ID-only selections are equally file-bound: the stale ID would be
		// parsed against the alternate file's track list downstream, so
		// derive the source index from it and remap like any other.
		if request.SubtitleTrackID == "" {
			return false, nil
		}
		_, kind, ordinal, ok := playback.ParseTrackIDV3(request.SubtitleTrackID)
		if !ok || kind != subtitleTrackKindV3 {
			return false, errors.New("selected subtitle track identity is invalid for the source file")
		}
		// Stale identities from candidate rotation still carry a usable
		// ordinal; the language/format matching below finds the equivalent
		// track on the target.
		request.SubtitleTrackIndex = &ordinal
	}
	index := *request.SubtitleTrackIndex
	if index < 0 {
		return false, errors.New("selected subtitle track index is invalid")
	}
	// index is a published combined ordinal. classify maps it to the source
	// segment and segment-local track, and the equivalent target track is
	// translated back to the target's published ordinal before it is written:
	// the target's own published base can differ from its source array sizes
	// once de-duplication suppressed a track, and downloaded entries follow
	// that published base.
	sourceLocation, classified := classifySubtitleIndexV3(source, index)
	if !classified {
		return false, errSubtitleUnavailableInTargetV3
	}
	targetIndex := -1
	// The selection's identity, for the cross-format fallback below. Downloaded
	// subtitles carry no forced/SDH flags, so they only match exactly.
	var wantLanguage, wantTitle string
	var wantForced, wantHearingImpaired bool
	switch sourceLocation.source {
	case playback.SubtitleSourceExternalV3:
		targetIndex, wantLanguage, wantTitle, wantForced, wantHearingImpaired = remapExternalSubtitleIndexV3(source, target, sourceLocation)
	case playback.SubtitleSourceEmbeddedV3:
		targetIndex, wantLanguage, wantTitle, wantForced, wantHearingImpaired = remapEmbeddedSubtitleIndexV3(source, target, sourceLocation)
	default:
		var remapErr error
		targetIndex, remapErr = h.remapDownloadedSubtitleIndexV3(ctx, carry, target, sourceLocation.offset)
		if remapErr != nil {
			return false, remapErr
		}
	}
	if targetIndex < 0 && wantLanguage != "" {
		// Editions often carry the same subtitle in different formats (PGS on
		// an HDR remux, SRT on an SDR encode). Keep the viewer's language and
		// forced/SDH variant in whatever format the effective file has.
		targetIndex = subtitleVariantIndexV3(target, wantLanguage, wantTitle, wantForced, wantHearingImpaired)
	}
	if targetIndex < 0 {
		// No equivalent track exists on the target version. Preserve the
		// selection and report a non-terminal miss: a caller iterating
		// candidates keeps hunting for one that can honor it, and only degrades
		// to subtitles-off once every candidate has been tried. Clearing the
		// selection here would accept the first candidate that cannot honor an
		// explicit user choice even when a later version can.
		slog.InfoContext(ctx, "no equivalent subtitle track in effective file; trying other candidates",
			"component", "playback", "source_file_id", source.ID, "target_file_id", target.ID,
			"subtitle_track_index", index)
		return false, errSubtitleUnavailableInTargetV3
	}
	request.SubtitleTrackIndex = &targetIndex
	request.SubtitleTrackID = playback.TrackIDV3(target.ID, "subtitle", targetIndex)
	return false, nil
}

// remapExternalSubtitleIndexV3 returns the target's published ordinal for the
// external track location names, plus the selection identity the cross-format
// fallback needs. Language/format/flags can name several physical tracks (two
// sidecars for one language). Prefer the one whose displayed title the viewer
// selected; otherwise keep the first identity match as the historical fallback
// rather than reporting a miss.
func remapExternalSubtitleIndexV3(source, target *models.MediaFile, location subtitleIndexLocationV3) (targetIndex int, language, title string, forced, hearingImpaired bool) {
	wanted := source.ExternalSubtitles[location.offset]
	language = wanted.Language
	title = displayedSubtitleTitleV3(wanted.Title, wanted.EmbeddedTitle)
	forced, hearingImpaired = wanted.Forced, wanted.HearingImpaired
	fallback := -1
	for candidateIndex, candidate := range target.ExternalSubtitles {
		if !strings.EqualFold(candidate.Language, wanted.Language) || !strings.EqualFold(candidate.Format, wanted.Format) || candidate.Forced != wanted.Forced || candidate.HearingImpaired != wanted.HearingImpaired {
			continue
		}
		published, mapped := playback.SubtitleInventoryOwnPublishedIndexV3(target, candidateIndex)
		if !mapped {
			continue
		}
		if subtitleRemapTitleMatchesV3(title, displayedSubtitleTitleV3(candidate.Title, candidate.EmbeddedTitle)) {
			return published, language, title, forced, hearingImpaired
		}
		if fallback < 0 {
			fallback = published
		}
	}
	return fallback, language, title, forced, hearingImpaired
}

// remapEmbeddedSubtitleIndexV3 is the embedded counterpart of
// remapExternalSubtitleIndexV3.
func remapEmbeddedSubtitleIndexV3(source, target *models.MediaFile, location subtitleIndexLocationV3) (targetIndex int, language, title string, forced, hearingImpaired bool) {
	wanted := source.SubtitleTracks[location.offset]
	language = wanted.Language
	title = displayedSubtitleTitleV3(wanted.Title, wanted.EmbeddedTitle)
	forced, hearingImpaired = wanted.Forced, wanted.HearingImpaired
	fallback := -1
	for candidateIndex, candidate := range target.SubtitleTracks {
		if !strings.EqualFold(candidate.Language, wanted.Language) || !strings.EqualFold(candidate.Codec, wanted.Codec) || candidate.Forced != wanted.Forced || candidate.HearingImpaired != wanted.HearingImpaired {
			continue
		}
		published, mapped := playback.SubtitleInventoryOwnPublishedIndexV3(target, len(target.ExternalSubtitles)+candidateIndex)
		if !mapped {
			continue
		}
		if subtitleRemapTitleMatchesV3(title, displayedSubtitleTitleV3(candidate.Title, candidate.EmbeddedTitle)) {
			return published, language, title, forced, hearingImpaired
		}
		if fallback < 0 {
			fallback = published
		}
	}
	return fallback, language, title, forced, hearingImpaired
}

// remapDownloadedSubtitleIndexV3 maps a downloaded-subtitle ordinal from the
// carry source onto target and returns the target's published combined ordinal,
// or -1 when no equivalent exists. The published position is only a menu
// ordinal: a row added, removed, or reordered ahead of it moves every ordinal
// after it without changing the row the viewer picked, so a captured inventory
// is resolved by its stable subtitle row instead.
func (h *PlaybackHandler) remapDownloadedSubtitleIndexV3(ctx context.Context, carry carriedInventorySourceV3, target *models.MediaFile, downloadedIndex int) (int, error) {
	if downloadedIndex < 0 {
		return -1, nil
	}
	if carry.downloadedKnown {
		return h.capturedDownloadedSubtitleIndexV3(ctx, carry, target, downloadedIndex)
	}
	// Evidence that predates the captured tail falls back to the historic match
	// against the live repository lists of the two rows.
	return h.remapDownloadedSubtitleFromLiveSourceV3(ctx, carry.file, target, downloadedIndex)
}

// capturedDownloadedSubtitleIndexV3 resolves a downloaded ordinal against the
// stable subtitle row id the plan published, not against whatever occupies its
// old position today.
func (h *PlaybackHandler) capturedDownloadedSubtitleIndexV3(ctx context.Context, carry carriedInventorySourceV3, target *models.MediaFile, downloadedIndex int) (int, error) {
	if downloadedIndex >= len(carry.downloaded) {
		return -1, nil
	}
	id := downloadedSubtitleCarryIDV3(carry.downloaded[downloadedIndex])
	if id <= 0 {
		// The captured item carries no durable identity (it predates the row id
		// or its format has no sync key). The live source row is then the only
		// identity available.
		return h.remapDownloadedSubtitleFromLiveSourceV3(ctx, carry.file, target, downloadedIndex)
	}
	targetDownloaded, err := h.listDownloadedSubtitlesV3(ctx, target)
	if err != nil {
		return -1, fmt.Errorf("load downloaded subtitles: %w", err)
	}
	base := len(playback.BuildSubtitleInventoryV3(target, nil))
	for candidateIndex, candidate := range targetDownloaded {
		if candidate.DownloadedSubtitleID == id {
			return base + candidateIndex, nil
		}
	}
	// A downloaded row is file-bound, so a version switch never carries the id
	// itself. Keep the viewer's language and format then — never the old
	// position, which is what a reordered list rewrites onto another row.
	wanted := carry.downloaded[downloadedIndex]
	for candidateIndex, candidate := range targetDownloaded {
		if strings.EqualFold(candidate.Language, wanted.Language) && strings.EqualFold(candidate.Codec, wanted.Codec) {
			return base + candidateIndex, nil
		}
	}
	return -1, nil
}

// remapDownloadedSubtitleFromLiveSourceV3 is the historic downloaded-subtitle
// identity match against the repository lists of source and target. Virtual
// rows persist release_name as "" (see ReplaceVirtualCandidates and
// upsertVirtualFileVariant), so two same-language/format downloaded tracks are
// indistinguishable by ReleaseName and a first-match scan would collapse them
// onto one target row; the row id is resolved first, and the
// language/format/ReleaseName match remains for rows that carry no id.
func (h *PlaybackHandler) remapDownloadedSubtitleFromLiveSourceV3(ctx context.Context, source, target *models.MediaFile, downloadedIndex int) (int, error) {
	if h.SubtitleRepo == nil || source == nil || target == nil {
		return -1, nil
	}
	sourceDownloaded, sourceErr := h.SubtitleRepo.ListDownloadedSubtitles(ctx, source.ID)
	targetDownloaded, targetErr := h.SubtitleRepo.ListDownloadedSubtitles(ctx, target.ID)
	if err := errors.Join(sourceErr, targetErr); err != nil {
		// A failed lookup says nothing about whether the track exists;
		// dropping the selection here would store subtitles-off for the
		// rest of the session over a transient error.
		return -1, fmt.Errorf("load downloaded subtitles: %w", wrapSubtitleStoreErrorV3(err))
	}
	if downloadedIndex >= len(sourceDownloaded) {
		return -1, nil
	}
	wanted := sourceDownloaded[downloadedIndex]
	base := len(playback.BuildSubtitleInventoryV3(target, nil))
	if wanted.ID > 0 {
		for candidateIndex, candidate := range targetDownloaded {
			if candidate.ID == wanted.ID {
				return base + candidateIndex, nil
			}
		}
	}
	for candidateIndex, candidate := range targetDownloaded {
		if strings.EqualFold(candidate.Language, wanted.Language) && strings.EqualFold(string(candidate.Format), string(wanted.Format)) && strings.EqualFold(candidate.ReleaseName, wanted.ReleaseName) {
			return base + candidateIndex, nil
		}
	}
	return -1, nil
}

// transportFailureClassificationsV3 are the client failure classifications
// that mean the route itself did not deliver bytes the device could render —
// as opposed to benign signals (quality_changed, track changes) that leave
// the previous route fully eligible. The vocabulary is the client failure
// taxonomy; prod route events name decoder_failure, player_failure, and
// http_failure as the observed direct-play failure classes.
// audio_renderer_error is deliberately excluded: a client-local audio hiccup
// does not indict the video route, and demoting on it would retire the only
// viable delivery for a session that is otherwise direct-playing fine.
// Unclassified failures do NOT demote either: several benign recovery paths
// send no classification, and demoting on those would retire the only viable
// route for a healthy stream.
var transportFailureClassificationsV3 = map[string]bool{
	"decoder_failure": true,
	"decode_error":    true,
	"player_failure":  true,
	"http_failure":    true,
	"parser_failure":  true,
}

// failureClassificationKeyV3 lowercases and trims a client-reported failure
// classification so the transport-failure lookup is spelling-insensitive.
func failureClassificationKeyV3(classification string) string {
	return strings.ToLower(strings.TrimSpace(classification))
}

// connectivityFailureClassificationsV3 use named constants so the repeated
// client-reported strings do not trip the repeated-string lint.
const (
	connectivityTransportStallV3 = "transport_stall"
	connectivityNetworkErrorV3   = "network_error"
	connectivityNetworkFailureV3 = "network_failure"
)

// connectivityFailureClassificationsV3 are client classifications that mean
// the network did not deliver bytes, so they do not indict the recipe. The
// protocol requires the client to reconnect rather than report these through
// failure_recovery (§6.2), but a client that reports one anyway must not spend
// the route ladder on a transient loss.
var connectivityFailureClassificationsV3 = map[string]bool{
	connectivityTransportStallV3: true,
	connectivityNetworkErrorV3:   true,
	connectivityNetworkFailureV3: true,
}

func connectivityFailureClassificationV3(classification string) bool {
	return connectivityFailureClassificationsV3[failureClassificationKeyV3(classification)]
}

// generationFenceClassificationV3 is the client classification for a segment
// URL the server answered with 412 stale_generation. The generation moved
// under a request that named the displaced one; nothing about the recipe that
// produced it changed.
const generationFenceClassificationV3 = "stale_generation"

// generationFenceFailureV3 reports whether a replan reports the segment
// layer's generation fence. A fence must never be spent as a failed attempt:
// the recipe is healthy, and retiring its key is what terminals a session at
// adaptation_exhausted while the same route could serve the rebuilt
// generation.
func generationFenceFailureV3(classification string) bool {
	return failureClassificationKeyV3(classification) == generationFenceClassificationV3
}

// failureRecoveryAbandonedDeliveryV3 reports whether this replan is a failure
// recovery (or its seek-scoped variant) that abandoned the current plan's
// delivery with an explicit transport-failure classification — the moment
// the attempt learns, durably, that the delivery failed.
func failureRecoveryAbandonedDeliveryV3(operation playback.ReplanOperationV3, failureClassification string) bool {
	if !failureRecoveryOperationV3(operation) {
		return false
	}
	return transportFailureClassificationsV3[failureClassificationKeyV3(failureClassification)]
}

// failureRecoveryOperationV3 reports whether operation is a failure recovery or
// its seek-scoped variant: a replan that exists because the current route
// failed, as opposed to a user-intent change or an automatic correction.
func failureRecoveryOperationV3(operation playback.ReplanOperationV3) bool {
	return operation == playback.ReplanOperationFailureRecoveryV3 ||
		operation == playback.ReplanOperationSeekFailureRecoveryV3
}

// virtualReplanNeedsFreshProviderListingV3 reports whether a session-bound
// virtual replan must ask the provider for a new listing instead of reusing a
// fresh resolved source. Only a cause that indicts the source qualifies: a
// confirmed decode rejection, a seek failure recovery, or a failure recovery
// that is not a track change reported as one. A user-intent replan (track,
// quality, or output change, and a seek reanchor) re-selects within the release
// already playing, so the resolver's fresh-serve floor and candidate TTL are the
// right reuse: forcing a listing there paid a multi-second provider round-trip
// per replan for an answer nothing had invalidated. A source that actually moved
// or stopped serving still re-lists on the failure path below, so reuse never
// keeps a dead release pinned.
func virtualReplanNeedsFreshProviderListingV3(operation playback.ReplanOperationV3, failureClassification string, decodeRotation bool) bool {
	if decodeRotation {
		return true
	}
	// A generation fence says the segment URL is stale, not the source: the
	// provider listing that served this route is still the right one.
	if generationFenceFailureV3(failureClassification) {
		return false
	}
	switch operation {
	case playback.ReplanOperationSeekFailureRecoveryV3:
		return true
	case playback.ReplanOperationFailureRecoveryV3:
		key := failureClassificationKeyV3(failureClassification)
		return key != failureSubtitleTrackChangedV3 && key != failureAudioTrackChangedV3
	default:
		return false
	}
}

// abandonedBeforeFirstSegmentV3 reports whether the live transcode session for
// this attempt served the playlist and never one segment request. See
// playback.TranscodeSession.AbandonedBeforeFirstSegment: the session carries
// the evidence, this is only the handler-side lookup. A session with no local
// transcode handle (direct play, or a route this replica does not own) reports
// false and keeps the ordinary failure handling.
func (h *PlaybackHandler) abandonedBeforeFirstSegmentV3(sessionID string) bool {
	ts := h.tm.GetTranscodeSession(sessionID)
	return ts != nil && ts.AbandonedBeforeFirstSegment()
}

// clientCanceledRecoveryWindow bounds how long after an observed transport
// cancel a failure_recovery that still names the same route is reported as a
// zombie candidate. It only colors the diagnostic: a cancel is never proof the
// session died, so it does not fence the recovery. The recovery path still
// drops a mark a committed successor has superseded.
const clientCanceledRecoveryWindow = 90 * time.Second

// sessionClientCanceledRecentlyV3 reports whether a transport cancel was
// observed for the session inside the diagnostic window. A zero cancel time
// means no cancel was recorded.
func sessionClientCanceledRecentlyV3(canceledAt, now time.Time) bool {
	if canceledAt.IsZero() {
		return false
	}
	age := now.Sub(canceledAt)
	return age >= 0 && age <= clientCanceledRecoveryWindow
}

// planHasVideoEncodeV3 reports whether a plan carries a real video encode.
// Only such a plan has a decode-mode dimension: a copy/remux route cannot
// recover from a decoder failure by changing decode mode.
func planHasVideoEncodeV3(plan playback.PlanV3) bool {
	for _, transformation := range plan.Transformations {
		switch transformation.Name {
		case playback.TransformationVideoToH264V3, playback.TransformationVideoToHEVCV3, playback.TransformationVideoToAV1V3:
			return true
		}
	}
	return false
}

// abandonmentDoesNotIndictDeliveryClassV3 reports whether a failure
// classification records a client that walked away from a delivery it had
// already reached, rather than evidence that the delivery class itself cannot
// serve this output.
//
// A player_failure or http_failure arrives after the client parsed the plan and
// started fetching it, so it proves the class reached the device; the same
// shape is also what a viewer backing out, a glitch on one recipe, or a
// transient fetch error produces, and the server cannot tell those apart. None
// of them is evidence about the class, and one class serves several recipes
// (server_remux_hls and server_transcode_hls are both DeliveryClassHLSV3), so
// retiring it also retires every untried recipe behind it. What a delivery
// class can do is knowable when the attempt starts, from the client's
// capabilities.
//
// Decode- and parser-shaped classifications are different evidence: they name a
// property of the recipe (codec, pixel format, container) rather than the
// client's appetite, so they still retire the class. Audio-output initialization
// failures are handled earlier by the audio-shape retry (see
// audioShapeRetryApplicableV3) and keep the ordinary demotion once that retry is
// spent. The failing recipe itself is retired by plan-key exclusion (§6.2), not
// by the class demotion this predicate suppresses.
func abandonmentDoesNotIndictDeliveryClassV3(classification string) bool {
	switch failureClassificationKeyV3(classification) {
	case "player_failure", "http_failure":
		return true
	default:
		return false
	}
}

// decodeFailureClassificationV3 reports whether a client failure classification
// indicts the video decoder specifically. Only these defer delivery demotion
// for a pending software-decode variant; other transport failures demote as
// before.
func decodeFailureClassificationV3(classification string) bool {
	switch failureClassificationKeyV3(classification) {
	case "decoder_failure", "decode_error":
		return true
	default:
		return false
	}
}

// corroboratedStartupTimeoutV3 admits a generic startup-timeout failure
// classification into candidate-rotation consideration. A timeout alone
// diagnoses nothing — the player reports it whenever the transport never
// produces, for any reason — so it is only a candidate here; the live
// generation verdict check below corroborates it before anything rotates.
// The production incident classified an undecodable provider stream exactly
// this way (startup_timeout → adaptation_exhausted with no rotation), because
// only explicit decode classes passed the old gate.
func corroboratedStartupTimeoutV3(classification string) bool {
	return failureClassificationKeyV3(classification) == "startup_timeout"
}

// routeDowngradeDirectPlayFailedV3 attributes a packaged plan to a direct-play
// route the client reported failing. Logged as route_downgrade beside
// decision_reason so an incident is diagnosable from logs alone.
const routeDowngradeDirectPlayFailedV3 = "direct_play_failed"

// routeDowngradeLogKeyV3 is the log attribute naming which rule declined a
// route the client's own claims allowed. The planner sets the same value on
// PlannerResultV3.RouteDowngrade.
const routeDowngradeLogKeyV3 = "route_downgrade"

// routeDowngradeV3 prefers this replan's own recovery attribution over the
// planner's, so a line that retired a route names why even when the planner
// also declined another route.
func routeDowngradeV3(recovery, planned string) string {
	if recovery != "" {
		return recovery
	}
	return planned
}

// directPlayRetiredByServerV3 reports whether this attempt's durable request
// carries a server-side original_http demotion. Every replan after the one that
// retired it reads that marker, so a chain that started with a failed direct
// route is attributable to the end of the ladder, not only on its first hop.
func directPlayRetiredByServerV3(record *playback.AttemptRecordV3) bool {
	if record == nil {
		return false
	}
	capability, ok := record.NormalizedRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	return ok && !capability.Enabled && capability.FailureReason == demoteDeliveryReasonV3
}

// retryableAfterDirectPlayV3 turns the planner's non-retryable exhaustion into
// the retryable routes_exhausted verdict when this attempt retired a
// client-failed direct route. Retiring direct play is per-attempt server
// evidence, not a verdict on the file or the device: the client has now tried
// every route the server could build for it, and a fresh start re-negotiates
// from its current capabilities, so trying again can help. A terminal that
// names its own cause (an unreadable source, a codec refusal) is left alone.
func retryableAfterDirectPlayV3(terminal *playback.TerminalV3, directPlayRetired bool) *playback.TerminalV3 {
	if terminal == nil || !directPlayRetired {
		return terminal
	}
	// transcoding_disabled names a first plan whose source needs an encode.
	// Here the attempt already had a playable direct route, and the client
	// reported it failing, so the routes were tried; the reason is exhaustion.
	switch terminal.Reason {
	case "adaptation_exhausted", "transcoding_disabled":
	default:
		return terminal
	}
	return &playback.TerminalV3{
		Reason:    playback.TerminalRoutesExhaustedV3,
		Message:   "Every playback route the server could build for this attempt was already tried. Starting playback again negotiates a fresh route.",
		Retryable: true,
		Detail:    terminal.Detail,
	}
}

// directPlayFailureClassificationsV3 are the client-reported playback failures
// that disqualify a direct-play plan. A failure_recovery operation already
// asserts the plan failed on the device; for original_http that is evidence
// about the exact bytes a packaged route would replace, so every recognized
// playback class indicts it, not just the decode classes that indict a
// server-produced recipe.
//
// Connectivity classes are included: pre-first-frame a lost connection is
// equally a route this client could not reach (the packaged route may use
// another egress), and post-first-frame the documented one-reissue path keeps
// the route by skipping this demotion (see progressReissue in
// executeReplanV3). Classes that mean a user intent or a client-local audio
// hiccup are deliberately absent: they say nothing about the video route.
var directPlayFailureClassificationsV3 = map[string]bool{
	"decoder_failure":            true,
	"decode_error":               true,
	"player_failure":             true,
	"http_failure":               true,
	"parser_failure":             true,
	"startup_timeout":            true,
	connectivityTransportStallV3: true,
	connectivityNetworkErrorV3:   true,
	connectivityNetworkFailureV3: true,
}

// directPlayFailureClassificationV3 reports whether a client-reported failure
// indicts the original-file route. An unsupported_* classification (for
// example an engine refusing the source container) counts as well: it names a
// property of the bytes a packaged route changes.
func directPlayFailureClassificationV3(classification string) bool {
	key := failureClassificationKeyV3(classification)
	if directPlayFailureClassificationsV3[key] {
		return true
	}
	return strings.Contains(key, "unsupported")
}

// softwareDecodeVariantPendingV3 reports whether the failed plan still has an
// untried software-decode variant of the same server-transcode HLS delivery.
// When one is pending, a transport failure must not demote the whole delivery:
// the decoded error indicts the hardware decoder, not the route. A delivery
// with no decode-mode dimension has no such variant and demotes as before.
func softwareDecodeVariantPendingV3(record *playback.AttemptRecordV3, req playback.ReplanRequestV3) bool {
	if record == nil || record.CurrentPlan.Delivery != playback.DeliveryTranscodeHLSV3 {
		return false
	}
	if record.CurrentPlan.EffectiveRecipe.SoftwareVideoDecode || !planHasVideoEncodeV3(record.CurrentPlan) {
		return false
	}
	softwarePlan := record.CurrentPlan
	softwarePlan.EffectiveRecipe.SoftwareVideoDecode = true
	// The durable plan key is always computed with no local mutations (see
	// finalizePlanIdentityV3); a software variant that already became the
	// current plan carries that same key, so compare against it directly.
	key := playback.PlanAttemptKeyV3(softwarePlan, record.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	attempted := append([]string(nil), req.AttemptedPlanKeys...)
	if !containsStringExactV3(attempted, req.PlanAttemptKey) {
		attempted = append(attempted, req.PlanAttemptKey)
	}
	return !containsStringExactV3(attempted, key)
}

// softwareDecodeRetryPendingV3 reports whether a decode failure on the current
// plan should defer delivery demotion for a software-decode retry. It requires
// both the structural variant and the live decoder verdict, so a remote or
// undetected decode failure still demotes as before.
func (h *PlaybackHandler) softwareDecodeRetryPendingV3(record *playback.AttemptRecordV3, req playback.ReplanRequestV3) bool {
	// gpu_only means there is no pending software retry to protect, so the
	// delivery demotes like any other transport failure instead of being held
	// open for a CPU attempt that policy forbids.
	if !h.softwareFallbackAllowedV3() || !softwareDecodeVariantPendingV3(record, req) {
		return false
	}
	ts := h.tm.GetTranscodeSession(record.SessionID)
	return ts != nil && ts.IsDecodeFailed()
}

// virtualCandidateRotationPendingV3 reports whether a decode-classified (or
// corroborated-timeout) failure recovery on a non-explicit virtual selection
// can still rotate to another provider candidate under the same
// server-transcode delivery. When true the delivery must not be demoted:
// rotation, not delivery retirement, is the next hop, and demoting first
// would make the planner abandon the only delivery the replacement candidate
// can use.
//
// The predicate requires the live generation's own decode verdict: a client's
// classification alone never triggers rotation, whether it names the decoder
// or reports a generic startup timeout the verdict corroborates. It is
// otherwise structural and deliberately conservative: it does not list
// candidates (that would put a provider round-trip on the demotion decision).
// If rotation later finds no sibling, executeReplanV3 retires the delivery
// explicitly on the exhausted path. An explicit pin is never a rotation.
func (h *PlaybackHandler) virtualCandidateRotationPendingV3(record *playback.AttemptRecordV3, req playback.ReplanRequestV3) bool {
	if h == nil || record == nil {
		return false
	}
	if req.EffectiveOperation() != playback.ReplanOperationFailureRecoveryV3 {
		return false
	}
	if !decodeFailureClassificationV3(req.Failure.Classification) &&
		!corroboratedStartupTimeoutV3(req.Failure.Classification) {
		return false
	}
	if h.explicitVirtualPinV3(record) {
		return false
	}
	if record.CurrentPlan.Delivery != playback.DeliveryTranscodeHLSV3 || !planHasVideoEncodeV3(record.CurrentPlan) {
		return false
	}
	session, err := h.sessionMgr.GetSession(record.SessionID)
	if err != nil || session == nil || strings.TrimSpace(session.VirtualSourceURI) == "" {
		return false
	}
	// The client's classification alone is not evidence: only a live generation
	// the decoder actually rejected is a candidate failure. This mirrors
	// softwareDecodeRetryPendingV3's live-verdict requirement and prevents a
	// spurious decode_error on a healthy session from rotating or demoting.
	ts := h.tm.GetTranscodeSession(record.SessionID)
	if ts == nil || !ts.IsSourceRejected() {
		return false
	}
	return true
}

// explicitVirtualPinV3 reports whether the attempt's requested release is an
// explicit user version pick, which is never substituted. It is the single
// predicate every rotation door reads, so the decode-rotation demotion hold and
// the rehydration's absent/dead-pin auto-retry cannot disagree about whether a
// release may rotate: an explicit pin protects, an auto selection does not.
func (h *PlaybackHandler) explicitVirtualPinV3(record *playback.AttemptRecordV3) bool {
	if record == nil {
		return false
	}
	return record.NormalizedRequest.FileSelection == playback.FileSelectionExplicitV3
}

// virtualAttemptProviderSourceV3 returns the provider/source scope for a
// session-bound virtual candidate: the provider-neutral URI with the concrete
// result pick removed. It scopes durable exclusions so an identically numbered
// result from another provider or release set cannot suppress this attempt's
// candidate.
func virtualAttemptProviderSourceV3(uri string) string {
	neutral := strings.TrimSpace(virtualPlaybackNeutralKey(uri))
	if neutral != "" {
		return neutral
	}
	return strings.TrimSpace(uri)
}

// recoveryStateStoreV3 returns the durable exclusion capability of the active
// plan store, or nil when the store cannot persist recovery state (a legacy
// in-memory fake). A nil store is not silently tolerated on a confirmed
// rotation: the caller surfaces a controlled failure instead.
func (h *PlaybackHandler) recoveryStateStoreV3() playback.RecoveryStateStoreV3 {
	if h == nil || h.PlanStoreV3 == nil {
		return nil
	}
	store, _ := h.PlanStoreV3.(playback.RecoveryStateStoreV3)
	return store
}

// appendRecoveryExclusionsV3 durably records the confirmed candidate exclusions
// on the attempt, unioning them with the chain the store already holds. It
// retries a losing revision compare against the committed state, so concurrent
// replans converge on the union instead of clobbering each other. It returns
// the first persistence error so the caller can fail the rotation as a
// controlled error rather than publish an untracked candidate.
func (h *PlaybackHandler) appendRecoveryExclusionsV3(ctx context.Context, record *playback.AttemptRecordV3, exclusions []playback.RecoveryExclusionV3) (playback.RecoveryStateV3, error) {
	if record == nil || len(exclusions) == 0 {
		return playback.RecoveryStateV3{}, nil
	}
	store := h.recoveryStateStoreV3()
	if store == nil {
		return playback.RecoveryStateV3{}, fmt.Errorf("recovery state store unavailable")
	}
	revision := record.RecoveryRevision
	var lastErr error
	for attempt := 0; attempt < recoveryAppendMaxAttempts; attempt++ {
		merged, newRevision, err := store.AppendRecoveryExclusions(ctx, record.SessionID, revision, exclusions)
		if err == nil {
			record.RecoveryState = merged
			record.RecoveryRevision = newRevision
			return merged, nil
		}
		if !errors.Is(err, playback.ErrRecoveryRevisionConflictV3) {
			return playback.RecoveryStateV3{}, err
		}
		// The committed chain advanced after this record was read. Re-read and
		// retry the union against the newer revision.
		lastErr = err
		_, currentRevision, readErr := store.GetRecoveryState(ctx, record.SessionID)
		if readErr != nil {
			return playback.RecoveryStateV3{}, readErr
		}
		revision = currentRevision
	}
	return playback.RecoveryStateV3{}, lastErr
}

// recoveryAppendMaxAttempts bounds the revision-conflict retry. The union is
// monotone and the only concurrent writer is another confirmed verdict for the
// same attempt, so a handful of retries converges; the bound keeps a
// pathological writer loop from spinning a replan.
const recoveryAppendMaxAttempts = 8

// appendRecipeExclusionsV3 durably records the plan attempt keys an abandonment
// retired so no later same-output replan can replay them (§6.2). It uses the
// same revision-checked union as the candidate chain and retries a losing
// compare. Unlike a confirmed candidate rotation, a failure to persist here
// must not fail playback: the delivery class is still eligible and the client's
// own echoed history still applies, so the guard degrades to that history and
// the caller only logs the error.
func (h *PlaybackHandler) appendRecipeExclusionsV3(ctx context.Context, record *playback.AttemptRecordV3, keys []string) error {
	if record == nil || len(keys) == 0 {
		return nil
	}
	store := h.recoveryStateStoreV3()
	if store == nil {
		return errors.New("recovery state store unavailable")
	}
	revision := record.RecoveryRevision
	var lastErr error
	for attempt := 0; attempt < recoveryAppendMaxAttempts; attempt++ {
		state, newRevision, err := store.AppendRecipeExclusions(ctx, record.SessionID, revision, keys)
		if err == nil {
			record.RecoveryState = state
			record.RecoveryRevision = newRevision
			return nil
		}
		if !errors.Is(err, playback.ErrRecoveryRevisionConflictV3) {
			return err
		}
		lastErr = err
		// The committed chain advanced after this record was read. Re-read and
		// retry the union against the newer revision.
		_, currentRevision, readErr := store.GetRecoveryState(ctx, record.SessionID)
		if readErr != nil {
			return readErr
		}
		revision = currentRevision
	}
	return lastErr
}

// abandonedRecipeAttemptKeysV3 returns the plan attempt keys an abandonment
// indicted: the durable recipe as this output planned it, plus its unmutated
// form when the client reports a local mutation. The server hashed that
// variation into the plan key, so both spellings name recipes that already
// failed for this output.
func abandonedRecipeAttemptKeysV3(record *playback.AttemptRecordV3, req playback.ReplanRequestV3) []string {
	if record == nil {
		return nil
	}
	outputContextID := record.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID
	if len(req.LocalMutations) == 0 {
		return []string{playback.PlanAttemptKeyV3(record.CurrentPlan, outputContextID, nil)}
	}
	return []string{
		playback.PlanAttemptKeyV3(record.CurrentPlan, outputContextID, req.LocalMutations),
		playback.PlanAttemptKeyV3(record.CurrentPlan, outputContextID, nil),
	}
}

// virtualCandidateReleaseIdentityV3 returns the durable release identity for a
// session-bound candidate (provider GUID or video hash), so an exclusion can
// match a renumbered result for the same release.
func virtualCandidateReleaseIdentityV3(file *models.MediaFile) string {
	if file == nil {
		return ""
	}
	if id := strings.TrimSpace(file.ProviderGUID); id != "" {
		return id
	}
	return strings.TrimSpace(file.ProviderVideoHash)
}

// demoteDeliveryCapabilityV3 disables one delivery class in the context's
// capability payload and clears its validated claims (the DV base-layer
// fallback among them), so the planner cannot reselect the delivery for the
// rest of this attempt. Idempotent and nil-safe.
func demoteDeliveryCapabilityV3(request *playback.StartRequestV3, delivery playback.DeliveryV3) {
	if request == nil || delivery == "" {
		return
	}
	class := playback.DeliveryClassV3(delivery)
	if request.ClientPlaybackContext.Deliveries == nil {
		return
	}
	capability, ok := request.ClientPlaybackContext.Deliveries[class]
	if !ok || !capability.Enabled {
		return
	}
	capability.Enabled = false
	capability.SupportedOnDevice = false
	capability.ValidatedClaims = nil
	capability.FailureReason = demoteDeliveryReasonV3
	request.ClientPlaybackContext.Deliveries[class] = capability
	slog.Debug("playback delivery demoted after transport failure",
		"component", "api", "delivery_class", class)
}

// demoteDeliveryReasonV3 is the marker demoteDeliveryCapabilityV3 stamps on a
// capability's FailureReason, distinguishing a server-side route demotion
// from a delivery the client itself advertised as unsupported (also
// Enabled=false). Only the marker form is re-applied across replans.
const demoteDeliveryReasonV3 = "transport_failed_demoted_by_server"

// degradationSoftwareDecodeFallbackV3 marks a plan that deliberately switched
// to CPU video decoding after the hardware decoder rejected the source.
const degradationSoftwareDecodeFallbackV3 = "software_decode_fallback"

// demotedDeliveryClassesV3 lists the delivery classes the durable request
// demotes (server-side demotion marker written by a previous failure
// recovery). The client cannot advertise itself back into a route the server
// demoted: the demotion is server-side evidence, so it must survive the
// client capability overlay every replan performs.
func demotedDeliveryClassesV3(durable playback.StartRequestV3) []string {
	var demoted []string
	for class, capability := range durable.ClientPlaybackContext.Deliveries {
		if strings.EqualFold(strings.TrimSpace(capability.FailureReason), demoteDeliveryReasonV3) {
			demoted = append(demoted, class)
		}
	}
	return demoted
}

// audioShapeRetryReasonV3 marks a delivery whose surround audio recipe failed
// to initialize and was rebuilt once as stereo. The delivery stays enabled:
// the video route was never at fault. A second audio-init failure on the
// rebuilt plan falls through to the ordinary demotion path.
const audioShapeRetryReasonV3 = "audio_init_failed_shape_retry"

// planAudioChannelsV3 reports the channel count a plan actually delivers. A
// copied or direct route carries no converted layout, so the source descriptor
// names the selected track's real shape.
func planAudioChannelsV3(plan playback.PlanV3) int {
	if plan.EffectiveRecipe.AudioChannels != nil && *plan.EffectiveRecipe.AudioChannels > 0 {
		return *plan.EffectiveRecipe.AudioChannels
	}
	return plan.Source.AudioChannels
}

// audioShapeRetriedV3 reports whether delivery already spent its one
// audio-shape retry on this attempt.
func audioShapeRetriedV3(request playback.StartRequestV3, delivery playback.DeliveryV3) bool {
	capability, ok := request.ClientPlaybackContext.Deliveries[playback.DeliveryClassV3(delivery)]
	return ok && strings.EqualFold(strings.TrimSpace(capability.FailureReason), audioShapeRetryReasonV3)
}

// audioShapeRetryApplicableV3 reports whether the current plan is a surround
// recipe whose delivery has not spent its audio-shape retry. A plan already
// downmixed to stereo has nothing smaller to try.
func audioShapeRetryApplicableV3(record *playback.AttemptRecordV3) bool {
	if record == nil || record.CurrentPlan.Delivery == "" || record.CurrentPlan.Source.VideoCodec == "" {
		return false
	}
	if audioShapeRetriedV3(record.NormalizedRequest, record.CurrentPlan.Delivery) {
		return false
	}
	return planAudioChannelsV3(record.CurrentPlan) > 2
}

// markAudioShapeRetryV3 stamps the one-shot audio-shape retry on a delivery
// without disabling it. Idempotent and nil-safe.
func markAudioShapeRetryV3(request *playback.StartRequestV3, delivery playback.DeliveryV3) {
	if request == nil || delivery == "" {
		return
	}
	class := playback.DeliveryClassV3(delivery)
	if request.ClientPlaybackContext.Deliveries == nil {
		return
	}
	capability, ok := request.ClientPlaybackContext.Deliveries[class]
	if !ok || !capability.Enabled {
		return
	}
	capability.FailureReason = audioShapeRetryReasonV3
	request.ClientPlaybackContext.Deliveries[class] = capability
	slog.Debug("playback delivery kept for a stereo audio-shape retry",
		"component", "api", "delivery_class", class)
}

// reapplyAudioShapeRetriesV3 re-stamps the durable record's audio-shape retry
// markers after the client's capability payload replaced the seeded map. A
// delivery the client no longer enables stays disabled; only a still-enabled
// class keeps the server-owned one-shot marker.
func reapplyAudioShapeRetriesV3(start *playback.StartRequestV3, durable playback.StartRequestV3) {
	if start == nil || start.ClientPlaybackContext.Deliveries == nil {
		return
	}
	for class, durableCapability := range durable.ClientPlaybackContext.Deliveries {
		if !strings.EqualFold(strings.TrimSpace(durableCapability.FailureReason), audioShapeRetryReasonV3) {
			continue
		}
		capability, ok := start.ClientPlaybackContext.Deliveries[class]
		if !ok || !capability.Enabled {
			continue
		}
		capability.FailureReason = audioShapeRetryReasonV3
		start.ClientPlaybackContext.Deliveries[class] = capability
	}
}

// reapplyDeliveryDemotionsV3 writes the durable record's delivery demotions
// onto a freshly overlaid request. Called after the client's capability
// payload replaces the seeded one, so a delivery a previous failure recovery
// demoted stays disabled for the rest of the attempt. The re-application
// re-stamps the demotion marker: the overlay replaced the seeded payload's
// map, and without the stamp the commit would persist an Enabled=false entry
// the next replan can no longer distinguish from a client-advertised
// unsupported delivery — the stickiness would die after one hop.
//
// The re-application is independent of what the client sent this round: an
// incoming entry that exists but is disabled still gets the marker (so the
// commit keeps the server-owned evidence), and a class the client omitted
// entirely gets a synthesized disabled entry — otherwise a successful
// intermediate replan that carried the class absent would erase the marker
// and let a later replan re-enable the delivery that failed.
func reapplyDeliveryDemotionsV3(start *playback.StartRequestV3, durable playback.StartRequestV3) {
	if start == nil {
		return
	}
	if start.ClientPlaybackContext.Deliveries == nil {
		start.ClientPlaybackContext.Deliveries = make(map[string]playback.DeliveryCapabilityV3)
	}
	for _, class := range demotedDeliveryClassesV3(durable) {
		capability, ok := start.ClientPlaybackContext.Deliveries[class]
		if !ok {
			// Omitted this round: synthesize the disabled entry so the
			// demotion marker survives into the commit.
			capability = playback.DeliveryCapabilityV3{}
		}
		capability.Enabled = false
		capability.SupportedOnDevice = false
		capability.ValidatedClaims = nil
		capability.FailureReason = demoteDeliveryReasonV3
		start.ClientPlaybackContext.Deliveries[class] = capability
	}
}

// subtitleVariantIndexV3 returns the combined index of the one deliverable
// external or embedded subtitle in file with the given language and forced/SDH
// flags, whatever its format, or -1. Several such tracks (main dialog and
// commentary, say) are narrowed by title; a match that stays ambiguous is not
// treated as the same selection.
// displayedSubtitleTitleV3 is the title the subtitle inventory shows a
// viewer: the stored title, else the container's embedded one.
func displayedSubtitleTitleV3(title, embeddedTitle string) string {
	if strings.TrimSpace(title) != "" {
		return title
	}
	return embeddedTitle
}

// subtitleRemapTitleMatchesV3 is the tiebreak when a language/format/flags
// match names more than one physical subtitle track. An untitled selection has
// no title evidence and therefore accepts the first identity match, preserving
// the historical behavior; a titled selection only prefers the track that
// carries the same displayed title.
func subtitleRemapTitleMatchesV3(want, got string) bool {
	if strings.TrimSpace(want) == "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(want), strings.TrimSpace(got))
}

func subtitleVariantIndexV3(file *models.MediaFile, language, title string, forced, hearingImpaired bool) int {
	type candidate struct {
		index int
		title string
	}
	var candidates []candidate
	for i, track := range file.ExternalSubtitles {
		// The policy burns in embedded bitmaps only and serves no external
		// bitmap sidecar, so an external candidate must be text.
		if strings.EqualFold(track.Language, language) && track.Forced == forced && track.HearingImpaired == hearingImpaired && playback.SubtitleFormatDeliverableV3(track.Format) && !playback.NeedsBurnIn(track.Format) {
			candidates = append(candidates, candidate{index: i, title: displayedSubtitleTitleV3(track.Title, track.EmbeddedTitle)})
		}
	}
	for i, track := range file.SubtitleTracks {
		if strings.EqualFold(track.Language, language) && track.Forced == forced && track.HearingImpaired == hearingImpaired && playback.SubtitleFormatDeliverableV3(track.Codec) {
			candidates = append(candidates, candidate{index: len(file.ExternalSubtitles) + i, title: displayedSubtitleTitleV3(track.Title, track.EmbeddedTitle)})
		}
	}
	if len(candidates) == 1 {
		return candidates[0].index
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return -1
	}
	match := -1
	for _, c := range candidates {
		if strings.EqualFold(strings.TrimSpace(c.title), title) {
			if match >= 0 {
				return -1
			}
			match = c.index
		}
	}
	return match
}

func sessionStartErrorV3(err error) *transportErrorV3 {
	switch {
	case errors.Is(err, playback.ErrTooManyStreams):
		return &transportErrorV3{reason: "stream_limit_reached", message: "The account's concurrent stream limit is in use.", retryable: true}
	case errors.Is(err, playback.ErrTooManyTranscodes):
		return &transportErrorV3{reason: "transcode_limit_reached", message: "The account's concurrent transcode limit is in use.", retryable: true}
	case errors.Is(err, playback.ErrAudioTranscodingDisabled):
		return &transportErrorV3{reason: "audio_transcoding_disabled", message: "The selected audio adaptation is disabled."}
	case errors.Is(err, playback.ErrTranscodingDisabled):
		return &transportErrorV3{reason: transcodingDisabledCode, message: "The selected server adaptation is disabled."}
	case errors.Is(err, playback.ErrPlaybackNotAllowed):
		return &transportErrorV3{reason: "policy_denied", message: "Playback is denied by server policy."}
	default:
		return &transportErrorV3{reason: "internal_error", message: "Failed to start the playback session.", cause: err}
	}
}

func (h *PlaybackHandler) persistTerminalStartDecisionV3(ctx context.Context, userID int, profileID string, req playback.StartRequestV3, requestDigests playbackStartRequestDigestsV3, requestedFileID, effectiveFileID int, response playback.DecisionResponseV3) (playback.DecisionResponseV3, error) {
	record := playback.AttemptRecordV3{
		PlaybackAttemptID:    req.PlaybackAttemptID,
		UserID:               userID,
		ProfileID:            profileID,
		RequestedMediaFileID: requestedFileID,
		EffectiveMediaFileID: effectiveFileID,
		NormalizedRequest:    req,
		ServerBitrateCapKbps: serverBitrateCapV3(ctx),
		StartResponse:        response,
		RequestDigest:        requestDigests.current,
		ExpiresAt:            time.Now().Add(playback.MaxTokenTTL),
	}
	if err := h.PlanStoreV3.SaveAttempt(ctx, record); err == nil {
		return response, nil
	} else if !errors.Is(err, playback.ErrPlaybackAttemptExistsV3) && !errors.Is(err, playback.ErrIdempotencyKeyReusedV3) {
		return playback.DecisionResponseV3{}, err
	}

	existing, err := h.PlanStoreV3.GetAttemptByPlaybackAttemptID(ctx, req.PlaybackAttemptID)
	if err != nil {
		return playback.DecisionResponseV3{}, err
	}
	if existing.UserID != userID || existing.ProfileID != profileID ||
		existing.RequestedMediaFileID != requestedFileID || !requestDigests.matches(existing.RequestDigest) {
		return playback.DecisionResponseV3{}, playback.ErrIdempotencyKeyReusedV3
	}
	existingResponse := decisionResponseFromAttemptV3(existing)
	// A terminal publishes no subtitle URLs and no playable plan, so it replays
	// on either surface; the surface guard protects only a playable plan. This
	// is the terminal-save collision: a concurrent duplicate start lost the
	// SaveAttempt CAS, re-read a winner whose plan may have negotiated the
	// deferred track-inventory lifecycle (or original SRT), and must not hand
	// that plan back through the other surface. Without this guard the losing
	// request's terminal save would replay the winner's playable plan —
	// tracks_pending and all — through a surface that never negotiated it.
	if existingResponse.Terminal == nil {
		if err := requireAttemptAPISurfaceV3(ctx, existing, req.ClientFeatures); err != nil {
			return playback.DecisionResponseV3{}, err
		}
		// A playable winner is only replayable while the durable decision
		// describes the session the attempt owns. A record left describing
		// another session by a mixed-version or partially applied writer must
		// not hand the losing request a plan it can never stream; refuse it as
		// a retryable terminal so the client mints a fresh attempt, exactly as
		// the ordinary start-replay path does.
		if !startReplayResponseBelongsToAttemptV3(existingResponse, existing) {
			return foreignAttemptSessionDecisionV3(), nil
		}
	}
	// A publication failure can leave a durable attempt whose session was
	// aborted. A terminal-save collision must not resurrect that playable plan.
	if existing.SessionID != "" {
		if _, err := h.sessionMgr.GetSession(existing.SessionID); err != nil {
			return playback.NewTerminalResponseV3(terminalSessionExpiredV3, "The playback session for this attempt has ended.", true), nil //nolint:nilerr // Expiration is a successful terminal protocol response.
		}
	}
	return existingResponse, nil
}

// startFailureDecisionV3 stores the transport failure as the attempt's durable
// terminal and answers it. A server-side fault is not a verdict: a store, session
// manager or commit fault says nothing about the request or the release, so
// publishing it as a terminal would tell the client never to retry a condition
// that is retryable by definition. Answer the same retryable 503 the failed
// terminal save maps to instead, and store nothing — there is no decision to
// replay, and a retry re-derives it from zero either way.
func (h *PlaybackHandler) startFailureDecisionV3(ctx context.Context, userID int, profileID string, req playback.StartRequestV3, requestDigests playbackStartRequestDigestsV3, requestedFileID, effectiveFileID int, failure *transportErrorV3) (playback.DecisionResponseV3, error) {
	if failure.reason == policyErrorInternal {
		return playback.DecisionResponseV3{}, retryableStartOperationErrorV3(failure)
	}
	response := playback.NewTerminalResponseV3(failure.reason, failure.message, failure.retryable)
	return h.persistTerminalStartDecisionV3(ctx, userID, profileID, req, requestDigests, requestedFileID, effectiveFileID, response)
}

// terminalStartFailureV3 describes the start verdict a terminal response
// carries, so a failed save of that verdict can answer in its words instead of a
// generic temporary-unavailable message.
func terminalStartFailureV3(response playback.DecisionResponseV3) *transportErrorV3 {
	if response.Terminal == nil {
		return nil
	}
	return &transportErrorV3{reason: response.Terminal.Reason, message: response.Terminal.Message, retryable: response.Terminal.Retryable}
}

// startTerminalPersistOperationErrorV3 maps a failed terminal-start save to the
// client answer. The virtual start owns one deadline for resolve, planning and
// transport startup, so a start that reaches it fails the following SaveAttempt
// with context.DeadlineExceeded: the save describes the expired request budget,
// not the playback verdict. A retryable failure (or a save cut off only by that
// budget) must stay retryable and advertise Retry-After instead of collapsing
// into a 500 internal_error; only a genuine store fault on a non-retryable
// verdict keeps the 500. failure is the verdict that could not be stored, and is
// nil only when the caller has none to name.
func startTerminalPersistOperationErrorV3(ctx context.Context, persistErr error, failure *transportErrorV3) error {
	if e, ok := errors.AsType[*PlaybackOperationError](persistErr); ok {
		return e
	}
	if errors.Is(persistErr, playback.ErrIdempotencyKeyReusedV3) {
		return playbackPersistenceOperationError(persistErr)
	}
	retryable := failure != nil && failure.retryable
	slog.WarnContext(ctx, "playback start terminal persist failed",
		"component", "api", "retryable", retryable, "error", persistErr)
	if !retryable && !errors.Is(persistErr, context.DeadlineExceeded) && !errors.Is(persistErr, context.Canceled) {
		return playbackPersistenceOperationError(persistErr)
	}
	return retryableStartOperationErrorV3(failure)
}

// retryableStartOperationErrorV3 classifies a transient start failure for the
// client. Route-capacity exhaustion names its own existing code (the replan
// path answers the same state with 503 route_capacity_unavailable); every other
// retryable start failure is dependency_unavailable, the catalog's 503 for
// temporary unavailability.
func retryableStartOperationErrorV3(failure *transportErrorV3) *PlaybackOperationError {
	if failure != nil && failure.reason == string(noderouting.OutcomeCapacityUnavailable) {
		return &PlaybackOperationError{
			Status:     http.StatusServiceUnavailable,
			Code:       string(noderouting.OutcomeCapacityUnavailable),
			Message:    "The playback route capacity is temporarily unavailable; retry shortly",
			RetryAfter: playbackStartRetryAfterSeconds,
		}
	}
	message := "Playback is temporarily unavailable; retry shortly"
	// A verdict's own words name the condition a retry would meet again, so they
	// carry the answer even when the verdict itself was final: the client is
	// being asked to retry a save, not the verdict. A server fault keeps the
	// generic text — its message describes our plumbing, not the viewer's next
	// step.
	if failure != nil && failure.reason != policyErrorInternal && strings.TrimSpace(failure.message) != "" {
		message = failure.message
	}
	if failure != nil && errors.Is(failure.cause, context.DeadlineExceeded) {
		message = "Playback start timed out; retry shortly"
	}
	return &PlaybackOperationError{
		Status:     http.StatusServiceUnavailable,
		Code:       playbackDependencyUnavailableCode,
		Message:    message,
		RetryAfter: playbackStartRetryAfterSeconds,
	}
}

func writeStartAttemptPersistenceErrorV3(w http.ResponseWriter, err error) {
	if errors.Is(err, playback.ErrIdempotencyKeyReusedV3) {
		writeError(w, http.StatusConflict, "playback_attempt_reused", "The playback attempt ID belongs to a different request")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "Failed to persist the playback decision")
}

func decisionResponseFromAttemptV3(record *playback.AttemptRecordV3) playback.DecisionResponseV3 {
	if record == nil {
		return playback.DecisionResponseV3{}
	}
	if record.StartResponse.Outcome != "" || record.StartResponse.Terminal != nil || record.StartResponse.PlaybackPlan != nil {
		return normalizeDecisionResponseV3(record.StartResponse)
	}
	plan := record.CurrentPlan
	if plan.AppliedQuirks == nil {
		plan.AppliedQuirks = []playback.AppliedQuirkV3{}
	}
	if plan.RuntimeCorrections == nil {
		plan.RuntimeCorrections = []string{}
	}
	return normalizeDecisionResponseV3(playback.DecisionResponseV3{ProtocolVersion: playback.ProtocolV3, ServerFeatures: playback.ServerFeaturesV3(), Outcome: playback.OutcomePlayableV3, SessionID: record.SessionID, PlaybackPlan: &plan})
}

// frozenV1DecisionV3 removes what only /api/v2 publishes from a decision
// written on the frozen /api/v1 surface: active_quality, and the menu rungs
// below 1.5 Mbps that Auto lowering added after the freeze.
func frozenV1DecisionV3(response playback.DecisionResponseV3) playback.DecisionResponseV3 {
	if response.PlaybackPlan == nil {
		return response
	}
	plan := *response.PlaybackPlan
	plan.ActiveQuality = ""
	plan.AvailableQualities = slices.DeleteFunc(slices.Clone(plan.AvailableQualities), func(quality playback.AvailableQualityV3) bool {
		return quality.Label == playback.QualityRung480pLowV3 || quality.Label == playback.QualityRung360pV3
	})
	plan.Subtitle.Inventory = slices.Clone(plan.Subtitle.Inventory)
	for i := range plan.Subtitle.Inventory {
		item := &plan.Subtitle.Inventory[i]
		item.StreamIndex, item.ContainerTrackID, item.NativeSelectable, item.HLSRendition = nil, "", false, nil
	}
	if rendition := plan.Subtitle.HLSRendition; rendition != nil && rendition.Index != 0 {
		frozen := *rendition
		frozen.Index = 0
		plan.Subtitle.HLSRendition = &frozen
	}
	response.PlaybackPlan = &plan
	return response
}

func startReplayResponseBelongsToAttemptV3(response playback.DecisionResponseV3, record *playback.AttemptRecordV3) bool {
	if record == nil || record.SessionID == "" || response.SessionID != record.SessionID {
		return false
	}
	return response.PlaybackPlan == nil || response.PlaybackPlan.SessionID == record.SessionID
}

func foreignAttemptSessionDecisionV3() playback.DecisionResponseV3 {
	return playback.NewTerminalResponseV3("session_expired", "The playback plan for this attempt does not belong to its session.", true)
}

func normalizeDecisionResponseV3(response playback.DecisionResponseV3) playback.DecisionResponseV3 {
	if response.ServerFeatures == nil {
		response.ServerFeatures = playback.ServerFeaturesV3()
	}
	if response.PlaybackPlan == nil {
		return response
	}
	plan := response.PlaybackPlan
	if plan.Stream.Headers == nil {
		plan.Stream.Headers = map[string]string{}
	}
	if plan.Transformations == nil {
		plan.Transformations = []playback.TransformationV3{}
	}
	if plan.AppliedQuirks == nil {
		plan.AppliedQuirks = []playback.AppliedQuirkV3{}
	}
	if plan.RuntimeCorrections == nil {
		plan.RuntimeCorrections = []string{}
	}
	if plan.AvailableQualities == nil {
		plan.AvailableQualities = []playback.AvailableQualityV3{}
	}
	if plan.DegradationWarnings == nil {
		plan.DegradationWarnings = []playback.DegradationWarningV3{}
	}
	if plan.Subtitle.Inventory == nil {
		plan.Subtitle.Inventory = []playback.SubtitleInventoryItemV3{}
	}
	return response
}

func completedReplanResponseMatchesAttemptV3(raw json.RawMessage, record *playback.AttemptRecordV3) bool {
	if record == nil {
		return false
	}
	var response playback.DecisionResponseV3
	if len(raw) == 0 || json.Unmarshal(raw, &response) != nil {
		return false
	}
	if response.PlaybackPlan == nil {
		// Terminal responses deliberately leave the attempt plan untouched. Their
		// freshness is carried by CurrentReplanRequestID (and its DB trigger). A
		// terminal is session-less by construction; one that names another
		// session was written by a different attempt and must not be replayed
		// across the session boundary either.
		return response.Terminal != nil && (response.SessionID == "" || response.SessionID == record.SessionID)
	}
	if response.SessionID != record.SessionID || response.PlaybackPlan.SessionID != record.SessionID {
		return false
	}
	candidate, candidateErr := json.Marshal(response.PlaybackPlan)
	current, currentErr := json.Marshal(record.CurrentPlan)
	return candidateErr == nil && currentErr == nil && bytes.Equal(candidate, current)
}

func appliedQuirkIDsV3(plan *playback.PlanV3) []string {
	if plan == nil {
		return nil
	}
	result := make([]string, 0, len(plan.AppliedQuirks))
	for _, quirk := range plan.AppliedQuirks {
		result = append(result, quirk.ID)
	}
	return result
}

func appliedQuirkRevisionV3(plan *playback.PlanV3) string {
	if plan == nil || len(plan.AppliedQuirks) == 0 {
		return ""
	}
	return plan.AppliedQuirks[0].RegistryRevision
}

func writeV3FileError(w http.ResponseWriter, err error) {
	if errors.Is(err, catalog.ErrItemNotFound) || errors.Is(err, catalog.ErrEpisodeNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "Media file not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "Failed to authorize media file")
}
func readBoundedV3Body(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	return ioReadAllV3(http.MaxBytesReader(w, r.Body, limit))
}
func ioReadAllV3(reader interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var buffer bytes.Buffer
	_, err := buffer.ReadFrom(reader)
	return buffer.Bytes(), err
}
func chiURLParamV3(r *http.Request, key string) string { return chi.URLParam(r, key) }
func floatOrZeroHandlerV3(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func intOrZeroHandlerV3(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}
func firstNonEmptyHandlerV3(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
func subtitleMIMEV3(format string) string {
	switch strings.ToLower(format) {
	case "ass", "ssa":
		return "text/x-ssa"
	case "srt", "subrip":
		return "application/x-subrip"
	case "pgs", subtitleFormatSUP, subtitleCodecPGSFFmpegV3:
		return "application/octet-stream"
	default:
		return subtitleMIMEVTTV3
	}
}

// forceSubtitleExtensionV3 replaces the URL's sidecar extension. It also drops
// original=1, which only means something on a .srt URL.
func forceSubtitleExtensionV3(rawURL, extension string) string {
	pathPart, query, hasQuery := strings.Cut(rawURL, "?")
	if extension != playback.SubtitleExtSRTV3 {
		query = strings.Join(slices.DeleteFunc(strings.Split(query, "&"), func(field string) bool {
			return field == playback.SubtitleOriginalParamV3+"=1"
		}), "&")
		hasQuery = query != ""
	}
	if slash := strings.LastIndex(pathPart, "/"); slash >= 0 {
		if dot := strings.LastIndex(pathPart[slash+1:], "."); dot >= 0 {
			pathPart = pathPart[:slash+1+dot] + extension
		} else {
			pathPart += extension
		}
	}
	if hasQuery {
		return pathPart + "?" + query
	}
	return pathPart
}

func remuxDVModeForPlanV3(plan *playback.PlanV3) playback.RemuxDVMode {
	if plan == nil {
		return ""
	}
	for _, transformation := range plan.Transformations {
		if transformation.Name == playback.TransformationServerDV7HDR10V3 {
			return playback.RemuxDVStripToHDR10V3
		}
	}
	if plan.Source.DVProfile == 0 {
		return ""
	}
	if plan.Source.DVProfile == 7 {
		// Without the strip transformation a P7 remux would drop the
		// enhancement layer and leave dangling RPUs. A P7 plan claiming Dolby
		// Vision is a client-side transform of the original bytes or the
		// server's HLS-only 8.1 conversion, so any progressive remux attempt
		// against this session must still be rejected.
		return playback.RemuxDVRejectP7V3
	}
	if plan.Claims.Video.DolbyVision {
		return playback.RemuxDVPreserveV3
	}
	return ""
}

// planDVProfileV3 returns the Dolby Vision profile the plan's probe validated
// (0 when the plan carries none). Stream time re-deriving the profile from the
// catalog row can return 0 for virtual candidates whose video_tracks are
// empty, so the session persists the plan's ground truth.
func planDVProfileV3(plan *playback.PlanV3) int {
	if plan == nil {
		return 0
	}
	return plan.Source.DVProfile
}

// videoBitstreamFilterForPlanV3 returns the copy-mode filter chain a plan's
// server-run DV7 strip needs. It is the current chain whatever recipe version
// the plan names: executors validate that version against what they advertise
// before starting, and a node that predates the chain rejects it, so a plan
// frozen at an older recipe fails rather than copying Dolby Vision unstripped.

// applyDVPinToEffectiveFileV3 overwrites a freshly loaded effective media
// file's Dolby Vision profile with the session's first-verified-wins pin when
// the pin names that same file. A replan re-reads the catalog row and can see
// a drifting dv_profile on bytes that never changed; pinning the planner's
// input keeps route selection stable for the session without touching catalog
// state (the file is a per-request load). The pin never crosses files, so a
// substitution or candidate rotation still plans from the new file's own read.
func applyDVPinToEffectiveFileV3(session *playback.Session, file *models.MediaFile) {
	if session == nil || file == nil {
		return
	}
	file.VideoTracks = playback.ApplyDVPinToFileV3(file, session.DVProfilePin)
}

func videoBitstreamFilterForPlanV3(plan *playback.PlanV3) string {
	if plan == nil {
		return ""
	}
	for _, transformation := range plan.Transformations {
		if transformation.Executor == playback.ExecutorServerV3 && transformation.Name == playback.TransformationServerDV7HDR10V3 {
			return playback.DV7ToHDR10BitstreamFilter
		}
		if transformation.Executor == playback.ExecutorServerV3 && transformation.Name == playback.TransformationServerDV7DV81V3 {
			return playback.DV7ToDV81BitstreamFilter
		}
	}
	return ""
}

const playbackVideoCodecHEVC = "hevc"

// videoSampleEntryForPlanV3 also serves MPEG-TS copy plans, which publish no
// sample entry: the derived dvh1 for preserved Dolby Vision brings
// `-strict unofficial`, under which FFmpeg's MPEG-TS muxer writes the DOVI
// video stream descriptor. The tag itself has no effect on MPEG-TS.
func videoSampleEntryForPlanV3(plan *playback.PlanV3) string {
	if plan == nil {
		return ""
	}
	if plan.Delivery == playback.DeliveryTranscodeHLSV3 && plan.EffectiveRecipe.VideoCodec == playbackVideoCodecHEVC && plan.EffectiveRecipe.VideoSampleEntry == playback.VideoSampleEntryHVC1 {
		return playback.VideoSampleEntryHVC1
	}
	if plan.Delivery != playback.DeliveryRemuxHLSV3 {
		return ""
	}
	if plan.EffectiveRecipe.VideoSampleEntry != "" {
		return plan.EffectiveRecipe.VideoSampleEntry
	}
	for _, transformation := range plan.Transformations {
		if transformation.Name == playback.TransformationServerDV7HDR10V3 || transformation.Name == playback.TransformationServerDV7DV81V3 {
			return playback.VideoSampleEntryHVC1
		}
	}
	if plan.EffectiveRecipe.DynamicRange == playback.DynamicRangeDolbyVisionV3 &&
		(plan.Source.DVProfile == 5 || plan.Source.DVProfile == 8) {
		return playback.VideoSampleEntryDVH1
	}
	return ""
}

// copyHLSVideoRangeV3 is the VIDEO-RANGE an HLS copy remux's subtitle
// rendition variant names, from the source video track the copy keeps. Other
// plans name none.
func copyHLSVideoRangeV3(file *models.MediaFile, result playback.PlannerResultV3, videoCodec string) string {
	if !result.SubtitleRendition || videoCodec != "copy" || file == nil || len(file.VideoTracks) == 0 {
		return ""
	}
	return playback.CopyHLSVideoRange(file.VideoTracks[0].ColorTransfer, videoBitstreamFilterForPlanV3(result.Plan), videoSampleEntryForPlanV3(result.Plan))
}

// lazyDVRPUStrippableV3 defers (and memoizes) the per-source RPU probe so the
// planner only shells out to ffmpeg when a Dolby Vision strip route is
// genuinely on the table; every other start never touches it.
//
// The probe belongs to planning, not to the transport: the plan's HDR10 promise
// and the durable session's RemuxDVMode are both derived from the strip
// decision and are re-read by the restart and audio-switch paths, so
// suppressing the filter downstream would leave those claims describing a
// stream the server is no longer producing.
func (h *PlaybackHandler) lazyDVRPUStrippableV3(ctx context.Context, file *models.MediaFile) func() bool {
	if file == nil || strings.TrimSpace(file.FilePath) == "" || isVirtualPlaybackFile(file) || strings.HasPrefix(strings.ToLower(file.FilePath), "virtual://") {
		return nil
	}
	// The shared DV RPU probe cache keys on bin|inputPath; the transport URL
	// rotates per relay registration, so a sidecar-only replan would miss it
	// and re-probe every time. Memoize on the stable file-row identity instead.
	// A missing mtime or size still falls through to the probe: the memo must
	// never claim a verdict the probe could not have reached.
	var memoKey dvRPUMemoKeyV3
	memoize := false
	if file.FileModifiedAt != nil && file.FileSize > 0 {
		memoKey = dvRPUMemoKeyV3{fileID: file.ID, size: file.FileSize, mtimeUnixNano: file.FileModifiedAt.UnixNano()}
		h.v3DVRPUMu.Lock()
		verdict, ok := h.v3DVRPUVerds[memoKey]
		h.v3DVRPUMu.Unlock()
		if ok {
			return func() bool { return verdict }
		}
		memoize = true
	}
	var once sync.Once
	strippable := true
	return func() bool {
		once.Do(func() {
			strippable = playback.DVRPUStrippable(ctx, h.playbackConfig().FFmpegPath, file.FilePath)
			// DVRPUStrippable returns only a bool, so a transient probe failure
			// (timeout, unreadable mount) is indistinguishable from a real
			// verdict and pins `true` here — the safe default: the strip is
			// attempted, and the transcoder handles failure gracefully.
			if memoize {
				h.v3DVRPUMu.Lock()
				if h.v3DVRPUVerds == nil {
					h.v3DVRPUVerds = make(map[dvRPUMemoKeyV3]bool)
				}
				// Bound the memo: evict a random entry when the map exceeds
				// the cap so it never grows unbounded over the process
				// lifetime. A full LRU is overkill for a probe-verdict cache
				// whose entries are cheap to recompute on miss.
				const dvRPUMemoMax = 256
				if len(h.v3DVRPUVerds) >= dvRPUMemoMax {
					for k := range h.v3DVRPUVerds {
						delete(h.v3DVRPUVerds, k)
						break
					}
				}
				h.v3DVRPUVerds[memoKey] = strippable
				h.v3DVRPUMu.Unlock()
			}
		})
		return strippable
	}
}

func configureHLSTimelineV3(plan *playback.PlanV3, videoCodec string, segmentDuration int, durationSeconds float64) (float64, int) {
	if plan == nil {
		return 0, 0
	}
	requested := plan.Timeline.SourceStartSeconds
	seek := alignedSeekSeconds(requested, segmentDuration, videoCodec)
	startSegment := computeStartSegment(seek, segmentDuration)
	plan.Timeline.SourceStartSeconds = requested
	usesGrowingManifest := !playback.CanGenerateSyntheticManifest(durationSeconds, segmentDuration)
	if startSegment > 0 && !usesGrowingManifest {
		// A resume starts the generation at the segment boundary before the
		// requested position, and the complete VOD playlist begins there (see
		// generateSyntheticPlaylist). The transport's clock zero is therefore
		// that seek anchor, not the source start: the client seeks to the
		// requested position expressed on the playlist's own timeline. The
		// previous source-zero shape advertised segments the generation never
		// produced, so a player that prefetches the playlist's first entry
		// requested one of them and the segment layer restarted a healthy
		// encoder just to serve a segment the client then discarded.
		//
		// The playlist is complete, so the window is closed. Positions after
		// the anchor are served in place; a target before it belongs to
		// another generation, so the plan routes that seek back through the
		// server rather than letting the player clamp to the anchor.
		plan.Timeline.PlayerStartSeconds = max(0, requested-seek)
		plan.Timeline.StreamOriginSeconds = seek
		plan.Timeline.TimelineOffsetSeconds = seek
		windowStart := seek
		windowEnd := durationSeconds
		plan.Timeline.SeekWindowStartSeconds = &windowStart
		plan.Timeline.SeekWindowEndSeconds = &windowEnd
		plan.Timeline.CanSeekAnywhere = false
		plan.Timeline.SeekRestoration = "source_position"
	} else if usesGrowingManifest {
		// Encoded streams seek to the preceding segment boundary. Preserve the
		// requested sub-segment offset so playback still begins at the exact
		// requested source position. Copy remuxes are configured separately with
		// their probed keyframe origin.
		plan.Timeline.PlayerStartSeconds = max(0, requested-seek)
		plan.Timeline.StreamOriginSeconds = seek
		plan.Timeline.TimelineOffsetSeconds = seek
		windowStart := seek
		plan.Timeline.SeekWindowStartSeconds = &windowStart
		// This transport is served from FFmpeg's live, still-growing playlist
		// (see BuildPlaybackManifest), so the seekable extent is whatever has
		// been produced so far — a value this plan cannot know and could not
		// keep current if it did. Publishing the media runtime here instead
		// made the window look *complete*, which clients read as proof that
		// any target inside it is locally seekable; they then native-seek past
		// the produced head instead of asking for a reanchor. Leaving the end
		// open marks the window incomplete, which with can_seek_anywhere=false
		// routes every seek back through the server.
		//
		// The media runtime is published on source.duration_seconds, which is
		// a fact about the file rather than a claim about this transport.
		plan.Timeline.SeekWindowEndSeconds = nil
		plan.Timeline.CanSeekAnywhere = false
		plan.Timeline.SeekRestoration = "source_position"
	} else {
		plan.Timeline.PlayerStartSeconds = requested
		plan.Timeline.StreamOriginSeconds = 0
		plan.Timeline.TimelineOffsetSeconds = 0
		plan.Timeline.SeekWindowStartSeconds = nil
		plan.Timeline.SeekWindowEndSeconds = nil
		plan.Timeline.CanSeekAnywhere = durationSeconds > 0
		plan.Timeline.SeekRestoration = seekRestorationPlayerV3
	}
	return seek, startSegment
}

const throughputLogKeyV3 = "throughput_kbps"

// The decode evidence a reactive software-decode fallback reports: the frame
// counter and muxed output time separate a decoder that produced no picture
// from one that produced pictures before it failed.
const (
	videoFramesLogKeyV3 = "video_frames"
	outTimeUSLogKeyV3   = "out_time_us"
)

// playbackThroughputKbpsV3 reads a plan summary's throughput_kbps: the rate
// its media arrived at, kept as a lower bound on the connection's speed. A
// value outside the probe's own bounds, which are bandwidth_estimate_kbps's,
// is not a measurement, and a kept one could not be sent as an estimate.
func playbackThroughputKbpsV3(diagnostics map[string]string) (int, bool) {
	kbps, err := strconv.Atoi(diagnostics[throughputLogKeyV3])
	if err != nil || kbps < 100 || kbps > 1_000_000 {
		return 0, false
	}
	return kbps, true
}

var diagnosticKeysV3 = map[string]struct{}{
	"played_ms": {}, "stall_count": {}, "stall_total_ms": {}, "stall_longest_ms": {}, "delivered_kbps": {}, "buffering_mode": {}, "buffering_action": {}, throughputLogKeyV3: {},
	"decoder_name": {}, "decoder_init_ms": {}, "first_frame_ms": {},
	"device_model": {}, "requested_quality": {}, "effective_quality": {},
	"pcm_recovery": {}, "retry_outcome": {}, "replan_request_id": {},
	"video_mime": {}, "video_codecs": {}, "video_width": {}, "video_height": {},
	"color_transfer": {}, "color_range": {},
	"error_code": {}, "error_code_name": {}, "error_cause": {},
	"transformation_name": {}, "transformation_version": {}, "transformation_stage": {},
	"input_dv_profile": {}, "output_dv_profile": {}, "rpu_converted_count": {},
	"rpu_failed_count": {}, "el_nal_dropped_count": {}, "sample_count": {},
	"transform_buffer_peak_bytes": {}, "requested_media_file_id": {}, "effective_media_file_id": {},
	"audio_output_mode": {}, "audio_mime": {}, "audio_channels": {}, "audio_decoder_name": {},
	"correction_id": {}, "correction_stage": {},
	"network_transport": {}, "network_metered": {}, "network_validated": {},
	bandwidthEstimateLogKeyV3: {}, linkDownstreamLogKeyV3: {},
	"target_source_position_seconds": {}, "reason": {},
}

func validRouteEventV3(event playback.RouteEventV3) bool {
	if event.ProtocolVersion != playback.ProtocolV3 || len(event.PlaybackAttemptID) < 8 || len(event.PlaybackAttemptID) > 128 || len(event.OutputContextID) > 128 || len(event.SessionID) > 128 || len(event.PlanID) > 128 || len(event.PlanAttemptID) > 128 || len(event.PlanAttemptKey) > 128 || len(event.FailureClassification) > 64 || len(event.FallbackReason) > 64 || len(event.AppliedQuirkIDs) > 16 || len(event.QuirkRegistryRevision) > 128 || len(event.Diagnostics) > 32 {
		return false
	}
	for _, id := range event.AppliedQuirkIDs {
		if len(id) == 0 || len(id) > 128 {
			return false
		}
	}
	return playback.ValidRouteEventNameV3(event.Event)
}
func sanitizeDiagnosticsV3(values map[string]string) map[string]string {
	// Iterate the approved keys, not the client map: map iteration order is
	// random, so a count-limited walk over client keys would keep an
	// arbitrary subset and drop different diagnostics on identical retries.
	result := make(map[string]string)
	for key := range diagnosticKeysV3 {
		value, ok := values[key]
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) > 256 {
			value = value[:256]
		}
		result[key] = value
	}
	return result
}

func containsStringFoldV3(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}

// containsStringExactV3 compares attempt keys byte-for-byte: they are
// case-sensitive FNV hex digests, so case-folding would treat distinct keys
// as equal.
func containsStringExactV3(values []string, wanted string) bool {
	wanted = strings.TrimSpace(wanted)
	for _, value := range values {
		if strings.TrimSpace(value) == wanted {
			return true
		}
	}
	return false
}

// withoutRotatedPlanKeysV3 drops the rejected plan's attempt keys from the
// history when a decode rejection is substituting the provider candidate.
// Attempt keys hash the recipe and delivery, not the provider bytes, so a
// replaced candidate necessarily reuses the rejected plan's key; leaving it in
// the history would terminal every rotation as adaptation_exhausted before it
// is tried. The durable exclusion chain, not this guard, keeps the undecodable
// release from being selected again.
func withoutRotatedPlanKeysV3(keys []string, failedKey, currentKey string) []string {
	if len(keys) == 0 {
		return keys
	}
	failedKey = strings.TrimSpace(failedKey)
	currentKey = strings.TrimSpace(currentKey)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" || trimmed == failedKey || trimmed == currentKey {
			continue
		}
		out = append(out, key)
	}
	return out
}

func optionalIntEqualV3(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func optionalFloatEqualV3(left, right *float64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// LocalTransformationAvailableV3 reports whether this API node's cached
// FFmpeg registry can run the named transformation. Theme conversions use it
// to decide whether the API-executed route is legal.
func (h *PlaybackHandler) LocalTransformationAvailableV3(ctx context.Context, name string) bool {
	if h == nil {
		return false
	}
	registry := h.transformationRegistryV3(ctx)
	return registry != nil && registry.Available(name)
}

func (h *PlaybackHandler) proxyEgressOriginsAvailableV3() bool {
	if h == nil || h.NodePlanner == nil {
		return false
	}
	if h.ProxyGrantStore == nil || !h.ProxyGrantStore.Enabled() {
		return false
	}
	enumerator, ok := h.NodePlanner.(proxyNodeEnumeratorV3)
	return ok && len(enumerator.ProxyNodeURLs()) > 0
}

// sessionProxyReservationReleaserV3 gives back only the proxy half of a node
// reservation, for a start that keeps its transcode node but publishes a URL the
// planned proxy does not serve. Optional: a planner without the method simply
// keeps the whole reservation until it ages out. *nodepool.Planner implements it.
type sessionProxyReservationReleaserV3 interface {
	ReleaseSessionProxy(string)
}

// revokeStaleProxyGrantOnCommitV3 drops the egress grant when an
// authorized-origins attempt commits onto a transport this server serves
// itself. A replan that moves a proxy-served session onto the API origin (or
// onto a local transcode) publishes a URL the proxy has no part in, and the
// surviving grant would keep the proxy authorized to serve the previous recipe
// for the rest of its TTL.
func (h *PlaybackHandler) revokeStaleProxyGrantOnCommitV3(ctx context.Context, sessionID string, mode mediaAuthModeV3, servedByProxy bool) {
	if !mode.proxyEgress || servedByProxy {
		return
	}
	h.deleteProxyGrantV3(ctx, sessionID)
}
