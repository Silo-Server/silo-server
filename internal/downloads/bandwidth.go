package downloads

import (
	"context"
	"io"
	"sync"

	"golang.org/x/time/rate"
)

// BandwidthManager enforces server-wide and per-user bandwidth limits
// using token bucket rate limiters.
type BandwidthManager struct {
	mu            sync.RWMutex
	serverLimiter *rate.Limiter // nil when no server cap
	userLimiters  sync.Map      // map[int]*rate.Limiter
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

// Reload updates bandwidth limits. Existing in-flight downloads keep their old limiters;
// new downloads will use the updated rates.
func (bm *BandwidthManager) Reload(serverBPS, userBPS int64) {
	if bm == nil {
		return
	}
	bm.mu.Lock()
	defer bm.mu.Unlock()
	bm.serverBPS = serverBPS
	bm.userBPS = userBPS
	if serverBPS > 0 {
		burst := max(int(serverBPS/4), 32*1024)
		bm.serverLimiter = rate.NewLimiter(rate.Limit(serverBPS), burst)
	} else {
		bm.serverLimiter = nil
	}
	// Clear per-user limiters so they're recreated with the new rate.
	// Use Range+Delete instead of replacing the sync.Map struct value,
	// which would race with concurrent Load/LoadOrStore calls.
	bm.userLimiters.Range(func(key, _ any) bool {
		bm.userLimiters.Delete(key)
		return true
	})
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
	if bm == nil {
		return r
	}
	bm.mu.RLock()
	serverLim := bm.serverLimiter
	bm.mu.RUnlock()
	userLim := bm.getUserLimiter(userID)
	if serverLim == nil && userLim == nil {
		return r
	}
	return &throttledReader{
		r:         r,
		ctx:       ctx,
		serverLim: serverLim,
		userLim:   userLim,
	}
}

// throttledReader implements io.ReadSeeker with bandwidth limiting.
// Seek is delegated directly (not throttled), only forward reads are limited.
type throttledReader struct {
	r         io.ReadSeeker
	ctx       context.Context
	serverLim *rate.Limiter
	userLim   *rate.Limiter
	bytesRead int64
}

func (t *throttledReader) Read(p []byte) (int, error) {
	// Read first, then charge tokens for the bytes actually read.
	// This avoids over-charging on short reads (which io.Reader permits).
	n, err := t.r.Read(p)
	if n > 0 {
		t.bytesRead += int64(n)
		// Charge tokens for bytes read. Split into burst-sized chunks
		// since WaitN panics if n > burst.
		charged := 0
		for charged < n {
			chunk := n - charged
			if t.serverLim != nil && chunk > t.serverLim.Burst() {
				chunk = t.serverLim.Burst()
			}
			if t.userLim != nil && chunk > t.userLim.Burst() {
				chunk = t.userLim.Burst()
			}
			if t.serverLim != nil {
				if waitErr := t.serverLim.WaitN(t.ctx, chunk); waitErr != nil {
					return n, waitErr
				}
			}
			if t.userLim != nil {
				if waitErr := t.userLim.WaitN(t.ctx, chunk); waitErr != nil {
					return n, waitErr
				}
			}
			charged += chunk
		}
	}
	return n, err
}

func (t *throttledReader) Seek(offset int64, whence int) (int64, error) {
	return t.r.Seek(offset, whence)
}

// BytesRead returns the total bytes read through this throttled reader.
func (t *throttledReader) BytesRead() int64 {
	return t.bytesRead
}
