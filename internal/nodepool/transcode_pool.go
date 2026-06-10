package nodepool

import (
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

// SetNodes replaces the node list.
func (p *TranscodePool) SetNodes(nodes []*Node) {
	p.mu.Lock()
	defer p.mu.Unlock()
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
func (p *TranscodePool) ApplyHealth(id int, healthy bool, activeJobs, egressKbps int, checkedAt time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	applyNodeHealth(p.nodes, id, healthy, activeJobs, egressKbps, checkedAt)
}

// applyNodeHealth replaces the slice entry for id with an updated copy.
func applyNodeHealth(nodes []*Node, id int, healthy bool, activeJobs, egressKbps int, checkedAt time.Time) {
	for i, n := range nodes {
		if n.ID != id {
			continue
		}
		clone := *n
		clone.Healthy = healthy
		clone.ActiveJobs = activeJobs
		clone.EgressKbps = egressKbps
		clone.LastHealthCheck = &checkedAt
		nodes[i] = &clone
		return
	}
}
