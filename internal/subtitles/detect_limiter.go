package subtitles

import "sync"

const (
	// detectionMaxInFlight bounds the language detections one account runs at
	// once on this server. A client detects one file at a time; two leaves
	// room for a second file picked while the first is still uploading.
	detectionMaxInFlight = 2
	// detectionNodeMaxInFlight bounds detections on this server across all
	// accounts. Each one buffers and parses an upload of up to MaxUploadSize,
	// and the request rate limiter counts requests, not that work.
	detectionNodeMaxInFlight = 4
)

// DetectionLimiter bounds the subtitle language detections running at once on
// this server, overall and per account. It is per node on purpose: the memory
// and CPU a detection takes are per node too.
type DetectionLimiter struct {
	mu       sync.Mutex
	inFlight int
	accounts map[int]int
}

// NewDetectionLimiter returns a limiter with no detection running.
func NewDetectionLimiter() *DetectionLimiter {
	return &DetectionLimiter{accounts: map[int]int{}}
}

// Acquire takes a detection slot for userID without waiting. It reports false
// when the account or the server already runs its limit; otherwise release
// gives the slot back and is safe to call more than once.
func (l *DetectionLimiter) Acquire(userID int) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight >= detectionNodeMaxInFlight || l.accounts[userID] >= detectionMaxInFlight {
		return nil, false
	}
	l.inFlight++
	l.accounts[userID]++
	return sync.OnceFunc(func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.inFlight--
		if l.accounts[userID]--; l.accounts[userID] == 0 {
			delete(l.accounts, userID)
		}
	}), true
}
