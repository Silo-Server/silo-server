package nodeconfig

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/logthrottle"
	"github.com/Silo-Server/silo-server/internal/nodecontrol"
)

// RemotePollInterval is how often a transcode node asks the core whether its
// configuration changed. Server-wide settings reach a node within one poll;
// the core pushes changes to the node's own row at once through
// /admin/reload-config.
const RemotePollInterval = 30 * time.Second

// transcodeMode is the server mode of every config Remote builds.
const transcodeMode = "transcode"

// rejectedPollLogInterval spaces the poll warning while the core refuses the
// node's key. The change into and out of that state is logged at once; the
// polls in between fail the same way.
const rejectedPollLogInterval = 10 * time.Minute

// Startup backoff while the core does not answer.
const (
	remoteInitialBackoff = time.Second
	remoteMaxBackoff     = 30 * time.Second
)

// ConfigFetcher reads a node's configuration document from the core.
// *nodecontrol.Client implements it.
type ConfigFetcher interface {
	FetchConfig(ctx context.Context, etag string) (doc *nodecontrol.NodeConfig, notModified bool, err error)
}

// Remote is a transcode node's configuration, read from the core rather than
// from the database: the counterpart of Watcher for a node that holds no
// database, Redis or secret. It polls every RemotePollInterval with
// If-None-Match, and on ForceReload or RequestReload.
//
// The *config.Config it returns changes only when the configuration a node
// runs with does. Handlers refuse work when the pointer changes under them, so
// a document whose only change is the drain request, the source access or the
// node's name or URL keeps the pointer; a failed read keeps everything.
type Remote struct {
	fetcher ConfigFetcher
	// listen is the node's own listen address (from PORT), which the core
	// does not know.
	listen string

	// fetchMu makes one read atomic from the request through the swap and its
	// callbacks, so a slow read cannot put an older document back over a newer
	// one.
	fetchMu sync.Mutex

	mu          sync.RWMutex
	cfg         *config.Config
	doc         *nodecontrol.NodeConfig
	normalizers []func(*config.Config)
	onChange    []func(old, updated *config.Config)
	onDrain     []func(requested bool)

	reloadCh chan struct{}

	// keyRejected is set when the core answered a read with 401 and cleared
	// by the next read it answers: the node's key was rotated or the node
	// deleted, so every call the node makes fails until it restarts with a
	// new key. The node reports itself unhealthy meanwhile, which takes it
	// out of placement.
	keyRejected atomic.Bool
	// rejectedReadAt is when a read last got 401, or ReportUnauthorized last
	// asked for one while the key was rejected, in Unix nanoseconds. It paces
	// ReportUnauthorized while the key stays rejected.
	rejectedReadAt atomic.Int64
	// rejectedPollLog spaces Run's warning about a poll the core refused.
	rejectedPollLog *logthrottle.Keyed[string]
	// now is the clock; tests replace it.
	now func() time.Time
}

// NewRemote returns a Remote that reads through fetcher. Call Load before
// using it and Run to keep it current.
func NewRemote(fetcher ConfigFetcher, listen string) *Remote {
	return &Remote{
		fetcher:         fetcher,
		listen:          listen,
		reloadCh:        make(chan struct{}, 1),
		rejectedPollLog: logthrottle.New[string](rejectedPollLogInterval),
		now:             time.Now,
	}
}

// OnLoad registers a normalization applied to every config Remote builds.
// Register before Load.
func (r *Remote) OnLoad(fn func(*config.Config)) {
	r.normalizers = append(r.normalizers, fn)
}

// OnChange registers fn to run after the config changes.
func (r *Remote) OnChange(fn func(old, updated *config.Config)) {
	r.mu.Lock()
	r.onChange = append(r.onChange, fn)
	r.mu.Unlock()
}

// OnNodeDrainChange registers fn to run whenever a read finds a different
// drain request. fn must not block.
func (r *Remote) OnNodeDrainChange(fn func(requested bool)) {
	r.mu.Lock()
	r.onDrain = append(r.onDrain, fn)
	r.mu.Unlock()
}

// Load reads the configuration, retrying with backoff from one second up to
// thirty, until a read succeeds or ctx ends. Each failure is logged.
func (r *Remote) Load(ctx context.Context) error {
	backoff := remoteInitialBackoff
	for attempt := 1; ; attempt++ {
		err := r.refresh(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slog.WarnContext(ctx, "cannot read this node's configuration from the core; retrying",
			"component", "nodeconfig", "attempt", attempt, "retry_in", backoff, "error", err)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		backoff = min(2*backoff, remoteMaxBackoff)
	}
}

// Run polls every RemotePollInterval, and at once on RequestReload, until ctx
// ends. A failed poll keeps the last configuration. A rejected key is retried
// on the same cadence, so a node that a replica refused only until it loaded
// the key recovers within a poll.
func (r *Remote) Run(ctx context.Context) {
	ticker := time.NewTicker(RemotePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.reloadCh:
		}
		if err := r.refresh(ctx); err != nil && ctx.Err() == nil {
			r.warnPollFailed(ctx, err)
		}
	}
}

// ForceReload reads the configuration now and reports whether that worked.
// /admin/reload-config and /admin/force-reload call it.
func (r *Remote) ForceReload(ctx context.Context) error {
	return r.refresh(ctx)
}

// warnPollFailed logs a failed poll. A refused key is logged when it starts
// and ends (see refresh), and the polls between only every
// rejectedPollLogInterval: every one fails the same way until the node
// restarts with a new key.
func (r *Remote) warnPollFailed(ctx context.Context, err error) {
	if nodecontrol.Status(err) == http.StatusUnauthorized {
		suppressed, ok := r.rejectedPollLog.Allow("unauthorized")
		if !ok {
			return
		}
		slog.WarnContext(ctx, "node configuration poll failed; the core still refuses this node's key",
			"component", "nodeconfig", "suppressed", suppressed, "error", err)
		return
	}
	slog.WarnContext(ctx, "node configuration poll failed; keeping the last configuration",
		"component", "nodeconfig", "error", err)
}

// RequestReload asks Run to read the configuration soon, without waiting.
func (r *Remote) RequestReload() {
	select {
	case r.reloadCh <- struct{}{}:
	default:
	}
}

// ReportUnauthorized is told that the core answered another call (a report,
// a log batch, a resolve) with 401. The first one asks for a configuration
// read at once, so a rotated or deleted key shows in health promptly. While
// the key stays rejected, every call the node makes gets 401, so further ones
// ask for a read at most once per RemotePollInterval after the last refused
// one, which Run's own poll already covers.
func (r *Remote) ReportUnauthorized() {
	if r.keyRejected.Load() {
		last, now := r.rejectedReadAt.Load(), r.now().UnixNano()
		if now-last < int64(RemotePollInterval) || !r.rejectedReadAt.CompareAndSwap(last, now) {
			return
		}
	}
	r.RequestReload()
}

// refresh reads the document, sending the current revision so an unchanged
// one costs a 304, and applies a new one.
func (r *Remote) refresh(ctx context.Context) error {
	r.fetchMu.Lock()
	defer r.fetchMu.Unlock()
	etag := ""
	if doc := r.document(); doc != nil {
		etag = `"` + doc.Revision + `"`
	}
	doc, notModified, err := r.fetcher.FetchConfig(ctx, etag)
	if err != nil {
		if nodecontrol.Status(err) != http.StatusUnauthorized {
			return err
		}
		r.rejectedReadAt.Store(r.now().UnixNano())
		if !r.keyRejected.Swap(true) {
			slog.ErrorContext(ctx, "the core refuses this node's key; reporting unhealthy until it accepts it. "+
				"A rotated key needs a restart with the new SILO_NODE_KEY",
				"component", "nodeconfig")
		}
		return err
	}
	if r.keyRejected.Swap(false) {
		slog.InfoContext(ctx, "the core accepts this node's key again", "component", "nodeconfig")
	}
	if notModified {
		if etag == "" {
			return errors.New("core answered 304 to a read without a revision")
		}
		return nil
	}
	return r.apply(doc)
}

// KeyRejected reports whether the core refused the node's key on the last
// configuration read: 401, after a key rotation or the node's deletion.
func (r *Remote) KeyRejected() bool {
	return r.keyRejected.Load()
}

func (r *Remote) apply(doc *nodecontrol.NodeConfig) error {
	if doc == nil {
		return errors.New("empty node configuration")
	}
	cfg, err := r.build(doc)
	if err != nil {
		return err
	}
	r.mu.Lock()
	old, oldDoc := r.cfg, r.doc
	changed := old == nil || !reflect.DeepEqual(*old, *cfg)
	if changed {
		r.cfg = cfg
	}
	r.doc = doc
	onChange := slices.Clone(r.onChange)
	onDrain := slices.Clone(r.onDrain)
	r.mu.Unlock()

	if changed && old != nil {
		for _, fn := range onChange {
			fn(old, cfg)
		}
	}
	if oldDoc != nil && oldDoc.DrainRequested != doc.DrainRequested {
		for _, fn := range onDrain {
			fn(doc.DrainRequested)
		}
	}
	return nil
}

// build turns a document into the config the node runs with: the defaults
// for everything the document does not carry, then the document, then the
// normalizers.
func (r *Remote) build(doc *nodecontrol.NodeConfig) (*config.Config, error) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		return nil, fmt.Errorf("default config: %w", err)
	}
	cfg.Server.Mode = transcodeMode
	if r.listen != "" {
		cfg.Server.Listen = r.listen
	}
	cfg.Server.LogLevel = doc.Logging.Level
	cfg.Server.LogFormat = doc.Logging.Format
	cfg.Server.LogQuiet = doc.Logging.Quiet
	cfg.Playback.FFmpegPath = doc.Playback.FFmpegPath
	cfg.Playback.HWAccel = doc.Playback.HWAccel
	cfg.Playback.HWDevice = doc.Playback.HWDevice
	cfg.Playback.TranscodeDir = doc.Playback.TranscodeDir
	cfg.Playback.SegmentRetentionSeconds = doc.Playback.SegmentRetentionSeconds
	cfg.Playback.SubtitleSyncNodeCapacity = doc.Subtitles.SyncNodeCapacity
	cfg.Download.ArtifactDir = doc.Download.ArtifactDir
	cfg.Download.ArtifactMaxBytes = doc.Download.ArtifactMaxBytes
	for _, normalize := range r.normalizers {
		normalize(cfg)
	}
	return cfg, nil
}

func (r *Remote) document() *nodecontrol.NodeConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.doc
}

// Config returns the current config; nil before Load succeeds.
func (r *Remote) Config() *config.Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg
}

// Revision is the revision of the document last read; empty before Load.
func (r *Remote) Revision() string {
	if doc := r.document(); doc != nil {
		return doc.Revision
	}
	return ""
}

// NodeRowID returns the node's stream_nodes id.
func (r *Remote) NodeRowID() (int, bool) {
	doc := r.document()
	if doc == nil || doc.Node.ID <= 0 {
		return 0, false
	}
	return doc.Node.ID, true
}

// NodeRegisteredURL returns the URL the node's row carries: how API servers
// reach it.
func (r *Remote) NodeRegisteredURL() (string, bool) {
	doc := r.document()
	if doc == nil || doc.Node.URL == "" {
		return "", false
	}
	return doc.Node.URL, true
}

// NodeName returns the node's registered name.
func (r *Remote) NodeName() string {
	if doc := r.document(); doc != nil {
		return doc.Node.Name
	}
	return ""
}

// NodeDrainRequested reports whether an administrator asked the node to
// drain, as of the last read.
func (r *Remote) NodeDrainRequested() bool {
	doc := r.document()
	return doc != nil && doc.DrainRequested
}

// SourceAccess is how the node reads source media, as of the last read.
func (r *Remote) SourceAccess() string {
	if doc := r.document(); doc != nil {
		return doc.SourceAccess
	}
	return ""
}

// CaptureLevel is the lowest log level the node ships to the core, as of the
// last read. A node reads it once at startup, as other nodes read
// opslog.capture_level.
func (r *Remote) CaptureLevel() string {
	if doc := r.document(); doc != nil {
		return doc.Logging.CaptureLevel
	}
	return ""
}
