package nodepool

import (
	"net/http"
	"slices"
	"strings"
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

// NodeRefusalKindHeader names, on an at_capacity refusal, the job kind whose
// own cap (SILO_NODE_MAX_VIDEO_TRANSCODES and the like) refused the work, as
// a JobKind value. It is absent when the node's job cap refused it, and from
// a node that predates kind caps: either way the whole node is full.
const NodeRefusalKindHeader = "X-Silo-Node-Refusal-Kind"

// RefusedKind returns the job kind whose own cap an at_capacity refusal
// names, or "" when the node's job cap refused the work or the header names
// no kind this build knows.
func RefusedKind(resp *http.Response) JobKind {
	if !RefusedAtCapacity(resp) {
		return ""
	}
	return knownJobKind(resp.Header.Get(NodeRefusalKindHeader))
}

// knownJobKind returns raw as a JobKind when it names one, else "".
func knownJobKind(raw string) JobKind {
	kind := JobKind(strings.TrimSpace(raw))
	if slices.Contains(JobKinds, kind) {
		return kind
	}
	return ""
}

// atCapacityMarker is implemented by *Planner.
type atCapacityMarker interface {
	MarkTranscodeNodeAtCapacity(nodeURL string, kind JobKind)
}

// ReportAtCapacity tells planner that the transcode node at nodeURL refused
// new work at its own job cap, or with kind (RefusedKind) at that kind's own
// cap, which holds the node back from that kind only: the node still takes
// the other kinds. planner is whatever planner the caller holds; one that
// cannot hold nodes back ignores the report.
func ReportAtCapacity(planner any, nodeURL string, kind JobKind) {
	if marker, ok := planner.(atCapacityMarker); ok && nodeURL != "" {
		marker.MarkTranscodeNodeAtCapacity(nodeURL, kind)
	}
}

// atCapacityKey is one hold: a node's job cap (kind "") or one kind's cap.
type atCapacityKey struct {
	url  string
	kind JobKind
}

// MarkTranscodeNodeAtCapacity holds the node at nodeURL back from new work,
// or from work of kind when kind is set; see ReportAtCapacity.
func (p *Planner) MarkTranscodeNodeAtCapacity(nodeURL string, kind JobKind) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.atCapacity == nil {
		p.atCapacity = make(map[atCapacityKey]time.Time)
	}
	p.atCapacity[atCapacityKey{url: normalizeNodeURL(nodeURL), kind: kind}] = p.now()
}

// heldAtCapacity reports whether n refused new work at its own job cap, or
// work of kind at that kind's cap when kind is set, and no health check has
// reported its load since. The caller holds p.mu.
func (p *Planner) heldAtCapacity(n *Node, kind JobKind, now time.Time) bool {
	refusedAt, ok := p.atCapacity[atCapacityKey{url: normalizeNodeURL(n.URL), kind: kind}]
	if !ok || now.Sub(refusedAt) >= atCapacityHold {
		return false
	}
	return n.LastHealthCheck == nil || !n.LastHealthCheck.After(refusedAt)
}

// pruneAtCapacity drops holds that have run out. The caller holds p.mu.
func (p *Planner) pruneAtCapacity(now time.Time) {
	for key, refusedAt := range p.atCapacity {
		if now.Sub(refusedAt) >= atCapacityHold {
			delete(p.atCapacity, key)
		}
	}
}
