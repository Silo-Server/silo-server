package nodepool

import (
	"encoding/json"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/netaccess"
)

// TranscodePool manages transcode nodes with least-connections selection.
// Thread-safe for concurrent use.
type TranscodePool struct {
	nodes []*Node
	mu    sync.RWMutex
	// suppressed marks nodes that recently failed a remote start, keyed by
	// normalized URL. A suppressed node is skipped by Acquire until its
	// cooldown lapses or a healthy sweep clears it. It never flips
	// Node.Healthy: the health sweep stays the authority on node health,
	// and suppression is purely a latency mitigation so the next start does
	// not re-pay a full readiness timeout against a node that just 500'd.
	// Process-local by construction: each API replica suppresses from its
	// own observed failures. Entries are bounded (one per node URL) and
	// pruned on SetNodes and on SuppressNode writes.
	suppressed map[string]suppressedNode
	// recovered tracks the per-URL recovery epoch independently of the
	// suppression entry: ApplyHealth bumps it on every healthy result, and
	// SuppressNode refuses completions admitted before the latest recovery.
	// Without this, deleting the suppression entry on recovery would leave
	// no record that a recovery happened, and a stale in-flight failure
	// could re-suppress the just-recovered node. Bounded like suppressed
	// (one entry per URL) and pruned together with it.
	recovered map[string]uint64
	now       func() time.Time
	// unreachable holds the normalized URLs of nodes marked unreachable by
	// MarkUnreachable. The mark has to outlive SetNodes, which the node
	// configuration reconciliation calls every few seconds with the database
	// rows, and those still carry the last sweep's verdict. A successful
	// health result for the node, or the node leaving the pool, clears it.
	unreachable map[string]struct{}
	// keyCheck, when set, reports whether a node id has a usable key (the
	// credential registry's HasKey). Without it each node's KeyIssued stands
	// in, which only a pool reload updates.
	keyCheck atomic.Pointer[func(nodeID int) bool]
}

// suppressedNode is one cooldown entry: the recovery epoch observed when it
// tripped, and its expiry.
type suppressedNode struct {
	epoch     uint64
	expiresAt time.Time
}

// transcodeSuppressCooldown bounds how long one failed remote start keeps a
// node out of selection. One failure already cost the full readiness timeout
// (~35s, or ~8s observed on fast 500s); the next start must not re-pay it.
// The sweep (30s) clears suppression early on a healthy result, so the
// cooldown only binds a node the sweep has not yet re-validated. Jitter is
// applied per trip at the call site so replicas do not reopen in lockstep.
const transcodeSuppressCooldown = 15 * time.Second

// transcodeSuppressEpoch is the process-wide monotonic recovery epoch for
// suppression entries. ApplyHealth bumps it on every healthy result and
// records it per URL; SuppressNode refuses completions admitted before the
// latest recovery for that URL, so a stale in-flight failure cannot
// re-suppress a node the sweep just cleared.
var transcodeSuppressEpoch atomic.Uint64

func (p *TranscodePool) poolClock() time.Time {
	if p != nil && p.now != nil {
		return p.now()
	}
	return time.Now()
}

// NewTranscodePool creates an empty transcode pool.
func NewTranscodePool() *TranscodePool {
	return &TranscodePool{}
}

// SetKeyCheck makes the pool ask hasKey whether a node can be signed for.
// Wire it to the credential registry's HasKey, which a missed
// node_pool_changed event delays by one registry refresh at most.
func (p *TranscodePool) SetKeyCheck(hasKey func(nodeID int) bool) {
	p.keyCheck.Store(&hasKey)
}

// HasKey reports whether a request to n can be signed. A transcode node
// without a key cannot be, so it takes no new work: it would only fail every
// job sent to it, and as the node that never has a job it would otherwise win
// least-loaded selection. It stays pooled, listed, and health-checked.
func (p *TranscodePool) HasKey(n *Node) bool {
	if n == nil {
		return false
	}
	// Vio signs worker calls with the shared auth secret rather than issuing
	// each transcode node its own key, so every pooled node is signable here.
	// A deployment that starts issuing node keys wires a key check, which then
	// answers for each node.
	if p == nil {
		return true
	}
	if check := p.keyCheck.Load(); check != nil && *check != nil {
		return (*check)(n.ID)
	}
	return true
}

// KeyedNodes returns the pooled nodes a request can be signed for: the only
// ones new work may go to.
func (p *TranscodePool) KeyedNodes() []*Node {
	nodes := p.Nodes()
	keyed := nodes[:0]
	for _, n := range nodes {
		if p.HasKey(n) {
			keyed = append(keyed, n)
		}
	}
	return keyed
}

// SetNodes replaces the node list. Node URLs are normalized (trailing slashes
// trimmed) at the storage boundary so every consumer compares them
// consistently, including TranscodeNodeHealthy and remote-start adoption.
// Physical GPU identities are derived here too, so the planner's shared-GPU
// accounting works from the stored capability report immediately after a
// restart, before any node has advertised a changed hash.
func (p *TranscodePool) SetNodes(nodes []*Node) {
	p.mu.Lock()
	defer p.mu.Unlock()
	present := make(map[string]struct{}, len(nodes))
	for _, n := range nodes {
		if n != nil {
			n.URL = normalizeNodeURL(n.URL)
			applyPhysicalGPUKeys(n)
			present[n.URL] = struct{}{}
			if _, marked := p.unreachable[n.URL]; marked {
				n.Healthy = false
			}
		}
	}
	for url := range p.unreachable {
		if _, ok := present[url]; !ok {
			delete(p.unreachable, url)
		}
	}
	p.nodes = nodes
	p.pruneSuppressedLocked(p.poolClock())
}

// Acquire returns the healthy node with the fewest active jobs.
// Returns nil if no healthy nodes are available. Nodes inside a suppression
// cooldown (a recent failed remote start this process observed) are skipped
// so the next start does not re-pay a full readiness timeout; the cooldown
// lapses on its own and a healthy sweep clears it early.
func (p *TranscodePool) Acquire() *Node {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()

	now := p.poolClock()
	var best *Node
	for _, n := range p.nodes {
		if !n.Healthy || !n.Enabled {
			continue
		}
		if p.suppressedLocked(n.URL, now) {
			continue
		}
		if best == nil || n.ActiveJobs < best.ActiveJobs {
			best = n
		}
	}
	return best
}

// suppressedLocked reports whether url is inside a live suppression window.
// Caller holds at least p.mu.RLock. Expired entries stay in the map until the
// next write-side prune: Acquire is a hot read path shared with capacity
// routing, and deleting under RLock would force a full write lock per start.
// The map holds at most one entry per node URL, so the garbage is bounded;
// SetNodes, SuppressNode, and ApplyHealth all prune on write.
func (p *TranscodePool) suppressedLocked(url string, now time.Time) bool {
	if p == nil || len(p.suppressed) == 0 {
		return false
	}
	entry, ok := p.suppressed[normalizeNodeURL(url)]
	if !ok {
		return false
	}
	if !now.Before(entry.expiresAt) {
		return false
	}
	return true
}

// pruneSuppressedLocked drops lapsed windows and entries for nodes no longer
// in the pool, plus recovery epochs for removed nodes. Caller holds p.mu
// (write). Only write paths prune: read paths (Acquire, suppressedAt) never
// mutate, so -race stays clean on the hot path and both maps stay bounded at
// one entry per node URL regardless.
func (p *TranscodePool) pruneSuppressedLocked(now time.Time) {
	live := make(map[string]struct{}, len(p.nodes))
	for _, n := range p.nodes {
		if n != nil {
			live[normalizeNodeURL(n.URL)] = struct{}{}
		}
	}
	for url, entry := range p.suppressed {
		if _, ok := live[url]; !ok || !now.Before(entry.expiresAt) {
			delete(p.suppressed, url)
		}
	}
	for url := range p.recovered {
		if _, ok := live[url]; !ok {
			delete(p.recovered, url)
		}
	}
}

// SuppressNode records a failed remote start against nodeURL, keeping it out
// of Acquire for a short jittered cooldown. Only genuine transport failures
// qualify: HTTP 5xx, connection/DNS/TLS errors, or the caller-side transport
// timeout firing while its parent context is still live. Parent cancellation,
// 4xx, virtual-input resolution, recipe/attestation, and typed source
// validation failures must never call this — they are not evidence the node
// is sick. A stale completion (admitted before the latest healthy recovery
// for this URL) cannot re-arm: pass the recovery epoch observed when the
// start was admitted via RecoveryEpoch, or admit with the current epoch for
// an unconditional trip.
func (p *TranscodePool) SuppressNode(nodeURL string, admittedEpoch uint64, cooldown time.Duration) {
	if p == nil || strings.TrimSpace(nodeURL) == "" {
		return
	}
	if cooldown <= 0 {
		cooldown = transcodeSuppressCooldown
	}
	// Independent jitter per trip so replicas that failed together do not
	// reopen in lockstep and thunder the recovering node.
	jitter := time.Duration(rand.Int64N(int64(5 * time.Second)))
	p.mu.Lock()
	defer p.mu.Unlock()
	key := normalizeNodeURL(nodeURL)
	if p.suppressed == nil {
		p.suppressed = make(map[string]suppressedNode)
	}
	now := p.poolClock()
	if epoch, ok := p.recovered[key]; ok && epoch > admittedEpoch {
		return
	}
	p.pruneSuppressedLocked(now)
	p.suppressed[key] = suppressedNode{
		epoch:     admittedEpoch,
		expiresAt: now.Add(cooldown + jitter),
	}
}

// RecoveryEpoch captures the current recovery epoch for nodeURL. Call it when
// a remote start is admitted; pass the value to SuppressNode if that start
// later fails, so a healthy recovery in between rejects the stale failure.
func (p *TranscodePool) RecoveryEpoch(nodeURL string) uint64 {
	if p == nil {
		return 0
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.recovered[normalizeNodeURL(nodeURL)]
}

// suppressedAt reports whether url is inside a live suppression window at
// now. The planner calls it with its own clock so fake-clock tests control
// both sides. SuppressedForTest (wall clock) remains the tests-only helper.
func (p *TranscodePool) suppressedAt(url string, now time.Time) bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.suppressedLocked(url, now)
}

// SuppressedForTest reports whether url is currently suppressed. Tests only.
func (p *TranscodePool) SuppressedForTest(url string) bool {
	return p.suppressedAt(url, p.poolClock())
}

// ClearSuppressionForTest drops one suppression entry. Tests only.
func (p *TranscodePool) ClearSuppressionForTest(url string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.suppressed, normalizeNodeURL(url))
}

// FindByURL returns the node with the given URL, or nil if not found.
// Used for soft-affinity during quality switches.
func (p *TranscodePool) FindByURL(url string) *Node {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, n := range p.nodes {
		if n.URL == url {
			return n
		}
	}
	return nil
}

// normalizeNodeURL trims trailing slashes so a node URL stored with a
// trailing slash (e.g. an admin-entered base URL) compares equal to a lookup
// without one. Applied where URLs enter the pool and where lookups are made.
func normalizeNodeURL(url string) string {
	return strings.TrimRight(url, "/")
}

// Nodes returns a copy of the current node list.
func (p *TranscodePool) Nodes() []*Node {
	p.mu.RLock()
	defer p.mu.RUnlock()
	cp := make([]*Node, len(p.nodes))
	copy(cp, p.nodes)
	return cp
}

// ApplyHealth records a health check result by swapping the node for an
// updated copy, keeping published *Node values immutable. A healthy result
// clears any suppression the pool holds for the node: the sweep is the
// authority on node health, and a node it just validated must serve
// immediately rather than wait out a cooldown. The per-URL recovery epoch
// bumps so a stale in-flight failure (admitted before this recovery)
// cannot re-arm it via SuppressNode. An unhealthy result keeps ordinary
// health exclusion; suppression entries are left alone because Acquire
// already skips unhealthy nodes.
func (p *TranscodePool) ApplyHealth(id int, checkedURL string, healthy bool, activeJobs, egressKbps int, advertisedHash string, lastStats []byte, networkAccess netaccess.NodeNetworkAccess, checkedAt time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	applyNodeHealth(p.nodes, id, checkedURL, healthy, activeJobs, egressKbps, advertisedHash, lastStats, networkAccess, nil, checkedAt)
	p.clearUnreachableLocked(id, checkedURL, healthy)
	if healthy {
		key := normalizeNodeURL(checkedURL)
		delete(p.suppressed, key)
		if p.recovered == nil {
			p.recovered = make(map[string]uint64)
		}
		p.recovered[key] = transcodeSuppressEpoch.Add(1)
		p.pruneSuppressedLocked(p.poolClock())
	}
}

// ApplyHealthReport is ApplyHealth for a whole report, drain state included.
func (p *TranscodePool) ApplyHealthReport(id int, checkedURL string, report HealthReport, checkedAt time.Time) {
	drainState := report.DrainState.normalized()
	p.mu.Lock()
	defer p.mu.Unlock()
	applyNodeHealth(p.nodes, id, checkedURL, report.Healthy, report.ActiveJobs, report.EgressKbps, report.CapabilitiesHash, report.LastStats, report.NetworkAccess, &drainState, checkedAt)
	p.clearUnreachableLocked(id, checkedURL, report.Healthy)
}

// clearUnreachableLocked lifts the unreachable mark on checkedURL after a
// healthy result. Cleared only by a result about the node now at that URL: a
// late result for a removed or repointed node says nothing about its
// replacement. The caller holds p.mu.
func (p *TranscodePool) clearUnreachableLocked(id int, checkedURL string, healthy bool) {
	if !healthy {
		return
	}
	for _, n := range p.nodes {
		if n != nil && n.ID == id && sameNodeURL(n.URL, checkedURL) {
			delete(p.unreachable, normalizeNodeURL(checkedURL))
			return
		}
	}
}

// ApplyCapabilities records a freshly fetched capability report by swapping the
// node for an updated copy, keeping published *Node values immutable.
func (p *TranscodePool) ApplyCapabilities(id int, fetchedFrom string, capabilities []byte, hash string, refreshedAt time.Time, drift *string, driftBaseline []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	applyNodeCapabilities(p.nodes, id, fetchedFrom, capabilities, hash, refreshedAt, drift, driftBaseline)
}

// NormalizeNodeURL is how a node's address is written wherever it is used as an
// identity: a map key, a comparison, or the base of a request.
//
// A stored URL may carry a trailing slash and mean the same worker, so anything
// that keys by the raw value ends up with two entries for one node — and the
// one an invalidation deletes is not the one a lookup finds.
func NormalizeNodeURL(nodeURL string) string {
	return normalizeNodeURL(nodeURL)
}

// NodeEndpoint joins a stored node URL with one of the node's own routes.
//
// A stored URL may carry a trailing slash — an operator pasting a base URL is
// the usual way, and everything here already treats the two forms as the same
// worker. Concatenating a route onto it produces "//admin/…", which the node's
// router does not have: the request 404s, and the operator's action fails
// against a node that is running and reachable. The normalization that makes
// the two forms equal for comparison has to make them equal for addressing too.
func NodeEndpoint(nodeURL, path string) string {
	return normalizeNodeURL(nodeURL) + path
}

// sameNodeURL compares two node addresses the way the pools store them, so a
// trailing slash on one side is not a different worker.
func sameNodeURL(a, b string) bool {
	return normalizeNodeURL(a) == normalizeNodeURL(b)
}

// applyNodeHealth replaces the slice entry for id with an updated copy.
//
// checkedURL fences the write the same way the database update does, and for
// the same reason: a health request is bounded at five seconds, which is ample
// time for an administrator to repoint the row and reload the pools. Publishing
// by id alone would then write one worker's health — and the scratch fill
// transcode admission reads — onto the replacement, and the database fence
// downstream cannot undo that. The pool would stay wrong until a later sweep.
//
// A nil drainState leaves the node's drain state as it was.
func applyNodeHealth(nodes []*Node, id int, checkedURL string, healthy bool, activeJobs, egressKbps int, advertisedHash string, lastStats []byte, networkAccess netaccess.NodeNetworkAccess, drainState *DrainState, checkedAt time.Time) {
	for i, n := range nodes {
		if n.ID != id || !sameNodeURL(n.URL, checkedURL) {
			continue
		}
		clone := *n
		clone.Healthy = healthy
		clone.ActiveJobs = activeJobs
		clone.EgressKbps = egressKbps
		clone.LastHealthCheck = &checkedAt
		// Always a pointer once a check has happened, empty string included: the
		// distinction between "this node reports no hash" and "no one has asked"
		// is the whole reason the field is one.
		clone.AdvertisedCapabilitiesHash = &advertisedHash
		// A check that carried no stats clears them rather than keeping the
		// previous sample: an unreachable node's five-minute-old CPU number
		// looks live on a dashboard, which is worse than no number at all. The
		// payload is cloned because the caller's buffer is a decoded HTTP body.
		if len(lastStats) > 0 {
			clone.LastStats = append(json.RawMessage(nil), lastStats...)
		} else {
			clone.LastStats = nil
		}
		// Same rule for the network access report: a check that carried none
		// clears it, so ClientURLFor never hands out an origin the node has
		// stopped confirming. Cloned so the published copy is immutable.
		clone.NetworkAccess = networkAccess.Normalized().Clone()
		if drainState != nil {
			clone.DrainState = *drainState
		}
		nodes[i] = &clone
		return
	}
}

// applyNodeCapabilities replaces the slice entry for id with a copy carrying
// the new capability report. The payload is cloned because the caller's buffer
// (a decoded HTTP response) is not ours to publish.
//
// drift is set verbatim, nil included: the note describes the comparison that
// produced this payload, so a node whose hardware recovered must lose the note
// at the same moment it gains the clean report.
func applyNodeCapabilities(nodes []*Node, id int, fetchedFrom string, capabilities []byte, hash string, refreshedAt time.Time, drift *string, driftBaseline []byte) {
	for i, n := range nodes {
		if n.ID != id || !sameNodeURL(n.URL, fetchedFrom) {
			continue
		}
		clone := *n
		clone.Capabilities = append(json.RawMessage(nil), capabilities...)
		clone.CapabilitiesHash = &hash
		clone.CapabilitiesRefreshedAt = &refreshedAt
		clone.CapabilityDrift = drift
		if len(driftBaseline) > 0 {
			clone.CapabilityDriftBaseline = append(json.RawMessage(nil), driftBaseline...)
		} else {
			clone.CapabilityDriftBaseline = nil
		}
		// The GPU identities belong to the payload being replaced, so they are
		// re-derived rather than carried over from the previous report.
		applyPhysicalGPUKeys(&clone)
		nodes[i] = &clone
		return
	}
}
