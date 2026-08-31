package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodemetrics"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/transcodeproxy"
)

// Server is the HTTP handler for proxy mode.
type Server struct {
	watcher *nodeconfig.Watcher
	tracker *nodesessions.Tracker
	// nodeRowID resolves this proxy's stable stream_nodes identity. Production
	// uses the config watcher; tests replace it to model sibling proxies.
	nodeRowID            func() (int, bool)
	httpClient           *http.Client
	artifactMissReporter remoteArtifactMissReporter
	// grants and loginSessions back the credential-free /stream/v3 routes: the
	// grant says what to serve, the login-session validator says whether the
	// caller may still have it. Both nil in a deployment that predates the
	// mode, which is why those routes answer 503 rather than assuming either.
	grants        proxyGrantLookup
	loginSessions loginSessionValidator
	egress        *egressMeter
	clientIP      *clientip.Resolver
	telemetry     *streamtelemetry.Registry
	// subCache stores full-track PGS (.sup) extracts under the transcode dir
	// so repeat selections skip the whole-file ffmpeg demux.
	subCache *playback.SubtitleCache
	// Download limits are node-local once egress is delegated. Rebuild the
	// manager when hot-reloaded settings change so existing transfers retain
	// their original limiter while new transfers use the new values.
	downloadBandwidthMu sync.Mutex
	downloadBandwidth   *downloads.BandwidthManager
	downloadServerBPS   int64
	downloadUserBPS     int64

	// capabilityHash is the last computed capability snapshot's hash, published
	// by /health without probing. Nil until the first snapshot or capability
	// request completes.
	capabilityHash atomic.Pointer[string]

	// metrics samples host and GPU resources in the background. Nil until
	// StartMetricsSampler runs, which leaves health exactly as it was before.
	metrics *nodemetrics.Sampler

	// capabilityBuildMu serializes capability assemblies with each other, so an
	// operator re-probe cannot run its ffmpeg probes beside the scheduled
	// snapshot's. The probe caches no longer coalesce the two — bumping the
	// invalidation generation is what makes the re-probe honest — so without
	// this they would genuinely run at once.
	capabilityBuildMu sync.Mutex
	// countProbesInFlight overrides the detached-probe count the re-probe route
	// refuses on. Tests set it; production leaves it nil.
	countProbesInFlight func() int
}

type remoteArtifactMissReporter interface {
	ReportRemoteArtifactMissing(ctx context.Context, artifactID, originNodeURL, originArtifactID string) error
}

// NewServer creates a new proxy server backed by a config watcher and session
// tracker.
func NewServer(watcher *nodeconfig.Watcher, tracker *nodesessions.Tracker) *Server {
	server := &Server{
		watcher: watcher,
		tracker: tracker,
		// No overall timeout — stream bodies are long-lived. Hung nodes are
		// bounded by the transport's response-header timeout instead.
		httpClient: &http.Client{
			Transport: newStreamTransport(),
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		egress: newEgressMeter(),
		subCache: playback.NewSubtitleCache(func() string {
			return watcher.Config().Playback.TranscodeDir
		}),
	}
	if watcher != nil {
		server.nodeRowID = watcher.NodeRowID
	}
	return server
}

// SetMediaGrantAuthority wires the two dependencies the credential-free
// /stream/v3 routes need: the store central writes a session's recipe to, and
// the live login-session validator this proxy re-checks every request against.
// It must be called during construction, before the server begins handling
// requests. Either argument may be nil, which leaves those routes unavailable
// (503) while the token routes keep working unchanged.
func (s *Server) SetMediaGrantAuthority(grants proxyGrantLookup, sessions loginSessionValidator) {
	s.grants = grants
	s.loginSessions = sessions
}

// SetRemoteArtifactMissReporter wires the authoritative database transition
// used when an origin returns 404 after the API's proxy preflight. It must be
// called during construction, before the server begins handling requests.
func (s *Server) SetRemoteArtifactMissReporter(reporter remoteArtifactMissReporter) {
	s.artifactMissReporter = reporter
}

// SetClientIPResolver wires trusted-proxy client IP resolution. It must be
// called during construction, before the server begins handling requests.
func (s *Server) SetClientIPResolver(resolver *clientip.Resolver) {
	s.clientIP = resolver
}

// SetStreamTelemetry wires local stream observation. A nil registry is a
// complete no-op.
func (s *Server) SetStreamTelemetry(registry *streamtelemetry.Registry) {
	s.telemetry = registry
}

// newStreamTransport tunes the proxy→transcode-node connection pool. Many
// concurrent viewers fan their segment fetches through one proxy→node pair,
// and Go's default of 2 idle connections per host causes constant connection
// churn (and TLS re-handshakes) under load. The response-header timeout
// bounds requests to a hung node; the longest legitimate server-side wait is
// the 30s manifest-readiness poll on the transcode node.
func newStreamTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 128
	t.MaxIdleConnsPerHost = 32
	t.ResponseHeaderTimeout = 60 * time.Second
	return t
}

// Handler returns the chi.Router with all proxy routes mounted.
func (s *Server) Handler() http.Handler {
	declareProxyMediaRoutes()
	r := chi.NewRouter()
	if s.clientIP != nil {
		r.Use(clientip.Middleware(s.clientIP))
	}
	// hls.js uses XHR for manifest/segment fetches which are subject to
	// CORS when the proxy runs on a different origin than the web app.
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "HEAD", "OPTIONS"},
		AllowedHeaders: []string{
			"Accept", "Authorization", "Content-Type", "Range",
			"If-Match", "If-Modified-Since", "If-None-Match", "If-Range", "If-Unmodified-Since",
		},
		// direct_stream_resume_v1 has the client re-request a byte range with
		// If-Range against the entity tag it stored. Cross-origin JavaScript
		// can only read a response header that is explicitly exposed, so
		// without these the client can send the conditional request headers
		// above but never learn the values to put in them — the resume
		// contract silently degrades to a full restart on a proxy that is on a
		// different origin than the web app, which is the normal deployment.
		ExposedHeaders: []string{
			"Accept-Ranges", "Content-Encoding", "Content-Length", "Content-Range",
			"ETag", "Last-Modified",
		},
		MaxAge: 86400,
	}))
	r.Get("/api/v1/health", s.handleHealth)
	// Unauthenticated, matching the API listener's own /metrics posture: a
	// scrape target that needs a credential is a scrape target that goes
	// unmonitored, and the exposure is host resource counters, not media.
	r.Method(http.MethodGet, "/metrics", promhttp.Handler())
	r.Group(func(r chi.Router) {
		// Streaming and download bytes count toward the node's measured egress.
		r.Use(s.meterEgress)
		r.Head("/stream/direct/{token}", observeProxy(s.telemetry, http.MethodHead, "/stream/direct/{token}", s.handleDirectPlay))
		r.Get("/stream/direct/{token}", observeProxy(s.telemetry, http.MethodGet, "/stream/direct/{token}", s.handleDirectPlay))
		r.Head("/stream/remux/{token}", observeProxy(s.telemetry, http.MethodHead, "/stream/remux/{token}", s.handleRemux))
		r.Get("/stream/remux/{token}", observeProxy(s.telemetry, http.MethodGet, "/stream/remux/{token}", s.handleRemux))
		r.Head("/stream/remux/audio-v2/{token}", observeProxy(s.telemetry, http.MethodHead, "/stream/remux/audio-v2/{token}", s.handleAudioV2Remux))
		r.Get("/stream/remux/audio-v2/{token}", observeProxy(s.telemetry, http.MethodGet, "/stream/remux/audio-v2/{token}", s.handleAudioV2Remux))
		r.Head("/stream/transcode/{token}/master.m3u8", observeProxy(s.telemetry, http.MethodHead, "/stream/transcode/{token}/master.m3u8", s.handleTranscodeManifest))
		r.Get("/stream/transcode/{token}/master.m3u8", observeProxy(s.telemetry, http.MethodGet, "/stream/transcode/{token}/master.m3u8", s.handleTranscodeManifest))
		r.Get("/stream/transcode/{token}/segment/{name}", observeProxy(s.telemetry, http.MethodGet, "/stream/transcode/{token}/segment/{name}", s.handleTranscodeSegment))
		// Credential-free grant routes (authorized_media_origins_v1). Same media
		// bytes as the token routes above, addressed by session id and
		// authorized by the caller's own Authorization header.
		r.Head("/stream/v3/{session_id}", observeProxy(s.telemetry, http.MethodHead, "/stream/v3/{session_id}", s.handleGrantIdentity))
		r.Get("/stream/v3/{session_id}", observeProxy(s.telemetry, http.MethodGet, "/stream/v3/{session_id}", s.handleGrantIdentity))
		r.Head("/stream/v3/{session_id}/master.m3u8", observeProxy(s.telemetry, http.MethodHead, "/stream/v3/{session_id}/master.m3u8", s.handleGrantTranscodeManifest))
		r.Get("/stream/v3/{session_id}/master.m3u8", observeProxy(s.telemetry, http.MethodGet, "/stream/v3/{session_id}/master.m3u8", s.handleGrantTranscodeManifest))
		r.Get("/stream/v3/{session_id}/segment/{name}", observeProxy(s.telemetry, http.MethodGet, "/stream/v3/{session_id}/segment/{name}", s.handleGrantTranscodeSegment))
		r.Get("/stream/subtitles/{token}/{track}/fonts", observeProxy(s.telemetry, http.MethodGet, "/stream/subtitles/{token}/{track}/fonts", s.handleSubtitleFonts))
		r.Get("/stream/subtitles/{token}/{track}", observeProxy(s.telemetry, http.MethodGet, "/stream/subtitles/{token}/{track}", s.handleSubtitle))
		r.Head("/downloads/file/{token}", observeProxy(s.telemetry, http.MethodHead, "/downloads/file/{token}", s.handleDownloadFile))
		r.Get("/downloads/file/{token}", observeProxy(s.telemetry, http.MethodGet, "/downloads/file/{token}", s.handleDownloadFile))
	})

	// Admin routes — bearer-auth protected.
	r.Group(func(r chi.Router) {
		r.Use(s.requireBearer)
		r.Get("/hw-capabilities", s.handleHWCapabilities)
		r.Post("/admin/force-reload", s.handleForceReload)
		r.Post("/admin/reload-config", s.handleReloadConfig)
		r.Post("/admin/reprobe-capabilities", s.handleReprobeCapabilities)
		r.Get("/status", s.handleStatus)
	})
	return r
}

// handleHWCapabilities advertises what this proxy's ffmpeg can actually do, in
// the same shape and at the same path as a transcode node.
//
// A proxy executes recipes too: /stream/remux runs ffmpeg to convert audio or
// strip a Dolby Vision RPU. Without this endpoint the API has no way to tell
// whether the proxy it just picked can run the transformations a plan froze, so
// a pool whose proxies carry a different ffmpeg build (a rolling upgrade, a
// custom image) would fail at stream time rather than at selection time.
//
// The report is deliberately hardware-free. A proxy relays bytes and runs
// identity/remux recipes; it never executes a hardware transcode, and nothing
// on the API side reads a proxy's acceleration fields. Probing them anyway cost
// every proxy a full GPU smoke-encode matrix every 15 minutes to produce an
// answer no planner consults.
func (s *Server) handleHWCapabilities(w http.ResponseWriter, r *http.Request) {
	info, err := s.buildCapabilitySnapshot(r.Context())
	if err != nil {
		// An incomplete probe would hash differently from the same ffmpeg probed
		// successfully, so serving it would announce a capability change that did
		// not happen.
		slog.WarnContext(r.Context(), "proxy capability probe incomplete", "component", "proxy", "error", err)
		http.Error(w, "capability probe unavailable", http.StatusServiceUnavailable)
		return
	}
	// A served report is as authoritative as a scheduled snapshot, so health
	// starts advertising this hash immediately rather than at the next tick.
	s.storeCapabilityHash(info.CapabilityHash)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(info); err != nil {
		slog.WarnContext(r.Context(), "encode proxy capabilities", "component", "proxy", "error", err)
	}
}

// buildCapabilitySnapshot assembles this proxy's capability report and its
// identity hash. It is the single assembly used by both the capability endpoint
// and the background snapshot, so the hash a health response advertises always
// describes the payload the endpoint would serve.
//
// An error means the probe did not finish — a caller that gave up, or an ffmpeg
// slower than the probe deadline — not that the proxy lost a capability. The
// caller must keep the previous hash rather than publish the partial report,
// exactly as a transcode node does.
func (s *Server) buildCapabilitySnapshot(ctx context.Context) (playback.HWAccelInfo, error) {
	s.capabilityBuildMu.Lock()
	defer s.capabilityBuildMu.Unlock()
	return s.buildCapabilitySnapshotLocked(ctx)
}

// buildCapabilitySnapshotLocked is buildCapabilitySnapshot's body. Callers must
// hold capabilityBuildMu; the re-probe takes it itself so its cache
// invalidation and its rebuild are one step no other builder can interleave
// with.
func (s *Server) buildCapabilitySnapshotLocked(ctx context.Context) (playback.HWAccelInfo, error) {
	ffmpegPath := ""
	if cfg := s.watcher.Config(); cfg != nil {
		ffmpegPath = cfg.Playback.FFmpegPath
	}
	// Hardware acceleration is not probed here, and the report says so rather
	// than leaving the fields unset by accident. A proxy relays streams and runs
	// identity/remux recipes on ffmpeg; it never executes a hardware transcode,
	// and the only fields planning reads off this report are Transformations and
	// TransportFeatures. So there is no hardware inventory to report and nothing
	// a GPU smoke-encode matrix could tell the planner.
	//
	// The consequence worth stating: the hash now tracks only what this proxy
	// can *do*. A reboot, a renumbered render node, or a card appearing on the
	// host no longer moves it, so the API refetches a proxy's report exactly
	// when its ffmpeg's abilities changed. Nothing derived from the host may
	// enter this report, the advertised budget below included — see
	// playback.RegistryCapabilityEndpointTimeout.
	info := playback.HWAccelInfo{
		Resolved: playback.HWAccelNone,
		Source:   "local",
	}
	// One deadline over the registry probe, matching the transcode node: it has
	// its own internal per-command bounds, but only a shared budget keeps the
	// whole rebuild inside the window a caller was told to allow, and only a
	// deadline the builder owns bounds the background snapshot, whose context
	// lives as long as the process. It is the registry-only budget — the same
	// formula the transcode node uses, with no hardware in it — and the same one
	// advertised below, so a caller's allowance and this deadline cannot drift.
	ctx, cancel := context.WithTimeout(ctx, playback.RegistryCapabilityEndpointTimeout())
	defer cancel()
	// A registry probe that ran out of budget is refused rather than published:
	// it marks transformations unavailable, which is byte-identical to an ffmpeg
	// that genuinely cannot run them, so hashing it would announce a change that
	// did not happen and drop this proxy out of remux eligibility.
	registry, err := playback.ProbeTransformationRegistryWithToneMapV3Result(ctx, ffmpegPath, nil)
	if err != nil {
		return playback.HWAccelInfo{}, err
	}
	info.Transformations = registry.Advertised()
	info.TransportFeatures = []string{playback.TransportFeatureProgressiveRemuxRelayV1}
	// Advertised before the hash is taken, because it is part of what the hash
	// covers: a build that needs longer reaches the sweep rather than sitting
	// behind an unchanged identity.
	info.ProbeRequestTimeoutMillis = playback.RegistryCapabilityRequestTimeout().Milliseconds()
	info.CapabilityHash = playback.ComputeCapabilityHash(info)
	return info, nil
}

// capabilitySnapshotInterval is how often the proxy recomputes its capability
// snapshot. It exists to notice the ffmpeg underneath a long-running proxy
// changing — a swapped binary, a rolling image upgrade — without waiting for a
// restart. The transformation registry re-execs ffmpeg every time, which is the
// other reason a snapshot that did not finish must not be published.
const capabilitySnapshotInterval = 15 * time.Minute

// StartCapabilitySnapshots keeps the capability hash published by /health
// current, in the background, until ctx is canceled.
func (s *Server) StartCapabilitySnapshots(ctx context.Context) {
	if s == nil || ctx == nil {
		return
	}
	go func() {
		s.refreshCapabilitySnapshot(ctx)
		ticker := time.NewTicker(capabilitySnapshotInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.refreshCapabilitySnapshot(ctx)
			}
		}
	}()
}

func (s *Server) refreshCapabilitySnapshot(ctx context.Context) {
	info, err := s.buildCapabilitySnapshot(ctx)
	if err != nil {
		// Keep the previous hash: a failed probe is not evidence this proxy's
		// ffmpeg changed, and republishing a degraded one would make the API
		// refetch a report that lost nothing.
		slog.WarnContext(ctx, "proxy capability snapshot incomplete", "component", "proxy", "error", err)
		return
	}
	if previous := s.storedCapabilityHash(); previous != "" && previous != info.CapabilityHash {
		slog.InfoContext(ctx, "proxy capabilities changed", "component", "proxy",
			"previous_hash", previous, "hash", info.CapabilityHash, "resolved", info.Resolved)
	}
	s.storeCapabilityHash(info.CapabilityHash)
}

// storedCapabilityHash returns the last published capability hash, or empty
// when none has been computed yet.
func (s *Server) storedCapabilityHash() string {
	if hash := s.capabilityHash.Load(); hash != nil {
		return *hash
	}
	return ""
}

func (s *Server) storeCapabilityHash(hash string) {
	s.capabilityHash.Store(&hash)
}

type healthResponse struct {
	Status     string `json:"status"`
	ActiveJobs int    `json:"active_jobs"`
	EgressKbps int    `json:"egress_kbps"`
	// CapabilitiesHash identifies this proxy's last computed capability
	// snapshot. It is read from the stored snapshot only — health must stay a
	// cheap liveness answer, so it never triggers a probe — and is empty until
	// the first background snapshot completes.
	CapabilitiesHash string `json:"capabilities_hash,omitempty"`
	// System and GPU are this proxy's last resource sample, read from the
	// published snapshot for the same reason as the hash above. A proxy runs
	// ffmpeg too (remux, Dolby Vision RPU strip), so it reports GPU usage on the
	// same code path a transcode node does.
	//
	// This route takes no credential, so the sample is path-free: disk entries
	// carry their role and their fill, never where they are mounted. See
	// nodemetrics.Snapshot.RedactPaths.
	System *nodemetrics.SystemStats `json:"system,omitempty"`
	GPU    []nodemetrics.GPUStats   `json:"gpu,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	activeJobs := 0
	if s.tracker != nil {
		activeJobs = s.tracker.ActiveCount()
	}
	snapshot := s.metrics.Snapshot().RedactPaths()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(healthResponse{
		Status:           "ok",
		ActiveJobs:       activeJobs,
		EgressKbps:       s.egress.RateKbps(),
		CapabilitiesHash: s.storedCapabilityHash(),
		System:           snapshot.System,
		GPU:              snapshot.GPU,
	})
}

// StartMetricsSampler begins background resource sampling until ctx is
// canceled, and publishes the readings on /health, /status and /metrics.
//
// A proxy's only working directory is the subtitle/remux scratch under the
// configured transcode dir, so that is the mount it samples; media roots belong
// to the API host, which is the process that knows what the library is.
func (s *Server) StartMetricsSampler(ctx context.Context) {
	if s == nil || ctx == nil {
		return
	}
	// Read per sample, not captured here: playback.transcode_dir is
	// hot-reloadable, and a proxy that snapshotted it at startup would go on
	// measuring a volume nothing writes to.
	scratchDir := func() string {
		if s.watcher == nil {
			return ""
		}
		cfg := s.watcher.Config()
		if cfg == nil {
			return ""
		}
		return cfg.Playback.TranscodeDir
	}
	s.metrics = nodemetrics.NewSampler(nodemetrics.Options{
		ScratchDir:       scratchDir,
		DeviceSessions:   playback.HWDeviceLoadSnapshot,
		DeviceIdentities: playback.SamplerDeviceIdentities,
	})
	s.metrics.Start(ctx)
}

// requireBearer checks Authorization: Bearer {secret} for admin endpoints.
func (s *Server) requireBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.watcher.Config()
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != cfg.Auth.JWTSecret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// verifyToken extracts and validates the stream token from the URL.
func (s *Server) verifyToken(w http.ResponseWriter, r *http.Request) *streamtoken.Claims {
	cfg := s.watcher.Config()
	tokenStr := chi.URLParam(r, "token")
	claims, err := streamtoken.Verify(tokenStr, cfg.Auth.JWTSecret)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil
	}
	return claims
}

func (s *Server) verifyPlaybackToken(w http.ResponseWriter, r *http.Request) *streamtoken.Claims {
	claims := s.verifyToken(w, r)
	if claims == nil {
		return nil
	}
	nodeID, nodeIDKnown := s.currentNodeRowID()
	if status := proxyEgressStatusV3(
		claims.RoutingWorkload, claims.RoutingExecution, claims.RoutingEgress,
		claims.RoutingEgressNodeID, nodeID, nodeIDKnown,
	); status != 0 {
		writeProxyRouteStatusV3(w, status)
		return nil
	}
	return claims
}

// proxyEgressStatusV3 enforces the media origin frozen into a routed playback
// artifact. An entirely empty tuple predates node routing and remains usable
// until its bounded token/grant lifetime expires; a partial tuple is an
// uncommitted route and must not fail open on any proxy media endpoint.
func proxyEgressStatusV3(
	workload, execution, egress string,
	egressNodeID, currentNodeID int,
	currentNodeIDKnown bool,
) int {
	workload = strings.TrimSpace(workload)
	execution = strings.TrimSpace(execution)
	egress = strings.TrimSpace(egress)
	if workload == "" && execution == "" && egress == "" && egressNodeID == 0 {
		return 0
	}
	if workload == "" || execution == "" || egress == "" || egressNodeID < 0 {
		return http.StatusConflict
	}
	if egress != string(noderouting.EgressProxy) {
		return http.StatusServiceUnavailable
	}
	if egressNodeID == 0 {
		return http.StatusConflict
	}
	if !currentNodeIDKnown || currentNodeID <= 0 || egressNodeID != currentNodeID {
		return http.StatusServiceUnavailable
	}
	return 0
}

func (s *Server) currentNodeRowID() (int, bool) {
	if s == nil || s.nodeRowID == nil {
		return 0, false
	}
	return s.nodeRowID()
}

type proxyPlaybackEndpointV3 uint8

const (
	proxyPlaybackEndpointDirectV3 proxyPlaybackEndpointV3 = iota
	proxyPlaybackEndpointRemuxV3
	proxyPlaybackEndpointTranscodeV3
	proxyPlaybackEndpointIdentityV3
	proxyPlaybackEndpointAuxiliaryV3
)

// proxyPlaybackEndpointStatusV3 binds a valid proxy artifact to the serving
// recipe family. A signed direct-play token is not authority to start remux or
// transcode work merely because every endpoint shares the same signing key.
func proxyPlaybackEndpointStatusV3(claims *streamtoken.Claims, endpoint proxyPlaybackEndpointV3) int {
	if claims == nil {
		return http.StatusServiceUnavailable
	}
	direct := claims.RoutingWorkload == string(noderouting.WorkloadDirectPlay) &&
		claims.RoutingExecution == string(noderouting.ExecutionNone) &&
		claims.RoutingEgress == string(noderouting.EgressProxy) &&
		claims.PlayMethod == string(playback.PlayDirect)
	remuxExecution := claims.RoutingExecution == string(noderouting.ExecutionProxy) ||
		claims.RoutingExecution == string(noderouting.ExecutionTranscode)
	remux := claims.RoutingWorkload == string(noderouting.WorkloadRemux) &&
		remuxExecution &&
		claims.RoutingEgress == string(noderouting.EgressProxy) &&
		proxyRemuxPlayMethodV3(claims.PlayMethod)
	transcode := (claims.RoutingWorkload == string(noderouting.WorkloadRemux) ||
		claims.RoutingWorkload == string(noderouting.WorkloadVideoTranscode)) &&
		claims.RoutingExecution == string(noderouting.ExecutionTranscode) &&
		claims.RoutingEgress == string(noderouting.EgressProxy) &&
		proxyTranscodePlayMethodV3(claims.PlayMethod)

	// Tokens from the released pre-routing server have no tuple. They retain
	// only their original method authority during the bounded token lifetime.
	legacy := claims.RoutingWorkload == "" && claims.RoutingExecution == "" &&
		claims.RoutingEgress == "" && claims.RoutingEgressNodeID == 0
	if legacy {
		direct = claims.PlayMethod == string(playback.PlayDirect)
		remux = proxyRemuxPlayMethodV3(claims.PlayMethod)
		transcode = proxyTranscodePlayMethodV3(claims.PlayMethod)
	}

	allowed := false
	switch endpoint {
	case proxyPlaybackEndpointDirectV3:
		allowed = direct
	case proxyPlaybackEndpointRemuxV3:
		allowed = remux
	case proxyPlaybackEndpointTranscodeV3:
		allowed = transcode
	case proxyPlaybackEndpointIdentityV3:
		allowed = direct || remux
	case proxyPlaybackEndpointAuxiliaryV3:
		allowed = direct || remux || transcode
	}
	if !allowed {
		return http.StatusServiceUnavailable
	}
	return 0
}

func proxyRemuxPlayMethodV3(method string) bool {
	return method == string(playback.PlayRemux) || method == streamtoken.PlayMethodAudioDownmixRemux
}

func proxyTranscodePlayMethodV3(method string) bool {
	switch method {
	case "", string(playback.PlayTranscode), streamtoken.PlayMethodToneMapTranscode,
		streamtoken.PlayMethodAudioDownmixTranscode, streamtoken.PlayMethodCopyFMP4Transcode:
		return true
	default:
		return false
	}
}

func requireProxyPlaybackEndpointV3(w http.ResponseWriter, claims *streamtoken.Claims, endpoint proxyPlaybackEndpointV3) bool {
	status := proxyPlaybackEndpointStatusV3(claims, endpoint)
	if status != 0 {
		writeProxyRouteStatusV3(w, status)
		return false
	}
	return true
}

func writeProxyRouteStatusV3(w http.ResponseWriter, status int) {
	switch status {
	case http.StatusConflict:
		http.Error(w, "playback route unbound", status)
	default:
		http.Error(w, "routing policy unsatisfied", status)
	}
}

func (s *Server) handleDirectPlay(w http.ResponseWriter, r *http.Request) {
	claims := s.verifyPlaybackToken(w, r)
	if claims == nil || !requireProxyPlaybackEndpointV3(w, claims, proxyPlaybackEndpointDirectV3) {
		return
	}
	s.serveDirectPlayClaims(w, r, claims)
}

// serveDirectPlayClaims serves a direct-play session from an already-authorized
// recipe. The token routes reach it with claims they verified; the grant routes
// reach it with the same claims projected from a grant they authorized against
// the caller's login session — the serving behavior must not differ.
func (s *Server) serveDirectPlayClaims(w http.ResponseWriter, r *http.Request, claims *streamtoken.Claims) {
	// Attach here rather than at the call sites so both the token routes and the
	// grant routes attribute their bytes to the viewer.
	attachStream(r.Context(), claims)
	info := sessionInfo(s.tracker, claims, "direct_play")
	s.tracker.Track(r.Context(), info)
	defer s.tracker.Remove(r.Context(), claims.SessionID)

	// Serve through the same path the integrated server uses rather than a bare
	// http.ServeFile: direct_stream_resume_v1 requires the strong ETag that
	// ServeDirectPlay sets before ServeContent (ServeFile sets none, so
	// If-Range never validates and a resumed range silently restarts at 200),
	// and it carries the rolling write deadline and stream metrics with it.
	_ = playback.ServeDirectPlay(w, r, claims.MediaPath)
}

func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	claims := s.verifyToken(w, r)
	if claims == nil {
		return
	}
	remoteArtifact := claims.DownloadArtifactID != "" && strings.TrimSpace(claims.TranscodeNode) != ""
	attestedRemote := claims.PlayMethod == streamtoken.PlayMethodToneMapDownload
	if (claims.PlayMethod != streamtoken.PlayMethodDownload && !attestedRemote) ||
		(strings.TrimSpace(claims.MediaPath) == "" && !remoteArtifact) ||
		(attestedRemote && (!remoteArtifact || claims.DownloadArtifactSize <= 0 || strings.TrimSpace(claims.DownloadExecutionFingerprint) == "")) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !remoteArtifact {
		attachTransfer(r.Context(), claims)
	}

	// HEAD is a capability/path preflight, not an active transfer. Counting it
	// would briefly consume job capacity and could make a health report retire
	// the API's reservation before the client starts its GET.
	if s.tracker != nil && r.Method != http.MethodHead {
		info := sessionInfo(s.tracker, claims, "download")
		s.tracker.Track(r.Context(), info)
		defer s.tracker.Remove(context.WithoutCancel(r.Context()), claims.SessionID)
	}
	if remoteArtifact {
		s.relayDownloadArtifact(w, r, claims)
		return
	}

	f, err := os.Open(claims.MediaPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	stat, err := f.Stat()
	if err != nil {
		http.Error(w, "download unavailable", http.StatusInternalServerError)
		return
	}

	filename := filepath.Base(claims.MediaPath)
	if disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filename}); disposition != "" {
		w.Header().Set("Content-Disposition", disposition)
	}
	w.Header().Set("Content-Type", playback.MimeFromExtension(claims.MediaPath))
	reader := io.ReadSeeker(f)
	if bandwidth := s.downloadBandwidthManager(); bandwidth != nil {
		reader = bandwidth.ThrottledReader(r.Context(), f, claims.UserID)
	}
	http.ServeContent(w, r, stat.Name(), stat.ModTime(), reader)
}

func (s *Server) relayDownloadArtifact(w http.ResponseWriter, r *http.Request, claims *streamtoken.Claims) {
	if !downloadprepare.ValidArtifactID(claims.DownloadArtifactID) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	attachTransfer(r.Context(), claims)
	cfg := s.watcher.Config()
	if cfg == nil || strings.TrimSpace(cfg.Auth.JWTSecret) == "" {
		http.Error(w, "download unavailable", http.StatusServiceUnavailable)
		return
	}
	client := downloadprepare.HTTPPreparer{Client: s.httpClient}
	resp, err := client.Open(r.Context(), claims.TranscodeNode, cfg.Auth.JWTSecret, claims.DownloadArtifactID, r.Method, r.Header)
	if err != nil {
		slog.WarnContext(r.Context(), "download artifact relay failed", "component", "proxy", "artifact_id", claims.DownloadArtifactID, "node", claims.TranscodeNode, "error", err)
		http.Error(w, "download unavailable", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		if s.artifactMissReporter != nil && strings.TrimSpace(claims.DownloadArtifactRowID) != "" {
			reportCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
			err := s.artifactMissReporter.ReportRemoteArtifactMissing(
				reportCtx, claims.DownloadArtifactRowID, claims.TranscodeNode, claims.DownloadArtifactID,
			)
			cancel()
			if err != nil {
				slog.WarnContext(r.Context(), "report missing remote download artifact", "component", "proxy", "artifact_id", claims.DownloadArtifactRowID, "error", err)
			}
		}
		http.NotFound(w, r)
		return
	}
	if !downloadprepare.RelayStatusAllowed(resp.StatusCode) {
		http.Error(w, "download unavailable", http.StatusBadGateway)
		return
	}
	if claims.PlayMethod == streamtoken.PlayMethodToneMapDownload {
		attestation, attestationErr := downloadprepare.ResultFromHeaders(resp.Header)
		if attestationErr != nil || attestation.ExecutionFingerprint != claims.DownloadExecutionFingerprint || attestation.FileSize != claims.DownloadArtifactSize {
			if s.artifactMissReporter != nil && strings.TrimSpace(claims.DownloadArtifactRowID) != "" {
				reportCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
				_ = s.artifactMissReporter.ReportRemoteArtifactMissing(reportCtx, claims.DownloadArtifactRowID, claims.TranscodeNode, claims.DownloadArtifactID)
				cancel()
			}
			http.Error(w, "download unavailable", http.StatusBadGateway)
			return
		}
	}
	downloadprepare.CopyResponseHeaders(w.Header(), resp.Header)
	if filename := filepath.Base(strings.TrimSpace(claims.DownloadFilename)); filename != "" && filename != "." {
		if disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filename}); disposition != "" {
			w.Header().Set("Content-Disposition", disposition)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	var reader io.Reader = resp.Body
	if bandwidth := s.downloadBandwidthManager(); bandwidth != nil {
		reader = bandwidth.ThrottledStreamReader(r.Context(), reader, claims.UserID)
	}
	if _, err := io.Copy(w, reader); err != nil && r.Context().Err() == nil {
		slog.WarnContext(r.Context(), "download artifact relay interrupted", "component", "proxy", "artifact_id", claims.DownloadArtifactID, "error", err)
	}
}

func (s *Server) downloadBandwidthManager() *downloads.BandwidthManager {
	cfg := s.watcher.Config()
	if cfg == nil {
		return nil
	}
	serverBPS := cfg.Download.ServerBandwidthBPS
	userBPS := cfg.Download.UserBandwidthBPS
	s.downloadBandwidthMu.Lock()
	defer s.downloadBandwidthMu.Unlock()
	if s.downloadBandwidth == nil || serverBPS != s.downloadServerBPS || userBPS != s.downloadUserBPS {
		s.downloadBandwidth = downloads.NewBandwidthManager(serverBPS, userBPS)
		s.downloadServerBPS = serverBPS
		s.downloadUserBPS = userBPS
	}
	return s.downloadBandwidth
}

func (s *Server) handleRemux(w http.ResponseWriter, r *http.Request) {
	claims := s.verifyPlaybackToken(w, r)
	if claims == nil || !requireProxyPlaybackEndpointV3(w, claims, proxyPlaybackEndpointRemuxV3) {
		return
	}
	// A boosted recipe must use the versioned route below. Keeping it off the
	// legacy route makes the URL itself part of rolling-upgrade negotiation: a
	// pre-v2 proxy has no matching endpoint and therefore cannot silently run
	// the old quiet downmix after a stale capability probe.
	if claims.PlayMethod == streamtoken.PlayMethodAudioDownmixRemux {
		http.NotFound(w, r)
		return
	}
	if remuxRunsOnTranscodeNodeV3(claims) {
		s.relayProgressiveRemux(w, r, claims, chi.URLParam(r, "token"))
		return
	}
	s.serveRemuxClaims(w, r, claims)
}

func (s *Server) handleAudioV2Remux(w http.ResponseWriter, r *http.Request) {
	claims := s.verifyPlaybackToken(w, r)
	if claims == nil || !requireProxyPlaybackEndpointV3(w, claims, proxyPlaybackEndpointRemuxV3) {
		return
	}
	// The endpoint attests the audio_to_aac v2 execution contract, not merely a
	// second spelling of the legacy route. Reject an ordinary or internally
	// inconsistent token before FFmpeg opens the source.
	if !validAudioV2RemuxClaims(claims) {
		http.NotFound(w, r)
		return
	}
	if remuxRunsOnTranscodeNodeV3(claims) {
		s.relayProgressiveRemux(w, r, claims, chi.URLParam(r, "token"))
		return
	}
	s.serveRemuxClaims(w, r, claims)
}

func remuxRunsOnTranscodeNodeV3(claims *streamtoken.Claims) bool {
	return claims != nil && claims.RoutingExecution == string(noderouting.ExecutionTranscode)
}

func (s *Server) relayProgressiveRemux(w http.ResponseWriter, r *http.Request, claims *streamtoken.Claims, forwardToken string) {
	attachStream(r.Context(), claims)
	info := sessionInfo(s.tracker, claims, "remux")
	s.tracker.Track(r.Context(), info)
	defer s.tracker.Remove(r.Context(), claims.SessionID)
	s.proxyToTranscodeNode(w, r, claims, "/remux/"+transcodeTransportIDFromClaims(claims), forwardToken)
}

// validAudioV2RemuxClaims proves the complete shape consumed by the proxy's
// fixed AAC remux path. The versioned URL is not a general remux alias.
func validAudioV2RemuxClaims(claims *streamtoken.Claims) bool {
	return claims != nil &&
		claims.PlayMethod == streamtoken.PlayMethodAudioDownmixRemux &&
		claims.TranscodeAudio &&
		playback.IsAudioToAACStereoDownmixV3(claims.SourceAudioChannels, claims.TargetCodecAudio, claims.TargetAudioChannels) &&
		claims.TargetAudioChannels == 2
}

// serveRemuxClaims serves a progressive remux from an already-authorized
// recipe, shared by the token routes and the grant routes for the same reason
// serveDirectPlayClaims is.
func (s *Server) serveRemuxClaims(w http.ResponseWriter, r *http.Request, claims *streamtoken.Claims) {
	// See serveDirectPlayClaims: shared by the token and grant routes.
	attachStream(r.Context(), claims)
	info := sessionInfo(s.tracker, claims, "remux")
	s.tracker.Track(r.Context(), info)
	defer s.tracker.Remove(r.Context(), claims.SessionID)

	seekSeconds := 0.0
	if seekStr := r.URL.Query().Get("seek"); seekStr != "" {
		if v, err := strconv.ParseFloat(seekStr, 64); err == nil {
			seekSeconds = v
		}
	}
	// Honor the Dolby Vision mode frozen in the token (empty decodes as the
	// legacy auto behavior for old tokens), mirroring how the integrated
	// server's stream handler serves the same claims.
	_ = playback.ServeRemuxWithOptions(w, r, claims.MediaPath, "mp4", seekSeconds, claims.TranscodeAudio, claims.AudioTrackIndex, claims.DVProfile, playback.RemuxServeOptions{
		DVMode:                 playback.RemuxDVMode(claims.RemuxDVMode),
		FFmpegPath:             s.watcher.Config().Playback.FFmpegPath,
		ContentType:            playback.RemuxContentType(claims.AudioOnly),
		AudioOnly:              claims.AudioOnly,
		SourceAudioChannels:    claims.SourceAudioChannels,
		TargetAudioChannels:    claims.TargetAudioChannels,
		TargetAudioBitrateKbps: claims.TargetAudioBitrateKbps,
	})
}

func (s *Server) handleTranscodeManifest(w http.ResponseWriter, r *http.Request) {
	claims := s.verifyPlaybackToken(w, r)
	if claims == nil || !requireProxyPlaybackEndpointV3(w, claims, proxyPlaybackEndpointTranscodeV3) {
		return
	}
	attachStream(r.Context(), claims)
	s.touchTranscodeSession(r, claims)
	s.proxyToTranscodeNode(w, r, claims, "/transcode/"+transcodeTransportIDFromClaims(claims)+"/master.m3u8", chi.URLParam(r, "token"))
}

func (s *Server) handleTranscodeSegment(w http.ResponseWriter, r *http.Request) {
	claims := s.verifyPlaybackToken(w, r)
	if claims == nil || !requireProxyPlaybackEndpointV3(w, claims, proxyPlaybackEndpointTranscodeV3) {
		return
	}
	attachStream(r.Context(), claims)
	s.touchTranscodeSession(r, claims)
	name := chi.URLParam(r, "name")
	s.proxyToTranscodeNode(w, r, claims, "/transcode/"+transcodeTransportIDFromClaims(claims)+"/segment/"+name, chi.URLParam(r, "token"))
}

func transcodeTransportIDFromClaims(claims *streamtoken.Claims) string {
	if claims != nil && claims.TranscodeTransportID != "" {
		return claims.TranscodeTransportID
	}
	if claims == nil {
		return ""
	}
	return claims.SessionID
}

// touchTranscodeSession keeps HLS sessions visible in the active stream count.
// Unlike direct play and remux, transcode playback reaches the proxy as many
// short manifest/segment requests, so the session is tracked by recent
// activity instead of request lifetime.
func (s *Server) touchTranscodeSession(r *http.Request, claims *streamtoken.Claims) {
	s.tracker.Touch(r.Context(), sessionInfo(s.tracker, claims, "transcode"))
}

// sessionInfo builds the node-session tracker record for a verified token,
// copying the numeric ownership keys the node-session tracker needs.
func sessionInfo(tr *nodesessions.Tracker, claims *streamtoken.Claims, kind string) nodesessions.SessionInfo {
	startedAt, source := claims.StartedAt()
	if source == streamtoken.StartedAtSourceNone {
		startedAt = time.Now().UTC()
	}
	return nodesessions.SessionInfo{
		SessionID:         claims.SessionID,
		NodeURL:           tr.NodeURL(),
		NodeName:          tr.NodeName(),
		Type:              kind,
		StartedAt:         startedAt.Format(time.RFC3339),
		StartedAtUnixNano: startedAt.UnixNano(),
		StartedAtSource:   string(source),
		AuthUserID:        claims.UserID,
		ProfileID:         claims.ProfileID,
		MediaFileID:       claims.MediaFileID,
	}
}

func (s *Server) handleSubtitle(w http.ResponseWriter, r *http.Request) {
	claims := s.verifyPlaybackToken(w, r)
	if claims == nil || !requireProxyPlaybackEndpointV3(w, claims, proxyPlaybackEndpointAuxiliaryV3) {
		return
	}
	attachStream(r.Context(), claims)
	cfg := s.watcher.Config()
	trackParam := chi.URLParam(r, "track")
	trackIndex, requestedFormat, err := playback.ParseSubtitleTrackParam(trackParam)
	if err != nil {
		http.Error(w, "invalid subtitle index", http.StatusBadRequest)
		return
	}

	// When the URL requests SUP format (e.g. /subtitles/{token}/2.sup),
	// serve the PGS track as a raw .sup elementary stream for client-side
	// bitmap rendering (libpgs). Unlike the buffered text paths below, this
	// serves the cached full-track extract when present, and otherwise
	// streams ffmpeg output directly (the client renders progressively as
	// data arrives) while teeing it into the cache for the next request.
	// Clients that manage their own sliding window opt in with ?windowed=1
	// (+ ?position=/?duration=), mirroring the API stream handler; windowed
	// requests extract only the requested slice — from the cached full
	// track when one exists (warming it in the background when not).
	if requestedFormat == "sup" {
		allowWindow, seek, duration := playback.PGSWindowRequest(r.URL.Query())
		err := s.subCache.ServeSUPExtract(w, r, playback.StreamExtractOpts{
			InputPath:       claims.MediaPath,
			TrackIndex:      trackIndex,
			SourceCodec:     "hdmv_pgs_subtitle", // .sup URLs are only generated for PGS tracks
			SeekSeconds:     seek,
			DurationSeconds: duration,
			AllowWindow:     allowWindow,
			FFmpegPath:      cfg.Playback.FFmpegPath,
		}, playback.StreamExtractSubtitle)
		if err != nil && r.Context().Err() == nil {
			// Headers already committed — log and let the client see a
			// truncated response.
			slog.ErrorContext(r.Context(), "stream subtitle (sup)", "component", "proxy", "error", err, "track", trackIndex,
				"path", claims.MediaPath, "playback_session_id", claims.SessionID)
		}
		return
	}

	// When the URL requests ASS format (e.g. /subtitles/{token}/2.ass),
	// extract as raw ASS to preserve styling for client-side rendering.
	if requestedFormat == "ass" {
		data, err := playback.ExtractSubtitleWithFormat(r.Context(), claims.MediaPath, trackIndex, "ass", cfg.Playback.FFmpegPath)
		if err != nil {
			slog.ErrorContext(r.Context(), "extract subtitle (ass)", "component", "proxy", "error", err, "track", trackIndex, "path", claims.MediaPath, "playback_session_id", claims.SessionID)
			http.Error(w, "subtitle extraction failed", http.StatusInternalServerError)
			return
		}
		playback.ServeSubtitle(w, data, "ass")
		return
	}

	data, format, err := playback.ExtractSubtitle(r.Context(), claims.MediaPath, trackIndex, cfg.Playback.FFmpegPath)
	if err != nil {
		slog.ErrorContext(r.Context(), "extract subtitle", "component", "proxy", "error", err, "track", trackIndex, "path", claims.MediaPath, "playback_session_id", claims.SessionID)
		http.Error(w, "subtitle extraction failed", http.StatusInternalServerError)
		return
	}

	vtt, err := playback.ConvertToVTT(data, format)
	if err != nil {
		slog.ErrorContext(r.Context(), "convert to vtt", "component", "proxy", "error", err, "playback_session_id", claims.SessionID)
		http.Error(w, "subtitle conversion failed", http.StatusInternalServerError)
		return
	}

	playback.ServeSubtitle(w, vtt, "vtt")
}

func (s *Server) handleSubtitleFonts(w http.ResponseWriter, r *http.Request) {
	claims := s.verifyPlaybackToken(w, r)
	if claims == nil || !requireProxyPlaybackEndpointV3(w, claims, proxyPlaybackEndpointAuxiliaryV3) {
		return
	}
	attachStream(r.Context(), claims)
	cfg := s.watcher.Config()
	trackParam := chi.URLParam(r, "track")
	trackIndex, _, err := playback.ParseSubtitleTrackParam(trackParam)
	if err != nil {
		http.Error(w, "invalid subtitle index", http.StatusBadRequest)
		return
	}

	fonts, err := playback.ExtractAttachedSubtitleFonts(r.Context(), claims.MediaPath, cfg.Playback.FFmpegPath)
	if err != nil {
		slog.ErrorContext(r.Context(), "extract subtitle fonts", "component", "proxy", "error", err, "track", trackIndex, "path", claims.MediaPath, "playback_session_id", claims.SessionID)
		http.Error(w, "subtitle font extraction failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(playback.EncodeSubtitleFontBundle(fonts)); err != nil {
		slog.WarnContext(r.Context(), "subtitle font response encode failed", "component", "proxy", "error", err, "playback_session_id", claims.SessionID)
	}
}

// proxyToTranscodeNode forwards the request to the transcode node specified in
// the claims. forwardToken is the stream token handed to the node out of band
// (never to the client): the client's own token on a token route, a
// proxy-minted one on a grant route.
func (s *Server) proxyToTranscodeNode(w http.ResponseWriter, r *http.Request, claims *streamtoken.Claims, path, forwardToken string) {
	cfg := s.watcher.Config()
	if claims.TranscodeNode == "" {
		http.Error(w, "no transcode node in token", http.StatusBadRequest)
		return
	}

	targetURL := claims.TranscodeNode + path
	isSegmentRoute := strings.Contains(path, "/segment/")
	_, segmentParseErr := playback.ParseSegmentNumber(filepath.Base(path))
	isMediaSegment := segmentParseErr == nil
	if rawQuery := r.URL.RawQuery; rawQuery != "" {
		targetURL = fmt.Sprintf("%s?%s", targetURL, rawQuery)
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, nil)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Auth.JWTSecret)
	if isSegmentRoute {
		transcodeproxy.PrepareRequest(req, r)
	}
	// Forward the verified stream token so the transcode node can self-reconstruct
	// a lost session after its OWN restart: the token carries the full byte-affecting
	// recipe, so the node can re-spawn ffmpeg seeked to the requested segment instead
	// of 404ing (the integrated server already does this from the same token). The
	// node re-verifies the token independently before trusting it.
	if forwardToken != "" {
		req.Header.Set("X-Silo-Stream-Token", forwardToken)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		slog.ErrorContext(r.Context(), "proxy to transcode node", "component", "proxy", "error", err, "url", targetURL, "playback_session_id", claims.SessionID)
		http.Error(w, "transcode node unavailable", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	generation := resp.Header.Get(transcodeproxy.GenerationHeader)
	transcodeproxy.CopyResponseHeaders(w.Header(), resp.Header)
	sw := httpstream.NewRollingDeadlineWriter(w)
	sw.WriteHeader(resp.StatusCode)
	if _, copyErr := io.Copy(sw, resp.Body); copyErr != nil {
		return
	}
	if isMediaSegment && generation != "" && r.Method == http.MethodGet &&
		sw.CompletedFullResponse(transcodeproxy.FullRepresentationSize(resp)) {
		if ackErr := transcodeproxy.Acknowledge(r.Context(), s.httpClient, claims.TranscodeNode+path, cfg.Auth.JWTSecret, generation); ackErr != nil {
			slog.WarnContext(r.Context(), "acknowledge transcode segment completion", "component", "proxy", "error", ackErr, "playback_session_id", claims.SessionID)
		}
	}
}

// handleReloadConfig re-reads this proxy's configuration. A proxy's force
// reload is already config-only — it holds no transcode sessions to tear down —
// so this is the same work under the name the control plane uses on both node
// types, which saves the API branching on node type for its own housekeeping.
func (s *Server) handleReloadConfig(w http.ResponseWriter, r *http.Request) {
	s.handleForceReload(w, r)
}

func (s *Server) handleForceReload(w http.ResponseWriter, r *http.Request) {
	if err := s.watcher.ForceReload(r.Context()); err != nil {
		http.Error(w, "reload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	slog.InfoContext(r.Context(), "proxy force reload completed", slog.String("component", "proxy"))
	w.WriteHeader(http.StatusNoContent)
}

type statusResponse struct {
	ActiveSessions int                      `json:"active_sessions"`
	System         *nodemetrics.SystemStats `json:"system,omitempty"`
	GPU            []nodemetrics.GPUStats   `json:"gpu,omitempty"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	// NewServer accepts a nil tracker and handleHealth already tolerates one;
	// this must too, or the same construction that answers /health panics here.
	activeSessions := 0
	if s.tracker != nil {
		activeSessions = s.tracker.ActiveCount()
	}
	snapshot := s.metrics.Snapshot()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(statusResponse{
		ActiveSessions: activeSessions,
		System:         snapshot.System,
		GPU:            snapshot.GPU,
	})
}
