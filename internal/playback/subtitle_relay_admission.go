package playback

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Relay demux admission: bounds concurrent heavy subtitle reads against one
// remote relay source.
//
// A virtual playback session reads the same slow relay pipe with several
// independent consumers: every window extract, the detached full-track warm,
// the subtitle font dump, and layout ffprobes. The video transcode reads it
// too, but that one is never admitted here (playback must not wait on
// subtitle work). Letting all the subtitle reads pile onto the pipe does not
// make any of them faster — the pipe is the bottleneck — it makes each one
// lose to its own timeout instead: a window overruns the client's fetch
// deadline, and the warm's 5 s layout probe times out while the window it
// exists to validate is still reading. The guard admits a small number of
// subtitle demuxes per relay source so those consumers pace each other
// instead of starving each other.
//
// Admission has three classes:
//
//   - Request-path reads (a window serve) are what a client is waiting on.
//     They wait a short, bounded time for a slot and, when none frees up,
//     report ErrRelayDemuxBusy so the caller answers the retryable shape it
//     already supports (503 + Retry-After). Making a client sit out a 20-70 s
//     demux inside its own fetch deadline is what turns a slow relay into a
//     failed fetch.
//   - Detached reads (full-track warms, layout probes, and the param-less
//     whole-track upgrade probe) are opportunistic. They never queue: when
//     their budget is taken they are dropped and retried by the next trigger
//     (the next windowed miss, warm admission, client probe cadence, or fetch).
//   - Font reads (bundle extraction and single-attachment serves) have their
//     own per-source budget. A request-triggered font serve waits the same
//     short bound as a request-path read; a detached font warm skips a taken
//     font budget. Fonts never draw from the request-path pool: a full-pack
//     attachment dump can hold the pipe for minutes, and letting it occupy a
//     foreground slot starves the very window extract it exists to support.
//
// The three classes draw from separate per-source budgets, so a foreground read
// is admitted whenever fewer than relayDemuxSlotsPerSource other foreground
// reads hold the source, regardless of what background or font work is doing.
//
// A fourth class covers the one read that is neither opportunistic nor a
// client's own window: the recovery extraction a refused window or a refused
// bitmap page is explicitly waiting on (relayDemuxRecovery, marked by
// StreamExtractOpts.WaitingRelayDemux). That work has its own reserved slot per
// source and waits for it, because the alternative is not "shed the read" but
// "the retry pays the same 26 s relay read again". A background fill for
// another track of the same session routinely holds the single detached slot
// for minutes (production: a 4K PGS fill ran 95 s while the text window's
// covering prefetch was shed and never retried), and the request-path pool is
// held by the reads the client has already given up on. The reserved slot is
// one per source and its wait is bounded, so the pipe gains at most one read it
// is about to be asked for anyway, and that read replaces a full window extract.
//
// Local-file reads are not admitted at all: they do not share the relay pipe
// and are the cheap paths the guard exists to protect.

// ErrRelayDemuxBusy reports that the relay source backing an input is already
// at its concurrent subtitle-demux ceiling. Callers translate it into their
// own retryable shape; it is not a source failure and must not arm a failure
// cooldown.
var ErrRelayDemuxBusy = errors.New("relay demux admission: source saturated")

const (
	// relayDemuxSlotsPerSource caps concurrent request-path subtitle demuxes
	// per remote source. Two slots let one client-facing read and one second
	// client-facing read move at once: a single slot would serialize a
	// household's two simultaneous subtitle requests. The video transcode is
	// deliberately outside this budget.
	relayDemuxSlotsPerSource = 2

	// relayDemuxDetachedSlotsPerSource caps concurrent detached demuxes per
	// remote source. It is a separate, smaller budget so background work
	// (full-track warms, layout probes, the param-less upgrade probe) can never
	// occupy a request-path slot. One slot keeps a session's background reads
	// from stacking on the pipe; a detached read that finds it taken is skipped
	// and re-kicked by its next trigger.
	relayDemuxDetachedSlotsPerSource = 1

	// relayDemuxFontSlotsPerSource caps concurrent font-attachment reads per
	// remote source. Font dumps are optional to playback and can hold the pipe
	// for minutes, so they get their own budget instead of borrowing either the
	// request-path slots a window needs or the detached slot a full-track warm
	// needs. One slot means a second font fetch answers its existing pending
	// shape until the dump settles.
	relayDemuxFontSlotsPerSource = 1

	// relayDemuxRecoverySlotsPerSource caps concurrent recovery reads per remote
	// source. A recovery read is the extraction a refused window or bitmap page
	// is explicitly waiting on (see relayDemuxRecovery), so it gets one reserved
	// slot that no client window, background fill or font dump can hold. One is
	// enough: a session waits on at most one recovering track at a time, and a
	// second recovery would only re-read the same relay pipe in parallel.
	relayDemuxRecoverySlotsPerSource = 1

	// relayDemuxRecoveryWait bounds how long a recovery read waits for its
	// reserved slot. It is far longer than the request-path wait because nobody's
	// fetch deadline depends on this read finishing promptly: the client is
	// already in its Retry-After backoff, and serving the retry from the artifact
	// is what this read exists for. It stays bounded so a wedged holder cannot
	// keep the recovery from answering (and being logged as shed) forever.
	relayDemuxRecoveryWait = 20 * time.Second

	// relayDemuxRequestWait bounds how long a request-path read waits for a
	// slot before answering retryable. A read that holds a slot longer than
	// this is a multi-second remote demux; the client is better served by its
	// existing retry loop than by holding the connection inside its own fetch
	// deadline.
	relayDemuxRequestWait = 750 * time.Millisecond

	// relayDemuxShedMemory bounds how long a shed timestamp is retained for the
	// source-map cleanup. The warm quiet window is shorter; this only keeps the
	// map from accumulating hosts that stopped shedding.
	relayDemuxShedMemory = 2 * time.Minute
)

// relayDemuxClass selects how an admission request treats a saturated source.
type relayDemuxClass int

const (
	// relayDemuxRequestPath waits the short bound, then reports busy.
	relayDemuxRequestPath relayDemuxClass = iota
	// relayDemuxDetached never waits: it skips a saturated source.
	relayDemuxDetached
	// relayDemuxFont is a request-triggered font read: it waits the short bound
	// on the font budget, then reports busy.
	relayDemuxFont
	// relayDemuxFontDetached is an opportunistic font read: it never waits and
	// skips a taken font budget.
	relayDemuxFontDetached
	// relayDemuxRecovery is the extraction a refused window or bitmap page is
	// waiting on. It draws from its own reserved budget and waits for it, so a
	// background fill or a client window cannot starve the read that makes the
	// retry cheap.
	relayDemuxRecovery
)

// relayDemuxClassKey carries the admission class through a context whose
// origin (a request or a detached goroutine) is not otherwise visible to the
// shared font and probe entry points.
type relayDemuxClassKey struct{}

// relayDemuxSlotKey marks a context that already holds a demux slot.
type relayDemuxSlotKey struct{}

// relayDemuxSourceSlots is the per-source admission budget: request-path,
// detached, font and recovery reads draw from separate pools, so background or
// font work cannot hold a foreground slot and neither can starve the recovery a
// refused request is waiting on.
type relayDemuxSourceSlots struct {
	requestPath chan struct{}
	detached    chan struct{}
	font        chan struct{}
	recovery    chan struct{}
}

// relayDemuxGuard bounds concurrent subtitle demuxes per remote source. It is
// safe for concurrent use.
type relayDemuxGuard struct {
	limit         int
	detachedLimit int
	fontLimit     int
	recoveryLimit int
	wait          time.Duration
	recoveryWait  time.Duration

	mu    sync.Mutex
	slots map[string]*relayDemuxSourceSlots
	// lastRequestShed records the last time a client-facing read could not
	// take a request-path slot per relay source. Detached warms consult it so
	// they do not add a full-track demux to a pipe that is already shedding the
	// windows viewers are waiting on.
	lastRequestShed map[string]time.Time
}

func newRelayDemuxGuard(limit int, wait time.Duration) *relayDemuxGuard {
	return newRelayDemuxGuardWithDetached(limit, relayDemuxDetachedSlotsPerSource, wait)
}

func newRelayDemuxGuardWithDetached(limit, detachedLimit int, wait time.Duration) *relayDemuxGuard {
	return newRelayDemuxGuardWithPools(limit, detachedLimit, relayDemuxFontSlotsPerSource, wait)
}

func newRelayDemuxGuardWithPools(limit, detachedLimit, fontLimit int, wait time.Duration) *relayDemuxGuard {
	if limit <= 0 {
		limit = 1
	}
	if detachedLimit <= 0 {
		detachedLimit = 1
	}
	if fontLimit <= 0 {
		fontLimit = 1
	}
	return &relayDemuxGuard{
		limit:           limit,
		detachedLimit:   detachedLimit,
		fontLimit:       fontLimit,
		recoveryLimit:   relayDemuxRecoverySlotsPerSource,
		wait:            wait,
		recoveryWait:    relayDemuxRecoveryWait,
		slots:           make(map[string]*relayDemuxSourceSlots),
		lastRequestShed: make(map[string]time.Time),
	}
}

// relayDemuxGuardState holds the process-wide guard. A server relays through
// its own listener, so production holds one entry in the slot map; test and
// multi-upstream fixtures add one entry per host and never grow unbounded. It
// is an atomic pointer so a test can install a narrower guard without racing
// an in-flight admission.
var relayDemuxGuardState atomic.Pointer[relayDemuxGuard]

// activeRelayDemuxGuard returns the process-wide guard, installing the
// production one on first use.
func activeRelayDemuxGuard() *relayDemuxGuard {
	if guard := relayDemuxGuardState.Load(); guard != nil {
		return guard
	}
	guard := newRelayDemuxGuard(relayDemuxSlotsPerSource, relayDemuxRequestWait)
	if relayDemuxGuardState.CompareAndSwap(nil, guard) {
		return guard
	}
	return relayDemuxGuardState.Load()
}

// pools returns the slot pools for a relay source, creating them on first use.
func (g *relayDemuxGuard) pools(source string) *relayDemuxSourceSlots {
	g.mu.Lock()
	defer g.mu.Unlock()
	pools, ok := g.slots[source]
	if !ok {
		recoveryLimit := g.recoveryLimit
		if recoveryLimit <= 0 {
			recoveryLimit = relayDemuxRecoverySlotsPerSource
		}
		pools = &relayDemuxSourceSlots{
			requestPath: make(chan struct{}, g.limit),
			detached:    make(chan struct{}, g.detachedLimit),
			font:        make(chan struct{}, g.fontLimit),
			recovery:    make(chan struct{}, recoveryLimit),
		}
		g.slots[source] = pools
	}
	return pools
}

// acquire takes one slot for input and returns its release. ok is false when
// the read must be skipped or answered retryable. A local input is always
// admitted with a no-op release.
func (g *relayDemuxGuard) acquire(ctx context.Context, input string, class relayDemuxClass) (func(), bool) {
	source, remote := relayDemuxSource(input)
	if !remote {
		return func() {}, true
	}
	pools := g.pools(source)
	label := relayDemuxClassLabel(class)
	started := time.Now()
	var release func()
	var ok bool
	switch class {
	case relayDemuxDetached:
		// Detached work never waits and never touches another budget: if its
		// own budget is taken it is skipped and re-kicked later.
		select {
		case pools.detached <- struct{}{}:
			release, ok = func() { <-pools.detached }, true
		default:
		}
	case relayDemuxFontDetached:
		select {
		case pools.font <- struct{}{}:
			release, ok = func() { <-pools.font }, true
		default:
		}
	case relayDemuxFont:
		release, ok = g.acquireWaiting(ctx, pools.font)
		observeSubtitleRelayAdmissionWait(label, time.Since(started))
	case relayDemuxRecovery:
		// The read a refused request is waiting on gets its own reserved budget
		// and a long, bounded wait: shedding it is what makes the retry pay the
		// full relay read again.
		release, ok = g.acquireWaitingFor(ctx, pools.recovery, g.recoveryWaitDuration())
		observeSubtitleRelayAdmissionWait(label, time.Since(started))
	default:
		release, ok = g.acquireWaiting(ctx, pools.requestPath)
		observeSubtitleRelayAdmissionWait(label, time.Since(started))
	}
	recordSubtitleRelayAdmission(label, source, ok)
	if !ok {
		if class == relayDemuxRequestPath {
			g.noteRequestShed(source, time.Now())
		}
		return nil, false
	}
	return release, true
}

// noteRequestShed records that a client-facing read was shed for source. The
// map is tiny (one entry per relay host), but a long-lived server that talks to
// many hosts still prunes expired entries so it cannot grow without bound.
func (g *relayDemuxGuard) noteRequestShed(source string, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.lastRequestShed == nil {
		g.lastRequestShed = make(map[string]time.Time)
	}
	g.lastRequestShed[source] = now
	if len(g.lastRequestShed) > 64 {
		for key, at := range g.lastRequestShed {
			if now.Sub(at) > relayDemuxShedMemory {
				delete(g.lastRequestShed, key)
			}
		}
	}
}

// requestPathSheddingRecently reports whether a client-facing read for input
// was refused a slot within the quiet window. Detached work uses it to stay off
// a pipe that is actively shedding.
func (g *relayDemuxGuard) requestPathSheddingRecently(input string, within time.Duration) bool {
	source, remote := relayDemuxSource(input)
	if !remote {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	at, ok := g.lastRequestShed[source]
	return ok && time.Since(at) < within
}

// requestPathSaturated reports whether every request-path slot for input is
// held right now, so the next client read would have to wait for one. Detached
// work consults it before it starts: a pipe whose request path is at capacity
// has nothing to spare for opportunistic reads.
//
// The shed timestamp alone cannot answer this. A shed is recorded only after a
// client read has already waited out its whole admission window, so a prefetch
// kick launched against a saturated-but-not-yet-shedding source used to start,
// take its detached slot, and add the demux the client read was waiting behind
// — the production 503 (753ms, the full request-path wait) followed by five
// shed-skipped kicks.
func (g *relayDemuxGuard) requestPathSaturated(input string) bool {
	source, remote := relayDemuxSource(input)
	if !remote {
		return false
	}
	pools := g.pools(source)
	return len(pools.requestPath) >= cap(pools.requestPath)
}

// relayDemuxRequestPathShedding reports whether the process-wide guard shed a
// client-facing read for input inside the quiet window.
func relayDemuxRequestPathShedding(input string, within time.Duration) bool {
	return activeRelayDemuxGuard().requestPathSheddingRecently(input, within)
}

// relayDemuxRequestPathSaturated reports whether the process-wide guard's
// request-path budget for input is fully held right now.
func relayDemuxRequestPathSaturated(input string) bool {
	return activeRelayDemuxGuard().requestPathSaturated(input)
}

// relayDemuxDetachedBlocked reports whether an opportunistic detached read of
// input must stay off the source: a client-facing read is waiting (or about to
// wait) for a request-path slot, or one was shed inside the quiet window.
func relayDemuxDetachedBlocked(input string, within time.Duration) bool {
	return relayDemuxRequestPathSaturated(input) || relayDemuxRequestPathShedding(input, within)
}

// relayDemuxClassLabel is the bounded metric label for an admission class.
// The two font classes share one pool and one label.
func relayDemuxClassLabel(class relayDemuxClass) string {
	switch class {
	case relayDemuxDetached:
		return relayDemuxClassDetachedLabel
	case relayDemuxFont, relayDemuxFontDetached:
		return relayDemuxClassFontLabel
	case relayDemuxRecovery:
		return relayDemuxClassRecoveryLabel
	default:
		return relayDemuxClassRequestLabel
	}
}

const (
	relayDemuxClassRequestLabel  = "request"
	relayDemuxClassDetachedLabel = "detached"
	relayDemuxClassFontLabel     = "font"
	relayDemuxClassRecoveryLabel = "recovery"
)

// acquireWaiting takes one slot from pool, waiting up to the guard's short
// bound. It reports false when the pool stays full: the caller answers its
// existing retryable or pending shape.
func (g *relayDemuxGuard) acquireWaiting(ctx context.Context, pool chan struct{}) (func(), bool) {
	return g.acquireWaitingFor(ctx, pool, g.wait)
}

// acquireWaitingFor takes one slot from pool, waiting up to wait. A zero or
// negative wait keeps the immediate-try-then-shed behavior.
func (g *relayDemuxGuard) acquireWaitingFor(ctx context.Context, pool chan struct{}, wait time.Duration) (func(), bool) {
	select {
	case pool <- struct{}{}:
		return func() { <-pool }, true
	default:
	}
	if wait <= 0 {
		return nil, false
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case pool <- struct{}{}:
		return func() { <-pool }, true
	case <-timer.C:
		return nil, false
	case <-ctx.Done():
		return nil, false
	}
}

// recoveryWaitDuration is the bounded admission wait for a recovery read.
func (g *relayDemuxGuard) recoveryWaitDuration() time.Duration {
	if g != nil && g.recoveryWait > 0 {
		return g.recoveryWait
	}
	if g != nil && g.wait > 0 {
		return g.wait
	}
	return relayDemuxRecoveryWait
}

// relayDemuxSource reduces a remote input URL to the relay source the bound
// applies to: scheme and host. A local filesystem path (or a non-HTTP input,
// such as a cached extracted artifact) has no source and is never admitted.
// The relay URL path carries a per-registration token, so it is not part of
// the key: every read a session performs through the relay shares one pipe.
func relayDemuxSource(input string) (string, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", false
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	if parsed.Host == "" {
		return "", false
	}
	return scheme + "://" + strings.ToLower(parsed.Host), true
}

// detachedRelayDemux marks ctx as detached admission: opportunistic work that
// must skip a taken detached budget instead of queueing behind request-path
// reads.
func detachedRelayDemux(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, relayDemuxClassKey{}, relayDemuxDetached)
}

// recoveryRelayDemux marks ctx as the read a refused request is waiting on: it
// draws from the reserved recovery budget and waits for it instead of being
// shed, so a background fill cannot starve the extraction that makes the
// client's retry cheap.
func recoveryRelayDemux(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, relayDemuxClassKey{}, relayDemuxRecovery)
}

// fontRelayDemuxContext marks ctx as a font read. A font dump never draws from
// the request-path or detached pools: request-triggered fonts wait on the font
// budget, and font warms (which already carry the detached marker) skip a taken
// font budget. The slot marker is preserved, so a nested read inside an admitted
// font extraction still takes no second slot.
func fontRelayDemuxContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	class := relayDemuxFont
	if relayDemuxClassOf(ctx) == relayDemuxDetached {
		class = relayDemuxFontDetached
	}
	return context.WithValue(ctx, relayDemuxClassKey{}, class)
}

// relayDemuxAdmissionContext applies an extract's admission class to the
// context used for its relay read. A whole-track upgrade probe against a
// committed-or-warming track only fills the committed artifact, so it draws
// from the detached budget and can never take a slot a window needs. A cold
// param-less whole-track fetch is client-facing and keeps the request-path
// budget.
func relayDemuxAdmissionContext(ctx context.Context, opts StreamExtractOpts) context.Context {
	if opts.WaitingRelayDemux {
		// The read a refused window or page is waiting on outranks every
		// opportunistic class: it must not be shed because another fill or a
		// client window holds the pipe.
		return recoveryRelayDemux(ctx)
	}
	if opts.DetachedRelayDemux {
		return detachedRelayDemux(ctx)
	}
	return ctx
}

// warmRelayDemuxContext is relayDemuxAdmissionContext for a fill that runs on a
// detached goroutine: opportunistic fills draw from the detached budget, and the
// one read a refused request is waiting on draws from the reserved recovery
// budget instead of being shed.
func warmRelayDemuxContext(ctx context.Context, opts StreamExtractOpts) context.Context {
	if opts.WaitingRelayDemux {
		return recoveryRelayDemux(ctx)
	}
	return detachedRelayDemux(ctx)
}

// relayDemuxClassOf reports how ctx was admitted.
func relayDemuxClassOf(ctx context.Context) relayDemuxClass {
	if ctx == nil {
		return relayDemuxRequestPath
	}
	if class, ok := ctx.Value(relayDemuxClassKey{}).(relayDemuxClass); ok {
		return class
	}
	return relayDemuxRequestPath
}

// acquireRelayDemuxRead admits one heavy read of input and returns a context
// that carries the slot plus its release. The returned context must be the one
// handed to the extraction, so nested work (a layout probe inside an admitted
// warm) is recognized and does not take a second slot: a context that already
// holds one is admitted with a no-op release, which also makes a nested read
// unable to deadlock against its own parent.
func acquireRelayDemuxRead(ctx context.Context, input string) (context.Context, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Value(relayDemuxSlotKey{}) != nil {
		return ctx, func() {}, nil
	}
	if _, remote := relayDemuxSource(input); !remote {
		return ctx, func() {}, nil
	}
	release, ok := activeRelayDemuxGuard().acquire(ctx, input, relayDemuxClassOf(ctx))
	if !ok {
		return ctx, func() {}, ErrRelayDemuxBusy
	}
	return context.WithValue(ctx, relayDemuxSlotKey{}, struct{}{}), release, nil
}
