// Package librarymonitor implements real-time library monitoring: it watches
// the folders of enabled libraries with inotify and queues the narrowest scan
// for each settled change.
//
// Monitoring is node-local. Every API or integrated node monitors the library
// roots it can see and reports a per-library status row; scans go to the
// shared scan queue. See docs/architecture/realtime-monitoring.md.
package librarymonitor

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

// Trigger is the scan trigger string of every scan the monitor queues.
const Trigger = "realtime_monitor"

// maxTargetsPerLibrary caps one library's targets in one flush; more collapse
// to a single library scan (the same cap as autoscan's per-poll limit).
const maxTargetsPerLibrary = 1000

// Default timings. Config zero values fall back to these.
const (
	DefaultQuietWindow       = 5 * time.Second
	DefaultFlushInterval     = 2 * time.Second
	DefaultCreatedFallback   = 2 * time.Minute
	DefaultStablePoll        = 30 * time.Second
	DefaultReconcileInterval = 30 * time.Second
	DefaultStatusRefresh     = 60 * time.Second
	DefaultWalkStall         = 30 * time.Second
)

const (
	listTimeout    = 15 * time.Second
	enqueueTimeout = 30 * time.Second
	statusTimeout  = 10 * time.Second
	stopWait       = 5 * time.Second
)

const fuseCaveat = "FUSE filesystem: changes made outside this mount (for example directly on a pool member disk) aren't seen."

// FolderLister lists every library; *catalog.FolderRepository satisfies it.
type FolderLister interface {
	List(ctx context.Context) ([]*models.MediaFolder, error)
}

// Resolver maps changed paths to scan targets; the subset of
// *scantrigger.Resolver the monitor uses.
type Resolver interface {
	Resolve(ctx context.Context, req scantrigger.Request) (*scantrigger.Target, error)
	ResolveVanishedPath(ctx context.Context, path, trigger string) (*scantrigger.Target, error)
	ResolveMissingSubtree(ctx context.Context, subtreePath, trigger string) (*scantrigger.Target, error)
}

// Queuer enqueues resolved scan targets.
type Queuer interface {
	EnqueueScans(ctx context.Context, targets []scantrigger.Target) error
}

// Config configures a Monitor. Zero timings use the defaults above; tests
// shrink them.
type Config struct {
	// NodeID identifies this node's status rows.
	NodeID  string
	Folders FolderLister
	// NewResolver builds the resolver a flush maps its changes with, over
	// the libraries the last reconcile listed; nil uses
	// scantrigger.NewResolver.
	NewResolver func(scantrigger.FolderRepository) Resolver
	Queue       Queuer
	// Status receives node reports; nil disables reporting.
	Status StatusReporter
	Logger *slog.Logger
	// ServerEnabled is the initial scanner.realtime_monitoring value;
	// SetServerEnabled changes it.
	ServerEnabled bool
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	// QuietWindow: a path is reported once complete and quiet this long.
	QuietWindow time.Duration
	// FlushInterval: how often reported changes are resolved and queued.
	FlushInterval time.Duration
	// CreatedFallback: how long a created file waits for its close-write
	// before stability polling starts.
	CreatedFallback time.Duration
	// StablePoll: the stability polling interval.
	StablePoll time.Duration
	// ReconcileInterval: how often the desired roots are re-read.
	ReconcileInterval time.Duration
	// StatusRefresh: how often the node's rows are refreshed when nothing
	// changed.
	StatusRefresh time.Duration
	// WalkStall: initial walks run one at a time in library sort order, so
	// it is predictable which libraries win when the watch limit is tight. A
	// walk that records no directory for this long stops holding the next
	// one back, so a hung mount cannot block other libraries.
	WalkStall time.Duration

	hooks testHooks
}

// testHooks are seams for tests in this package.
type testHooks struct {
	// primary, when set, replaces the inotify backend.
	primary  func(BackendOptions) (Backend, error)
	classify func(string) (fsClass, error)
	inotify  inotifyHooks
	stat     func(string) (fileState, error)
	// mounts, when set, replaces reading /proc/self/mountinfo.
	mounts func() ([]mountEntry, error)
	// Observation points for tests.
	afterAttempt   func(path string, state State)
	afterReconcile func()
	// afterWalk runs when an attempt walked a root and its outcome was
	// applied, with the mount signature it recorded.
	afterWalk func(path, mounts string)
	// stopWait, when set, replaces how long Stop waits for stuck work.
	stopWait time.Duration
}

// inotifyHooks is the test seam for the kernel calls a test cannot provoke
// safely: tests inject ENOSPC through addWatch instead of lowering the
// host's fs.inotify.max_user_watches.
type inotifyHooks struct {
	addWatch       func(fd int, path string, mask uint32) (int, error)
	maxUserWatches func() int
}

// Monitor is the real-time monitoring engine for one node.
type Monitor struct {
	cfg  Config
	log  *slog.Logger
	now  func() time.Time
	gate *walkGate

	serverEnabled atomic.Bool
	pokeCh        chan struct{}
	dirtyCh       chan struct{}
	events        chan backendEvent

	startOnce  sync.Once
	stopOnce   sync.Once
	ctx        context.Context
	cancel     context.CancelFunc
	loopDone   chan struct{}
	statusDone chan struct{}
	attempts   sync.WaitGroup

	mu           sync.Mutex
	stopped      bool
	roots        map[string]*rootState
	desiredOrder []*models.MediaFolder
	desired      map[int]*models.MediaFolder
	// listed is every library the last reconcile listed; a flush resolves
	// changes against it.
	listed       []*models.MediaFolder
	libraryScans map[int]struct{}
	primary      Backend
	// removals counts detached roots; a limit_reached root retries when
	// watches were freed since it hit the limit.
	removals uint64
	// releasing counts, per root path, the backend releases collected under
	// mu that have not run yet (see rootRelease).
	releasing map[string]*pendingReleases

	// Owned by the event loop.
	tracker      *tracker
	retry        []change
	retryTargets []scantrigger.Target
}

type backendEvent struct {
	backend Backend
	ev      Event
	// closed: the backend's event stream ended while the monitor runs.
	closed bool
}

// rootState is one configured root path on this node.
type rootState struct {
	path      string
	libraries []int
	wanted    bool
	running   bool
	cancel    context.CancelFunc
	// lost records a root-lost event that arrived while an attempt ran.
	lost bool
	// rewalk asks the next attempt to walk an attached root again.
	rewalk bool
	// attaching is the backend a running attempt is recording a root with
	// that has none yet. It already delivers the root's events, so an
	// overflow on it during that first walk counts for the root.
	attaching Backend
	// limitHit records a runtime watch-limit hit that arrived while an
	// attempt ran; finish applies it instead of the attempt's outcome.
	limitHit *WatchLimitError

	state       State
	backend     Backend
	backendName string
	notes       []string
	fsName      string
	errText     string
	dirs        int
	limit       int
	limitSeen   uint64
	identity    fileID
	// mounts is the mount signature recorded by the last walk (see
	// mountView); unsupportedMounts are network mounts below the root that
	// the walk skipped.
	mounts            string
	unsupportedMounts []string
	// unreadable are directories below the root the walk could not watch
	// because Silo may not read them; the scanner cannot read them either.
	unreadable []string
	everSeen   bool
}

// rootRelease is a root to drop from a backend. Detaching collects them
// under m.mu and runs them once it is unlocked: releasing watches on a hung
// mount can block, and nothing that holds m.mu may wait on a mount.
type rootRelease struct {
	backend Backend
	path    string
}

// pendingReleases counts a path's collected releases that have not run.
// An attempt on the path waits for done before it records the path again,
// so a late RemoveRoot cannot drop a fresh walk.
type pendingReleases struct {
	n    int
	done chan struct{}
}

// fileID is a directory's device and inode; zero when unknown.
type fileID struct {
	dev uint64
	ino uint64
}

// New builds a Monitor. Start runs it.
func New(cfg Config) (*Monitor, error) {
	if cfg.Folders == nil || cfg.Queue == nil {
		return nil, errors.New("librarymonitor: Folders and Queue are required")
	}
	if cfg.NewResolver == nil {
		cfg.NewResolver = func(f scantrigger.FolderRepository) Resolver { return scantrigger.NewResolver(f) }
	}
	setDefault(&cfg.QuietWindow, DefaultQuietWindow)
	setDefault(&cfg.FlushInterval, DefaultFlushInterval)
	setDefault(&cfg.CreatedFallback, DefaultCreatedFallback)
	setDefault(&cfg.StablePoll, DefaultStablePoll)
	setDefault(&cfg.ReconcileInterval, DefaultReconcileInterval)
	setDefault(&cfg.StatusRefresh, DefaultStatusRefresh)
	setDefault(&cfg.WalkStall, DefaultWalkStall)
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	m := &Monitor{
		cfg:          cfg,
		log:          logger.With("component", "librarymonitor"),
		now:          now,
		gate:         newWalkGate(cfg.WalkStall, now),
		pokeCh:       make(chan struct{}, 1),
		dirtyCh:      make(chan struct{}, 1),
		events:       make(chan backendEvent, 256),
		loopDone:     make(chan struct{}),
		statusDone:   make(chan struct{}),
		roots:        make(map[string]*rootState),
		desired:      make(map[int]*models.MediaFolder),
		libraryScans: make(map[int]struct{}),
		releasing:    make(map[string]*pendingReleases),
		tracker:      newTracker(cfg.QuietWindow, cfg.CreatedFallback, cfg.StablePoll),
	}
	if cfg.hooks.stat != nil {
		m.tracker.stat = cfg.hooks.stat
	}
	m.serverEnabled.Store(cfg.ServerEnabled)
	return m, nil
}

func setDefault(d *time.Duration, def time.Duration) {
	if *d <= 0 {
		*d = def
	}
}

// Start runs the monitor in the background until ctx ends or Stop. It never
// blocks on the initial walks, which queue no scans.
func (m *Monitor) Start(ctx context.Context) {
	m.startOnce.Do(func() {
		m.ctx, m.cancel = context.WithCancel(ctx)
		go m.run(m.ctx)
		go m.statusLoop(m.ctx)
	})
}

// Stop ends monitoring, releases the kernel resources, and deletes this
// node's status rows (best effort).
func (m *Monitor) Stop() {
	m.stopOnce.Do(func() {
		if m.cancel == nil {
			return
		}
		m.cancel()
		// The event loop and the status loop can be stuck in a filesystem
		// or database call that ignores ctx (a stat on a hung FUSE mount);
		// shutdown goes on without them.
		m.waitOrWarn(m.loopDone, "the event loop")
		m.waitOrWarn(m.statusDone, "the status loop")

		m.mu.Lock()
		m.stopped = true
		primary := m.primary
		m.mu.Unlock()

		// A walk blocked in a syscall on a hung mount cannot be interrupted;
		// don't let it hold shutdown.
		waited := make(chan struct{})
		go func() {
			m.attempts.Wait()
			close(waited)
		}()
		select {
		case <-waited:
		case <-time.After(m.stopWait()):
			m.log.Warn("librarymonitor: stopping without waiting for a stuck folder walk")
		}
		if primary != nil {
			closed := make(chan error, 1)
			go func() { closed <- primary.Close() }()
			select {
			case err := <-closed:
				if err != nil {
					m.log.Warn("librarymonitor: closing backend failed", "backend", primary.Name(), "err", err)
				}
			case <-time.After(m.stopWait()):
				m.log.Warn("librarymonitor: stopping without waiting for a stuck backend", "backend", primary.Name())
			}
		}
		if m.cfg.Status != nil {
			ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
			defer cancel()
			if err := m.cfg.Status.RemoveNode(ctx, m.cfg.NodeID); err != nil {
				m.log.Warn("librarymonitor: removing node status failed", "node_id", m.cfg.NodeID, "err", err)
			}
		}
	})
}

func (m *Monitor) stopWait() time.Duration {
	if m.cfg.hooks.stopWait > 0 {
		return m.cfg.hooks.stopWait
	}
	return stopWait
}

// waitOrWarn waits up to stopWait for done.
func (m *Monitor) waitOrWarn(done <-chan struct{}, what string) {
	timer := time.NewTimer(m.stopWait())
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		m.log.Warn("librarymonitor: stopping without waiting for stuck work", "waiting_for", what)
	}
}

// Poke requests a reconcile without blocking; call it after a library
// create, update, or delete.
func (m *Monitor) Poke() {
	select {
	case m.pokeCh <- struct{}{}:
	default:
	}
}

// SetServerEnabled applies a live scanner.realtime_monitoring change.
func (m *Monitor) SetServerEnabled(enabled bool) {
	m.serverEnabled.Store(enabled)
	m.Poke()
}

func (m *Monitor) markDirty() {
	select {
	case m.dirtyCh <- struct{}{}:
	default:
	}
}

func (m *Monitor) run(ctx context.Context) {
	defer close(m.loopDone)
	flush := time.NewTicker(m.cfg.FlushInterval)
	defer flush.Stop()
	reconcile := time.NewTicker(m.cfg.ReconcileInterval)
	defer reconcile.Stop()

	m.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case be := <-m.events:
			m.handleEvent(be, m.now())
		case <-flush.C:
			m.flush(ctx, m.now())
		case <-reconcile.C:
			m.reconcile(ctx)
		case <-m.pokeCh:
			m.reconcile(ctx)
		}
	}
}

// reconcile diffs the desired roots against the current ones.
func (m *Monitor) reconcile(ctx context.Context) {
	listCtx, cancel := context.WithTimeout(ctx, listTimeout)
	folders, err := m.cfg.Folders.List(listCtx)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			m.log.WarnContext(ctx, "librarymonitor: listing libraries failed", "err", err)
		}
		return
	}
	m.applyFolders(folders)
}

// applyFolders makes the desired set current: every enabled library with
// real-time monitoring on, while the server switch is on, expanded to its
// folder paths in sort order.
func (m *Monitor) applyFolders(folders []*models.MediaFolder) {
	sorted := sortedFolders(folders)
	var order []*models.MediaFolder
	desired := make(map[int]*models.MediaFolder)
	rootLibs := make(map[string][]int)
	var rootOrder []string
	if m.serverEnabled.Load() {
		for _, f := range sorted {
			if !f.Enabled || !f.RealtimeMonitoring {
				continue
			}
			order = append(order, f)
			desired[f.ID] = f
			for _, raw := range f.Paths {
				path := cleanRoot(raw)
				if path == "" {
					continue
				}
				if _, ok := rootLibs[path]; !ok {
					rootOrder = append(rootOrder, path)
				}
				if !slices.Contains(rootLibs[path], f.ID) {
					rootLibs[path] = append(rootLibs[path], f.ID)
				}
			}
		}
	}

	m.mu.Lock()
	m.desiredOrder = order
	m.desired = desired
	m.listed = sorted
	var releases []rootRelease
	for path, rs := range m.roots {
		if _, ok := rootLibs[path]; ok {
			continue
		}
		rs.wanted = false
		if rs.running {
			rs.cancel()
			continue
		}
		releases = m.detachLocked(rs, releases)
		delete(m.roots, path)
	}
	m.mu.Unlock()
	m.markDirty()
	// Released before any attempt starts, so a limit_reached root retried
	// below finds the watches freed.
	m.runReleases(releases)
	m.mu.Lock()
	for _, path := range rootOrder {
		rs := m.roots[path]
		if rs == nil {
			rs = &rootState{path: path}
			m.roots[path] = rs
		}
		rs.wanted = true
		rs.libraries = rootLibs[path]
		if m.needsAttemptLocked(rs) {
			m.spawnLocked(rs)
		}
	}
	m.mu.Unlock()
	m.markDirty()
	if m.cfg.hooks.afterReconcile != nil {
		m.cfg.hooks.afterReconcile()
	}
}

// sortedFolders returns the non-nil folders in library sort order: by
// SortOrder, then by ID.
func sortedFolders(folders []*models.MediaFolder) []*models.MediaFolder {
	sorted := make([]*models.MediaFolder, 0, len(folders))
	for _, f := range folders {
		if f != nil {
			sorted = append(sorted, f)
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].SortOrder != sorted[j].SortOrder {
			return sorted[i].SortOrder < sorted[j].SortOrder
		}
		return sorted[i].ID < sorted[j].ID
	})
	return sorted
}

// needsAttemptLocked decides whether reconcile starts an attempt for a
// wanted root. Monitored roots get a cheap check that the folder at the
// path is still the one recorded (a remounted disk is a new directory).
func (m *Monitor) needsAttemptLocked(rs *rootState) bool {
	if rs.running || m.stopped {
		return false
	}
	switch rs.state {
	case StateUnsupportedPlatform:
		return false
	case StateLimitReached:
		return m.maxUserWatches() != rs.limit || m.removals != rs.limitSeen
	default:
		return true
	}
}

func (m *Monitor) maxUserWatches() int {
	if m.cfg.hooks.inotify.maxUserWatches != nil {
		return m.cfg.hooks.inotify.maxUserWatches()
	}
	return readMaxUserWatches()
}

func (m *Monitor) spawnLocked(rs *rootState) {
	ctx, cancel := context.WithCancel(m.ctx)
	rs.running = true
	rs.cancel = cancel
	ticket := m.gate.issue()
	m.attempts.Add(1)
	go func() {
		defer m.attempts.Done()
		defer cancel()
		defer m.gate.done(ticket)
		m.finish(rs, m.try(ctx, rs, ticket))
	}()
}

// detachLocked forgets the root's backend and adds the release of its
// registration to releases, which the caller runs with runReleases once
// m.mu is unlocked.
func (m *Monitor) detachLocked(rs *rootState, releases []rootRelease) []rootRelease {
	if rs.backend != nil {
		releases = m.queueReleaseLocked(releases, rs.backend, rs.path)
		rs.backend = nil
		m.removals++
	}
	rs.backendName = ""
	return releases
}

// queueReleaseLocked adds the release of path from b to releases and marks
// it pending, so the next attempt on path waits for it.
func (m *Monitor) queueReleaseLocked(releases []rootRelease, b Backend, path string) []rootRelease {
	p := m.releasing[path]
	if p == nil {
		p = &pendingReleases{done: make(chan struct{})}
		m.releasing[path] = p
	}
	p.n++
	return append(releases, rootRelease{backend: b, path: path})
}

// runReleases runs releases collected under m.mu. The caller must not hold
// m.mu.
func (m *Monitor) runReleases(releases []rootRelease) {
	for _, r := range releases {
		r.backend.RemoveRoot(r.path)
		m.mu.Lock()
		if p := m.releasing[r.path]; p != nil {
			p.n--
			if p.n == 0 {
				close(p.done)
				delete(m.releasing, r.path)
			}
		}
		m.mu.Unlock()
	}
}

// waitReleases waits until the releases of path collected so far have run.
func (m *Monitor) waitReleases(ctx context.Context, path string) error {
	m.mu.Lock()
	p := m.releasing[path]
	m.mu.Unlock()
	if p == nil {
		return nil
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// outcome is the result of one attempt on a root.
type outcome struct {
	// keep: the attached root is still the recorded folder; nothing changes.
	keep     bool
	canceled bool

	state       State
	backend     Backend
	backendName string
	notes       []string
	fsName      string
	errText     string
	dirs        int
	limit       int
	identity    fileID
	mounts      mountView
	// unreadable lists directories below the root that could not be
	// watched because Silo may not read them.
	unreadable []string
}

// try checks that a root is visible, then verifies or re-walks an attached
// root, or attaches it to a backend by walking it. It runs in the root's own
// goroutine so a hung mount blocks only this root.
func (m *Monitor) try(ctx context.Context, rs *rootState, ticket *walkTicket) outcome {
	ctx, _ = withSkippedPaths(ctx)
	// A release of this path collected before the attempt started (a
	// previous attempt's, or a previous rootState's) must not land after
	// the walk below.
	if err := m.waitReleases(ctx, rs.path); err != nil {
		return outcome{canceled: true}
	}
	m.mu.Lock()
	attached, rewalk, everSeen, recorded := rs.backend, rs.rewalk, rs.everSeen, rs.identity
	recordedMounts := rs.mounts
	notes := rs.notes
	unreadable := slices.Clone(rs.unreadable)
	rs.rewalk = false
	m.mu.Unlock()

	info, err := os.Stat(rs.path)
	if err != nil || !info.IsDir() {
		if everSeen {
			return outcome{state: StateRootUnavailable}
		}
		// A root this node cannot see is someone else's; report nothing.
		return outcome{state: stateInvisible}
	}
	id := identityOf(info)
	// Read before any walk: a mount that changes while the walk runs then
	// differs from the recorded signature, and the next reconcile walks
	// again.
	mounts, ok := m.viewMounts(rs.path)
	if !ok {
		mounts.signature = recordedMounts
	}
	if attached != nil {
		sameFolder := id == recorded
		if sameFolder && !rewalk && mounts.signature == recordedMounts && !anyReadable(unreadable) {
			return outcome{keep: true}
		}
		if sameFolder {
			// Asked to (overflow, an unmount below the root), a filesystem
			// was mounted or unmounted inside it, or a directory the last
			// walk could not read is readable now: nothing reports a
			// permission change on a directory that is not watched.
			if err := m.gate.wait(ctx, ticket); err != nil {
				return outcome{canceled: true}
			}
			m.gate.setProgress(ticket, func() int { return attached.Directories(rs.path) })
			return m.walkOutcome(ctx, rs.path, attached, attached.AddRoot(ctx, rs.path), notes, id, mounts)
		}
		// The folder was replaced or remounted: record it from scratch.
		var releases []rootRelease
		m.mu.Lock()
		if rs.backend == attached {
			releases = m.detachLocked(rs, releases)
		}
		m.mu.Unlock()
		m.runReleases(releases)
	}
	return m.attach(ctx, rs, ticket, id, mounts)
}

// attach records a root that has no backend: it classifies the root's
// filesystem, reports the root starting, and walks it with the first backend
// that accepts it.
func (m *Monitor) attach(ctx context.Context, rs *rootState, ticket *walkTicket, id fileID, mounts mountView) outcome {
	if !platformSupported {
		return outcome{state: StateUnsupportedPlatform}
	}
	classify := classifyRoot
	if m.cfg.hooks.classify != nil {
		classify = m.cfg.hooks.classify
	}
	class, err := classify(rs.path)
	if err != nil {
		if ctx.Err() != nil {
			return outcome{canceled: true}
		}
		return outcome{state: StateError, errText: "Couldn't read the folder's filesystem type: " + err.Error()}
	}
	if class.Support == fsUnsupported {
		return outcome{state: StateUnsupportedFilesystem, fsName: class.Name}
	}
	var notes []string
	if class.Support == fsMonitoredCaveat {
		notes = append(notes, fuseCaveat)
	}

	m.mu.Lock()
	rs.state = StateStarting
	rs.notes = notes
	rs.backendName = ""
	m.mu.Unlock()
	m.markDirty()

	if err := m.gate.wait(ctx, ticket); err != nil {
		return outcome{canceled: true}
	}
	b, err := m.attachBackend(ctx, rs, ticket)
	return m.walkOutcome(ctx, rs.path, b, err, notes, id, mounts)
}

// viewMounts reads root's view of the mount table; ok is false when the
// table cannot be read.
func (m *Monitor) viewMounts(root string) (mountView, bool) {
	read := readMounts
	if m.cfg.hooks.mounts != nil {
		read = m.cfg.hooks.mounts
	}
	mounts, err := read()
	if err != nil {
		return mountView{}, false
	}
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		physical = root
	}
	return viewMounts(root, filepath.Clean(physical), mounts), true
}

func (m *Monitor) walkOutcome(ctx context.Context, path string, b Backend, err error, notes []string, id fileID, mounts mountView) outcome {
	if err == nil {
		// Symlinks the walk left alone because they lead onto network
		// filesystems are reported like network mounts inside the root.
		var unreadable []string
		if sp := skippedPathsFrom(ctx); sp != nil {
			var links []string
			links, unreadable = sp.lists()
			mounts.unsupported = append(slices.Clone(mounts.unsupported), links...)
		}
		return outcome{state: StateMonitoring, backend: b, backendName: b.Name(), notes: notes, identity: id, mounts: mounts, unreadable: unreadable}
	}
	if ctx.Err() != nil {
		return outcome{canceled: true}
	}
	var limitErr *WatchLimitError
	if errors.As(err, &limitErr) {
		return outcome{state: StateLimitReached, backendName: BackendInotify, notes: notes, dirs: limitErr.Directories, limit: limitErr.Limit}
	}
	if info, statErr := os.Stat(path); statErr != nil || !info.IsDir() {
		return outcome{state: StateRootUnavailable}
	}
	m.log.WarnContext(ctx, "librarymonitor: recording folder failed", "path", path, "err", err)
	return outcome{state: StateError, errText: err.Error()}
}

// attachBackend records the root with the inotify backend.
func (m *Monitor) attachBackend(ctx context.Context, rs *rootState, ticket *walkTicket) (Backend, error) {
	b, err := m.primaryBackend()
	if err != nil {
		return nil, err
	}
	m.setAttaching(rs, b)
	m.gate.setProgress(ticket, func() int { return b.Directories(rs.path) })
	if err := b.AddRoot(ctx, rs.path); err != nil {
		b.RemoveRoot(rs.path)
		return nil, err
	}
	return b, nil
}

func (m *Monitor) setAttaching(rs *rootState, b Backend) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rs.attaching = b
}

// primaryBackend creates the process's inotify backend on first use. A
// creation failure (for example the max_user_instances limit) is retried on
// the next attempt.
func (m *Monitor) primaryBackend() (Backend, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return nil, errBackendClosed
	}
	if m.primary != nil {
		return m.primary, nil
	}
	opts := BackendOptions{Logger: m.log}
	var (
		b   Backend
		err error
	)
	if m.cfg.hooks.primary != nil {
		b, err = m.cfg.hooks.primary(opts)
	} else {
		b, err = newInotifyBackend(opts, m.cfg.hooks.inotify)
	}
	if err != nil {
		return nil, err
	}
	m.primary = b
	go m.forward(b)
	return b, nil
}

func (m *Monitor) forward(b Backend) {
	for ev := range b.Events() {
		select {
		case m.events <- backendEvent{backend: b, ev: ev}:
		case <-m.ctx.Done():
			return
		}
	}
	// The stream ends when Stop closes the backend, or when its reader
	// failed. Only the second needs handling.
	if m.ctx.Err() != nil {
		return
	}
	select {
	case m.events <- backendEvent{backend: b, closed: true}:
	case <-m.ctx.Done():
	}
}

// anyReadable reports whether any of dirs can be opened for reading now,
// which is what watching it needs.
func anyReadable(dirs []string) bool {
	for _, dir := range dirs {
		if f, err := os.Open(dir); err == nil {
			_ = f.Close()
			return true
		}
	}
	return false
}

// finish applies an attempt's outcome.
func (m *Monitor) finish(rs *rootState, out outcome) {
	m.mu.Lock()
	retry, releases := m.finishLocked(rs, out)
	state := rs.state
	walked := out.backend != nil && rs.backend == out.backend && !out.keep && !out.canceled
	m.mu.Unlock()
	m.runReleases(releases)
	m.markDirty()
	if retry {
		m.Poke()
	}
	if m.cfg.hooks.afterAttempt != nil {
		m.cfg.hooks.afterAttempt(rs.path, state)
	}
	if walked && m.cfg.hooks.afterWalk != nil {
		m.cfg.hooks.afterWalk(rs.path, out.mounts.signature)
	}
}

// finishLocked applies an attempt's outcome. It returns whether to reconcile
// again at once, and the releases to run once m.mu is unlocked.
func (m *Monitor) finishLocked(rs *rootState, out outcome) (retry bool, releases []rootRelease) {
	rs.running = false
	rs.cancel = nil
	rs.attaching = nil
	hit := rs.limitHit
	rs.limitHit = nil
	if !rs.wanted || m.stopped {
		releases = m.discardLocked(rs, out, releases)
		if !rs.wanted && m.roots[rs.path] == rs {
			delete(m.roots, rs.path)
		}
		return false, releases
	}
	if rs.lost {
		// The root disappeared while the attempt ran.
		rs.lost = false
		releases = m.discardLocked(rs, out, releases)
		rs.state = StateRootUnavailable
		rs.everSeen = true
		return false, releases
	}
	if out.backend != nil && out.backend != m.primary {
		// The backend failed while the attempt walked with it.
		releases = m.discardLocked(rs, out, releases)
		rs.state = StateStarting
		return true, releases
	}
	if hit != nil {
		// The watch limit was hit at runtime while the attempt ran, and the
		// backend released the root; the attempt's outcome no longer holds.
		releases = m.discardLocked(rs, out, releases)
		rs.state = StateLimitReached
		rs.backendName = BackendInotify
		if out.notes != nil {
			rs.notes = out.notes
		}
		rs.dirs = max(hit.Directories, out.dirs)
		rs.limit = hit.Limit
		rs.limitSeen = m.removals
		rs.everSeen = true
		return false, releases
	}
	if out.canceled {
		// Canceled, then wanted again before it finished: retry now.
		return true, releases
	}
	if out.keep {
		// A re-walk asked for while the attempt ran starts right away.
		return rs.rewalk, releases
	}
	rs.state = out.state
	rs.notes = out.notes
	rs.fsName = out.fsName
	rs.errText = out.errText
	rs.dirs = out.dirs
	rs.limit = out.limit
	if out.backend != nil {
		rs.backend = out.backend
		rs.identity = out.identity
		rs.mounts = out.mounts.signature
		rs.unsupportedMounts = out.mounts.unsupported
		rs.unreadable = out.unreadable
		rs.everSeen = true
	} else {
		releases = m.detachLocked(rs, releases)
		rs.mounts = ""
		rs.unsupportedMounts = nil
		rs.unreadable = nil
	}
	rs.backendName = out.backendName
	if out.state == StateLimitReached {
		rs.limitSeen = m.removals
	}
	return rs.backend != nil && rs.rewalk, releases
}

// discardLocked drops an attempt's result: it queues the root's release from
// the backend the attempt recorded it with, and from the one it was
// attached to.
func (m *Monitor) discardLocked(rs *rootState, out outcome, releases []rootRelease) []rootRelease {
	if out.backend != nil {
		releases = m.queueReleaseLocked(releases, out.backend, rs.path)
	}
	return m.detachLocked(rs, releases)
}

// handleEvent routes one backend event.
func (m *Monitor) handleEvent(be backendEvent, now time.Time) {
	if be.closed {
		m.handleBackendClosed(be.backend)
		return
	}
	ev := be.ev
	switch ev.Kind {
	case EventOverflow:
		m.handleOverflow(be.backend)
	case EventRootLost:
		m.handleRootLost(be.backend, ev.Root)
	case EventLimitReached:
		m.handleLimit(be.backend, ev)
	case EventRewalk:
		m.handleRewalk(be.backend, ev.Root)
	case EventRescanRoot:
		m.handleRescanRoot(ev.Root)
	case EventRename:
		switch {
		case ignoredName(ev.OldName) && ignoredName(ev.Name):
			return
		case ignoredName(ev.OldName):
			ev = Event{Kind: EventMovedTo, Dir: ev.Dir, Name: ev.Name, IsDir: ev.IsDir}
		case ignoredName(ev.Name):
			ev = Event{Kind: EventMovedFrom, Dir: ev.OldDir, Name: ev.OldName, IsDir: ev.IsDir}
		}
		m.tracker.observe(ev, now)
	default:
		if ignoredName(ev.Name) {
			return
		}
		m.tracker.observe(ev, now)
	}
}

// handleBackendClosed replaces a backend whose event stream ended while the
// monitor runs: its reader failed, so nothing it records is watched any
// more. Its roots report starting and are recorded again with a new backend
// on the reconcile this asks for; a root still being walked with it is
// discarded when that attempt finishes (see finishLocked).
func (m *Monitor) handleBackendClosed(b Backend) {
	m.log.Error("librarymonitor: the kernel event stream stopped; recording library folders again", "backend", b.Name())
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	if m.primary == b {
		m.primary = nil
	}
	for _, rs := range m.roots {
		if rs.backend == b {
			rs.backend = nil
			rs.backendName = ""
			rs.state = StateStarting
			m.removals++
		}
	}
	m.mu.Unlock()
	// Frees the descriptor; the reader has already stopped.
	go func() { _ = b.Close() }()
	m.markDirty()
	m.Poke()
}

// handleRescanRoot queues a whole-library scan for each monitored library
// with root as a folder.
func (m *Monitor) handleRescanRoot(root string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rs := m.roots[root]; rs != nil && rs.wanted {
		for _, id := range rs.libraries {
			m.libraryScans[id] = struct{}{}
		}
	}
}

// handleOverflow re-walks every root on the backend, to record directories
// created in the gap, and queues one library scan per affected library. A
// root whose first walk on the backend is still running counts too: the
// walk may have missed a directory created in the gap, and it is walked
// again once it ends.
func (m *Monitor) handleOverflow(b Backend) {
	m.log.Warn("librarymonitor: kernel event queue overflowed; rescanning monitored libraries", "backend", b.Name())
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, rs := range m.roots {
		if (rs.backend != b && rs.attaching != b) || !rs.wanted {
			continue
		}
		for _, id := range rs.libraries {
			m.libraryScans[id] = struct{}{}
		}
		m.rewalkLocked(rs)
	}
}

// handleRewalk walks a root again after kernel state below it went away
// without events (see EventRewalk). Nothing is queued.
func (m *Monitor) handleRewalk(b Backend, root string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rs := m.roots[root]
	if rs == nil || !rs.wanted || (rs.backend != b && !rs.running) {
		return
	}
	m.rewalkLocked(rs)
}

// rewalkLocked asks for rs to be walked again: now, or when its running
// attempt finishes.
func (m *Monitor) rewalkLocked(rs *rootState) {
	rs.rewalk = true
	if !rs.running && !m.stopped {
		m.spawnLocked(rs)
	}
}

// handleRootLost marks a root unavailable. Nothing is queued: a missing root
// must not turn into cleanup. Reconcile retries it.
func (m *Monitor) handleRootLost(b Backend, root string) {
	m.log.Warn("librarymonitor: library folder disappeared", "path", root, "backend", b.Name())
	m.mu.Lock()
	if rs := m.roots[root]; rs != nil {
		if rs.backend == b {
			rs.backend = nil
			rs.backendName = ""
			m.removals++
			rs.state = StateRootUnavailable
			rs.everSeen = true
		}
		if rs.running {
			rs.lost = true
		}
	}
	m.mu.Unlock()
	m.tracker.dropUnder(root)
	m.markDirty()
}

// handleLimit marks a root limit_reached after a runtime watch failure; the
// backend already released its watches. An attempt running on the root (a
// first walk, or an overflow re-walk) must not report it monitored when it
// finishes, so the hit is recorded for finish too.
func (m *Monitor) handleLimit(b Backend, ev Event) {
	var limitErr *WatchLimitError
	if !errors.As(ev.Err, &limitErr) {
		limitErr = &WatchLimitError{}
	}
	m.log.Warn("librarymonitor: inotify watch limit reached", "path", ev.Root, "limit", limitErr.Limit, "directories", limitErr.Directories)
	m.mu.Lock()
	if rs := m.roots[ev.Root]; rs != nil && (rs.backend == b || rs.running) {
		if rs.backend == b {
			rs.backend = nil
			m.removals++
		}
		if rs.running {
			rs.limitHit = limitErr
		}
		rs.state = StateLimitReached
		rs.backendName = BackendInotify
		rs.dirs = limitErr.Directories
		rs.limit = limitErr.Limit
		rs.limitSeen = m.removals
	}
	m.mu.Unlock()
	m.markDirty()
}

// statusLoop reports this node's rows when they change and every
// StatusRefresh.
func (m *Monitor) statusLoop(ctx context.Context) {
	defer close(m.statusDone)
	if m.cfg.Status == nil {
		<-ctx.Done()
		return
	}
	refresh := time.NewTicker(m.cfg.StatusRefresh)
	defer refresh.Stop()
	var last []LibraryStatus
	reported := false
	for {
		force := false
		select {
		case <-ctx.Done():
			return
		case <-m.dirtyCh:
		case <-refresh.C:
			force = true
		}
		m.mu.Lock()
		rows := m.statusRowsLocked()
		m.mu.Unlock()
		if !force && reported && slices.Equal(rows, last) {
			continue
		}
		reportCtx, cancel := context.WithTimeout(ctx, statusTimeout)
		err := m.cfg.Status.Report(reportCtx, m.cfg.NodeID, rows)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				m.log.WarnContext(ctx, "librarymonitor: reporting status failed", "node_id", m.cfg.NodeID, "err", err)
			}
			continue
		}
		last = rows
		reported = true
	}
}
