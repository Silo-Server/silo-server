package policy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// DecisionLogVerbosityDigest logs only the stable input digest.
	DecisionLogVerbosityDigest = "digest"
	// DecisionLogVerbosityVerbose logs sampled input and result JSON payloads.
	DecisionLogVerbosityVerbose = "verbose"

	// SettingDecisionLogVerbosity controls decision-log payload retention.
	SettingDecisionLogVerbosity = "policy.decision_log_verbosity"
	// SettingDecisionLogScopeSampleRate controls successful scope decision sampling.
	SettingDecisionLogScopeSampleRate = "policy.decision_log_scope_sample_rate"
	// SettingDecisionLogRetentionDays controls partition retention for decisions.
	SettingDecisionLogRetentionDays = "policy.decision_log_retention_days"

	DefaultDecisionLogVerbosity       = DecisionLogVerbosityDigest
	DefaultDecisionLogScopeSampleRate = 50
	DefaultDecisionLogRetentionDays   = 14

	defaultDecisionLogBufferSize    = 1024
	defaultDecisionLogBatchSize     = 128
	defaultDecisionLogFlushInterval = 2 * time.Second
	dropWarnInterval                = time.Minute
)

const (
	decisionLogVerbosityDigestValue int32 = iota
	decisionLogVerbosityVerboseValue
)

type decisionLogDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// Entry is one row in policy_decisions.
type Entry struct {
	ID               int64           `json:"id,omitempty"`
	Timestamp        time.Time       `json:"timestamp,omitempty"`
	DecisionName     DecisionName    `json:"decision_name"`
	PolicyGeneration int64           `json:"policy_generation"`
	UserID           *int            `json:"user_id,omitempty"`
	ProfileID        string          `json:"profile_id,omitempty"`
	SessionID        string          `json:"session_id,omitempty"`
	RequestID        string          `json:"request_id,omitempty"`
	NodeID           string          `json:"node_id,omitempty"`
	Allowed          *bool           `json:"allowed,omitempty"`
	EvalTimeNS       int64           `json:"eval_time_ns"`
	InputDigest      string          `json:"input_digest"`
	InputSample      json.RawMessage `json:"input_sample,omitempty"`
	ResultSample     json.RawMessage `json:"result_sample,omitempty"`
	Error            string          `json:"error,omitempty"`
}

// DecisionLogger asynchronously writes sampled policy decisions to Postgres.
type DecisionLogger struct {
	db            decisionLogDB
	nodeID        string
	ch            chan Entry
	batchSize     int
	flushInterval time.Duration
	logger        *slog.Logger

	cancelMu sync.Mutex
	cancel   context.CancelFunc
	wg       sync.WaitGroup

	stopped         atomic.Bool
	dropped         atomic.Uint64
	lastDropWarnNS  atomic.Int64
	verbosity       atomic.Int32
	scopeSampleRate atomic.Int64
	scopeCounter    atomic.Uint64
}

// DecisionLoggerOption configures a DecisionLogger.
type DecisionLoggerOption func(*decisionLoggerConfig)

type decisionLoggerConfig struct {
	bufferSize    int
	batchSize     int
	flushInterval time.Duration
	logger        *slog.Logger
}

// WithDecisionLogBufferSize configures the in-process queue length.
func WithDecisionLogBufferSize(size int) DecisionLoggerOption {
	return func(cfg *decisionLoggerConfig) {
		if size > 0 {
			cfg.bufferSize = size
		}
	}
}

// WithDecisionLogBatchSize configures the batch insert size.
func WithDecisionLogBatchSize(size int) DecisionLoggerOption {
	return func(cfg *decisionLoggerConfig) {
		if size > 0 {
			cfg.batchSize = size
		}
	}
}

// WithDecisionLogFlushInterval configures the maximum delay before a partial
// batch is flushed.
func WithDecisionLogFlushInterval(interval time.Duration) DecisionLoggerOption {
	return func(cfg *decisionLoggerConfig) {
		if interval > 0 {
			cfg.flushInterval = interval
		}
	}
}

// WithDecisionLogLogger configures the logger used for degraded write notices.
func WithDecisionLogLogger(logger *slog.Logger) DecisionLoggerOption {
	return func(cfg *decisionLoggerConfig) {
		if logger != nil {
			cfg.logger = logger
		}
	}
}

// NewDecisionLogger creates an asynchronous decision logger backed by pgxpool.
func NewDecisionLogger(pool *pgxpool.Pool, nodeID string, opts ...DecisionLoggerOption) *DecisionLogger {
	return newDecisionLogger(pool, nodeID, opts...)
}

func newDecisionLogger(db decisionLogDB, nodeID string, opts ...DecisionLoggerOption) *DecisionLogger {
	cfg := decisionLoggerConfig{
		bufferSize:    defaultDecisionLogBufferSize,
		batchSize:     defaultDecisionLogBatchSize,
		flushInterval: defaultDecisionLogFlushInterval,
		logger:        slog.Default(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	logger := cfg.logger.With("component", "policy.decisionlog")
	l := &DecisionLogger{
		db:            db,
		nodeID:        nodeID,
		ch:            make(chan Entry, cfg.bufferSize),
		batchSize:     cfg.batchSize,
		flushInterval: cfg.flushInterval,
		logger:        logger,
	}
	l.SetVerbosity(DefaultDecisionLogVerbosity)
	l.SetScopeSampleRate(DefaultDecisionLogScopeSampleRate)
	return l
}

// Start launches the flush goroutine. Calling Start more than once is a no-op.
func (l *DecisionLogger) Start(ctx context.Context) {
	if l == nil {
		return
	}
	l.cancelMu.Lock()
	defer l.cancelMu.Unlock()
	if l.cancel != nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	l.stopped.Store(false)
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		l.run(runCtx)
	}()
}

// Stop prevents new entries, cancels the flush goroutine, and drains the queue.
func (l *DecisionLogger) Stop() {
	if l == nil {
		return
	}
	l.stopped.Store(true)
	l.cancelMu.Lock()
	cancel := l.cancel
	l.cancel = nil
	l.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
	l.Wait()
}

// Wait blocks until the flush goroutine has exited.
func (l *DecisionLogger) Wait() {
	if l != nil {
		l.wg.Wait()
	}
}

// SetVerbosity hot-updates the payload verbosity. Invalid values fall back to
// digest mode.
func (l *DecisionLogger) SetVerbosity(v string) {
	if l == nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case DecisionLogVerbosityVerbose:
		l.verbosity.Store(decisionLogVerbosityVerboseValue)
	default:
		l.verbosity.Store(decisionLogVerbosityDigestValue)
	}
}

// SetScopeSampleRate hot-updates successful scope sampling. n <= 1 logs every
// scope decision.
func (l *DecisionLogger) SetScopeSampleRate(n int) {
	if l == nil {
		return
	}
	if n <= 1 {
		n = 1
	}
	l.scopeSampleRate.Store(int64(n))
}

// DroppedCount returns the number of entries dropped because the buffer was full.
func (l *DecisionLogger) DroppedCount() uint64 {
	if l == nil {
		return 0
	}
	return l.dropped.Load()
}

// Log enqueues a fully prepared entry without blocking. If the queue is full,
// the entry is dropped and counted.
func (l *DecisionLogger) Log(entry Entry) {
	if l == nil || !l.shouldLog(entry) {
		return
	}
	if entry.InputDigest == "" {
		entry.InputDigest = digestBytes([]byte("null"))
	}
	l.enqueue(entry)
}

// LogDecision samples, digests, and optionally captures JSON payloads before
// enqueueing. It performs no I/O and never blocks on the database.
func (l *DecisionLogger) LogDecision(entry Entry, input any, result any) {
	if l == nil || !l.shouldLog(entry) {
		return
	}
	inputJSON, digest, err := marshalForDigest(input)
	if err != nil {
		inputJSON = []byte("null")
		digest = digestBytes(inputJSON)
		if entry.Error == "" {
			entry.Error = fmt.Sprintf("marshal policy input for decision log: %v", err)
		}
	}
	entry.InputDigest = digest

	if l.verbosity.Load() == decisionLogVerbosityVerboseValue {
		entry.InputSample = cloneRawMessage(inputJSON)
		if result != nil {
			if resultJSON, err := json.Marshal(result); err == nil {
				entry.ResultSample = cloneRawMessage(resultJSON)
			}
		}
	}
	l.enqueue(entry)
}

func (l *DecisionLogger) shouldLog(entry Entry) bool {
	if entry.Error != "" {
		return true
	}
	if entry.Allowed != nil && !*entry.Allowed {
		return true
	}
	if entry.DecisionName != DecisionScope {
		return true
	}

	rate := l.scopeSampleRate.Load()
	if rate <= 1 {
		return true
	}
	count := l.scopeCounter.Add(1)
	return (count-1)%uint64(rate) == 0
}

func (l *DecisionLogger) enqueue(entry Entry) {
	if l.stopped.Load() {
		return
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	entry.NodeID = l.nodeID

	select {
	case l.ch <- entry:
	default:
		l.recordDrop()
	}
}

func (l *DecisionLogger) recordDrop() {
	dropped := l.dropped.Add(1)
	now := time.Now().UnixNano()
	last := l.lastDropWarnNS.Load()
	if now-last < dropWarnInterval.Nanoseconds() {
		return
	}
	if l.lastDropWarnNS.CompareAndSwap(last, now) {
		l.logger.Warn("policy decision log buffer full; dropping entries", "dropped", dropped)
	}
}

func (l *DecisionLogger) run(ctx context.Context) {
	ticker := time.NewTicker(l.flushInterval)
	defer ticker.Stop()

	batch := make([]Entry, 0, l.batchSize)
	flush := func(flushCtx context.Context) {
		if len(batch) == 0 {
			return
		}
		if err := l.insertBatch(flushCtx, batch); err != nil {
			l.logger.WarnContext(flushCtx, "policy decision log batch insert failed", "entries", len(batch), "error", err)
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case entry := <-l.ch:
					batch = append(batch, entry)
					if len(batch) >= l.batchSize {
						flush(context.Background())
					}
				default:
					flush(context.Background())
					return
				}
			}
		case entry := <-l.ch:
			batch = append(batch, entry)
			if len(batch) >= l.batchSize {
				flush(ctx)
			}
		case <-ticker.C:
			flush(ctx)
		}
	}
}

func (l *DecisionLogger) insertBatch(ctx context.Context, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	if l.db == nil {
		return fmt.Errorf("policy decision log database is nil")
	}

	var b strings.Builder
	b.WriteString(`INSERT INTO policy_decisions ("timestamp", decision_name, policy_generation, user_id, profile_id, session_id, request_id, node_id, allowed, eval_time_ns, input_digest, input_sample, result_sample, error) VALUES `)

	args := make([]any, 0, len(entries)*14)
	for i, e := range entries {
		if i > 0 {
			b.WriteString(", ")
		}
		base := i * 14
		fmt.Fprintf(&b, "($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d::jsonb, $%d::jsonb, $%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10, base+11, base+12, base+13, base+14)
		args = append(args,
			e.Timestamp,
			string(e.DecisionName),
			e.PolicyGeneration,
			e.UserID,
			nullableString(e.ProfileID),
			nullableString(e.SessionID),
			nullableString(e.RequestID),
			nullableString(e.NodeID),
			e.Allowed,
			e.EvalTimeNS,
			e.InputDigest,
			nullableJSON(e.InputSample),
			nullableJSON(e.ResultSample),
			nullableString(e.Error),
		)
	}

	if _, err := l.db.Exec(ctx, b.String(), args...); err != nil {
		return fmt.Errorf("insert policy decisions: %w", err)
	}
	return nil
}

func marshalForDigest(v any) ([]byte, string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, "", err
	}
	return raw, digestBytes(raw), nil
}

func digestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func cloneRawMessage(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return []byte(value)
}
