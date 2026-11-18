package nodepool

import (
	"net/http"
	"time"
)

// A transcode node enforces the job cap its operator declared, whatever its
// row says, and refuses new work past it. The planner counts the node's
// health-reported jobs against the row's cap, so between health sweeps, or
// when the row's cap was raised on the core, it can keep choosing a node
// that refuses. A caller the node refused reports it here: the node takes no
// new work from this process until a health check newer than the refusal
// reports its load again, or atCapacityHold passes, whichever comes first.
const atCapacityHold = 30 * time.Second

// NodeRefusalHeader names why a transcode node answered 503 to new work, for
// a caller that treats the reasons differently. A draining refusal does not
// carry it, and a core that predates it treats every 503 as a draining
// refusal: it tries another executor or runs the work itself.
const NodeRefusalHeader = "X-Silo-Node-Refusal"

// Values of NodeRefusalHeader.
const (
	// RefusalAtCapacity: the node runs as many jobs as its own job cap
	// (SILO_NODE_MAX_TRANSCODES) allows. It takes new work again once one
	// ends.
	RefusalAtCapacity = "at_capacity"
	// RefusalStorageFull: the node's prepared-download directory holds the
	// budget the node declares (SILO_NODE_DOWNLOAD_ARTIFACT_MAX_BYTES).
	RefusalStorageFull = "storage_full"
)

// RefusedAtCapacity reports whether a node's answer is a refusal at its own
// job cap.
func RefusedAtCapacity(resp *http.Response) bool {
	return resp != nil && resp.StatusCode == http.StatusServiceUnavailable &&
		resp.Header.Get(NodeRefusalHeader) == RefusalAtCapacity
}

// RefusedStorageFull reports whether a node's answer is a refusal at the
// prepared-download budget it declares.
func RefusedStorageFull(resp *http.Response) bool {
	return resp != nil && resp.StatusCode == http.StatusServiceUnavailable &&
		resp.Header.Get(NodeRefusalHeader) == RefusalStorageFull
}

// atCapacityMarker is implemented by *Planner.
type atCapacityMarker interface {
	MarkTranscodeNodeAtCapacity(nodeURL string)
}

// ReportAtCapacity tells planner that the transcode node at nodeURL refused
// new work at its own job cap. planner is whatever planner the caller holds;
// one that cannot hold nodes back ignores the report.
func ReportAtCapacity(planner any, nodeURL string) {
	if marker, ok := planner.(atCapacityMarker); ok && nodeURL != "" {
		marker.MarkTranscodeNodeAtCapacity(nodeURL)
	}
}

// MarkTranscodeNodeAtCapacity holds the node at nodeURL back from new work;
// see ReportAtCapacity.
func (p *Planner) MarkTranscodeNodeAtCapacity(nodeURL string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.atCapacity == nil {
		p.atCapacity = make(map[string]time.Time)
	}
	p.atCapacity[normalizeNodeURL(nodeURL)] = p.now()
}

// heldAtCapacity reports whether n refused new work at its own cap and no
// health check has reported its load since. The caller holds p.mu.
func (p *Planner) heldAtCapacity(n *Node, now time.Time) bool {
	refusedAt, ok := p.atCapacity[normalizeNodeURL(n.URL)]
	if !ok || now.Sub(refusedAt) >= atCapacityHold {
		return false
	}
	return n.LastHealthCheck == nil || !n.LastHealthCheck.After(refusedAt)
}

// pruneAtCapacity drops holds that have run out. The caller holds p.mu.
func (p *Planner) pruneAtCapacity(now time.Time) {
	for url, refusedAt := range p.atCapacity {
		if now.Sub(refusedAt) >= atCapacityHold {
			delete(p.atCapacity, url)
		}
	}
}
