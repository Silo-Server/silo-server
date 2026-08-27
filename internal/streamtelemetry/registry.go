package streamtelemetry

import (
	"context"
	"encoding/hex"
	"hash/maphash"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

const shardCount = 32

var now = time.Now

type sessionShard struct {
	sync.RWMutex
	sessions   map[string]*logicalSession
	tombstones map[string]sessionTombstone
	// pendingRealtime holds realtime-connection state that arrived before the
	// session existed. Clients open the control socket as soon as they have a
	// sessionId — before the first byte route is hit — so in the normal
	// ordering the state would otherwise be dropped and every live session
	// would report RealtimeConnectionAlive=false. Applied on session creation
	// and pruned by the sweep, so it cannot grow without bound.
	pendingRealtime map[string]pendingRealtime
}

// sessionTombstone is what the registry remembers about a session whose
// measurement it retired for idleness. It is the last view the session would
// have published, with every live quantity zeroed: the bytes on it are memory,
// not a current total, and nothing about it may read as activity.
//
// Without this memory a fully-buffered session is byte-for-byte
// indistinguishable from a session that never delivered anything once prune
// takes LastByteAccepted with it. The reporting publisher keeps publishing the
// session, so the merged row would otherwise become "reported, nothing
// measured" and hide a viewer who is still watching.
type sessionTombstone struct {
	view     SessionView
	prunedAt time.Time
}

type pendingRealtime struct {
	connected bool
	at        time.Time
}

type Registry struct {
	cfg    Config
	store  SnapshotStore
	logger *slog.Logger
	seed   maphash.Seed
	shards [shardCount]sessionShard

	transfersMu sync.RWMutex
	transfers   map[string]*transfer

	sessionReservations      atomic.Int64
	transferReservations     atomic.Int64
	observationReservations  atomic.Int64
	droppedObservations      atomic.Int64
	droppedBytes             atomic.Int64
	unattributedObservations atomic.Int64
	unattributedBytes        atomic.Int64
	// lastDropUnixNano records when an observation was last dropped. Truncated
	// is a statement about CURRENT blindness — BuildGlobalView pins
	// Complete=false for as long as a publisher reports it — so it has to
	// decay, otherwise one transient burst marks a process degraded until it
	// restarts and a later real truncation is indistinguishable. The monotonic
	// Dropped* counters remain the permanent record.
	lastDropUnixNano        atomic.Int64
	lastWarnUnixNano        atomic.Int64
	lastPublishWarnUnixNano atomic.Int64
	sequence                atomic.Uint64
	reportingPublisherID    atomic.Value
	startOnce               sync.Once
	stopOnce                sync.Once
	stop                    chan struct{}
	done                    chan struct{}
	started                 atomic.Bool
	leaveMu                 sync.Mutex
	left                    bool
}

func NewRegistry(cfg Config, store SnapshotStore, logger *slog.Logger) *Registry {
	if cfg.PublisherID == "" {
		cfg.PublisherID = uuid.NewString()
	}
	if cfg.PublisherEpoch == 0 {
		cfg.PublisherEpoch = now().UnixNano()
	}
	if store == nil {
		store = NewLocalStore()
	}
	if logger == nil {
		logger = slog.Default()
	}
	r := &Registry{cfg: cfg, store: store, logger: logger, seed: maphash.MakeSeed(), transfers: make(map[string]*transfer), stop: make(chan struct{}), done: make(chan struct{})}
	for i := range r.shards {
		r.shards[i].sessions = make(map[string]*logicalSession)
		r.shards[i].tombstones = make(map[string]sessionTombstone)
		r.shards[i].pendingRealtime = make(map[string]pendingRealtime)
	}
	return r
}

func (r *Registry) Enabled() bool { return r != nil && r.cfg.Enabled }

// ViewTTL exposes the resolved bounded-staleness window so the view cache can be
// built from the config this registry already parsed, rather than reading and
// re-validating every SILO_STREAM_TELEMETRY_* variable a second time.
func (r *Registry) ViewTTL() time.Duration {
	if r == nil {
		return 0
	}
	return r.cfg.ViewTTL
}

// Config returns the resolved configuration this registry was built with, so a
// caller that already has the registry does not re-read and re-validate the
// environment to learn one knob.
func (r *Registry) Config() Config {
	if r == nil {
		return Config{}
	}
	return r.cfg
}

func (r *Registry) Store() SnapshotStore {
	if r == nil {
		return nil
	}
	return r.store
}

func reserve(counter *atomic.Int64, max int64) bool {
	for {
		current := counter.Load()
		if current >= max {
			return false
		}
		if counter.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func (r *Registry) begin(route MediaRoute, capture CaptureSet) *Observation {
	obs := newObservation(r, route, capture)
	if reserve(&r.observationReservations, r.cfg.MaxObservations) {
		obs.reserved = true
	} else {
		obs.countingOnly = true
		r.drop("observation capacity exhausted")
	}
	return obs
}

func (r *Registry) attach(obs *Observation, attachment Attachment) {
	obs.mu.Lock()
	defer obs.mu.Unlock()
	if obs.released || obs.countingOnly {
		return
	}
	observedAt := obs.Capture.ReceivedAt
	if observedAt.IsZero() {
		observedAt = now()
	}
	if obs.attachment != nil {
		if obs.target.session != nil {
			s := obs.target.session
			s.mu.Lock()
			s.recordConflicts(attachment, observedAt, r.cfg.MaxIdentityConflictsPerSession)
			s.mu.Unlock()
		}
		return
	}
	if attachment.TokenIssuedAt.IsZero() && !obs.Capture.TokenIssuedAt.IsZero() {
		attachment.TokenIssuedAt = obs.Capture.TokenIssuedAt
		attachment.TokenIssuedAtSource = obs.Capture.TokenIssuedFrom
	}
	if attachment.TokenIssuedAtSource == "" {
		attachment.TokenIssuedAtSource = TokenIssuedAtSourceNone
	}
	if obs.route.Class.foldsIntoTransfer() {
		key := transferKey(attachment, obs.Capture)
		r.transfersMu.Lock()
		t := r.transfers[key]
		if t == nil {
			if !reserve(&r.transferReservations, r.cfg.MaxTransfers) {
				r.transfersMu.Unlock()
				obs.countingOnly = true
				r.drop("transfer capacity exhausted")
				return
			}
			t = &transfer{id: key, subject: attachment.Subject, profileID: attachment.ProfileID,
				mediaFileID: attachment.MediaFileID, route: obs.route, capture: obs.Capture,
				observations: make(map[string]*Observation),
				outcomes:     make(map[httpstream.StreamOutcome]int64)}
			r.transfers[key] = t
		}
		t.mu.Lock()
		if len(t.observations) >= r.cfg.MaxObservationsPerSession {
			t.mu.Unlock()
			r.transfersMu.Unlock()
			obs.countingOnly = true
			r.drop("per-transfer observation capacity exhausted")
			return
		}
		t.observations[obs.id] = obs
		t.openObservations++
		t.requestCount++
		// The newest request's capture wins: viewer IP, device and client can
		// legitimately change across a resumed download.
		t.capture = obs.Capture
		t.mu.Unlock()
		r.transfersMu.Unlock()
		obs.attachment = &attachment
		obs.target.transfer = t
		return
	}
	if attachment.SessionID == "" {
		obs.countingOnly = true
		r.drop("attachment has no canonical session id")
		return
	}
	shard := r.shard(attachment.SessionID)
	shard.Lock()
	s := shard.sessions[attachment.SessionID]
	if s == nil {
		if !reserve(&r.sessionReservations, r.cfg.MaxSessions) {
			shard.Unlock()
			obs.countingOnly = true
			r.drop("session capacity exhausted")
			return
		}
		s = newLogicalSession(attachment, r.cfg, observedAt)
		delete(shard.tombstones, attachment.SessionID)
		if pending, ok := shard.pendingRealtime[attachment.SessionID]; ok {
			s.realtimeAlive = pending.connected
			delete(shard.pendingRealtime, attachment.SessionID)
		}
		shard.sessions[attachment.SessionID] = s
	}
	s.mu.Lock()
	if len(s.observations) >= r.cfg.MaxObservationsPerSession {
		s.mu.Unlock()
		shard.Unlock()
		obs.countingOnly = true
		r.drop("per-session observation capacity exhausted")
		return
	}
	s.recordConflicts(attachment, observedAt, r.cfg.MaxIdentityConflictsPerSession)
	key := routeID(obs.Capture.Method, obs.Capture.Pattern)
	activity := s.routes[key]
	if activity == nil {
		if len(s.routes) >= r.cfg.MaxRoutesPerSession {
			s.routesOverflowed = true
		} else {
			activity = &routeActivity{Method: obs.Capture.Method, Pattern: obs.Capture.Pattern,
				Role: obs.route.Role, Class: obs.route.Class, CapRelevant: obs.route.CapRelevant}
			s.routes[key] = activity
		}
	}
	s.observations[obs.id] = obs
	s.openObservations++
	s.requestCount++
	if activity != nil {
		activity.Open++
		activity.Requests++
	}
	if obs.Capture.ViewerIP != "" {
		s.viewerIPs.add(obs.Capture.ViewerIP)
	}
	if obs.Capture.DeviceID != "" {
		s.deviceIDs.add(obs.Capture.DeviceID)
	}
	if obs.Capture.Client != (ClientVariant{}) {
		s.clientVariants.add(obs.Capture.Client)
	}
	if obs.Capture.UserAgent != "" {
		s.userAgents.add(obs.Capture.UserAgent)
	}
	s.tokenIssuedSources[attachment.TokenIssuedAtSource]++
	if !attachment.TokenIssuedAt.IsZero() {
		s.tokenIssuedAts.add(attachment.TokenIssuedAt.UnixNano())
	}
	s.mu.Unlock()
	shard.Unlock()
	obs.attachment = &attachment
	obs.target.session = s
}

func (r *Registry) release(obs *Observation, outcome httpstream.StreamOutcome) {
	obs.mu.Lock()
	if obs.released {
		obs.mu.Unlock()
		return
	}
	obs.released = true
	target := obs.target
	attached := obs.attachment != nil
	countingOnly := obs.countingOnly
	obs.mu.Unlock()
	bytes := obs.BytesAccepted()
	if countingOnly {
		r.droppedBytes.Add(bytes)
	} else if !attached {
		r.unattributedObservations.Add(1)
		r.unattributedBytes.Add(bytes)
	} else if target.transfer != nil {
		t := target.transfer
		t.mu.Lock()
		t.bytesFolded += bytes
		t.openObservations--
		t.lastObservationEnd = now()
		t.outcomes[outcome]++
		delete(t.observations, obs.id)
		t.mu.Unlock()
	} else if target.session != nil {
		s := target.session
		s.mu.Lock()
		delete(s.observations, obs.id)
		s.bytesFolded += bytes
		s.openObservations--
		s.lastObservationEnd = now()
		s.outcomes[outcome]++
		if activity := s.routes[routeID(obs.Capture.Method, obs.Capture.Pattern)]; activity != nil {
			activity.Open--
			activity.BytesFolded += bytes
			activity.LastObservationEnd = s.lastObservationEnd
		}
		s.mu.Unlock()
	}
	if obs.reserved {
		r.observationReservations.Add(-1)
	}
}

func (r *Registry) drop(reason string) {
	r.lastDropUnixNano.Store(now().UnixNano())
	r.droppedObservations.Add(1)
	r.warnRateLimited(reason, &r.lastWarnUnixNano)
}

// DeclareReportingPublisher records that this process also runs a reporting
// publisher under the given id, so consumers of the merged view can tell an
// absent reporter from a process that simply has nothing to report. A process
// running older code declares nothing, which is exactly the signal wanted.
func (r *Registry) DeclareReportingPublisher(publisherID string) {
	if r == nil || publisherID == "" {
		return
	}
	r.reportingPublisherID.Store(publisherID)
}

func (r *Registry) reportingCompanion() string {
	if r == nil {
		return ""
	}
	id, _ := r.reportingPublisherID.Load().(string)
	return id
}

func (r *Registry) warnRateLimited(message string, stamp *atomic.Int64, attrs ...any) {
	warnRateLimited(r.logger, stamp, message, attrs...)
}

// warnRateLimited emits at most one warning per minute per stamp, so a publisher
// that keeps failing cannot fill the log. Shared by every telemetry component
// that publishes on a ticker: they all fail the same way and should not each
// carry their own copy of the throttle.
func warnRateLimited(logger *slog.Logger, stamp *atomic.Int64, message string, attrs ...any) {
	n := now().UnixNano()
	for {
		previous := stamp.Load()
		if previous != 0 && n-previous < int64(time.Minute) {
			return
		}
		if stamp.CompareAndSwap(previous, n) {
			attrs = append([]any{"component", "stream_telemetry"}, attrs...)
			attrs = append([]any{"reason", message}, attrs...)
			logger.Warn("stream telemetry warning", attrs...)
			return
		}
	}
}

// truncatedAt reports whether the registry was blind recently enough for the
// snapshot at `at` to be incomplete. The window matches Freshness, which is the
// same horizon BuildGlobalView uses to decide a publisher is still current.
func (r *Registry) truncatedAt(at time.Time) bool {
	last := r.lastDropUnixNano.Load()
	if last == 0 {
		return false
	}
	window := r.cfg.Freshness
	if window <= 0 {
		window = defaultFreshness
	}
	if at.IsZero() {
		at = now()
	}
	return at.Sub(time.Unix(0, last)) < window
}

// maxPendingRealtimePerShard spreads the session budget over the shards so held
// realtime state can never outgrow the sessions it is waiting for.
func maxPendingRealtimePerShard(maxSessions int64) int64 {
	if maxSessions <= 0 {
		return 0
	}
	return maxSessions/shardCount + 1
}

// maxTombstonesPerShard spreads the session budget over the shards so retired
// measurement memory can never outgrow the live sessions it supplements.
func maxTombstonesPerShard(maxSessions int64) int64 {
	if maxSessions <= 0 {
		return 0
	}
	return maxSessions/shardCount + 1
}

// transferKey identifies one logical transfer: the same principal pulling the
// same file over the same route from the same place. Deliberately excludes
// anything per-request so overlapping Range GETs for the same file fold into a
// single record — every ranged byte route is transfer-class, and a record per
// request exhausts MaxTransfers within one retention window while requestCount,
// which exists to count exactly this, stays pinned at 1.
//
// Viewer IP and device stay IN the identity. Folding every viewer of one file
// into a single row and letting the newest capture win erases the fan-out signal
// the re-stream detection depends on: two households pulling the same file would
// read as one transfer whose viewer address flickered.
//
// The identity is HASHED rather than returned joined, because this value becomes
// a Redis hash field NAME (store_redis.go). Key names are far more exposed than
// values — they surface in SCAN, MONITOR, slowlog and RDB dumps — so a joined id
// publishes a viewer's IP address and their client-supplied device id in the
// clear, into the one place a redis operator sees without reading any record. It
// also carries the NUL separators into every tool that walks the keyspace, which
// is not a hypothetical: NUL-bearing field names silently truncate line-oriented
// readers. Nothing is lost by hashing. TransferView carries Subject, ProfileID,
// MediaFileID, Method, Pattern, ViewerIP and DeviceID as fields already, so the
// id only has to be stable and collision-free, and no consumer parses it —
// registry.go and store_redis.go sort by it, and the global merge never joins
// transfers across publishers on it.
//
// SHA-256 and not maphash: maphash is seeded per process, so two publishers
// observing the same logical transfer would emit different ids, which is exactly
// the cross-publisher agreement the merge is entitled to assume.
func transferKey(a Attachment, capture CaptureSet) string {
	digest := digest128([]byte(strings.Join([]string{
		string(a.Subject.Kind), a.Subject.ID, a.ProfileID, strconv.Itoa(a.MediaFileID),
		capture.Method, capture.Pattern, capture.ViewerIP, capture.DeviceID,
	}, "\x00")))
	return hex.EncodeToString(digest[:])
}

func (r *Registry) shard(id string) *sessionShard {
	var h maphash.Hash
	h.SetSeed(r.seed)
	h.WriteString(id)
	return &r.shards[h.Sum64()%shardCount]
}

// SetRealtimeConnection records whether a realtime control socket is alive for
// a session. It is routinely called BEFORE the session exists — a client opens
// the socket as soon as it has a sessionId, which is before it requests the
// first media route — so state for an unknown session is held until an attach
// creates it rather than discarded.
func (r *Registry) SetRealtimeConnection(sessionID string, connected bool) {
	if r == nil || !r.cfg.Enabled || sessionID == "" {
		return
	}
	shard := r.shard(sessionID)
	shard.Lock()
	if s := shard.sessions[sessionID]; s != nil {
		s.mu.Lock()
		s.realtimeAlive = connected
		s.mu.Unlock()
		delete(shard.pendingRealtime, sessionID)
		shard.Unlock()
		return
	}
	if _, held := shard.pendingRealtime[sessionID]; held || int64(len(shard.pendingRealtime)) < maxPendingRealtimePerShard(r.cfg.MaxSessions) {
		shard.pendingRealtime[sessionID] = pendingRealtime{connected: connected, at: now()}
	} else {
		r.drop("pending realtime capacity exhausted")
	}
	shard.Unlock()
}

func (r *Registry) Start(ctx context.Context) {
	if r == nil || !r.cfg.Enabled {
		return
	}
	r.startOnce.Do(func() {
		r.started.Store(true)
		go func() {
			defer close(r.done)
			ticker := time.NewTicker(r.cfg.SweepInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-r.stop:
					return
				case sweepStart := <-ticker.C:
					snapshot := r.sweep(sweepStart)
					snapshot.Sequence = r.sequence.Add(1)
					if err := r.store.Publish(ctx, snapshot); err != nil {
						r.warnRateLimited("failed to publish stream telemetry snapshot", &r.lastPublishWarnUnixNano, "error", err)
					}
				}
			}
		}()
	})
}

func (r *Registry) Stop(ctx context.Context) error {
	if r == nil || !r.cfg.Enabled || !r.started.Load() {
		return nil
	}
	r.stopOnce.Do(func() { close(r.stop) })
	select {
	case <-r.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	global, ok := r.store.(GlobalSnapshotStore)
	if !ok {
		return nil
	}
	r.leaveMu.Lock()
	defer r.leaveMu.Unlock()
	if r.left {
		return nil
	}
	if err := global.Leave(ctx); err != nil {
		return err
	}
	r.left = true
	return nil
}

func (r *Registry) Sweep() Snapshot { return r.sweep(now()) }

func (r *Registry) sweep(sweepStart time.Time) Snapshot {
	for i := range r.shards {
		shard := &r.shards[i]
		shard.Lock()
		for id, tombstone := range shard.tombstones {
			if sweepStart.Sub(tombstone.prunedAt) >= r.cfg.TombstoneRetention {
				delete(shard.tombstones, id)
			}
		}
		for id, s := range shard.sessions {
			s.mu.Lock()
			total := s.bytesFolded
			routeTotals := make(map[string]int64, len(s.routes))
			for key, activity := range s.routes {
				routeTotals[key] = activity.BytesFolded
			}
			for _, obs := range s.observations {
				bytes := obs.BytesAccepted()
				total += bytes
				key := routeID(obs.Capture.Method, obs.Capture.Pattern)
				if _, tracked := s.routes[key]; tracked {
					routeTotals[key] += bytes
				}
			}
			if total > s.lastSweptBytes {
				s.lastByteAccepted = sweepStart
			}
			s.lastSweptBytes = total
			for key, totalForRoute := range routeTotals {
				activity := s.routes[key]
				if totalForRoute > activity.LastSweptBytes {
					activity.LastByteAccepted = sweepStart
				}
				activity.LastSweptBytes = totalForRoute
			}
			prune := s.openObservations == 0 && !s.lastObservationEnd.IsZero() && sweepStart.Sub(s.lastObservationEnd) >= r.cfg.Retention
			var tombstone SessionView
			remember := false
			if prune {
				tombstone, remember = tombstoneViewOf(s)
			}
			s.mu.Unlock()
			if prune {
				delete(shard.sessions, id)
				r.sessionReservations.Add(-1)
				if remember {
					limit := maxTombstonesPerShard(r.cfg.MaxSessions)
					if int64(len(shard.tombstones)) >= limit {
						oldestID := ""
						var oldestAt time.Time
						for tombstoneID, candidate := range shard.tombstones {
							if oldestID == "" || candidate.prunedAt.Before(oldestAt) ||
								(candidate.prunedAt.Equal(oldestAt) && tombstoneID < oldestID) {
								oldestID, oldestAt = tombstoneID, candidate.prunedAt
							}
						}
						if oldestID != "" {
							delete(shard.tombstones, oldestID)
							r.warnRateLimited("session tombstone capacity exhausted", &r.lastWarnUnixNano)
						}
					}
					if limit > 0 {
						shard.tombstones[id] = sessionTombstone{view: tombstone, prunedAt: sweepStart}
					}
				}
			}
		}
		// Realtime state whose session never arrived — a socket that opened and
		// closed without the client ever requesting media — expires on the same
		// horizon as an idle session.
		for id, pending := range shard.pendingRealtime {
			if sweepStart.Sub(pending.at) >= r.cfg.Retention {
				delete(shard.pendingRealtime, id)
			}
		}
		shard.Unlock()
	}
	r.transfersMu.Lock()
	for id, t := range r.transfers {
		t.mu.Lock()
		total := t.bytesFolded
		for _, obs := range t.observations {
			total += obs.BytesAccepted()
		}
		if total > t.lastSweptBytes {
			t.lastByteAccepted = sweepStart
		}
		t.lastSweptBytes = total
		prune := t.openObservations == 0 && !t.lastObservationEnd.IsZero() && sweepStart.Sub(t.lastObservationEnd) >= r.cfg.Retention
		t.mu.Unlock()
		if prune {
			delete(r.transfers, id)
			r.transferReservations.Add(-1)
		}
	}
	r.transfersMu.Unlock()
	return r.SnapshotAt(sweepStart)
}

// Snapshot renders the registry state without sweeping live observations. Byte
// totals and LastByteAccepted reflect lastSweptBytes from the most recent sweep;
// callers that need current totals must call Sweep.
func (r *Registry) Snapshot() Snapshot { return r.SnapshotAt(now()) }

// SnapshotAt renders the registry state at capturedAt without sweeping live
// observations. Byte totals and LastByteAccepted reflect lastSweptBytes from the
// most recent sweep; callers that need current totals must call Sweep.
func (r *Registry) SnapshotAt(capturedAt time.Time) Snapshot {
	view := Snapshot{PublisherID: r.cfg.PublisherID, ReportingPublisherID: r.reportingCompanion(),
		NodeID: r.cfg.NodeID, PublisherEpoch: r.cfg.PublisherEpoch, Sequence: r.sequence.Load(), CapturedAt: capturedAt,
		Truncated: r.truncatedAt(capturedAt), DroppedObservations: r.droppedObservations.Load(),
		DroppedBytes: r.droppedBytes.Load(), UnattributedObservations: r.unattributedObservations.Load(),
		UnattributedBytes: r.unattributedBytes.Load()}
	for i := range r.shards {
		shard := &r.shards[i]
		shard.RLock()
		for _, s := range shard.sessions {
			s.mu.Lock()
			view.Sessions = append(view.Sessions, sessionViewOf(s))
			s.mu.Unlock()
		}
		for _, tombstone := range shard.tombstones {
			view.Sessions = append(view.Sessions, tombstone.view)
		}
		shard.RUnlock()
	}
	r.transfersMu.RLock()
	for _, t := range r.transfers {
		t.mu.Lock()
		view.Transfers = append(view.Transfers, transferViewOf(t))
		t.mu.Unlock()
	}
	r.transfersMu.RUnlock()
	sort.Slice(view.Sessions, func(i, j int) bool { return view.Sessions[i].SessionID < view.Sessions[j].SessionID })
	sort.Slice(view.Transfers, func(i, j int) bool { return view.Transfers[i].ID < view.Transfers[j].ID })
	return cloneSnapshot(view)
}
