package nodepool

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// TranscodePool manages transcode nodes with least-connections selection.
// Thread-safe for concurrent use.
type TranscodePool struct {
	nodes []*Node
	mu    sync.RWMutex
}

// NewTranscodePool creates an empty transcode pool.
func NewTranscodePool() *TranscodePool {
	return &TranscodePool{}
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
	for _, n := range nodes {
		if n != nil {
			n.URL = normalizeNodeURL(n.URL)
			applyPhysicalGPUKeys(n)
		}
	}
	p.nodes = nodes
}

// Acquire returns the healthy node with the fewest active jobs.
// Returns nil if no healthy nodes are available.
func (p *TranscodePool) Acquire() *Node {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var best *Node
	for _, n := range p.nodes {
		if !n.Healthy || !n.Enabled {
			continue
		}
		if best == nil || n.ActiveJobs < best.ActiveJobs {
			best = n
		}
	}
	return best
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
// updated copy, keeping published *Node values immutable.
func (p *TranscodePool) ApplyHealth(id int, checkedURL string, healthy bool, activeJobs, egressKbps int, advertisedHash string, lastStats []byte, checkedAt time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	applyNodeHealth(p.nodes, id, checkedURL, healthy, activeJobs, egressKbps, advertisedHash, lastStats, checkedAt)
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
func applyNodeHealth(nodes []*Node, id int, checkedURL string, healthy bool, activeJobs, egressKbps int, advertisedHash string, lastStats []byte, checkedAt time.Time) {
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
