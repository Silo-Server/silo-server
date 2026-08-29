package nodepool

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/config"
)

// Plan is the result of a node selection for one playback session.
// Either field may be nil when no suitable node exists.
type Plan struct {
	TranscodeNode *Node
	ProxyNode     *Node
}

// SessionPlanner selects transcode and proxy nodes for playback sessions.
// Implemented by *Planner; defined as an interface so handlers can be tested
// without a real pool.
type SessionPlanner interface {
	PlanSession(sessionID, currentTranscodeURL string, needsTranscode bool, estBitrateKbps int) Plan
}

// DownloadPlanner selects proxy nodes for unbounded file delivery. Downloads
// have no predictable bitrate, so implementations must not admit them onto a
// proxy with a configured bandwidth cap.
type DownloadPlanner interface {
	PlanDownload(sessionID string, preferredGroup ...string) Plan
	ReleaseSession(sessionID string)
}

// TranscodeWorkPlanner reserves capacity for non-streaming GPU work such as a
// prepared download. The returned release function must be called after the
// remote operation ends or falls back locally.
type TranscodeWorkPlanner interface {
	ReserveTranscodeWork(workID string) (*Node, func())
	TranscodeNode(nodeID int) (*Node, bool)
}

// reservation bridges the gap between assigning a session to a node and the
// node's health reports reflecting that session.
//
// The job count stops counting toward a node's effective load as soon as the
// node delivers a health report newer than the reservation (the node's own
// count then includes the session), or after maxReservationAge as a safety
// net. The bandwidth estimate instead counts for a fixed bandwidthBridgeAge
// regardless of health freshness: a proxy's measured egress is a rolling
// average that only converges on the new stream's rate gradually, so an
// early health report would otherwise drop the estimate before the meter
// reflects it.
type reservation struct {
	transcodeURL string
	proxyURL     string
	kbps         int // estimated stream bitrate, counted against the proxy
	createdAt    time.Time
}

const (
	maxReservationAge  = 90 * time.Second
	bandwidthBridgeAge = 60 * time.Second // matches the proxy egress meter window
)

// Planner makes group- and capacity-aware node selections on top of the
// existing pools.
//
// Grouping: nodes sharing a group label are co-located (same host/LAN). A
// group is eligible only while every enabled member is healthy. A transcode
// node from group G is always paired with a proxy from G so transcoded bytes
// never cross the LAN twice (round-robin when G has several proxies).
// Ungrouped nodes keep the historical behavior: least-connections transcode
// selection and global round-robin proxy selection.
//
// Capacity: a node with MaxJobs set is skipped once its effective load
// (health-reported active jobs plus unexpired reservations) reaches the cap.
// A proxy with MaxBandwidthKbps set is skipped once its measured egress plus
// the estimated bitrate of recently admitted streams would exceed the cap.
type Planner struct {
	proxies    *ProxyPool
	transcodes *TranscodePool

	mu       sync.Mutex
	rr       map[string]int          // per-group round-robin cursor; "" = global
	reserved map[string]*reservation // keyed by playback session ID
	now      func() time.Time        // overridable for tests
	// scratchPressed latches, by node id, which nodes were under scratch
	// pressure the last time a transcode selection looked. It exists only to
	// make the operator warning fire once per transition into pressure: the
	// eligibility path runs on every session start and every quality switch, so
	// logging there unlatched would produce a line per playback event for as
	// long as a disk stays full. Guarded by mu, which every selection path
	// already holds. Entries are pruned as nodes leave the pool.
	scratchPressed map[int]bool
	// scratchGuardDropped latches whether the last transcode selection had to
	// ignore the scratch guard because every eligible candidate was pressured.
	// Latched for the same reason as scratchPressed, and separately from it
	// because a cluster reaches "no headroom anywhere" on its own schedule.
	// Guarded by mu.
	scratchGuardDropped bool
}

// NewPlanner creates a planner over the given pools.
func NewPlanner(proxies *ProxyPool, transcodes *TranscodePool) *Planner {
	return &Planner{
		proxies:        proxies,
		transcodes:     transcodes,
		rr:             make(map[string]int),
		reserved:       make(map[string]*reservation),
		now:            time.Now,
		scratchPressed: make(map[int]bool),
	}
}

// PlanSession picks the nodes serving one playback session.
//
// When needsTranscode is true it selects a transcode node (soft affinity to
// currentTranscodeURL, matching the historical quality-switch behavior) and a
// proxy from the same group. When false (direct play / proxy-side remux) it
// selects only a proxy. estBitrateKbps is the expected stream bitrate (target
// bitrate for transcodes, source bitrate otherwise; 0 = unknown), used for
// bandwidth-cap admission. Re-planning the same session replaces its previous
// reservation, so quality switches don't double-count.
func (p *Planner) PlanSession(sessionID, currentTranscodeURL string, needsTranscode bool, estBitrateKbps int) Plan {
	return p.PlanSessionWith(sessionID, currentTranscodeURL, needsTranscode, estBitrateKbps, nil)
}

// TranscodeNode returns the current pool record for a persistent node id. It
// lets durable artifact locators follow an administrator-edited node URL/group.
func (p *Planner) TranscodeNode(nodeID int) (*Node, bool) {
	if p == nil || p.transcodes == nil || nodeID == 0 {
		return nil, false
	}
	for _, node := range p.transcodes.Nodes() {
		if node != nil && node.ID == nodeID && node.Enabled {
			return node, true
		}
	}
	return nil, false
}

// TranscodeNodeByURL returns the pooled record for a transcode node URL. It
// gives a dispatch path that node's own acceleration override, from the URL it
// is about to send a job to. Enabled or not: a caller holding the URL has
// already selected it.
func (p *Planner) TranscodeNodeByURL(nodeURL string) (*Node, bool) {
	if p == nil || p.transcodes == nil || nodeURL == "" {
		return nil, false
	}
	node := p.transcodes.FindByURL(normalizeNodeURL(nodeURL))
	if node == nil {
		return nil, false
	}
	return node, true
}

// ProxyNodeByURL returns the pooled record for a proxy node URL, under the
// same contract as TranscodeNodeByURL. Capability-budget pricing needs it:
// a proxy's stored report and overrides say how long its own cold probe may
// take, and a caller that can only resolve transcode nodes prices every proxy
// from the cluster policy instead.
func (p *Planner) ProxyNodeByURL(nodeURL string) (*Node, bool) {
	if p == nil || p.proxies == nil || nodeURL == "" {
		return nil, false
	}
	node := p.proxies.FindByURL(normalizeNodeURL(nodeURL))
	if node == nil {
		return nil, false
	}
	return node, true
}

// TranscodeNodeHealthy reports whether the pooled transcode node serving a URL
// is currently healthy and enabled. Remote-start adoption gates its redirect
// on this: a recipe another API server published is only trustworthy while
// its node still serves.
func (p *Planner) TranscodeNodeHealthy(nodeURL string) bool {
	if p == nil || p.transcodes == nil || nodeURL == "" {
		return false
	}
	node := p.transcodes.FindByURL(normalizeNodeURL(nodeURL))
	return node != nil && node.Healthy && node.Enabled
}

// PlanDownload picks a healthy proxy for an unbounded file transfer. A
// configured proxy bandwidth cap cannot be reserved accurately without a known
// transfer rate, so capped proxies are excluded instead of being oversubscribed
// during the egress meter's convergence window.
func (p *Planner) PlanDownload(sessionID string, preferredGroup ...string) Plan {
	if p == nil || p.proxies == nil || sessionID == "" {
		return Plan{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	p.pruneReservations(now)
	delete(p.reserved, sessionID)

	group := ""
	if len(preferredGroup) > 0 {
		group = preferredGroup[0]
	}
	var candidates, fallback []*Node
	for _, node := range p.proxies.Nodes() {
		if node == nil || !node.Enabled || !node.Healthy || !p.underCap(node, now) {
			continue
		}
		if node.MaxBandwidthKbps != nil && *node.MaxBandwidthKbps > 0 {
			continue
		}
		fallback = append(fallback, node)
		if group != "" && node.Group != nil && *node.Group == group {
			candidates = append(candidates, node)
		}
	}
	if group == "" || len(candidates) == 0 {
		candidates = fallback
	}
	if len(candidates) == 0 {
		return Plan{}
	}
	rrKey := "download:" + group
	proxy := candidates[p.rr[rrKey]%len(candidates)]
	p.rr[rrKey]++
	p.reserved[sessionID] = &reservation{proxyURL: proxy.URL, createdAt: now}
	return Plan{ProxyNode: proxy}
}

// PlanSessionWith behaves like PlanSession but restricts transcode-node
// selection to nodes accepted by eligible (nil accepts every node). Capability
// -aware playback planning uses it so a recipe that only some pooled nodes
// can execute is never load-balanced onto a node that cannot. The predicate
// runs under the planner lock and must be cheap and non-blocking (a set
// lookup, never a network call). Group health is still computed over the
// full pool: eligibility narrows selection, not co-location semantics.
func (p *Planner) PlanSessionWith(sessionID, currentTranscodeURL string, needsTranscode bool, estBitrateKbps int, eligible func(*Node) bool) Plan {
	if p == nil {
		return Plan{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	p.pruneReservations(now)
	// Drop this session's own reservation before computing loads so a
	// re-plan doesn't count the session against its current node.
	delete(p.reserved, sessionID)

	if estBitrateKbps < 0 {
		estBitrateKbps = 0
	}
	proxies := p.proxies.Nodes()
	pooledTranscodes := p.transcodes.Nodes()
	transcodes := pooledTranscodes
	// Group health is computed over the full pool before any narrowing:
	// eligibility restricts what may be selected, not co-location semantics.
	// Shared-GPU load is summed over the same full pool, for the same reason.
	groupHealthy := groupHealth(proxies, pooledTranscodes)

	var plan Plan
	if needsTranscode {
		if eligible != nil {
			transcodes = filterNodes(transcodes, eligible)
		}
		plan.TranscodeNode = p.pickTranscode(transcodes, pooledTranscodes, proxies, groupHealthy, currentTranscodeURL, estBitrateKbps, now)
		if plan.TranscodeNode != nil {
			plan.ProxyNode = p.pickProxy(proxies, groupHealthy, plan.TranscodeNode.Group, estBitrateKbps, now)
		}
	} else {
		// A proxy-only plan has no transcode node, so the predicate applies to
		// the proxy: it is the node that will execute the recipe. Filtering
		// before selection means a capability mismatch skips to a capable
		// sibling instead of abandoning the pool after one round-robin pick.
		if eligible != nil {
			proxies = filterNodes(proxies, eligible)
		}
		plan.ProxyNode = p.pickProxy(proxies, groupHealthy, nil, estBitrateKbps, now)
	}

	if plan.TranscodeNode != nil || plan.ProxyNode != nil {
		res := &reservation{createdAt: now}
		if plan.TranscodeNode != nil {
			res.transcodeURL = plan.TranscodeNode.URL
		}
		if plan.ProxyNode != nil {
			res.proxyURL = plan.ProxyNode.URL
			res.kbps = estBitrateKbps
		}
		p.reserved[sessionID] = res
	}
	return plan
}

// PlanTranscodeSessionWithLocalEgress selects and reserves only a transcode
// node. The API server remains the client-facing media endpoint and relays the
// selected node's manifest and segments, so no proxy node is needed or charged
// against its job/bandwidth budget. This is intentionally separate from
// PlanSessionWith: its normal grouped-node policy assumes the client talks to a
// selected proxy directly.
func (p *Planner) PlanTranscodeSessionWithLocalEgress(sessionID, currentTranscodeURL string, eligible func(*Node) bool) Plan {
	if p == nil || p.transcodes == nil || sessionID == "" {
		return Plan{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	p.pruneReservations(now)
	delete(p.reserved, sessionID)

	pooledTranscodes := p.transcodes.Nodes()
	transcodes := pooledTranscodes
	groupHealthy := groupHealth(nil, pooledTranscodes)
	if eligible != nil {
		transcodes = filterNodes(transcodes, eligible)
	}
	node := p.pickLocalEgressTranscode(transcodes, pooledTranscodes, groupHealthy, currentTranscodeURL, now)
	if node == nil {
		return Plan{}
	}
	p.reserved[sessionID] = &reservation{transcodeURL: node.URL, createdAt: now}
	return Plan{TranscodeNode: node}
}

// filterNodes returns the nodes accepted by keep, preserving pool order so
// round-robin cursors stay meaningful across selections.
func filterNodes(nodes []*Node, keep func(*Node) bool) []*Node {
	filtered := make([]*Node, 0, len(nodes))
	for _, node := range nodes {
		if keep(node) {
			filtered = append(filtered, node)
		}
	}
	return filtered
}

// ProxyNodeURLs lists the URLs of every enabled pooled proxy node, healthy or
// not, mirroring TranscodeNodeURLs. Capability planning wants the deployment's
// toolchain; an unreachable node excludes itself when its capability fetch
// fails.
func (p *Planner) ProxyNodeURLs() []string {
	if p == nil || p.proxies == nil {
		return nil
	}
	nodes := p.proxies.Nodes()
	urls := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if node != nil && node.URL != "" {
			urls = append(urls, node.URL)
		}
	}
	return urls
}

// TranscodeNodeURLs lists the URLs of every enabled pooled transcode node,
// healthy or not: capability planning wants the deployment's toolchain, and
// an unreachable node excludes itself when its capability fetch fails. An
// empty slice means no nodes are pooled.
func (p *Planner) TranscodeNodeURLs() []string {
	if p == nil || p.transcodes == nil {
		return nil
	}
	nodes := p.transcodes.Nodes()
	urls := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if node != nil && node.URL != "" {
			urls = append(urls, node.URL)
		}
	}
	return urls
}

// ReleaseSession removes a provisional node reservation when playback setup
// fails or falls back locally before a node health report can account for it.
func (p *Planner) ReleaseSession(sessionID string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	delete(p.reserved, sessionID)
	p.mu.Unlock()
}

// ReleaseSessionProxy drops only the proxy half of a session's reservation,
// leaving its transcode node charged. A start that selected both nodes but ends
// up publishing a URL the proxy does not serve (its egress grant could not be
// written, or the attempt fell back to the API-relayed manifest) would otherwise
// keep charging that proxy's job slot and estimated bandwidth for a stream no
// byte will cross it — enough grant-store failures and a healthy proxy looks
// saturated. The transcode node is still running the job, so its half stands.
func (p *Planner) ReleaseSessionProxy(sessionID string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	res, ok := p.reserved[sessionID]
	if !ok {
		return
	}
	res.proxyURL = ""
	res.kbps = 0
	if res.transcodeURL == "" {
		// Nothing left to bridge; drop the entry rather than wait out its age.
		delete(p.reserved, sessionID)
	}
}

// ReserveTranscodeWork selects the least-loaded healthy transcode node while
// sharing the same health-bridging reservation accounting as playback. Unlike
// a playback session it does not require a proxy partner: the completed file
// is written to the configured shared artifact store and served later.
func (p *Planner) ReserveTranscodeWork(workID string) (*Node, func()) {
	return p.ReserveTranscodeWorkWith(workID, nil)
}

// ReserveTranscodeWorkWith is ReserveTranscodeWork with an optional
// capability predicate. The predicate runs under the planner lock and must not
// perform I/O.
func (p *Planner) ReserveTranscodeWorkWith(workID string, eligible func(*Node) bool) (*Node, func()) {
	if p == nil || p.transcodes == nil || workID == "" {
		return nil, func() {}
	}
	p.mu.Lock()
	now := p.now()
	p.pruneReservations(now)
	reservationID := workID + "-" + uuid.NewString()

	var best *Node
	for _, node := range p.transcodes.Nodes() {
		if node == nil || !node.Enabled || !node.Healthy || !p.underCap(node, now) {
			continue
		}
		if eligible != nil && !eligible(node) {
			continue
		}
		if best == nil || p.effectiveJobs(node, now) < p.effectiveJobs(best, now) {
			best = node
		}
	}
	if best != nil {
		p.reserved[reservationID] = &reservation{transcodeURL: best.URL, createdAt: now}
	}
	p.mu.Unlock()
	if best == nil {
		return nil, func() {}
	}

	var once sync.Once
	return best, func() {
		once.Do(func() { p.ReleaseSession(reservationID) })
	}
}

// TranscodeWorkAvailableWith reports whether a healthy, under-cap transcode
// node satisfies eligible without creating a provisional reservation.
func (p *Planner) TranscodeWorkAvailableWith(eligible func(*Node) bool) bool {
	if p == nil || p.transcodes == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for _, node := range p.transcodes.Nodes() {
		if node == nil || !node.Enabled || !node.Healthy || !p.underCap(node, now) {
			continue
		}
		if eligible == nil || eligible(node) {
			return true
		}
	}
	return false
}

// groupHealth reports, for every group label present in either pool, whether
// all of its enabled members are healthy. Pools only hold enabled nodes, so
// disabled nodes never count against a group.
func groupHealth(proxies, transcodes []*Node) map[string]bool {
	health := make(map[string]bool)
	for _, nodes := range [][]*Node{proxies, transcodes} {
		for _, n := range nodes {
			if n.Group == nil {
				continue
			}
			healthy, seen := health[*n.Group]
			if !seen {
				healthy = true
			}
			health[*n.Group] = healthy && n.Healthy
		}
	}
	return health
}

// pickNode returns the eligible node with the fewest effective jobs, keeping
// the session on currentURL unless a candidate has at least two fewer jobs
// (the historical soft-affinity rule). Shared by pickTranscode and
// pickLocalEgressTranscode, which differ only in their eligibility predicate.
//
// tieBreak, when non-nil, scores candidates that are level on effective jobs;
// the lower score wins. It never overrides the job count or the affinity rule,
// so a caller that passes nil gets exactly the historical selection.
func (p *Planner) pickNode(nodes []*Node, currentURL string, now time.Time, eligible func(*Node) bool, tieBreak func(*Node) int) *Node {
	var best, current *Node
	bestJobs := 0
	for _, n := range nodes {
		if !eligible(n) {
			continue
		}
		if n.URL == currentURL {
			current = n
		}
		jobs := p.effectiveJobs(n, now)
		switch {
		case best == nil, jobs < bestJobs:
			best, bestJobs = n, jobs
		case jobs == bestJobs && tieBreak != nil && tieBreak(n) < tieBreak(best):
			best = n
		}
	}
	if current == nil || best == nil || current == best {
		return best
	}
	if p.effectiveJobs(best, now)+2 <= p.effectiveJobs(current, now) {
		return best
	}
	return current
}

// pickTranscode returns the eligible transcode node with the fewest effective
// jobs, keeping the session on currentURL unless a candidate has at least two
// fewer jobs (the historical soft-affinity rule). Candidates level on job count
// are separated by the load on the physical GPU behind them: two pooled nodes
// can be two containers on one card, and spreading jobs across node records
// that share silicon does not spread the work.
//
// pool is the full transcode pool the candidates were drawn from; shared-GPU
// load is summed over all of it, since a job on a node this plan may not select
// still occupies the same GPU.
func (p *Planner) pickTranscode(transcodes, pool, proxies []*Node, groupHealthy map[string]bool, currentURL string, estKbps int, now time.Time) *Node {
	return p.pickWithScratchGuard(transcodes, pool, currentURL, now, func(n *Node) bool {
		return p.transcodeEligible(n, proxies, groupHealthy, estKbps, now)
	}, p.physicalGPULoadScore(pool, now))
}

// pickWithScratchGuard is pickNode with the scratch-pressure exclusion applied
// as a *soft* filter: a node whose transcode scratch volume is at or past
// scratchPressureFillPercent is skipped, unless skipping leaves no candidate at
// all, in which case the guard is ignored and the ordinary pick stands.
//
// Soft is the whole point. The guard's job is to steer sessions away from a node
// that will die mid-stream while a healthy sibling exists; it is not a license
// to refuse playback. A cluster whose scratch volumes have all filled — one shared
// NFS export, a retention setting that is too generous everywhere — would
// otherwise go from degraded to dark on a threshold nobody chose for that
// purpose. Degraded service beats no service, and the WARN below is what tells
// an operator which it is.
//
// pool is the full transcode pool the candidates were drawn from; it scopes the
// warning latch, which tracks disks rather than the narrowed candidate set.
//
// Callers must hold mu.
func (p *Planner) pickWithScratchGuard(nodes, pool []*Node, currentURL string, now time.Time, eligible func(*Node) bool, tieBreak func(*Node) int) *Node {
	excluded := false
	guarded := func(n *Node) bool {
		if !eligible(n) {
			return false
		}
		if scratchPressured(n) {
			excluded = true
			return false
		}
		return true
	}
	picked := p.pickNode(nodes, currentURL, now, guarded, tieBreak)
	guardDropped := false
	if picked == nil && excluded {
		// Without an exclusion the unguarded pick would return the same nil:
		// nothing was eligible in the first place.
		picked = p.pickNode(nodes, currentURL, now, eligible, tieBreak)
		guardDropped = picked != nil
	}
	// Logged after the fallback decides, because what an operator needs to know
	// is whether the pressured node was actually kept out of selection.
	p.noteScratchPressure(pool, guardDropped)
	return picked
}

// noteScratchPressure logs each pooled node's transition into and out of
// scratch pressure exactly once, and separately latches the cluster-wide state
// where the guard had to give way.
//
// guardDropped reports that this pick found every eligible candidate pressured
// and admitted one anyway. It has to reach the log, because the two states the
// guard can be in are opposites for an operator: "sessions are being steered
// away from this disk" is a warning, while "sessions are landing on a disk that
// is about to fail mid-stream because nothing else is eligible" is an outage in
// progress. One invariant message asserting an exclusion cannot say which, and
// the pressure latch alone would keep asserting the exclusion long after the
// fallback started ignoring it.
//
// There is no context to log against: selection is synchronous inside
// PlanSession and friends, which take no ctx because they are called from
// several request paths and from reservation code that has none. The latch,
// not a context, is what keeps this out of the hot path's log volume — the
// eligibility check runs on every session start and every quality switch.
//
// Callers must hold mu.
func (p *Planner) noteScratchPressure(pool []*Node, guardDropped bool) {
	seen := make(map[int]struct{}, len(pool))
	for _, n := range pool {
		if n == nil {
			continue
		}
		seen[n.ID] = struct{}{}
		pressured := scratchPressured(n)
		if pressured == p.scratchPressed[n.ID] {
			continue
		}
		if pressured {
			pct, _ := scratchDiskFillPercent(n)
			attrs := []any{
				"component", "nodepool", "id", n.ID, "name", n.Name, "url", n.URL,
				"scratch_fill_pct", pct, "threshold_pct", scratchPressureFillPercent,
				"scratch_guard_dropped", guardDropped,
			}
			if guardDropped {
				slog.Warn("transcode node scratch volume nearly full, still selected because no eligible node has scratch headroom", attrs...)
			} else {
				slog.Warn("transcode node scratch volume nearly full, excluded from selection", attrs...)
			}
			p.scratchPressed[n.ID] = true
			continue
		}
		slog.Info("transcode node scratch volume recovered", "component", "nodepool",
			"id", n.ID, "name", n.Name, "url", n.URL)
		delete(p.scratchPressed, n.ID)
	}
	// A node that left the pool must not keep a latch entry: it would suppress
	// the warning if the same id came back still full.
	for id := range p.scratchPressed {
		if _, ok := seen[id]; !ok {
			delete(p.scratchPressed, id)
		}
	}
	p.noteScratchGuardDropped(guardDropped)
}

// noteScratchGuardDropped reports the transition into and out of the state where
// the scratch guard has nothing left to steer towards.
//
// It is latched separately from the per-node pressure warnings because the two
// move independently: a cluster can cross into "no headroom anywhere" long after
// its nodes' pressure transitions were logged, and without this the operator's
// only signal would be an exclusion message written before the exclusion stopped
// happening.
//
// Callers must hold mu.
func (p *Planner) noteScratchGuardDropped(dropped bool) {
	if dropped == p.scratchGuardDropped {
		return
	}
	p.scratchGuardDropped = dropped
	if dropped {
		slog.Warn("transcode scratch guard ignored: every eligible node is over the scratch threshold",
			"component", "nodepool", "threshold_pct", scratchPressureFillPercent)
		return
	}
	slog.Info("transcode scratch guard back in force: an eligible node has scratch headroom again",
		"component", "nodepool")
}

// physicalGPULoadScore precomputes, once per pick, the total effective jobs
// running on each pooled node's physical GPU group: itself plus every pooled
// node sharing at least one GPU identity with it, healthy or not, because a job
// occupies a card regardless of whether the node running it may take another. A
// node with no derived identities is a group of one.
//
// Pools only hold enabled nodes, so a node disabled while its transcodes are
// still running leaves the pool and stops counting against its group — the same
// blind spot the primary least-jobs rule already has, since there is no drain
// state between enabled and gone.
//
// Job counts only. Live GPU utilization is a richer signal but a much worse
// tie-breaker: it lags a newly admitted job by a sampling interval, so a burst
// of starts would all see the same idle card.
func (p *Planner) physicalGPULoadScore(pool []*Node, now time.Time) func(*Node) int {
	jobs := make(map[*Node]int, len(pool))
	byKey := make(map[string][]*Node)
	for _, n := range pool {
		if n == nil {
			continue
		}
		jobs[n] = p.effectiveJobs(n, now)
		for _, key := range n.PhysicalGPUKeys {
			byKey[key] = append(byKey[key], n)
		}
	}

	loads := make(map[*Node]int, len(pool))
	for _, n := range pool {
		if n == nil {
			continue
		}
		total := jobs[n]
		counted := map[*Node]struct{}{n: {}}
		for _, key := range n.PhysicalGPUKeys {
			for _, peer := range byKey[key] {
				// A peer sharing several keys with n must only be counted once.
				if _, seen := counted[peer]; seen {
					continue
				}
				counted[peer] = struct{}{}
				total += jobs[peer]
			}
		}
		loads[n] = total
	}

	return func(n *Node) int {
		if load, ok := loads[n]; ok {
			return load
		}
		// A candidate the pool snapshot does not contain (a concurrent pool
		// swap) scores as its own load, which is the no-sharing answer.
		return p.effectiveJobs(n, now)
	}
}

// pickLocalEgressTranscode applies the transcode half of normal session
// admission without requiring a healthy proxy partner. The API server is the
// egress hop for this route, so unrelated proxy health and capacity must not
// suppress an otherwise healthy transcode executor. Passing nil proxies to
// transcodeEligible reduces it to exactly that: healthy, enabled, under cap,
// and group-healthy, with no proxy partner required.
func (p *Planner) pickLocalEgressTranscode(transcodes, pool []*Node, groupHealthy map[string]bool, currentURL string, now time.Time) *Node {
	return p.pickWithScratchGuard(transcodes, pool, currentURL, now, func(n *Node) bool {
		return p.transcodeEligible(n, nil, groupHealthy, 0, now)
	}, p.physicalGPULoadScore(pool, now))
}

// transcodeEligible reports whether a transcode node may take a new session:
// it must be healthy and under cap, and a grouped node additionally requires
// its whole group healthy and — when the group contains proxies — at least
// one of them with job and bandwidth headroom (a group's capacity is bounded
// by its proxies).
//
// Scratch-volume pressure is deliberately *not* checked here. It is a soft
// exclusion applied by pickWithScratchGuard, which can drop it when it would
// empty the candidate set; a hard predicate could not. Callers that use this
// directly (ReserveTranscodeWorkWith's non-streaming reservations) intentionally
// keep the historical behavior.
func (p *Planner) transcodeEligible(n *Node, proxies []*Node, groupHealthy map[string]bool, estKbps int, now time.Time) bool {
	if !n.Healthy || !n.Enabled || !p.underCap(n, now) {
		return false
	}
	if n.Group == nil {
		return true
	}
	if !groupHealthy[*n.Group] {
		return false
	}
	groupHasProxy := false
	for _, proxy := range proxies {
		if proxy.Group == nil || *proxy.Group != *n.Group {
			continue
		}
		groupHasProxy = true
		if proxy.Healthy && proxy.Enabled && p.underCap(proxy, now) && p.underBandwidthCap(proxy, estKbps, now) {
			return true
		}
	}
	// A group without proxies pins nothing; its transcode nodes fall back
	// to global proxy selection.
	return !groupHasProxy
}

// pickProxy selects a proxy round-robin. When group is set and contains
// proxies, only that group's proxies are considered (keeping transcoded
// traffic on the group's LAN); otherwise any healthy proxy qualifies.
func (p *Planner) pickProxy(proxies []*Node, groupHealthy map[string]bool, group *string, estKbps int, now time.Time) *Node {
	var candidates []*Node
	rrKey := ""
	if group != nil {
		for _, n := range proxies {
			if n.Group != nil && *n.Group == *group && n.Healthy && n.Enabled &&
				groupHealthy[*group] && p.underCap(n, now) && p.underBandwidthCap(n, estKbps, now) {
				candidates = append(candidates, n)
			}
		}
		rrKey = *group
	}
	if len(candidates) == 0 {
		if group != nil {
			groupHasProxy := false
			for _, n := range proxies {
				if n.Group != nil && *n.Group == *group {
					groupHasProxy = true
					break
				}
			}
			// Strict pinning: a group that has proxies but none usable
			// never spills onto other LANs. (Unreachable from PlanSession
			// for transcode plans — transcodeEligible already requires a
			// usable group proxy — but enforced here for safety.)
			if groupHasProxy {
				return nil
			}
		}
		rrKey = ""
		for _, n := range proxies {
			if n.Healthy && n.Enabled && p.underCap(n, now) && p.underBandwidthCap(n, estKbps, now) {
				candidates = append(candidates, n)
			}
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	idx := p.rr[rrKey] % len(candidates)
	p.rr[rrKey]++
	return candidates[idx]
}

// underCap reports whether a node can take one more job.
func (p *Planner) underCap(n *Node, now time.Time) bool {
	return n.MaxJobs == nil || p.effectiveJobs(n, now) < *n.MaxJobs
}

// underBandwidthCap reports whether a proxy has bandwidth headroom for a
// stream of the given estimated bitrate. With an unknown bitrate (0) the
// node only needs to be below its cap.
func (p *Planner) underBandwidthCap(n *Node, estKbps int, now time.Time) bool {
	if n.MaxBandwidthKbps == nil {
		return true
	}
	egress := p.effectiveEgressKbps(n, now)
	if estKbps <= 0 {
		return egress < *n.MaxBandwidthKbps
	}
	return egress+estKbps <= *n.MaxBandwidthKbps
}

// effectiveEgressKbps is the node's health-reported egress plus the estimated
// bitrate of streams admitted within the bandwidth bridge window, which the
// rolling egress average doesn't fully reflect yet.
func (p *Planner) effectiveEgressKbps(n *Node, now time.Time) int {
	egress := n.EgressKbps
	for _, res := range p.reserved {
		if res.proxyURL != n.URL || res.kbps <= 0 {
			continue
		}
		if now.Sub(res.createdAt) >= bandwidthBridgeAge {
			continue
		}
		egress += res.kbps
	}
	return egress
}

// effectiveJobs is the node's health-reported job count plus reservations the
// health checker hasn't had a chance to observe yet.
func (p *Planner) effectiveJobs(n *Node, now time.Time) int {
	jobs := n.ActiveJobs
	for _, res := range p.reserved {
		if now.Sub(res.createdAt) > maxReservationAge {
			continue
		}
		if res.transcodeURL != n.URL && res.proxyURL != n.URL {
			continue
		}
		if n.LastHealthCheck != nil && n.LastHealthCheck.After(res.createdAt) {
			continue // a newer health report already reflects this session
		}
		jobs++
	}
	return jobs
}

func (p *Planner) pruneReservations(now time.Time) {
	for id, res := range p.reserved {
		if now.Sub(res.createdAt) > maxReservationAge {
			delete(p.reserved, id)
		}
	}
}

// LocalTranscodeFallbackAllowed reports whether the API server may transcode
// locally when no eligible transcode node exists, based on the
// playback.local_transcode_fallback setting. Defaults to allowed so
// deployments without the setting keep the historical behavior.
func LocalTranscodeFallbackAllowed(ctx context.Context, settings interface {
	Get(ctx context.Context, key string) (string, error)
}) bool {
	if settings == nil {
		return true
	}
	v, _ := settings.Get(ctx, config.PlaybackLocalTranscodeFallbackSettingKey)
	return v != "false"
}
