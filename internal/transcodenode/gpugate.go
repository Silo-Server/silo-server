package transcodenode

import "sync"

// gpuGate keeps an operator-triggered capability re-probe and the node's own
// GPU work off the encoder at the same time.
//
// Every hardware probe ends in a real smoke encode, which opens an encoder
// session. A card at its concurrent-session cap fails that encode with an error
// nothing can tell apart from a missing device or a broken driver, so a probe
// that races a transcode publishes a hardware regression for a GPU that is
// fine — and the API persists it, latches it, and routes the node to software
// until a clean report arrives.
//
// A point-in-time "are there active jobs" check cannot prevent that: a node
// idle at the check accepts a start milliseconds later, while the probe still
// has minutes to run. What is needed is one exclusion both sides consult, held
// from before the probe begins until after it ends.
//
// The gate is deliberately asymmetric. Work never waits: it is admitted or
// refused immediately, because a viewer pressing play must not queue behind a
// multi-minute probe. The re-probe never waits either: it refuses with 409 and
// tells the operator to retry when the node is idle, because blocking would
// hold an admin HTTP connection open for the length of a stream.
type gpuGate struct {
	mu sync.Mutex
	// workers counts GPU work that has been admitted and has not finished. It
	// is separate from Server.activeJobs because that counter is incremented
	// only once ffmpeg is already running: the window this gate exists to close
	// is precisely the one between admitting work and it becoming visible
	// there.
	workers int
	// reprobing is set for the whole capability rebuild, including the cache
	// invalidation that precedes it.
	reprobing bool
}

// beginWork admits one unit of GPU work, or reports false while a re-probe
// holds the encoder. A caller that is admitted must call endWork exactly once.
func (g *gpuGate) beginWork() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.reprobing {
		return false
	}
	g.workers++
	return true
}

// holdWork registers GPU work that is already running, and so cannot be
// refused.
//
// The gate admits work at its start and the node counts it in activeJobs once
// ffmpeg is up, which between them cover a session from admission to teardown —
// except for teardown itself. TranscodeSession.Close waits for ffmpeg to exit,
// so the encoder holds its GPU session for the whole call, while every teardown
// path drops activeJobs first so a stop is reflected immediately. That leaves a
// live encoder counted by neither, and a re-probe landing in the gap sees an
// idle node and smoke-encodes beside it — publishing exactly the false hardware
// failure this gate exists to prevent. Refusing here is not an option: a stop
// must always proceed. Counting it is.
func (g *gpuGate) holdWork() {
	g.mu.Lock()
	g.workers++
	g.mu.Unlock()
}

// endWork releases one unit of admitted GPU work.
func (g *gpuGate) endWork() {
	g.mu.Lock()
	if g.workers > 0 {
		g.workers--
	}
	g.mu.Unlock()
}

// beginReprobe claims the encoder exclusively, or reports the work in progress
// that stopped it.
//
// otherWork counts everything the gate does not track itself: the node's own
// running sessions, and the probes still running for callers that have gone
// away. It is a function rather than a value because it is read here, under the
// lock that grants the claim. Sampling it at the call site leaves a window — the
// request can be descheduled between reading zero and acquiring the lock, and a
// capability build that starts and is abandoned in that window leaves its
// background probe running while the count the gate sees still says idle.
//
// The detached probes are the piece that is not this node's own bookkeeping. A
// hardware probe runs on a background context so an abandoned caller cannot
// kill work another request is waiting on, which means the capability build
// releases its gate claim while ffmpeg may still be encoding. Without counting
// those, a re-probe claims an encoder that is not free and its smoke matrix
// races the one already running — publishing the false hardware verdict this
// gate exists to prevent, by the same mechanism, one layer down.
func (g *gpuGate) beginReprobe(otherWork func() int) (busy int, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	busy = g.workers
	if otherWork != nil {
		busy += otherWork()
	}
	if g.reprobing || busy > 0 {
		return busy, false
	}
	g.reprobing = true
	return 0, true
}

// endReprobe releases the exclusive claim.
func (g *gpuGate) endReprobe() {
	g.mu.Lock()
	g.reprobing = false
	g.mu.Unlock()
}
