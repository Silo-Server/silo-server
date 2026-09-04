package streamtelemetry

import (
	"context"
	"log/slog"
	"sort"
	"sync/atomic"
	"time"
)

// ReportedPublisherSuffix distinguishes a process's reporting publisher from its
// measuring one. They are deliberately SEPARATE publishers rather than one
// publisher carrying both: BuildGlobalView's rules are stated per publisher, and
// keeping them apart is what lets the merge say "the edge measured this, the
// session manager claimed that" without either having to trust the other.
const ReportedPublisherSuffix = "#reported"

// ReportedSession is one live session as a playback session manager knows it.
//
// This is what a client SAID, never what was delivered. There is deliberately no
// byte count and no viewer address on this type: those belong exclusively to the
// outermost viewer edge (§2.5), and a session manager is not one. Making them
// unrepresentable here is cheaper than remembering not to set them.
type ReportedSession struct {
	SessionID       string
	Subject         Subject
	ProfileID       string
	MediaFileID     int
	PlayMethod      string
	StartedAt       time.Time
	Paused          bool
	PositionSeconds float64
	// RealtimeAlive is the control-socket state the manager holds. For a paused
	// session this is the liveness signal, because a paused client stops pulling
	// bytes entirely (issue #243).
	RealtimeAlive bool
}

// ReportedSessionSource is implemented by a playback session manager.
type ReportedSessionSource interface {
	// ReportedSessions returns the sessions this process currently owns.
	ReportedSessions() []ReportedSession
}

// ReportedPublisher publishes what this process's session manager believes is
// playing, as an ordinary telemetry publisher.
//
// This is the piece that makes the merged view COMPLETE BY CONSTRUCTION. Without
// it, telemetry sees only sessions that moved bytes through an observed family,
// so every consumer had to reconcile the view against the legacy store at read
// time — and reconciling two sets is where the ghosts, the coverage gaps and the
// paused-session special cases all came from. Publishing the reported side into
// the same merge turns all of that into fields on a row:
//
//	Reported && no bytes -> a client claiming to watch something unsent (#666)
//	bytes && !Reported   -> delivery nobody claimed
//	both                 -> an ordinary viewer
//
// One publisher per process, matching every other publisher. There is no leader
// election because there is nothing to elect: a process reports the sessions it
// owns, and a session that moves between processes is briefly reported by both,
// which the merge resolves by taking the newest report.
type ReportedPublisher struct {
	cfg    Config
	source ReportedSessionSource
	store  SnapshotStore
	logger *slog.Logger

	sequence atomic.Uint64
	// loop is the publish-on-a-ticker lifecycle, shared with Registry so both of
	// a process's publishers start, stop and leave the roster identically.
	loop *publishLoop

	lastPublishWarnUnixNano atomic.Int64
}

// ReportedPublisherIDFor returns the publisher id a process's reporting
// publisher writes under, given its measuring publisher id. The store needs this
// before the publisher exists, because RedisStore binds one id for its lifetime.
func ReportedPublisherIDFor(publisherID string) string {
	return publisherID + ReportedPublisherSuffix
}

// NewReportedPublisher returns a publisher over source. A nil source, a nil store
// or disabled telemetry yields a publisher whose Start is a no-op, so callers do
// not have to branch on configuration.
func NewReportedPublisher(cfg Config, source ReportedSessionSource, store SnapshotStore, logger *slog.Logger) *ReportedPublisher {
	if logger == nil {
		logger = slog.Default()
	}
	cfg.PublisherID = ReportedPublisherIDFor(cfg.PublisherID)
	return &ReportedPublisher{cfg: cfg, source: source, store: store, logger: logger,
		loop: newPublishLoop()}
}

// Enabled reports whether this publisher will do anything.
func (p *ReportedPublisher) Enabled() bool {
	return p != nil && p.cfg.Enabled && p.source != nil && p.store != nil
}

// PublisherID is the id this publisher writes under.
func (p *ReportedPublisher) PublisherID() string {
	if p == nil {
		return ""
	}
	return p.cfg.PublisherID
}

// SnapshotAt renders the current reported state without publishing it.
func (p *ReportedPublisher) SnapshotAt(capturedAt time.Time) Snapshot {
	snapshot := Snapshot{PublisherID: p.cfg.PublisherID, NodeID: p.cfg.NodeID,
		Coverage:       PublisherCoverage{Declared: true},
		PublisherEpoch: p.cfg.PublisherEpoch, Sequence: p.sequence.Load(), CapturedAt: capturedAt}
	if p.source == nil {
		return snapshot
	}
	reported := p.source.ReportedSessions()
	// Drop the unusable entries and order BEFORE applying the cap. The source
	// returns Go map order, which is randomized per call, so truncating first
	// published a different arbitrary subset every sweep and made sessions
	// flicker in and out of the admin view; an empty id could also consume a slot
	// before being discarded.
	usable := reported[:0:0]
	for _, session := range reported {
		if session.SessionID != "" {
			usable = append(usable, session)
		}
	}
	sort.Slice(usable, func(i, j int) bool { return usable[i].SessionID < usable[j].SessionID })
	// The same bound the measured side gets. A session manager holding more live
	// sessions than the cap is a bigger problem than a truncated view, but a
	// truncated view that says so is still better than an unbounded publish.
	if limit := int(p.cfg.MaxSessions); limit > 0 && len(usable) > limit {
		usable = usable[:limit]
		snapshot.Truncated = true
	}
	for _, session := range usable {
		snapshot.Sessions = append(snapshot.Sessions, SessionView{
			SessionID: session.SessionID, Subject: session.Subject, ProfileID: session.ProfileID,
			MediaFileID: session.MediaFileID, PlayMethod: session.PlayMethod,
			StartedAt: session.StartedAt, StartedAtSource: StartedAtSourceSession,
			RealtimeConnectionAlive: session.RealtimeAlive,
			Reported:                true,
			ReportedPaused:          session.Paused,
			ReportedPositionSeconds: session.PositionSeconds,
			ReportedAt:              capturedAt,
			// Everything else stays zero on purpose. No bytes, no viewer IPs, no
			// routes: this publisher measured nothing, and saying otherwise would
			// let a claim be read as a measurement.
		})
	}
	return snapshot
}

// Start begins publishing on the sweep interval.
func (p *ReportedPublisher) Start(ctx context.Context) {
	if !p.Enabled() {
		return
	}
	p.loop.run(ctx, p.cfg.SweepInterval, func(ctx context.Context, capturedAt time.Time) {
		snapshot := p.SnapshotAt(capturedAt)
		snapshot.Sequence = p.sequence.Add(1)
		if err := p.store.Publish(ctx, snapshot); err != nil {
			warnRateLimited(p.logger, &p.lastPublishWarnUnixNano,
				"failed to publish reported session snapshot", "error", err)
		}
	})
}

// Stop ends publishing, waits for the loop to exit, and leaves the roster. Why
// leaving matters as much as stopping is documented on publishLoop.halt.
func (p *ReportedPublisher) Stop(ctx context.Context) error {
	if p == nil {
		return nil
	}
	return p.loop.halt(ctx, p.store)
}
