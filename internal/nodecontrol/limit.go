package nodecontrol

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Per-node request budget for the node's writes (report, logs and
// health-changed) on one API replica. A healthy node sends about one and a
// half a second (a report every second, a log batch every two, a rare health
// change), plus retries and split log batches, so the budget sits well above
// that and only a node that has gone wrong, or a stolen key, meets it.
const (
	nodeWriteRate  rate.Limit = 20
	nodeWriteBurst int        = 60
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

func (l *nodeLimiter) allow(nodeID int) bool {
	l.mu.Lock()
	bucket, ok := l.buckets[nodeID]
	if !ok {
		bucket = rate.NewLimiter(l.limit, l.burst)
		l.buckets[nodeID] = bucket
	}
	l.mu.Unlock()
	return bucket.AllowN(l.now(), 1)
}

// LimitNode answers 429 to a node that spends its write budget on this
// replica. It runs after RequireNode, which supplies the node's id; a request
// without one is refused as RequireNode would.
func (s *Server) LimitNode(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := NodeID(r.Context())
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !s.limiter.allow(id) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
