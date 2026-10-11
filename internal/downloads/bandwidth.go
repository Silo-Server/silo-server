package downloads

import (
	"context"
	"fmt"
	"io"
	"log/slog"
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
	// ratesKnown and ratesAt track the shared-mode copy of the two settings.
	// ratesMu lets one transfer refresh them while the others keep sending.
	ratesMu    sync.Mutex
	ratesKnown bool
	ratesAt    time.Time
}

// bandwidthRatesTTL bounds how long an edit takes to reach transfers that are
// already running, without a settings query per chunk.
const bandwidthRatesTTL = 5 * time.Second

// sharedBudgetGrace is how long a transfer retries an unavailable budget before
// it fails closed. It rides out a brief Redis or database hiccup. Tests shorten it.
var (
	sharedBudgetGrace = 10 * time.Second
	sharedBudgetRetry = 250 * time.Millisecond
)

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

// SetSharedBudget uses cluster-wide byte buckets in Redis. Rates come from the
// authoritative settings, refreshed at most every bandwidthRatesTTL and seeded
// from the manager's current limits. Without Redis the manager keeps its
// in-process limiters, which is exact for a single API process.
func (bm *BandwidthManager) SetSharedBudget(client *redis.Client, rates func(context.Context) (int64, int64, error)) {
	if client == nil {
		return
	}
	bm.mu.Lock()
	defer bm.mu.Unlock()
	bm.rates = rates
	bm.shared = client.WithTimeout(time.Second)
	bm.ratesKnown, bm.ratesAt = true, time.Now()
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
	bm.ratesKnown, bm.ratesAt = true, time.Now()
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

const minBandwidthChunk, maxBandwidthChunk = 32 * 1024, 1024 * 1024

// bandwidthChunk sizes one charged read: about 25 ms of the strictest active
// rate, so a fast capped transfer is not bounded by one budget round trip per
// 32 KiB. It never exceeds the strictest bucket's burst.
func bandwidthChunk(serverBPS, userBPS int64) int {
	strictest := int64(0)
	for _, bps := range []int64{serverBPS, userBPS} {
		if bps > 0 && (strictest == 0 || bps < strictest) {
			strictest = bps
		}
	}
	if strictest == 0 {
		return minBandwidthChunk
	}
	return int(min(max(strictest/40, minBandwidthChunk), maxBandwidthChunk))
}

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
// stream, such as an authenticated response relayed from a transcode node. A
// transfer that starts with both limits off gets the raw reader, keeping
// sendfile and avoiding per-chunk work; a later edit applies to new transfers.
func (bm *BandwidthManager) ThrottledStreamReader(ctx context.Context, r io.Reader, userID int) io.Reader {
	if bm == nil {
		return r
	}
	if server, user := bm.limits(ctx); server <= 0 && user <= 0 {
		return r
	}
	return &throttledReader{r: r, ctx: ctx, manager: bm, userID: userID}
}

// limits returns the limits a new transfer starts with. A failed shared-mode
// read reports a limit so the transfer is throttled and fails closed.
func (bm *BandwidthManager) limits(ctx context.Context) (int64, int64) {
	if bm.rates == nil {
		bm.mu.RLock()
		defer bm.mu.RUnlock()
		return bm.serverBPS, bm.userBPS
	}
	server, user, err := bm.currentRates(ctx)
	if err != nil {
		return 1, 1
	}
	return server, user
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
	t.manager.mu.RLock()
	chunk := bandwidthChunk(t.manager.serverBPS, t.manager.userBPS)
	t.manager.mu.RUnlock()
	if len(p) > chunk {
		p = p[:chunk]
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
	var failingSince time.Time
	for bytes > 0 {
		chunkCtx, cancel := context.WithTimeout(ctx, time.Second)
		server, user, err := bm.currentRates(chunkCtx)
		var delay time.Duration
		// A rate lowered mid-read can leave the chunk above the new burst,
		// which a bucket could never pay at once; charge it in pieces.
		charge := min(bytes, bandwidthChunk(server, user))
		if err == nil && (server > 0 || user > 0) {
			var micros int64
			micros, err = sharedBandwidthScript.Run(chunkCtx, bm.shared,
				[]string{"silo:downloads:{bandwidth}:global", fmt.Sprintf("silo:downloads:{bandwidth}:user:%d", userID)},
				server, user, charge).Int64()
			delay = time.Duration(micros) * time.Microsecond
		}
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if failingSince.IsZero() {
				failingSince = time.Now()
			}
			if time.Since(failingSince) >= sharedBudgetGrace {
				return fmt.Errorf("download bandwidth budget unavailable: %w", err)
			}
			delay = sharedBudgetRetry
		} else {
			failingSince = time.Time{}
			if delay <= 0 {
				bytes -= charge
				continue
			}
		}
		timer := time.NewTimer(min(delay, 250*time.Millisecond))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

// currentRates returns the shared-mode rates, reading the settings at most once
// per bandwidthRatesTTL. A failed read keeps the last known rates, so a database
// hiccup neither stops transfers nor lifts their limits. Without known rates it
// returns the error and the caller fails closed.
func (bm *BandwidthManager) currentRates(ctx context.Context) (int64, int64, error) {
	bm.mu.RLock()
	server, user, known, at := bm.serverBPS, bm.userBPS, bm.ratesKnown, bm.ratesAt
	bm.mu.RUnlock()
	if known && time.Since(at) < bandwidthRatesTTL {
		return server, user, nil
	}
	if known && !bm.ratesMu.TryLock() {
		return server, user, nil
	}
	if !known {
		bm.ratesMu.Lock()
	}
	defer bm.ratesMu.Unlock()
	bm.mu.RLock()
	if bm.ratesKnown && time.Since(bm.ratesAt) < bandwidthRatesTTL {
		server, user = bm.serverBPS, bm.userBPS
		bm.mu.RUnlock()
		return server, user, nil
	}
	bm.mu.RUnlock()

	freshServer, freshUser, err := bm.rates(ctx)
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if err != nil {
		if !bm.ratesKnown {
			return 0, 0, err
		}
		// Retry the read in about a second rather than on every chunk.
		bm.ratesAt = time.Now().Add(time.Second - bandwidthRatesTTL)
		slog.WarnContext(ctx, "download bandwidth settings unavailable; keeping last known limits",
			"component", "downloads", "error", err)
		return bm.serverBPS, bm.userBPS, nil
	}
	bm.serverBPS, bm.userBPS = freshServer, freshUser
	bm.ratesKnown, bm.ratesAt = true, time.Now()
	return freshServer, freshUser, nil
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
