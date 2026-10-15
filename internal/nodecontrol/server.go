package nodecontrol

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/Silo-Server/silo-server/internal/opslog"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

// NodeDirectory reads stream node rows. *nodepool.Repository implements it.
type NodeDirectory interface {
	GetByID(ctx context.Context, id int) (*nodepool.Node, error)
	GetIdentity(ctx context.Context, id int) (nodepool.Identity, error)
}

// IdentitySource answers a node's identity from memory. *nodepool.KeyRegistry
// implements it.
type IdentitySource interface {
	Identity(id int) (nodepool.Identity, bool)
}

// Options are what the core's node control endpoints work from. Nodes and
// Settings are required; each other field disables the endpoints, or the part
// of one, that need it when nil.
type Options struct {
	Nodes NodeDirectory
	// Identities answers the calling node's name and URL for reports, logs
	// and resolve without a database read, so a Postgres outage does not drop
	// them; a node it misses is read from Nodes. Nil reads every one from
	// Nodes.
	Identities IdentitySource
	// Settings reads every server setting, decrypted, as the configuration
	// watcher does; the node configuration is built from it per request.
	Settings func(ctx context.Context) (map[string]string, error)
	// Redis holds the report fence and the session records. Without it a
	// report is accepted and dropped: nothing would read what it carries.
	Redis *redis.Client
	// Telemetry is where reported stream telemetry goes; nil drops it.
	Telemetry *streamtelemetry.RedisStore
	// Logs stores shipped log batches.
	Logs *opslog.Consumer
	// Events carries health-changed to every API server.
	Events cache.EventBus
	// StreamSecret returns the secret stream tokens are signed with. Resolve
	// verifies the tokens nodes pass through with it; without one it answers
	// 503.
	StreamSecret func() string
	// Recipes is the recipe store resolve reads transcode recipes and remux
	// authority from; nil resolves no stored recipe.
	Recipes RecipeSource
	// Deny is the stream deny marker resolve checks; nil denies nothing.
	Deny DenySource
	// Grants mints the source grants resolve hands out and verifies and
	// renews the ones nodes open and read; without it resolve and the
	// source endpoints answer 503.
	Grants GrantIssuer
	// Files and Themes are the catalogs a source open checks a granted file
	// or theme against; without the one a grant needs, open answers 503.
	Files  FileCatalog
	Themes ThemeCatalog
	// Subtitles builds the subtitle bundles nodes burn text subtitles in
	// from, with FFmpegPath's FFmpeg; without it the subtitle endpoint
	// answers 503.
	Subtitles  SubtitleBundles
	FFmpegPath func() string
}

// Server serves the core's node control endpoints. Every handler is stateless
// with respect to other requests, so any API replica serves any node; it must
// run behind RequireNode, which supplies the node's identity.
type Server struct {
	opts Options
	now  func() time.Time
	// warnings throttles the per-node report warnings: a node reports every
	// second, and during a Redis outage each report would log, and this
	// server's warnings are captured into the operational log. It is only a
	// log limiter: no request depends on it.
	warnings *logthrottle.Keyed[reportWarning]
	// sourceOpened, when set, runs between a source's check and its serving;
	// tests use it to replace the file there.
	sourceOpened func()
}

// reportWarning is one kind of report warning about one node.
type reportWarning struct {
	nodeID int
	kind   string
}

// NewServer returns the endpoints' handlers.
func NewServer(opts Options) *Server {
	s := &Server{opts: opts, now: time.Now, warnings: logthrottle.New[reportWarning](reportWarnInterval)}
	s.warnings.Now = func() time.Time { return s.now() }
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
	_ = json.NewEncoder(w).Encode(doc)
}

// nodeConfig builds node id's configuration from the same settings and row
// overrides the node's database watcher overlaid before nodes lost their
// database access.
func (s *Server) nodeConfig(ctx context.Context, id int) (NodeConfig, error) {
	node, err := s.opts.Nodes.GetByID(ctx, id)
	if err != nil {
		return NodeConfig{}, err
	}
	settings, err := s.opts.Settings(ctx)
	if err != nil {
		return NodeConfig{}, err
	}
	cfg, err := config.LoadFromDB(settings)
	if err != nil {
		return NodeConfig{}, err
	}
	return buildNodeConfig(node, cfg, settings)
}

func buildNodeConfig(node *nodepool.Node, cfg *config.Config, settings map[string]string) (NodeConfig, error) {
	sourceAccess := node.SourceAccess
	if sourceAccess == "" {
		sourceAccess = nodepool.SourceAccessLocal
	}
	artifactDir := cfg.Download.ArtifactDir
	if node.DownloadArtifactDirOverride != nil {
		artifactDir = *node.DownloadArtifactDirOverride
	}
	artifactMaxBytes := cfg.Download.ArtifactMaxBytes
	if node.DownloadArtifactMaxBytesOverride != nil {
		artifactMaxBytes = *node.DownloadArtifactMaxBytesOverride
	}
	captureLevel := strings.ToLower(strings.TrimSpace(settings["opslog.capture_level"]))
	if captureLevel == "" {
		captureLevel = "info"
	}
	return newNodeConfig(NodeConfigBody{
		Node:           NodeIdentity{ID: node.ID, Name: node.Name, URL: node.URL},
		SourceAccess:   sourceAccess,
		DrainRequested: node.DrainRequested,
		Playback: PlaybackConfig{
			FFmpegPath: cfg.Playback.FFmpegPath,
			// The same resolution dispatch names in a start request, so the
			// node and its callers never disagree about its backend.
			HWAccel:                 node.EffectiveHWAccel(cfg.Playback.HWAccel),
			HWDevice:                node.EffectiveHWDevice(cfg.Playback.HWDevice),
			TranscodeDir:            cfg.Playback.TranscodeDir,
			SegmentRetentionSeconds: cfg.Playback.SegmentRetentionSeconds,
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
// the opposite order; the view is then at most one report old until the next
// one replaces it.
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

// reportWarnInterval spaces each kind of report warning per node.
const reportWarnInterval = time.Minute

// shouldWarn reports whether to log a report warning of kind about node id:
// at most once per reportWarnInterval each, so a failure every report hits
// stays one line a minute.
func (s *Server) shouldWarn(id int, kind string) bool {
	_, ok := s.warnings.Allow(reportWarning{nodeID: id, kind: kind})
	return ok
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
	var batch LogBatch
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes)).Decode(&batch); err != nil {
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
	return s.opts.Nodes.GetIdentity(ctx, id)
}

// LogNodeID is the node field of the operational log entries a transcode node
// ships: its name, which reads well in the admin log viewer, and its row id,
// since names are not unique. The viewer shows the field as text and filters
// on it by exact match.
func LogNodeID(name string, id int) string { return name + "#" + strconv.Itoa(id) }

// HandleHealthChanged serves POST /api/internal/nodes/self/health-changed:
// the calling node started or stopped draining, or finished. Every API
// server rechecks its health at once (node_drain_changed), rather than at its
// next sweep.
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
