package nodepool

import (
	"sync"
	"sync/atomic"
	"time"
)

// ProxyPool manages proxy nodes with round-robin selection.
// Thread-safe for concurrent use.
type ProxyPool struct {
	nodes   []*Node
	mu      sync.RWMutex
	nextIdx atomic.Uint64
}

// NewProxyPool creates an empty proxy pool.
func NewProxyPool() *ProxyPool {
	return &ProxyPool{}
}

// SetNodes replaces the node list. Called on startup and when admin changes nodes.
func (p *ProxyPool) SetNodes(nodes []*Node) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nodes = nodes
}

// Pick returns a healthy node using round-robin selection.
// Returns nil if no healthy nodes are available.
func (p *ProxyPool) Pick() *Node {
	p.mu.RLock()
	defer p.mu.RUnlock()

	n := len(p.nodes)
	if n == 0 {
		return nil
	}
	start := int(p.nextIdx.Add(1) - 1)
	for i := range n {
		node := p.nodes[(start+i)%n]
		if node.Healthy && node.Enabled {
			return node
		}
	}
	return nil
}

// Nodes returns a copy of the current node list.
func (p *ProxyPool) Nodes() []*Node {
	p.mu.RLock()
	defer p.mu.RUnlock()
	cp := make([]*Node, len(p.nodes))
	copy(cp, p.nodes)
	return cp
}

// ApplyHealth records a health check result by swapping the node for an
// updated copy, keeping published *Node values immutable.
func (p *ProxyPool) ApplyHealth(id int, healthy bool, activeJobs, egressKbps int, checkedAt time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	applyNodeHealth(p.nodes, id, healthy, activeJobs, egressKbps, checkedAt)
}
