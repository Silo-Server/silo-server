package transcodenode

import (
	"context"
	"time"
)

// Health status values. A draining node keeps answering 200: it is alive and
// still serving the sessions it has, and a node pool that read anything but
// 200 as dead would stop routing those sessions' manifests and segments.
const (
	HealthStatusOK = "ok"
	// HealthStatusDraining is a node that takes no new work and is finishing
	// what it runs.
	HealthStatusDraining = "draining"
	// HealthStatusDrained is a draining node with nothing left to finish.
	HealthStatusDrained = "drained"
)

// drainPollInterval is how often WaitDrained and announceWhenDrained look at
// the node's work.
const drainPollInterval = 250 * time.Millisecond

// draining reports whether the node takes no new work: it received SIGTERM, or
// an administrator asked to drain it. A draining node still serves the
// sessions it runs, including restarting their FFmpeg on a seek.
func (s *Server) draining() bool {
	return s.shutdownDrain.Load() || s.watcher.NodeDrainRequested()
}

// drained reports whether a draining node has finished all its work: no
// request counted by trackInFlight, and no session, progressive remux or job
// counted in activeJobs.
//
// inFlight is read first. Work moves from it to activeJobs (a handler counts
// its job before its deferred in-flight release), so a zero in-flight read
// already sees any job counted before it; read the other way round, a start
// moving between the two counters could be missed by both reads.
func (s *Server) drained() bool {
	return s.draining() && s.inFlight.Load() == 0 && s.activeJobs.Load() == 0
}

// healthStatus is the status health reports. Reporting drained latches it:
// from then on the node also starts no rebuild of a lost session and no fresh
// progressive remux, since whoever read the answer may stop the node. Before
// that both are ordinary work the drain waits for, so a viewer coming back
// from a long pause or seeking is not refused by a node that is still serving.
//
// The work check and the latch happen under drainFence, which reportedDrained
// also takes, so a rebuild or remux that counted itself in flight first is
// either seen by the check or refused after the latch.
func (s *Server) healthStatus() string {
	s.drainFence.Lock()
	defer s.drainFence.Unlock()
	switch {
	case s.drained():
		s.drainedReported.Store(true)
		return HealthStatusDrained
	case s.draining():
		return HealthStatusDraining
	default:
		s.drainedReported.Store(false)
		return HealthStatusOK
	}
}

// reportedDrained reports whether a draining node has told someone it is
// drained, so it must start no rebuild and no fresh progressive remux. Callers
// count themselves with trackInFlight before asking; see healthStatus.
func (s *Server) reportedDrained() bool {
	s.drainFence.Lock()
	defer s.drainFence.Unlock()
	return s.draining() && s.drainedReported.Load()
}

// SetDrainAnnouncer sets fn to run when the node starts or stops draining, and
// once more when a drain finishes, so the API servers can react before their
// next health sweep. fn must not block. A drain already in force (a request the
// first config load read) is announced at once.
func (s *Server) SetDrainAnnouncer(fn func()) {
	s.drainAnnouncer.Store(&fn)
	if s.watcher != nil {
		s.watcher.OnNodeDrainChange(func(bool) { s.announceDrain() })
	}
	if s.draining() {
		s.announceDrain()
	}
}

func (s *Server) announce() {
	if fn := s.drainAnnouncer.Load(); fn != nil && *fn != nil {
		(*fn)()
	}
}

func (s *Server) announceDrain() {
	s.announce()
	if !s.draining() {
		s.drainedReported.Store(false)
		return
	}
	if s.drainWatch.CompareAndSwap(false, true) {
		go s.announceWhenDrained()
	}
}

// announceWhenDrained announces once more when a draining node finishes its
// last work, so the admin page can say it is safe to stop without waiting for
// a health sweep. It ends early if the drain is lifted.
func (s *Server) announceWhenDrained() {
	ticker := time.NewTicker(drainPollInterval)
	defer ticker.Stop()
	for range ticker.C {
		if s.drained() {
			s.announce()
			s.drainWatch.Store(false)
			return
		}
		if !s.draining() {
			s.drainWatch.Store(false)
			// A drain that began again since the check found this watch still
			// running and started none; carry on for it.
			if s.draining() && s.drainWatch.CompareAndSwap(false, true) {
				continue
			}
			return
		}
	}
}

// BeginShutdownDrain makes the node take no new work, for the rest of the
// process's life, while the sessions it runs finish. It is how SIGTERM starts.
func (s *Server) BeginShutdownDrain() {
	if s.shutdownDrain.CompareAndSwap(false, true) {
		s.announceDrain()
	}
}

// WaitDrained blocks until the node has drained or ctx ends.
func (s *Server) WaitDrained(ctx context.Context) error {
	ticker := time.NewTicker(drainPollInterval)
	defer ticker.Stop()
	for !s.drained() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return nil
}

// ActiveJobs is the node's active job count, the number its health reports.
func (s *Server) ActiveJobs() int {
	return int(s.activeJobs.Load())
}

// trackInFlight counts one request as in flight until the returned func runs,
// so a drain waits for work activeJobs does not count: a prepared-download
// transfer, a frame extraction, a sample run, or a start, rebuild,
// preparation, trickplay run or progressive remux that has not been counted
// yet. Use as defer s.trackInFlight()(), before any drain check.
func (s *Server) trackInFlight() func() {
	s.inFlight.Add(1)
	return func() { s.inFlight.Add(-1) }
}

// errNodeDraining is the plain-text body of a refused start or preparation.
// The playback retry loops try the next node on any refused start.
const errNodeDraining = "node is draining"

// hostsSession reports whether the node runs an HLS session with this id.
func (s *Server) hostsSession(sessionID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.sessions[sessionID]
	return ok
}
