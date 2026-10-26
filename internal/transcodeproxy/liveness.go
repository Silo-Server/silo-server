package transcodeproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/telemetry"
)

// A node that hangs (its process stopped or deadlocked while its kernel still
// accepts connections and acknowledges requests) never refuses a call, never
// fails one at the TCP layer, and never answers. The connect bound and
// TCP_USER_TIMEOUT in nodepool catch a node that is gone; neither catches this.
// A response deadline cannot either: a live node may hold a manifest or
// segment request while it rebuilds a transcode, or a start until FFmpeg is
// ready, for over a minute (see nodeClient).
//
// DoNode tells the two apart by asking the node. Once a call has waited
// livenessGrace for response headers, the node's health endpoint is probed,
// and probed again every livenessInterval while the call keeps waiting. A live
// node answers health at once however slow the call it is serving, and the call
// keeps waiting. A probe that gets no answer within livenessProbeTimeout is
// retried at once on a fresh connection, so one lost packet cannot condemn a
// live node; a node that misses two probes without answering one in between is
// hung: the call is canceled and fails with nodepool.ErrNodeNotAnswering. A
// probe this host could not send (a name that does not resolve or a lookup that
// stalls, no local sockets left) says nothing about the node and counts as
// neither: it neither adds a miss nor clears one.
//
// A node found hung is remembered until it answers a probe or a call again.
// Calls to it probe at once instead of waiting out the grace, and one missed
// probe is enough, so every other session on the node finds out within one
// probe timeout.
const (
	livenessGrace        = 2 * time.Second
	livenessProbeTimeout = 2 * time.Second
	livenessInterval     = 3 * time.Second
)

type livenessTiming struct {
	grace, probeTimeout, interval time.Duration
}

var liveness atomic.Pointer[livenessTiming]

func currentLiveness() livenessTiming {
	if timing := liveness.Load(); timing != nil {
		return *timing
	}
	return livenessTiming{grace: livenessGrace, probeTimeout: livenessProbeTimeout, interval: livenessInterval}
}

// DoNode sends req to the transcode node at nodeURL through
// telemetry.DoTrustedNode and watches the node while the call waits for
// response headers. It returns an error wrapping nodepool.ErrNodeNotAnswering
// when the node stopped answering, which nodepool.IsConnectFailure counts as a
// failure to reach the node. The watch ends with the headers: a body that stops
// streaming is not detected here.
func DoNode(client *http.Client, req *http.Request, nodeURL, operation string) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(req.Context())
	const (
		waiting int32 = iota
		answered
		hung
	)
	var state atomic.Int32
	done := make(chan struct{})
	go watchNode(ctx, nodeURL, currentLiveness(), done, func() {
		// Only a call still waiting is condemned and remembers the node: once
		// the node answered, the answer stands and the miss is stale.
		if state.CompareAndSwap(waiting, hung) {
			notAnswering.Store(nodepool.NormalizeNodeURL(nodeURL), struct{}{})
			cancel(nodepool.ErrNodeNotAnswering)
		}
	})
	resp, err := telemetry.DoTrustedNode(client, req.WithContext(ctx), operation)
	close(done)
	if !state.CompareAndSwap(waiting, answered) {
		// The watch found the node hung before an answer arrived.
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil, fmt.Errorf("%w: no answer to %s or to a health probe", nodepool.ErrNodeNotAnswering, operation)
	}
	if err != nil {
		cancel(nil)
		return nil, err
	}
	// An answer is as good as a probe's.
	notAnswering.Delete(nodepool.NormalizeNodeURL(nodeURL))
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: func() { cancel(nil) }}
	return resp, nil
}

// cancelOnClose releases the call's context once its body is closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel func()
}

func (b *cancelOnClose) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// watchNode probes nodeURL while a call to it waits, and calls hung once the
// node misses the probes that prove it hung. hung marks the node as not
// answering, but only if the call had not been answered meanwhile. watchNode
// returns when the call is answered or given up.
func watchNode(ctx context.Context, nodeURL string, timing livenessTiming, done <-chan struct{}, hung func()) {
	key := nodepool.NormalizeNodeURL(nodeURL)
	wait := timing.grace
	misses, hungAfter := 0, 2
	if _, known := notAnswering.Load(key); known {
		wait, hungAfter = 0, 1
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case verdict := <-probeNode(key, timing.probeTimeout):
			switch verdict {
			case probeUnanswered:
				misses++
				if misses >= hungAfter {
					hung()
					return
				}
				// Retry at once: the miss may have been one lost packet.
				timer.Reset(0)
				continue
			case probeAnswered:
				// The node is serving again: it has to miss twice in a row anew.
				misses, hungAfter = 0, 2
			}
		}
		timer.Reset(timing.interval)
	}
}

type probeVerdict int

const (
	probeAnswered probeVerdict = iota
	probeUnanswered
	// probeInconclusive is a probe this host could not send.
	probeInconclusive
)

var (
	// probes shares one health probe among every call waiting on a node.
	probes singleflight.Group
	// notAnswering holds the nodes last found hung.
	notAnswering sync.Map
)

// probeNode reports, on the returned channel, whether the node answered a
// health request within timeout. The probe runs detached from any one caller,
// so a caller that gives up cannot fail it for the others sharing it.
func probeNode(key string, timeout time.Duration) <-chan probeVerdict {
	result := make(chan probeVerdict, 1)
	shared := probes.DoChan(key, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		verdict := probeHealth(ctx, key)
		if verdict == probeAnswered {
			notAnswering.Delete(key)
		}
		return verdict, nil
	})
	go func() {
		res := <-shared
		verdict, _ := res.Val.(probeVerdict)
		result <- verdict
	}()
	return result
}

// probeClient keeps health probes off the relay connection pool, whose
// connections to a hung node are all stuck.
var probeClient = &http.Client{
	Transport: newProbeTransport(),
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func newProbeTransport() *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	t := base.Clone()
	t.DialContext = nodepool.NodeDialContext()
	t.MaxIdleConnsPerHost = 2
	return t
}

// probeHealth asks the node's unauthenticated health endpoint whether the
// process is serving. Any HTTP response counts as an answer: the question is
// whether it serves requests, not whether it calls itself healthy. A failure
// that happened on this host (see nodepool.IsConnectFailure) is inconclusive,
// and so is a probe whose name lookup was still running at the deadline: the
// transport then reports only the deadline, not a *net.DNSError.
func probeHealth(ctx context.Context, nodeURL string) probeVerdict {
	var resolving atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { resolving.Store(true) },
		DNSDone:  func(httptrace.DNSDoneInfo) { resolving.Store(false) },
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nodepool.NodeEndpoint(nodeURL, "/api/v1/health"), nil)
	if err != nil {
		return probeInconclusive
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) || nodepool.IsLocalSocketFailure(err) || resolving.Load() {
			return probeInconclusive
		}
		return probeUnanswered
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	return probeAnswered
}
