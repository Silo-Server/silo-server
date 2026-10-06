package nodepool

import "sync"

// ReserveOutcome says why Reservations.Reserve did or did not pick a node.
type ReserveOutcome int

const (
	// Reserved picked a node.
	Reserved ReserveOutcome = iota
	// NoHealthyNode found no enabled, healthy node.
	NoHealthyNode
	// CapacityExhausted found healthy nodes, each already at capacity.
	CapacityExhausted
)

// Reservations tracks how much of one feature's side work (frame extraction,
// media sampling) this server has placed on each transcode node, so the
// feature stays within its per-node capacity on top of the node's own load.
// A feature keeps one Reservations; the zero value is ready to use.
type Reservations struct {
	mu     sync.Mutex
	counts map[string]int
}

// NewReservations starts with the given per-node counts, keyed by node URL;
// tests use it to model work already in flight.
func NewReservations(initial map[string]int) *Reservations {
	r := &Reservations{counts: make(map[string]int, len(initial))}
	for url, n := range initial {
		r.counts[NormalizeNodeURL(url)] = n
	}
	return r
}

// Reserve picks the enabled, healthy node with the least effective load (its
// reported active jobs plus this feature's reservations) among nodes with
// fewer than capacity reservations, and reserves it until release is called.
func (r *Reservations) Reserve(nodes []*Node, capacity int) (node *Node, release func(), outcome ReserveOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.counts == nil {
		r.counts = make(map[string]int)
	}
	var best *Node
	bestLoad := 0
	healthy := false
	for _, n := range nodes {
		if n == nil || !n.Enabled || !n.Healthy {
			continue
		}
		healthy = true
		reserved := r.counts[NormalizeNodeURL(n.URL)]
		if reserved >= capacity {
			continue
		}
		if load := n.ActiveJobs + reserved; best == nil || load < bestLoad {
			best, bestLoad = n, load
		}
	}
	if best == nil {
		if healthy {
			return nil, func() {}, CapacityExhausted
		}
		return nil, func() {}, NoHealthyNode
	}
	key := NormalizeNodeURL(best.URL)
	r.counts[key]++
	var once sync.Once
	return best, func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.counts[key] <= 1 {
				delete(r.counts, key)
				return
			}
			r.counts[key]--
		})
	}, Reserved
}
