package policy

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type fakeDecisionLogDB struct {
	mu       sync.Mutex
	argCount []int
	calls    chan int
}

func newFakeDecisionLogDB() *fakeDecisionLogDB {
	return &fakeDecisionLogDB{calls: make(chan int, 10)}
}

func (f *fakeDecisionLogDB) Exec(_ context.Context, _ string, arguments ...any) (pgconn.CommandTag, error) {
	count := len(arguments) / 14
	f.mu.Lock()
	f.argCount = append(f.argCount, len(arguments))
	f.mu.Unlock()
	f.calls <- count
	return pgconn.CommandTag{}, nil
}

func TestDecisionLoggerFlushesOnBatchSize(t *testing.T) {
	db := newFakeDecisionLogDB()
	logger := newDecisionLogger(
		db,
		"node-a",
		WithDecisionLogBufferSize(10),
		WithDecisionLogBatchSize(2),
		WithDecisionLogFlushInterval(time.Hour),
	)
	logger.Start(context.Background())
	defer logger.Stop()

	logger.Log(testDecisionEntry())
	logger.Log(testDecisionEntry())

	if got := waitForDecisionLogBatch(t, db); got != 2 {
		t.Fatalf("batch size = %d, want 2", got)
	}
}

func TestDecisionLoggerFlushesOnInterval(t *testing.T) {
	db := newFakeDecisionLogDB()
	logger := newDecisionLogger(
		db,
		"node-a",
		WithDecisionLogBufferSize(10),
		WithDecisionLogBatchSize(10),
		WithDecisionLogFlushInterval(10*time.Millisecond),
	)
	logger.Start(context.Background())
	defer logger.Stop()

	logger.Log(testDecisionEntry())

	if got := waitForDecisionLogBatch(t, db); got != 1 {
		t.Fatalf("batch size = %d, want 1", got)
	}
}

func TestDecisionLoggerDropsWhenBufferFull(t *testing.T) {
	logger := newDecisionLogger(nil, "node-a", WithDecisionLogBufferSize(1))

	logger.Log(testDecisionEntry())
	logger.Log(testDecisionEntry())

	if got := logger.DroppedCount(); got != 1 {
		t.Fatalf("DroppedCount() = %d, want 1", got)
	}
}

func TestDecisionLoggerSamplesScopeDecisions(t *testing.T) {
	logger := newDecisionLogger(nil, "node-a", WithDecisionLogBufferSize(10))
	logger.SetScopeSampleRate(2)

	for range 5 {
		logger.Log(Entry{DecisionName: DecisionScope, InputDigest: "digest"})
	}
	if got := len(logger.ch); got != 3 {
		t.Fatalf("buffered entries = %d, want 3", got)
	}
}

func TestDecisionLoggerDenialsAndErrorsBypassScopeSampling(t *testing.T) {
	logger := newDecisionLogger(nil, "node-a", WithDecisionLogBufferSize(10))
	logger.SetScopeSampleRate(100)
	denied := false

	logger.Log(Entry{DecisionName: DecisionScope, Allowed: &denied, InputDigest: "digest"})
	logger.Log(Entry{DecisionName: DecisionScope, Error: "boom", InputDigest: "digest"})

	if got := len(logger.ch); got != 2 {
		t.Fatalf("buffered entries = %d, want 2", got)
	}
}

func TestDecisionLoggerVerbosityGatesSamples(t *testing.T) {
	logger := newDecisionLogger(nil, "node-a", WithDecisionLogBufferSize(10))
	logger.SetScopeSampleRate(1)
	input := testScopeInput()
	result := ScopeDecision{SchemaVersion: 1, Unrestricted: true}

	logger.SetVerbosity(DecisionLogVerbosityDigest)
	logger.LogDecision(Entry{DecisionName: DecisionScope}, input, result)
	digestEntry := <-logger.ch
	if digestEntry.InputDigest == "" {
		t.Fatal("digest mode entry missing input digest")
	}
	if digestEntry.InputSample != nil || digestEntry.ResultSample != nil {
		t.Fatalf("digest mode samples = input %q result %q, want nil", digestEntry.InputSample, digestEntry.ResultSample)
	}

	logger.SetVerbosity(DecisionLogVerbosityVerbose)
	logger.LogDecision(Entry{DecisionName: DecisionScope}, input, result)
	verboseEntry := <-logger.ch
	if !json.Valid(verboseEntry.InputSample) {
		t.Fatalf("verbose input sample is not valid JSON: %q", verboseEntry.InputSample)
	}
	if !json.Valid(verboseEntry.ResultSample) {
		t.Fatalf("verbose result sample is not valid JSON: %q", verboseEntry.ResultSample)
	}
}

func TestDecisionLoggerDigestStable(t *testing.T) {
	input := testScopeInput()
	_, first, err := marshalForDigest(input)
	if err != nil {
		t.Fatalf("marshalForDigest() error: %v", err)
	}
	_, second, err := marshalForDigest(input)
	if err != nil {
		t.Fatalf("marshalForDigest() error: %v", err)
	}
	if first == "" || first != second {
		t.Fatalf("digest stability = %q and %q, want equal non-empty", first, second)
	}
}

func TestPDPResolveViewerScopeLogsDecision(t *testing.T) {
	ctx := context.Background()
	engine, err := NewEngine(ctx)
	if err != nil {
		t.Fatalf("NewEngine() error: %v", err)
	}
	logger := newDecisionLogger(nil, "node-a", WithDecisionLogBufferSize(10))
	logger.SetScopeSampleRate(1)
	logger.SetVerbosity(DecisionLogVerbosityVerbose)
	pdp := NewPDP(engine, WithDecisionLogger(logger))

	decision, meta, err := pdp.ResolveViewerScope(ctx, testScopeInput())
	if err != nil {
		t.Fatalf("ResolveViewerScope() error: %v", err)
	}
	if decision.SchemaVersion != 1 {
		t.Fatalf("decision schema version = %d, want 1", decision.SchemaVersion)
	}
	if len(logger.ch) != 1 {
		t.Fatalf("buffered entries = %d, want 1", len(logger.ch))
	}
	entry := <-logger.ch
	if entry.PolicyGeneration != meta.Revision {
		t.Fatalf("entry generation = %d, want %d", entry.PolicyGeneration, meta.Revision)
	}
	if entry.NodeID != "node-a" {
		t.Fatalf("entry node id = %q, want node-a", entry.NodeID)
	}
	if entry.InputDigest == "" || !json.Valid(entry.InputSample) || !json.Valid(entry.ResultSample) {
		t.Fatalf("entry missing digest or valid samples: %#v", entry)
	}
}

func waitForDecisionLogBatch(t *testing.T, db *fakeDecisionLogDB) int {
	t.Helper()
	select {
	case got := <-db.calls:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for decision log batch")
	}
	return 0
}

func testDecisionEntry() Entry {
	return Entry{
		DecisionName:     DecisionName("silo.permission.decision"),
		PolicyGeneration: 1,
		EvalTimeNS:       100,
		InputDigest:      "digest",
	}
}

func testScopeInput() ScopeInput {
	return ScopeInput{
		SchemaVersion:        1,
		UserID:               42,
		SessionID:            "session-a",
		ProfileID:            "profile-a",
		ProfilePresent:       true,
		ProfileVerified:      true,
		RequestTime:          "2026-07-02T00:00:00Z",
		AccountMaxQuality:    "4k",
		ProfileMaxQuality:    "1080p",
		ProfileMaxRating:     "PG-13",
		AccessPolicyRevision: 7,
	}
}
