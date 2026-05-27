package abs

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// loginLimitBurst caps the number of body-creds /login attempts a single
// source IP can make in quick succession. The token bucket refills at
// loginLimitPerToken (10/min ≈ one every 6s), so a bursty client can spend
// the burst and then must wait. Tuned to be invisible to legitimate listeners
// (who attempt login once and succeed) while making credential-stuffing
// expensive.
const (
	loginLimitBurst    = 10
	loginLimitPerToken = 6 * time.Second
	loginLimitIdle     = 10 * time.Minute
	loginLimitGCEvery  = 5 * time.Minute
)

type loginLimiterEntry struct {
	limiter *rate.Limiter
	last    time.Time
}

// LoginLimiter is a process-local per-IP rate limiter for the standalone-port
// body-creds login path. The header-authenticated path is never gated here —
// that traffic comes from the trusted silo host proxy.
//
// Construct one per process (in server wiring) and inject via
// Dependencies.LoginLimiter. Constructing one per Handler would leak a
// janitor goroutine on every reconfigure.
type LoginLimiter struct {
	mu      sync.Mutex
	buckets map[string]*loginLimiterEntry
	stopCh  chan struct{}
}

// NewLoginLimiter builds a limiter and starts its background janitor. The
// janitor exits when Stop() is called.
func NewLoginLimiter() *LoginLimiter {
	l := &LoginLimiter{
		buckets: make(map[string]*loginLimiterEntry),
		stopCh:  make(chan struct{}),
	}
	go l.janitor()
	return l
}

// Stop terminates the janitor goroutine. Safe to call once.
func (l *LoginLimiter) Stop() { close(l.stopCh) }

func (l *LoginLimiter) allow(key string) bool {
	if key == "" {
		return true
	}
	l.mu.Lock()
	e, ok := l.buckets[key]
	if !ok {
		e = &loginLimiterEntry{
			limiter: rate.NewLimiter(rate.Every(loginLimitPerToken), loginLimitBurst),
		}
		l.buckets[key] = e
	}
	e.last = time.Now()
	lim := e.limiter
	l.mu.Unlock()
	return lim.Allow()
}

func (l *LoginLimiter) janitor() {
	ticker := time.NewTicker(loginLimitGCEvery)
	defer ticker.Stop()
	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-loginLimitIdle)
			l.mu.Lock()
			for k, e := range l.buckets {
				if e.last.Before(cutoff) {
					delete(l.buckets, k)
				}
			}
			l.mu.Unlock()
		}
	}
}

// clientIP returns the rate-limit key for a request. The standalone listener
// is public, so spoofable forwarding headers are deliberately ignored.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
