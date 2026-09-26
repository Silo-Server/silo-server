package watchsync

import (
	"context"
	"crypto/sha256"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ParseRetryAfter reads an RFC 9110 Retry-After value: delay-seconds or an
// HTTP-date. ok is false when the value is absent or malformed. An HTTP-date
// that has already passed parses as zero, meaning "retry now".
func ParseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds < 0 {
			return 0, false
		}
		if seconds > int64(math.MaxInt64/time.Second) {
			return time.Duration(math.MaxInt64), true
		}
		return time.Duration(seconds) * time.Second, true
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(at.Sub(now), 0), true
	}
	return 0, false
}

// SleepContext waits for d, returning early with ctx.Err() when ctx ends.
func SleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// LimiterWaitError classifies a failed local limiter wait. A canceled context
// is returned as is. Otherwise the limiter refused because the next slot lies
// past the context deadline: the request was never sent, so it is reported as
// a RateLimitedError, which leaves the caller's work pending for a later run
// instead of failing it.
func LimiterWaitError(ctx context.Context, provider string, retryAfter time.Duration, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return RateLimitedError{Provider: provider, RetryAfter: retryAfter}
}

// credentialLimiterIdleTTL is how long a credential's limiter may sit unused
// before it becomes eligible for removal.
const credentialLimiterIdleTTL = 10 * time.Minute

// CredentialLimiter paces requests per provider credential (an access token
// or API key), so one account's burst never delays another account's
// requests. Limiters are keyed by a SHA-256 digest of the credential: the map
// never holds a raw secret, and its keys never appear in logs.
type CredentialLimiter struct {
	every time.Duration
	burst int
	now   func() time.Time

	mu        sync.Mutex
	limiters  map[[sha256.Size]byte]*credentialLimiterEntry
	lastSweep time.Time
}

type credentialLimiterEntry struct {
	limiter  *rate.Limiter
	lastUsed time.Time
}

// NewCredentialLimiter allows each credential burst requests at once and one
// more every interval after that.
func NewCredentialLimiter(every time.Duration, burst int) *CredentialLimiter {
	return &CredentialLimiter{
		every:    every,
		burst:    max(burst, 1),
		now:      time.Now,
		limiters: make(map[[sha256.Size]byte]*credentialLimiterEntry),
	}
}

// Wait blocks until credential may send its next request. It returns an error
// without waiting when ctx is already done or its deadline would pass first.
func (l *CredentialLimiter) Wait(ctx context.Context, credential string) error {
	return l.limiter(credential).Wait(ctx)
}

func (l *CredentialLimiter) limiter(credential string) *rate.Limiter {
	key := sha256.Sum256([]byte(credential))
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) >= credentialLimiterIdleTTL {
		l.lastSweep = now
		l.sweepLocked(now)
	}
	entry, ok := l.limiters[key]
	if !ok {
		entry = &credentialLimiterEntry{limiter: rate.NewLimiter(rate.Every(l.every), l.burst)}
		l.limiters[key] = entry
	}
	entry.lastUsed = now
	return entry.limiter
}

// sweepLocked drops idle limiters so the map stays bounded as access tokens
// rotate. An idle limiter with a full bucket behaves exactly like a new one,
// so removing it cannot let a credential exceed its rate.
func (l *CredentialLimiter) sweepLocked(now time.Time) {
	for key, entry := range l.limiters {
		if now.Sub(entry.lastUsed) >= credentialLimiterIdleTTL &&
			entry.limiter.TokensAt(now) >= float64(l.burst) {
			delete(l.limiters, key)
		}
	}
}
