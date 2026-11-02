package nodepool

import (
	"context"
	"fmt"
)

// DrainState is what a transcode node said about draining on its last health
// check. Draining takes the node out of new placement while the sessions it
// already runs finish, so a rolling upgrade or scale-down does not cut them.
type DrainState string

const (
	// DrainStateNone is a node that takes new work. A node that predates
	// draining, or did not answer, reads as this.
	DrainStateNone DrainState = "none"
	// DrainStateDraining is a node that refuses new work and is finishing the
	// work it has, after a drain request or a SIGTERM.
	DrainStateDraining DrainState = "draining"
	// DrainStateDrained is a draining node with nothing left to finish: it can
	// be stopped without cutting a stream.
	DrainStateDrained DrainState = "drained"
)

// drainStateOfHealthStatus maps the status a node's /api/v1/health answered
// with. "ok" and anything unknown are DrainStateNone.
func drainStateOfHealthStatus(status string) DrainState {
	switch DrainState(status) {
	case DrainStateDraining:
		return DrainStateDraining
	case DrainStateDrained:
		return DrainStateDrained
	default:
		return DrainStateNone
	}
}

// normalized maps the zero value, which an in-memory Node built outside the
// repository carries, onto DrainStateNone.
func (s DrainState) normalized() DrainState {
	if s == "" {
		return DrainStateNone
	}
	return s
}

// Draining reports whether new work must stay off the node: an administrator
// asked to drain it, or the node said it is draining.
func (n *Node) Draining() bool {
	if n == nil {
		return false
	}
	return n.DrainRequested || n.DrainState.normalized() != DrainStateNone
}

// TakesNewWork reports whether new work may be placed on the node: it is
// enabled, healthy, and not draining. Capacity is the planner's to judge.
// Checks that ask whether any node could run something use it, so they never
// count a node that placement will refuse.
func (n *Node) TakesNewWork() bool {
	return n != nil && n.Enabled && n.Healthy && !n.Draining()
}

// admitsSession reports whether the node may be picked for a playback session
// whose current transcode node is currentURL. A draining node takes no new
// session, but until it reports drained it stays eligible for the one it
// already runs: a Jellyfin client that asks for its master playlist again is
// planned once more, and planning it away would tear down a stream the node is
// still serving. The node itself refuses a new transport for such a session (a
// v3 seek or quality change), which moves it to another node. A drained node
// runs no session, so a returning client is planned elsewhere.
func admitsSession(n *Node, currentURL string) bool {
	if !n.Draining() {
		return true
	}
	if n.DrainState.normalized() == DrainStateDrained {
		return false
	}
	return currentURL != "" && sameNodeURL(n.URL, currentURL)
}

// TranscodeNodeDrained reports whether the pooled transcode node at nodeURL
// has reported itself drained. A drained node runs no session, so a runtime
// recorded on it is gone and must not be adopted.
func (p *Planner) TranscodeNodeDrained(nodeURL string) bool {
	node, ok := p.TranscodeNodeByURL(nodeURL)
	return ok && node.DrainState.normalized() == DrainStateDrained
}

// TranscodeNodeURLsForNewWork is TranscodeNodeURLs without draining nodes. A
// draining node accepts no new transport, so capability planning for a new
// plan must not count what it advertises.
func (p *Planner) TranscodeNodeURLsForNewWork() []string {
	if p == nil || p.transcodes == nil {
		return nil
	}
	nodes := p.transcodes.Nodes()
	urls := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if node != nil && node.URL != "" && !node.Draining() {
			urls = append(urls, node.URL)
		}
	}
	return urls
}

// SetDrainRequested records or clears an administrator's drain request. It is
// not configuration: it does not move the node's admin revision.
//
// Lifting a request also clears the drain state the node last reported. That
// report described the request being lifted, and keeping it would hold the node
// out of placement, and show it as stopping, until its next health check. A
// node that is draining for another reason (SIGTERM) sees the request change,
// announces, and says so again on the check that triggers, refusing new work
// meanwhile. Clearing a request that was not set changes nothing the node would
// announce, so the reported state stays.
func (r *Repository) SetDrainRequested(ctx context.Context, id int, requested bool) (*Node, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE stream_nodes SET drain_requested = $2,
		        drain_state = CASE WHEN $2 OR NOT drain_requested THEN drain_state ELSE 'none' END
		 WHERE id = $1`, id, requested)
	if err != nil {
		return nil, fmt.Errorf("set node drain request: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNodeNotFound
	}
	return r.GetByID(ctx, id)
}

// CheckTranscodeNode runs one health check of the pooled transcode node id now,
// the way the sweep would, and reports whether the node was pooled. A node that
// announces a drain change calls for this: the sweep may be up to its interval
// away, and placement should see the change at once.
func (hc *HealthChecker) CheckTranscodeNode(ctx context.Context, id int) bool {
	if hc == nil || hc.transcodePool == nil {
		return false
	}
	for _, n := range hc.transcodePool.Nodes() {
		if n != nil && n.ID == id {
			hc.checkOne(ctx, n, hc.transcodePool.ApplyHealthReport, hc.transcodePool.ApplyCapabilities)
			return true
		}
	}
	return false
}
