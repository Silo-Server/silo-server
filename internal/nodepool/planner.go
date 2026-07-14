package nodepool

import (
	"context"
	"sync"
	"time"
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
}

// NewPlanner creates a planner over the given pools.
func NewPlanner(proxies *ProxyPool, transcodes *TranscodePool) *Planner {
	return &Planner{
		proxies:    proxies,
		transcodes: transcodes,
		rr:         make(map[string]int),
		reserved:   make(map[string]*reservation),
		now:        time.Now,
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
	transcodes := p.transcodes.Nodes()
	groupHealthy := groupHealth(proxies, transcodes)

	var plan Plan
	if needsTranscode {
		plan.TranscodeNode = p.pickTranscode(transcodes, proxies, groupHealthy, currentTranscodeURL, estBitrateKbps, now)
		if plan.TranscodeNode != nil {
			plan.ProxyNode = p.pickProxy(proxies, groupHealthy, plan.TranscodeNode.Group, estBitrateKbps, now)
		}
	} else {
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

// pickTranscode returns the eligible transcode node with the fewest effective
// jobs, keeping the session on currentURL unless a candidate has at least two
// fewer jobs (the historical soft-affinity rule).
func (p *Planner) pickTranscode(transcodes, proxies []*Node, groupHealthy map[string]bool, currentURL string, estKbps int, now time.Time) *Node {
	var best, current *Node
	for _, n := range transcodes {
		if !p.transcodeEligible(n, proxies, groupHealthy, estKbps, now) {
			continue
		}
		if n.URL == currentURL {
			current = n
		}
		if best == nil || p.effectiveJobs(n, now) < p.effectiveJobs(best, now) {
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

// transcodeEligible reports whether a transcode node may take a new session:
// it must be healthy and under cap, and a grouped node additionally requires
// its whole group healthy and — when the group contains proxies — at least
// one of them with job and bandwidth headroom (a group's capacity is bounded
// by its proxies).
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
	v, _ := settings.Get(ctx, "playback.local_transcode_fallback")
	return v != "false"
}
