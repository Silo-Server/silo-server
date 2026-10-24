package bandwidthprobe

import (
	"sync"
	"time"
)

const (
	// bandwidthProbeMaxInFlight bounds the probes one account can run at once
	// on this server. A client needs one at a time; the request rate limiter
	// counts requests, not the bytes each one streams.
	bandwidthProbeMaxInFlight = 2
	// bandwidthProbeWindowBytes bounds the probe bytes one account may draw
	// from this server per bandwidthProbeWindow. It admits one accurate test
	// (MaxSize) beside the quick tests that preceded it, and refuses a run of
	// accurate tests back to back.
	bandwidthProbeWindowBytes = 2_000_000_000
	bandwidthProbeWindow      = 10 * time.Minute
	// bandwidthProbeNodeMaxInFlight bounds probes on this server across all
	// accounts. Each probe saturates the server's upload while it runs, so a
	// few at once is enough; a refused client tries again at its next check.
	bandwidthProbeNodeMaxInFlight = 4
	bandwidthProbeNodeRetryAfter  = 30 * time.Second
)

// Limiter bounds probes on this server: how many run at once
// overall and per account, and how many bytes each account's probes send per
// window. It is per node on purpose; what it protects (sockets, goroutines,
// the random-byte work) is per node too.
type Limiter struct {
	mu       sync.Mutex
	accounts map[int]*bandwidthProbeAccount
	// inFlight counts probes running on this server across all accounts.
	inFlight int
}

type bandwidthProbeAccount struct {
	inFlight    int
	windowStart time.Time
	// windowBytes is what the window has granted: sizes reserved by running
	// probes plus bytes finished probes actually sent.
	windowBytes int
}

// Acquire reserves a probe slot and size bytes of the account's window. It
// returns the window the reservation belongs to, for release, or how long to
// wait before retrying when the account is over either limit.
func (l *Limiter) Acquire(userID, size int, now time.Time) (bool, time.Time, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Forget accounts with nothing running whose window has passed. Probes are
	// rare (one per client about every 30 minutes), so a sweep per admission
	// is cheap and keeps the map to the accounts probing now.
	for id, idle := range l.accounts {
		if idle.inFlight <= 0 && now.Sub(idle.windowStart) >= bandwidthProbeWindow {
			delete(l.accounts, id)
		}
	}
	account := l.accounts[userID]
	if account == nil {
		account = &bandwidthProbeAccount{windowStart: now}
		l.accounts[userID] = account
	}
	if now.Sub(account.windowStart) >= bandwidthProbeWindow {
		account.windowStart, account.windowBytes = now, 0
	}
	if l.inFlight >= bandwidthProbeNodeMaxInFlight {
		return false, time.Time{}, bandwidthProbeNodeRetryAfter
	}
	if account.inFlight >= bandwidthProbeMaxInFlight {
		return false, time.Time{}, 5 * time.Second
	}
	if account.windowBytes+size > bandwidthProbeWindowBytes {
		return false, time.Time{}, account.windowStart.Add(bandwidthProbeWindow).Sub(now)
	}
	l.inFlight++
	account.inFlight++
	account.windowBytes += size
	return true, account.windowStart, 0
}

// Release frees the slot and refunds the bytes a probe reserved but did not
// send, as when the client closed the connection once it had timed enough.
// The refund only goes back to the window that granted it; a later window
// never held the reservation.
func (l *Limiter) Release(userID, unsent int, granted, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inFlight--
	account := l.accounts[userID]
	account.inFlight--
	if account.windowStart.Equal(granted) {
		account.windowBytes = max(0, account.windowBytes-unsent)
	}
	if account.inFlight <= 0 && now.Sub(account.windowStart) >= bandwidthProbeWindow {
		delete(l.accounts, userID)
	}
}

// NewLimiter returns an empty Limiter. The API and each proxy keep their own,
// because the sockets and egress it protects belong to one process.
func NewLimiter() *Limiter { return &Limiter{accounts: map[int]*bandwidthProbeAccount{}} }
