package nodepool

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"syscall"
	"time"
)

// A transcode node that dies between health sweeps keeps winning selection
// until the next sweep, up to 30 seconds later, and every playback start sent
// to it in that window fails. A caller that cannot open a connection to a node
// therefore reports it here. The node is marked unhealthy in this process's
// pool at once and rechecked right away and a few times more. A node that
// answers a recheck or the next sweep is healthy again, so one transient
// connect error costs a node a moment of selection, not a sweep interval.
const (
	unreachableRecheckAttempts = 3
	unreachableRecheckInterval = 5 * time.Second
	// unreachableRecheckWindow is how long this process's own recheck stays
	// the freshest evidence about a node it marked.
	unreachableRecheckWindow = unreachableRecheckAttempts * unreachableRecheckInterval
)

// dialOp is the net.OpError operation for opening a connection.
const dialOp = "dial"

// IsConnectFailure reports whether err means a connection to a node could not
// be opened: refused, timed out, no route, or a name that does not resolve. An
// error after the connection opened, an HTTP error status, and the caller
// giving up are not connect failures. Neither is running out of local sockets
// or file descriptors, which says nothing about the node.
func IsConnectFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	for _, local := range []error{syscall.EMFILE, syscall.ENFILE, syscall.EADDRNOTAVAIL, syscall.ENOBUFS} {
		if errors.Is(err, local) {
			return false
		}
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == dialOp
}

// unreachableMarker is implemented by *Planner.
type unreachableMarker interface {
	MarkTranscodeNodeUnreachable(nodeURL string) bool
}

// ReportConnectFailure marks the transcode node at nodeURL unreachable when err
// is a connect failure and planner can mark nodes. Reports whether a healthy
// node was marked.
//
// A name that fails to resolve is left to the sweep: the lookup runs against
// this server's resolver, and a resolver having a bad moment would otherwise
// mark every node it is asked about, on every API server.
func ReportConnectFailure(planner SessionPlanner, nodeURL string, err error) bool {
	marker, ok := planner.(unreachableMarker)
	if !ok || !IsConnectFailure(err) {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return false
	}
	return marker.MarkTranscodeNodeUnreachable(nodeURL)
}

// MarkTranscodeNodeUnreachable stops selecting the transcode node at nodeURL
// until it answers a health check again. Reports whether a healthy node was
// marked.
func (p *Planner) MarkTranscodeNodeUnreachable(nodeURL string) bool {
	if p == nil || p.transcodes == nil || nodeURL == "" {
		return false
	}
	if handler := p.unreachable.Load(); handler != nil {
		return (*handler)(nodeURL)
	}
	return p.transcodes.MarkUnreachable(nodeURL)
}

// SetUnreachableHandler routes MarkTranscodeNodeUnreachable to fn, normally
// HealthChecker.ReportUnreachable.
func (p *Planner) SetUnreachableHandler(fn func(nodeURL string) bool) {
	if p == nil || fn == nil {
		return
	}
	p.unreachable.Store(&fn)
}

// MarkUnreachable publishes the node at url as unhealthy. The next health
// result for the node replaces the mark. Reports whether a healthy node was
// marked.
func (p *TranscodePool) MarkUnreachable(url string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, n := range p.nodes {
		if n == nil || !sameNodeURL(n.URL, url) {
			continue
		}
		if !n.Healthy {
			return false
		}
		clone := *n
		clone.Healthy = false
		p.nodes[i] = &clone
		return true
	}
	return false
}

// SetUnreachableBroadcast wires how a node this process could not connect to is
// announced to the other API servers, whose own reports arrive through
// ApplyPeerUnreachable.
func (hc *HealthChecker) SetUnreachableBroadcast(fn func(nodeURL string)) {
	if hc == nil {
		return
	}
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.broadcastUnreachable = fn
}

// ReportUnreachable marks a transcode node this process could not connect to,
// rechecks it, and announces it to the other API servers. Reports whether a
// healthy node was marked.
func (hc *HealthChecker) ReportUnreachable(nodeURL string) bool {
	return hc.markUnreachable(nodeURL, true)
}

// ApplyPeerUnreachable marks a transcode node another API server could not
// connect to. This process rechecks it on its own, so a node that answers here
// is back in selection within one health request.
//
// The event bus also delivers this server's own announcements back to it. A
// report about a node this server marked itself within the recheck window is
// ignored: its own recheck is the fresher evidence, and taking the echo would
// pull a node the recheck just restored back out of selection.
func (hc *HealthChecker) ApplyPeerUnreachable(nodeURL string) {
	if hc == nil || nodeURL == "" {
		return
	}
	if stored, ok := hc.localMarks.Load(normalizeNodeURL(nodeURL)); ok {
		if markedAt, ok := stored.(time.Time); ok && time.Since(markedAt) < unreachableRecheckWindow {
			return
		}
	}
	hc.markUnreachable(nodeURL, false)
}

func (hc *HealthChecker) markUnreachable(nodeURL string, local bool) bool {
	if hc == nil || hc.transcodePool == nil || nodeURL == "" {
		return false
	}
	nodeURL = normalizeNodeURL(nodeURL)
	if !hc.transcodePool.MarkUnreachable(nodeURL) {
		return false
	}
	if local {
		hc.localMarks.Store(nodeURL, time.Now())
	}
	hc.mu.RLock()
	ctx, broadcast := hc.ctx, hc.broadcastUnreachable
	hc.mu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}
	source := "peer"
	if local {
		source = "connect_failure"
	}
	slog.WarnContext(ctx, "stream node marked unhealthy until it answers a health check", "component", "nodepool",
		"url", nodeURL, "source", source)
	hc.recheckUnreachable(ctx, nodeURL)
	if local && broadcast != nil {
		broadcast(nodeURL)
	}
	return true
}

// recheckUnreachable probes a node marked unreachable until it answers, up to
// unreachableRecheckAttempts times. It stops early once something else has
// published the node healthy again or the node has left the pool; after the
// last attempt the regular sweep takes over.
func (hc *HealthChecker) recheckUnreachable(ctx context.Context, nodeURL string) {
	if _, running := hc.recheckInFlight.LoadOrStore(nodeURL, struct{}{}); running {
		return
	}
	hc.rechecks.Go(func() {
		// The slot is released before the node is published healthy, so a
		// report that lands right after starts a recheck of its own. Released
		// once only: by then a newer recheck may hold the slot.
		released := false
		release := func() {
			if !released {
				released = true
				hc.recheckInFlight.Delete(nodeURL)
			}
		}
		defer release()
		for attempt := range unreachableRecheckAttempts {
			if attempt > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(hc.recheckInterval):
				}
			}
			n := hc.transcodePool.FindByURL(nodeURL)
			if n == nil || n.Healthy {
				return
			}
			healthy, activeJobs, egressKbps, capabilitiesHash, lastStats, networkAccess := CheckNode(ctx, n)
			if !healthy {
				continue
			}
			release()
			hc.transcodePool.ApplyHealth(n.ID, n.URL, true, activeJobs, egressKbps, capabilitiesHash, lastStats, networkAccess, time.Now())
			slog.InfoContext(ctx, "stream node answered a recheck after a connection failure", "component", "nodepool",
				"id", n.ID, "name", n.Name, "url", n.URL)
			// A node back from a crash may come back with different hardware,
			// which the sweep would otherwise only notice on its next pass.
			if capabilitiesHash != "" && capabilitiesHash != storedCapabilitiesHash(n) {
				hc.startCapabilityRefresh(ctx, n, hc.transcodePool.ApplyCapabilities)
			}
			return
		}
	})
}
