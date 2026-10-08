package downloads

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"golang.org/x/time/rate"
)

// BandwidthManager enforces server-wide and per-user bandwidth limits
// using token bucket rate limiters.
type BandwidthManager struct {
	mu            sync.RWMutex
	serverLimiter *rate.Limiter // nil when no server cap
	userLimiters  sync.Map      // map[int]*rate.Limiter
	shared        *redis.Client
	rates         func(context.Context) (int64, int64, error)
	serverBPS     int64
	userBPS       int64
}

// NewBandwidthManager creates a bandwidth manager with the given limits.
// A value of 0 for either limit means unlimited.
func NewBandwidthManager(serverBPS, userBPS int64) *BandwidthManager {
	bm := &BandwidthManager{
		serverBPS: serverBPS,
		userBPS:   userBPS,
	}
	if serverBPS > 0 {
		burst := max(int(serverBPS/4), 32*1024) // 250ms burst window, min 32KB
		bm.serverLimiter = rate.NewLimiter(rate.Limit(serverBPS), burst)
	}
	return bm
}

// SetSharedBudget uses cluster-wide byte buckets and authoritative rates read
// at every chunk. Production callers always set this, including without Redis:
// a capped transfer then fails closed rather than getting a node-local budget.
func (bm *BandwidthManager) SetSharedBudget(client *redis.Client, rates func(context.Context) (int64, int64, error)) {
	bm.rates = rates
	if client != nil {
		bm.shared = client.WithTimeout(time.Second)
	}
}

// Reload updates the standalone manager without replacing its buckets. Active
// readers acquire the current limits on every read, even when initially unlimited.
func (bm *BandwidthManager) Reload(serverBPS, userBPS int64) {
	if bm == nil {
		return
	}
	bm.mu.Lock()
	defer bm.mu.Unlock()
	bm.serverBPS, bm.userBPS = serverBPS, userBPS
	if serverBPS > 0 {
		if bm.serverLimiter == nil {
			bm.serverLimiter = rate.NewLimiter(rate.Limit(serverBPS), bandwidthBurst(serverBPS))
		} else {
			bm.serverLimiter.SetLimit(rate.Limit(serverBPS))
			bm.serverLimiter.SetBurst(bandwidthBurst(serverBPS))
		}
	} else {
		bm.serverLimiter = nil
	}
	bm.userLimiters.Range(func(_, value any) bool {
		limiter, _ := value.(*rate.Limiter)
		if userBPS > 0 {
			limiter.SetLimit(rate.Limit(userBPS))
			limiter.SetBurst(bandwidthBurst(userBPS))
		}
		return true
	})
}

func bandwidthBurst(bps int64) int { return int(max(bps/4, 32*1024)) }

func (bm *BandwidthManager) getUserLimiter(userID int) *rate.Limiter {
	bm.mu.RLock()
	ubps := bm.userBPS
	bm.mu.RUnlock()
	if ubps <= 0 {
		return nil
	}
	if v, ok := bm.userLimiters.Load(userID); ok {
		lim, _ := v.(*rate.Limiter)
		return lim
	}
	burst := max(int(ubps/4), 32*1024)
	limiter := rate.NewLimiter(rate.Limit(ubps), burst)
	actual, _ := bm.userLimiters.LoadOrStore(userID, limiter)
	lim, _ := actual.(*rate.Limiter)
	return lim
}

// ThrottledReader wraps an io.ReadSeeker with bandwidth limiting for the given user.
// If no limits are configured, the original reader is returned as-is.
func (bm *BandwidthManager) ThrottledReader(ctx context.Context, r io.ReadSeeker, userID int) io.ReadSeeker {
	limited := bm.ThrottledStreamReader(ctx, r, userID)
	if limited == r {
		return r
	}
	return &throttledReadSeeker{Reader: limited, seeker: r}
}

// ThrottledStreamReader applies the same aggregate limits to a non-seekable
// stream, such as an authenticated response relayed from a transcode node.
func (bm *BandwidthManager) ThrottledStreamReader(ctx context.Context, r io.Reader, userID int) io.Reader {
	if bm == nil {
		return r
	}
	return &throttledReader{r: r, ctx: ctx, manager: bm, userID: userID}
}

type throttledReadSeeker struct {
	io.Reader
	seeker io.Seeker
}

func (r *throttledReadSeeker) Seek(offset int64, whence int) (int64, error) {
	return r.seeker.Seek(offset, whence)
}

// throttledReader applies bandwidth limiting to forward reads.
type throttledReader struct {
	r         io.Reader
	ctx       context.Context
	manager   *BandwidthManager
	userID    int
	bytesRead int64
}

func (t *throttledReader) Read(p []byte) (int, error) {
	// Bound exposure to one charged chunk, regardless of io.Copy's buffer size.
	if len(p) > 32*1024 {
		p = p[:32*1024]
	}
	if err := t.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := t.r.Read(p)
	if n > 0 {
		if chargeErr := t.manager.wait(t.ctx, t.userID, n); chargeErr != nil {
			// io.Copy writes n even with an error: never expose uncharged bytes.
			return 0, chargeErr
		}
		t.bytesRead += int64(n)
	}
	return n, err
}

func (bm *BandwidthManager) wait(ctx context.Context, userID, bytes int) error {
	if bm.rates == nil {
		bm.mu.RLock()
		serverLim := bm.serverLimiter
		bm.mu.RUnlock()
		userLim := bm.getUserLimiter(userID)
		for _, limiter := range []*rate.Limiter{serverLim, userLim} {
			if limiter != nil {
				if err := limiter.WaitN(ctx, bytes); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for {
		chunkCtx, cancel := context.WithTimeout(ctx, time.Second)
		server, user, err := bm.rates(chunkCtx)
		var delay int64
		if err == nil && (server > 0 || user > 0) {
			if bm.shared == nil {
				err = fmt.Errorf("shared download bandwidth requires Redis")
			} else {
				delay, err = sharedBandwidthScript.Run(chunkCtx, bm.shared,
					[]string{"silo:downloads:{bandwidth}:global", fmt.Sprintf("silo:downloads:{bandwidth}:user:%d", userID)},
					server, user, bytes).Int64()
			}
		}
		cancel()
		if err != nil {
			return fmt.Errorf("download bandwidth budget unavailable: %w", err)
		}
		if delay <= 0 {
			return nil
		}
		timer := time.NewTimer(min(time.Duration(delay)*time.Microsecond, 250*time.Millisecond))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Both buckets are debited atomically. Redis time prevents node clock skew;
// stored balances/rates survive restarts and rate edits do not refill budgets.
var sharedBandwidthScript = redis.NewScript(`
local clock = redis.call('TIME')
local now = tonumber(clock[1]) + tonumber(clock[2]) / 1000000
local n = tonumber(ARGV[3])
local buckets = {}
local delay = 0
for i = 1,2 do
 local rate = tonumber(ARGV[i])
 local burst = math.max(rate / 4, 32768)
 local state = redis.call('HMGET', KEYS[i], 'tokens', 'time', 'rate')
 local tokens = burst
 if state[1] then
  tokens = math.min(burst, tonumber(state[1]) + math.max(0, now - tonumber(state[2])) * tonumber(state[3]))
 end
 if rate > 0 then
  delay = math.max(delay, math.max(0, n - tokens) / rate)
 end
 buckets[i] = {tokens, rate}
end
for i = 1,2 do
 local tokens, rate = buckets[i][1], buckets[i][2]
 if rate > 0 then
  if delay == 0 then tokens = tokens - n end
  redis.call('HSET', KEYS[i], 'tokens', tokens, 'time', now, 'rate', rate)
  redis.call('EXPIRE', KEYS[i], 60)
 else
  redis.call('DEL', KEYS[i])
 end
end
return math.ceil(delay * 1000000)
`)

func (t *throttledReader) BytesRead() int64 { return t.bytesRead }
