package nodecontrol

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Per-node request budget for the node's background calls (config, report,
// logs, health-changed and revoke-recipes) on one API replica. A healthy node
// sends about one and a half a second (a report every second, a log batch
// every two, a configuration poll every thirty seconds, a rare health change
// or revoke), plus retries and split log batches, so the budget sits well
// above that and only a node that has gone wrong, or a stolen key, meets it.
const (
	nodeWriteRate  rate.Limit = 20
	nodeWriteBurst int        = 60
)

// Per-node request budget for the calls on a viewer's path (resolve and
// sources/open) on one API replica, separate from the background budget so a
// burst of either never refuses the other.
//
// Every progressive remux GET or HEAD resolves once, every job start opens
// its source once, and a lost session's rebuild does both. A node at 64
// concurrent jobs, all progressive remux, whose players each send two range
// requests a second, resolves 128 times a second; source renewals add one
// call per source every 20 minutes. The worst burst is a restarted node whose
// 64 sessions all rebuild at once while their players seek: a resolve, an
// open and about four range requests each, 384 calls. The rate and burst sit
// above both, so a busy node never meets them, while a loop on a stolen key is
// held to a few hundred catalog reads a second instead of whatever the
// Postgres pool can be made to serve.
const (
	nodePlaybackRate  rate.Limit = 200
	nodePlaybackBurst int        = 600
)

// Per-node byte budget for log batches on one API replica. The request budget
// alone would let a node send its 1 MiB body bound twenty times a second into
// operational_logs. A node's log lines, FFmpeg output included, come to a few
// kilobytes a second per session, so the rate sits far above a busy node, and
// the burst takes the backlog a node ships after the core was unreachable.
// Only a node that has gone wrong, or a stolen key, meets it.
const (
	nodeLogBytesRate  rate.Limit = 128 << 10
	nodeLogBytesBurst int        = 4 << 20
)

// nodeLimiter holds one token bucket per authenticated node. Its keys are
// verified node ids, so it grows only with the number of nodes.
type nodeLimiter struct {
	limit rate.Limit
	burst int
	now   func() time.Time

	mu      sync.Mutex
	buckets map[int]*rate.Limiter
}

func newNodeLimiter(limit rate.Limit, burst int) *nodeLimiter {
	return &nodeLimiter{limit: limit, burst: burst, now: time.Now, buckets: make(map[int]*rate.Limiter)}
}

func (l *nodeLimiter) allow(nodeID int) bool { return l.allowN(nodeID, 1) }

// allowN takes n tokens from nodeID's bucket, or none when it holds fewer.
func (l *nodeLimiter) allowN(nodeID, n int) bool {
	l.mu.Lock()
	bucket, ok := l.buckets[nodeID]
	if !ok {
		bucket = rate.NewLimiter(l.limit, l.burst)
		l.buckets[nodeID] = bucket
	}
	l.mu.Unlock()
	return bucket.AllowN(l.now(), n)
}

// LimitNode answers 429 to a node that spends its background budget on this
// replica. It runs after RequireNode, which supplies the node's id; a request
// without one is refused as RequireNode would.
func (s *Server) LimitNode(next http.Handler) http.Handler {
	return limitWith(s.limiter, next)
}

// LimitNodePlayback answers 429 to a node that spends its viewer-path budget
// (resolve, sources/open) on this replica. The node treats that 429 as any
// other answer it can retry: a 503 to the relay, a retried renewal.
func (s *Server) LimitNodePlayback(next http.Handler) http.Handler {
	return limitWith(s.playbackLimiter, next)
}

func limitWith(limiter *nodeLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := NodeID(r.Context())
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !limiter.allow(id) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
