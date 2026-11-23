package nodecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/logthrottle"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/opslog"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

// NodeDirectory reads stream node rows. *nodepool.Repository implements it.
type NodeDirectory interface {
	GetByID(ctx context.Context, id int) (*nodepool.Node, error)
}

// IdentitySource answers a node's identity from memory. *nodepool.KeyRegistry
// implements it.
type IdentitySource interface {
	Identity(id int) (nodepool.Identity, bool)
}

// LogSink stores a batch of a node's log entries under nodeID, whatever node
// each entry names. *opslog.Consumer implements it.
type LogSink interface {
	InsertNodeBatch(ctx context.Context, nodeID string, entries []opslog.Entry) error
}

// runsFile reports whether session is a remote transcode this server started
// on the reporting node (at reportingNodeURL) for the media file it names:
// the playback session it holds runs that transport on that node and plays
// that file. It also returns when that playback session started, which no
// run of its transcode precedes. A node's stream end lowers a stored duration
// only then, so a node cannot shorten a file it is not transcoding. A session
// another API server holds is left to the reports that server receives.
func (s *Server) runsFile(reportingNodeURL string, session nodesessions.SessionInfo) (time.Time, bool) {
	if s.opts.Transcodes == nil || session.MediaFileID <= 0 || reportingNodeURL == "" {
		return time.Time{}, false
	}
	nodeURL, fileID, startedAt, ok := s.opts.Transcodes.RemoteTranscode(session.SessionID)
	return startedAt, ok && fileID == session.MediaFileID && nodepool.CanonicalNodeURL(reportingNodeURL) == nodepool.CanonicalNodeURL(nodeURL)
}

// TranscodeSource finds the remote transcode a playback session this server
// holds runs as a transport. *playback.SessionManager implements it.
type TranscodeSource interface {
	RemoteTranscode(transportID string) (nodeURL string, mediaFileID int, startedAt time.Time, ok bool)
}

// StreamEndNoter stores where a transcode found a media file's media ends
// (playback.StreamEndRecorder).
type StreamEndNoter interface {
	Note(ctx context.Context, mediaFileID int, end playback.StreamEnd)
}

// Options are what the core's node control routes work from. Nodes and
// Settings are required; each other field disables the route, or the part of
// one, that needs it when nil.
type Options struct {
	Nodes NodeDirectory
	// Identities answers the calling node's name and URL for reports and log
	// batches without a database read, so a Postgres outage does not drop
	// them; a node it misses is read from Nodes.
	Identities IdentitySource
	// Settings reads every server setting, decrypted, as the configuration
	// watcher does; the node configuration is built from it per request.
	Settings func(ctx context.Context) (map[string]string, error)
	// Redis holds the report fence and the session records. Without it a
	// report is accepted and dropped: nothing would read what it carries.
	Redis *redis.Client
	// Telemetry is where reported stream telemetry goes; nil drops it.
	Telemetry *streamtelemetry.RedisStore
	// Logs stores shipped log batches; nil answers 503.
	Logs LogSink
	// Events carries health-changed to every API server; nil answers 503.
	Events cache.EventBus
	// StreamSecret returns the secret stream tokens are signed with; resolve
	// answers 503 to a request that needs a token verified while it is nil or
	// returns "".
	StreamSecret func() string
	// Recipes is the transport recipe store resolve reads; nil holds none.
	Recipes RecipeSource
	// Deny is the playback deny marker store resolve checks; nil denies
	// nothing.
	Deny DenySource
	// Revocations withdraws the recipes bound to a node for revoke-recipes;
	// nil, like a core without Redis, revokes nothing and answers 204.
	Revocations RecipeRevoker
	// Grants mints and renews source grants; resolve and sources/open answer
	// 503 without it.
	Grants GrantIssuer
	// Files and Themes are the catalog sources/open and resolve check a
	// granted source against; a nil one answers 503 for its kind.
	Files  FileCatalog
	Themes ThemeCatalog
	// Registrar applies the declaration a node sends at startup; nil answers
	// registration with 503.
	Registrar Registrar
	// StreamEnds stores the media ends nodes report for their transcodes
	// (nodesessions.SessionInfo.StreamEndSeconds) when Transcodes confirms
	// the node runs that file's transcode; nil, or a nil Transcodes, stores
	// nothing.
	StreamEnds StreamEndNoter
	Transcodes TranscodeSource
	// Lifetime is the API server's lifetime: when it ends, as graceful
	// shutdown begins, every source byte response in flight is cut, so a
	// long read does not hold the shutdown. Nil never cuts them.
	Lifetime context.Context //nolint:containedctx // the server's lifetime, read by long-running responses
}

// Server serves the core's node control routes. Every handler is stateless
// with respect to other requests, so any API replica serves any node; the
// only process state is the per-node rate limits, source stream counts and
// log throttles. It must run behind RequireNode, which supplies the node's
// identity.
type Server struct {
	opts    Options
	now     func() time.Time
	limiter *nodeLimiter
	// playbackLimiter is the separate budget of resolve and sources/open.
	playbackLimiter *nodeLimiter
	// registrationLimiter is the small budget of registration.
	registrationLimiter *nodeLimiter
	// logBytes is the byte budget of log batches.
	logBytes *nodeLimiter
	// warnings throttles the per-node report warnings: a node reports every
	// second, and during a Redis outage each report would log, and this
	// server's warnings are captured into the operational log.
	warnings *logthrottle.Throttle[reportWarning]
	// afterSourceOpen runs once a source read has opened and checked its
	// descriptor, before it serves; tests replace the file there.
	afterSourceOpen func()
	// sourceStreams counts the source byte responses in flight per node,
	// capped at sourceStreamLimit.
	sourceStreams     nodeStreams
	sourceStreamLimit int
}

// reportWarning is one kind of report warning about one node.
type reportWarning struct {
	nodeID int
	kind   string
}

// reportWarnInterval spaces each kind of report warning per node.
const reportWarnInterval = time.Minute

// NewServer returns the routes' handlers.
func NewServer(opts Options) *Server {
	s := &Server{opts: opts, now: time.Now,
		limiter:             newNodeLimiter(nodeWriteRate, nodeWriteBurst),
		playbackLimiter:     newNodeLimiter(nodePlaybackRate, nodePlaybackBurst),
		registrationLimiter: newNodeLimiter(nodeRegistrationRate, nodeRegistrationBurst),
		logBytes:            newNodeLimiter(nodeLogBytesRate, nodeLogBytesBurst),
		warnings:            logthrottle.New[reportWarning](reportWarnInterval),
		sourceStreamLimit:   maxSourceStreamsPerNode}
	s.warnings.SetClock(func() time.Time { return s.now() })
	s.limiter.now = func() time.Time { return s.now() }
	s.playbackLimiter.now = func() time.Time { return s.now() }
	s.registrationLimiter.now = func() time.Time { return s.now() }
	s.logBytes.now = func() time.Time { return s.now() }
	return s
}

// HandleConfig serves GET /api/internal/nodes/self/config: the configuration
// document of the calling node, with its revision as a strong ETag, or 304
// when If-None-Match names the current revision.
func (s *Server) HandleConfig(w http.ResponseWriter, r *http.Request) {
	id, ok := NodeID(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	doc, err := s.nodeConfig(r.Context(), id)
	if errors.Is(err, nodepool.ErrNodeNotFound) {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "build node configuration", "component", "nodecontrol", "node_id", id, "error", err)
		http.Error(w, "node configuration unavailable", http.StatusServiceUnavailable)
		return
	}
	etag := `"` + doc.Revision + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-store")
	if etagMatches(r.Header.Get(headerIfNoneMatch), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(doc); err != nil {
		slog.DebugContext(r.Context(), "write node configuration", "component", "nodecontrol", "node_id", id, "error", err)
	}
}

// nodeConfig builds node id's configuration from the server settings and its
// row's overrides, as a proxy's database watcher (internal/nodeconfig) reads
// them.
func (s *Server) nodeConfig(ctx context.Context, id int) (NodeConfig, error) {
	node, err := s.opts.Nodes.GetByID(ctx, id)
	if err != nil {
		return NodeConfig{}, err
	}
	if node == nil || node.Type != nodepool.NodeTypeTranscode {
		return NodeConfig{}, nodepool.ErrNodeNotFound
	}
	settings, err := s.opts.Settings(ctx)
	if err != nil {
		return NodeConfig{}, err
	}
	cfg, err := config.LoadFromDB(settings)
	if err != nil {
		return NodeConfig{}, err
	}
	return buildNodeConfig(node, cfg, settings), nil
}

// nodeSourceAccess is the source access mode a node's document names: the
// row's, with anything but core read as local.
func nodeSourceAccess(node *nodepool.Node) string {
	if node.SourceAccess == nodepool.SourceAccessCore {
		return nodesource.ModeCore
	}
	return nodesource.ModeLocal
}

func buildNodeConfig(node *nodepool.Node, cfg *config.Config, settings map[string]string) NodeConfig {
	artifactDir := cfg.Download.ArtifactDir
	if node.DownloadArtifactDirOverride != nil {
		if override := strings.TrimSpace(*node.DownloadArtifactDirOverride); override != "" {
			artifactDir = override
		}
	}
	artifactMaxBytes := cfg.Download.ArtifactMaxBytes
	if node.DownloadArtifactMaxBytesOverride != nil {
		artifactMaxBytes = *node.DownloadArtifactMaxBytesOverride
	}
	// The setting's own default (opslog.SeedDefaults), read as
	// configureOperationalLogging reads it.
	captureLevel := strings.ToLower(strings.TrimSpace(settings["opslog.capture_level"]))
	if captureLevel == "" {
		captureLevel = "info"
	}
	return newNodeConfig(NodeConfigBody{
		Node:           NodeIdentity{ID: node.ID, Name: node.Name, URL: node.URL},
		DrainRequested: node.DrainRequested,
		SourceAccess:   nodeSourceAccess(node),
		Playback: PlaybackConfig{
			FFmpegPath: cfg.Playback.FFmpegPath,
			// The same resolution dispatch names in a start request, so the
			// node and its callers never disagree about its backend.
			HWAccel:                 node.EffectiveHWAccel(cfg.Playback.HWAccel),
			HWDevice:                node.EffectiveHWDevice(cfg.Playback.HWDevice),
			TranscodeDir:            cfg.Playback.TranscodeDir,
			SegmentRetentionSeconds: cfg.Playback.SegmentRetentionSeconds,
			TrickplayNodeCapacity:   cfg.Playback.TrickplayNodeCapacity,
		},
		Download:  DownloadConfig{ArtifactDir: artifactDir, ArtifactMaxBytes: artifactMaxBytes},
		Subtitles: SubtitlesConfig{SyncNodeCapacity: cfg.Playback.SubtitleSyncNodeCapacity},
		Logging: LoggingConfig{
			Level: cfg.Server.LogLevel, Format: cfg.Server.LogFormat, Quiet: cfg.Server.LogQuiet,
			CaptureLevel: captureLevel,
		},
	})
}

// etagMatches reports whether an If-None-Match header names etag, comparing
// weakly as RFC 9110 has If-None-Match do.
func etagMatches(header, etag string) bool {
	for candidate := range strings.SplitSeq(header, ",") {
		candidate = strings.TrimPrefix(strings.TrimSpace(candidate), "W/")
		if candidate == "*" || candidate == etag {
			return true
		}
	}
	return false
}

// reportFenceTTL is how long the core remembers a node's last accepted
// (boot, seq). It only has to outlive the 30-second window a signed request
// is valid in, so a replay or a delayed report always meets the fence; a node
// restarted with a clock set back is fenced out for at most this long after
// its previous process's last report.
const reportFenceTTL = 2 * time.Minute

// reportFence stores (boot, seq) for a node only when it is greater than the
// stored pair, boot first, and reports whether it did. Both are canonical
// decimal strings, compared by length and then lexically, because Lua numbers
// cannot hold a nanosecond timestamp exactly.
var reportFence = redis.NewScript(`
local function less_or_equal(a, b)
  if #a ~= #b then return #a < #b end
  return a <= b
end
local stored = redis.call('HMGET', KEYS[1], 'boot', 'seq')
if stored[1] and stored[2] then
  if ARGV[1] == stored[1] then
    if less_or_equal(ARGV[2], stored[2]) then return 0 end
  elseif less_or_equal(ARGV[1], stored[1]) then
    return 0
  end
end
redis.call('HSET', KEYS[1], 'boot', ARGV[1], 'seq', ARGV[2])
redis.call('PEXPIRE', KEYS[1], ARGV[3])
return 1
`)

func reportFenceKey(nodeID int) string { return "silo:nodereport:" + strconv.Itoa(nodeID) }

// HandleReport serves POST /api/internal/nodes/self/report: it fences the
// report on (boot_id, seq), then replaces the calling node's session records
// and telemetry snapshot with what it lists. An older or repeated report gets
// 409 and changes nothing.
//
// Two replicas can pass the fence for consecutive reports and write them in
// the opposite order; the view is then one report old until the next one
// replaces it.
func (s *Server) HandleReport(w http.ResponseWriter, r *http.Request) {
	id, ok := NodeID(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var report Report
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes)).Decode(&report); err != nil {
		http.Error(w, "invalid report", http.StatusBadRequest)
		return
	}
	boot, err := strconv.ParseUint(report.BootID, 10, 64)
	if err != nil || boot == 0 || boot > math.MaxInt64 || strconv.FormatUint(boot, 10) != report.BootID {
		http.Error(w, "boot_id must be a positive decimal integer", http.StatusBadRequest)
		return
	}
	if len(report.Sessions) > MaxReportSessions ||
		(report.Telemetry != nil && (len(report.Telemetry.Sessions) > MaxReportSessions || len(report.Telemetry.Transfers) > MaxReportTransfers)) {
		http.Error(w, "report exceeds its session or transfer bound", http.StatusRequestEntityTooLarge)
		return
	}
	if s.opts.Redis == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ctx := r.Context()
	node, err := s.identity(ctx, id)
	if errors.Is(err, nodepool.ErrNodeNotFound) {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	if err != nil {
		if s.shouldWarn(id, "identity") {
			slog.WarnContext(ctx, "read reporting node", "component", "nodecontrol", "node_id", id, "error", err)
		}
		http.Error(w, "report unavailable", http.StatusServiceUnavailable)
		return
	}
	accepted, err := reportFence.Run(ctx, s.opts.Redis, []string{reportFenceKey(id)},
		report.BootID, strconv.FormatUint(report.Seq, 10), reportFenceTTL.Milliseconds()).Int()
	if err != nil {
		if s.shouldWarn(id, "fence") {
			slog.WarnContext(ctx, "fence node report", "component", "nodecontrol", "node_id", id, "error", err)
		}
		http.Error(w, "report unavailable", http.StatusServiceUnavailable)
		return
	}
	if accepted != 1 {
		http.Error(w, "report is not newer than the last one accepted", http.StatusConflict)
		return
	}

	now := s.now()
	latest := now.Add(maxClockSkew)
	if err := nodesessions.IngestNodeSessions(ctx, s.opts.Redis, id, node.URL, node.Name, report.Sessions, latest); err != nil {
		s.reportFailed(w, r, id, err, errors.Is(err, nodesessions.ErrInvalidSession))
		return
	}
	if s.opts.StreamEnds != nil {
		for _, session := range report.Sessions {
			if session.StreamEndSeconds <= 0 {
				continue
			}
			if startedAt, ok := s.runsFile(node.URL, session); ok {
				s.opts.StreamEnds.Note(ctx, session.MediaFileID, playback.StreamEnd{
					Seconds:        session.StreamEndSeconds,
					PlannedSeconds: session.StreamPlannedSeconds,
					ReadSince:      startedAt,
				})
			}
		}
	}
	if report.Telemetry != nil && s.opts.Telemetry != nil {
		err := s.opts.Telemetry.IngestNodeSnapshot(ctx, PublisherID(id), node.Name, int64(boot), report.Seq, *report.Telemetry, now, latest)
		if err != nil {
			s.reportFailed(w, r, id, err, errors.Is(err, streamtelemetry.ErrInvalidNodeSnapshot))
			return
		}
	}
	if report.Truncated && s.shouldWarn(id, "truncated") {
		slog.WarnContext(ctx, "a transcode node has more sessions or transfers than one report carries; the admin views show its newest only",
			"component", "nodecontrol", "node_id", id, "max_sessions", MaxReportSessions, "max_transfers", MaxReportTransfers)
	}
	w.WriteHeader(http.StatusNoContent)
}

// PublisherID is the telemetry publisher a transcode node's snapshot is
// stored under. The core derives it from the authenticated node, so a node
// cannot write another publisher's snapshot.
func PublisherID(nodeID int) string { return "node:" + strconv.Itoa(nodeID) }

func (s *Server) reportFailed(w http.ResponseWriter, r *http.Request, id int, err error, invalid bool) {
	if invalid {
		if s.shouldWarn(id, "invalid") {
			slog.WarnContext(r.Context(), "refused an invalid node report", "component", "nodecontrol", "node_id", id, "error", err)
		}
		http.Error(w, "invalid report", http.StatusBadRequest)
		return
	}
	if s.shouldWarn(id, "store") {
		slog.WarnContext(r.Context(), "store node report", "component", "nodecontrol", "node_id", id, "error", err)
	}
	http.Error(w, "report unavailable", http.StatusServiceUnavailable)
}

// shouldWarn reports whether to log a report warning of kind about node id:
// at most once per reportWarnInterval each, so a failure every report hits
// stays one line a minute.
func (s *Server) shouldWarn(id int, kind string) bool {
	return s.warnings.Allow(reportWarning{nodeID: id, kind: kind})
}

// HandleLogs serves POST /api/internal/nodes/self/logs: it stores one batch of
// the calling node's operational log entries, filed under LogNodeID of that
// node whatever node id they carry, and sends them to the admin live tail.
func (s *Server) HandleLogs(w http.ResponseWriter, r *http.Request) {
	id, ok := NodeID(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.opts.Logs == nil {
		http.Error(w, "node logs unavailable", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		http.Error(w, "invalid log batch", http.StatusBadRequest)
		return
	}
	if !s.logBytes.allowN(id, len(body)) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "too many log bytes", http.StatusTooManyRequests)
		return
	}
	var batch LogBatch
	if err := json.Unmarshal(body, &batch); err != nil {
		http.Error(w, "invalid log batch", http.StatusBadRequest)
		return
	}
	if len(batch.Entries) > MaxLogBatchEntries {
		http.Error(w, "log batch exceeds its entry bound", http.StatusRequestEntityTooLarge)
		return
	}
	if len(batch.Entries) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ctx := r.Context()
	node, err := s.identity(ctx, id)
	if errors.Is(err, nodepool.ErrNodeNotFound) {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "node logs unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.opts.Logs.InsertNodeBatch(ctx, LogNodeID(node.Name, id), batch.Entries); err != nil {
		// Not logged: this server's own log records go through the same
		// table, and the node retries the batch.
		http.Error(w, "node logs unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// identity is node id's identity: from Identities when it holds the node,
// else from its row.
func (s *Server) identity(ctx context.Context, id int) (nodepool.Identity, error) {
	if s.opts.Identities != nil {
		if node, ok := s.opts.Identities.Identity(id); ok {
			return node, nil
		}
	}
	if s.opts.Nodes == nil {
		return nodepool.Identity{}, errors.New("no node directory")
	}
	node, err := s.opts.Nodes.GetByID(ctx, id)
	if err != nil {
		return nodepool.Identity{}, err
	}
	return nodepool.Identity{ID: node.ID, Name: node.Name, Type: node.Type, URL: node.URL}, nil
}

// LogNodeID is the node field of the operational log entries a transcode node
// ships: its name, which reads well in the admin log viewer, and its row id,
// since names are not unique. The viewer shows the field as text and filters
// on it by exact match.
func LogNodeID(name string, id int) string { return name + "#" + strconv.Itoa(id) }

// HandleHealthChanged serves POST /api/internal/nodes/self/health-changed:
// the calling node started or stopped draining, or finished. It publishes the
// node_drain_changed event a node used to publish itself, so every API server
// health-checks the node at once rather than at its next sweep.
func (s *Server) HandleHealthChanged(w http.ResponseWriter, r *http.Request) {
	id, ok := NodeID(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.opts.Events == nil {
		http.Error(w, "events unavailable", http.StatusServiceUnavailable)
		return
	}
	event := cache.Event{Type: cache.EventNodeDrainChanged, Payload: strconv.Itoa(id)}
	if err := s.opts.Events.Publish(r.Context(), cache.ChannelAdmin, event); err != nil {
		slog.WarnContext(r.Context(), "announce node health change", "component", "nodecontrol", "node_id", id, "error", err)
		http.Error(w, "events unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
