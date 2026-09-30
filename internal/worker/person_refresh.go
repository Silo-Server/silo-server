package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type PersonRefresher interface {
	RefreshPerson(ctx context.Context, id int64) (*models.Person, error)
	// ClaimCandidates claims up to limit people due for a background lookup,
	// so API nodes sweeping at once never get the same person.
	ClaimCandidates(ctx context.Context, limit int) ([]int64, error)
}

// Background lookups pause when a provider rate limits: first for
// personRefreshRateLimitBackoff, doubling on each further limit up to
// personRefreshMaxRateLimitBackoff, or for as long as the provider asked if
// that's longer. A lookup that isn't rate limited resets the backoff.
const (
	personRefreshRateLimitBackoff    = time.Minute
	personRefreshMaxRateLimitBackoff = 15 * time.Minute
)

// rateLimitedLookup is implemented by the refresher's error for a lookup a
// provider rate limited.
type rateLimitedLookup interface {
	RateLimitRetryAfter() time.Duration
}

type PersonRefreshWorkerConfig struct {
	// Interval is how often an idle worker looks for due people. While a
	// sweep keeps claiming full batches, the next batch starts right away.
	Interval time.Duration
	// Delay is the pause between background lookups until SetRatePerMinute
	// replaces it.
	Delay          time.Duration
	BatchSize      int
	RefreshTimeout time.Duration
	// ClaimLease is how long a claim holds a person before another node may
	// claim them (catalog.PersonRefreshAttemptLease). A batch stops starting
	// lookups once less than RefreshTimeout is left on it.
	ClaimLease time.Duration
}

func DefaultPersonRefreshWorkerConfig() PersonRefreshWorkerConfig {
	return PersonRefreshWorkerConfig{
		Interval:       10 * time.Minute,
		Delay:          200 * time.Millisecond,
		BatchSize:      100,
		RefreshTimeout: 2 * time.Minute,
		ClaimLease:     time.Hour,
	}
}

type PersonRefreshWorker struct {
	service PersonRefresher
	config  PersonRefreshWorkerConfig

	mu          sync.Mutex
	manualQueue []int64
	queued      map[int64]struct{}
	stop        chan struct{}
	wake        chan struct{}

	// delay is the pause between background lookups, in nanoseconds.
	delay atomic.Int64
	// backoff and pausedUntil hold the rate-limit pause, under mu.
	backoff     time.Duration
	pausedUntil time.Time
	now         func() time.Time
}

func NewPersonRefreshWorker(service PersonRefresher, config PersonRefreshWorkerConfig) *PersonRefreshWorker {
	if config.Interval <= 0 {
		config.Interval = 10 * time.Minute
	}
	if config.Delay < 0 {
		config.Delay = 0
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 100
	}
	if config.RefreshTimeout <= 0 {
		config.RefreshTimeout = 2 * time.Minute
	}
	if config.ClaimLease <= 0 {
		config.ClaimLease = time.Hour
	}

	w := &PersonRefreshWorker{
		service: service,
		config:  config,
		queued:  make(map[int64]struct{}),
		stop:    make(chan struct{}),
		wake:    make(chan struct{}, 1),
		now:     time.Now,
	}
	w.delay.Store(int64(config.Delay))
	return w
}

// SetRatePerMinute caps background lookups at perMinute, the
// metadata.person_refresh_per_minute setting. It applies from the next lookup
// and ignores values below 1.
func (w *PersonRefreshWorker) SetRatePerMinute(perMinute int) {
	if perMinute < 1 {
		return
	}
	w.delay.Store(int64(time.Minute / time.Duration(perMinute)))
}

// Enqueue asks for an on-demand lookup, such as for a person whose page was
// just opened. It runs before the rest of any background batch in progress.
func (w *PersonRefreshWorker) Enqueue(id int64) {
	if id <= 0 {
		return
	}

	w.mu.Lock()
	if _, exists := w.queued[id]; exists {
		w.mu.Unlock()
		return
	}
	w.queued[id] = struct{}{}
	w.manualQueue = append(w.manualQueue, id)
	w.mu.Unlock()

	w.signalWake()
}

func (w *PersonRefreshWorker) signalWake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *PersonRefreshWorker) Start() {
	go func() {
		ticker := time.NewTicker(w.config.Interval)
		defer ticker.Stop()

		for {
			select {
			case <-w.stop:
				return
			case <-w.wake:
				w.drain()
			case <-ticker.C:
				w.drain()
			}
		}
	}()
}

func (w *PersonRefreshWorker) Stop() {
	close(w.stop)
}

// drain runs background batches back to back while each claims a full batch,
// so a backlog is worked through continuously instead of one batch per
// Interval. It returns once a batch comes up short or the worker stops.
func (w *PersonRefreshWorker) drain() {
	for w.processBatch() {
		if w.stopped() {
			return
		}
	}
}

// processBatch runs the on-demand queue, then claims and refreshes one
// background batch. On-demand requests that arrive meanwhile run between its
// lookups. It reports whether the batch was full, meaning more may be due.
// While a provider's rate limit pauses background lookups it claims nothing.
//
// A lookup renews its person's lease when it starts, but claimed people it
// hasn't reached yet are only held by the claim. When slow lookups use up the
// claim's lease, the batch stops before another node could claim the rest;
// they come back once their lease runs out.
func (w *PersonRefreshWorker) processBatch() bool {
	if w.service == nil {
		return false
	}

	done := map[int64]struct{}{}
	w.runManual(done)
	if w.paused() {
		return false
	}
	lastStart := w.now().Add(w.config.ClaimLease - w.config.RefreshTimeout)
	batch, err := w.service.ClaimCandidates(context.Background(), w.config.BatchSize)
	if err != nil {
		slog.Warn("person refresh worker: failed to claim candidates", "error", err)
		return false
	}

	for index, id := range batch {
		w.runManual(done)
		if w.stopped() {
			return false
		}
		if _, ran := done[id]; ran || w.isQueued(id) {
			// Its on-demand lookup already ran, or is pending and will cover it.
			continue
		}
		if w.now().After(lastStart) {
			slog.Warn("person refresh worker: batch outlasted its claim; leaving the rest for later",
				"remaining", len(batch)-index)
			return true
		}
		if err := w.refresh(id); w.rateLimited(err) {
			// The rest of the batch comes back when its claims' lease runs out.
			return false
		}

		if delay := time.Duration(w.delay.Load()); delay > 0 && index < len(batch)-1 {
			select {
			case <-time.After(delay):
			case <-w.stop:
				return false
			}
		}
	}
	return len(batch) >= w.config.BatchSize
}

// rateLimited pauses background lookups when err reports a provider rate
// limit, and otherwise resets the backoff. It reports whether it paused.
func (w *PersonRefreshWorker) rateLimited(err error) bool {
	var limited rateLimitedLookup
	if !errors.As(err, &limited) {
		w.mu.Lock()
		w.backoff = 0
		w.mu.Unlock()
		return false
	}

	w.mu.Lock()
	if w.backoff == 0 {
		w.backoff = personRefreshRateLimitBackoff
	} else {
		w.backoff = min(w.backoff*2, personRefreshMaxRateLimitBackoff)
	}
	wait := max(w.backoff, limited.RateLimitRetryAfter())
	w.pausedUntil = w.now().Add(wait)
	w.mu.Unlock()

	slog.Warn("person refresh worker: provider rate limited; pausing background lookups", "pause", wait)
	time.AfterFunc(wait, w.signalWake)
	return true
}

func (w *PersonRefreshWorker) paused() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.now().Before(w.pausedUntil)
}

// runManual refreshes every on-demand request queued so far, recording each
// in done.
func (w *PersonRefreshWorker) runManual(done map[int64]struct{}) {
	for {
		w.mu.Lock()
		if len(w.manualQueue) == 0 {
			w.mu.Unlock()
			return
		}
		id := w.manualQueue[0]
		w.manualQueue = w.manualQueue[1:]
		w.mu.Unlock()

		_ = w.refresh(id)
		done[id] = struct{}{}

		w.mu.Lock()
		delete(w.queued, id)
		w.mu.Unlock()
	}
}

func (w *PersonRefreshWorker) refresh(id int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), w.config.RefreshTimeout)
	defer cancel()
	_, err := w.service.RefreshPerson(ctx, id)
	if err != nil {
		slog.Warn("person refresh worker: refresh failed", "person_id", id, "error", err)
	}
	return err
}

func (w *PersonRefreshWorker) isQueued(id int64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.queued[id]
	return ok
}

func (w *PersonRefreshWorker) stopped() bool {
	select {
	case <-w.stop:
		return true
	default:
		return false
	}
}
