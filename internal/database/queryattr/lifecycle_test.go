package queryattr

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type panicContext struct{ context.Context }

func (panicContext) Value(any) any { panic("private panic sentinel") }

func TestEnrichPanicBalancesRequestScope(t *testing.T) {
	previous := enabled.Swap(true)
	t.Cleanup(func() { enabled.Store(previous) })
	for _, kind := range []Kind{Query, CopyFrom} {
		t.Run(strconv.Itoa(int(kind)), func(t *testing.T) {
			s := NewStore()
			tr := Tracer{Store: s}
			ctx, scope := StartRequest(context.Background())
			scope.owner.Context = panicContext{context.Background()}
			var operation context.Context
			switch kind {
			case Query:
				operation = tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
				tr.TraceQueryEnd(operation, nil, pgx.TraceQueryEndData{})
			case CopyFrom:
				operation = tr.TraceCopyFromStart(ctx, nil, pgx.TraceCopyFromStartData{})
				tr.TraceCopyFromEnd(operation, nil, pgx.TraceCopyFromEndData{})
			}
			got := scope.Seal()
			if got.Queries != 1 || got.InFlight != 0 || !got.Incomplete {
				t.Fatalf("request queries=%d in_flight=%d incomplete=%t want=1/0/true", got.Queries, got.InFlight, got.Incomplete)
			}
			if got := s.Totals(); got.Started != 1 || got.Completed != 1 || got.AttributionFailures != 1 {
				t.Fatalf("node starts=%d completions=%d failures=%d want=1/1/1", got.Started, got.Completed, got.AttributionFailures)
			}
		})
	}
}

type scopeProbe struct {
	starts, finishes int
	ns               uint64
	last             Observation
}

func (p *scopeProbe) Start(_ Identity, _ Kind, _ uint64, _ Sizes, _ int64) { p.starts++ }
func (p *scopeProbe) Finish(o Observation)                                 { p.finishes++; p.ns += o.NS; p.last = o }

func TestStartPanicCountsAndFinishes(t *testing.T) {
	s := NewStore()
	tr := Tracer{Store: s}
	ctx := tr.TraceQueryStart(panicContext{context.Background()}, nil, pgx.TraceQueryStartData{SQL: "SELECT 'private'"})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
	got := s.Totals()
	if got.Started != 1 || got.Completed != 1 || got.Observed != 1 || got.AttributionFailures != 1 {
		t.Fatalf("panic totals=%+v", got)
	}
	if s.reserved[4].counts.values[cCompleted].Load() != 1 {
		t.Fatal("panic lost unavailable completion")
	}
}
func TestFinishedReferencesAndInternalOwnership(t *testing.T) {
	s := NewStore()
	tr := Tracer{Store: s}
	p := &scopeProbe{}
	ctx := WithOwnership(context.Background(), RequestOrigin, p)
	b := &pgx.Batch{}
	b.Queue("SELECT 'private'")
	old := tr.TraceBatchStart(ctx, nil, pgx.TraceBatchStartData{Batch: b})
	tr.TraceBatchEnd(old, nil, pgx.TraceBatchEndData{})
	r := old.(*record)
	if r.scope != nil || r.batch != nil || r.done.Load() != 1 {
		t.Fatal("finished record retains live references")
	}
	if p.starts != 1 || p.finishes != 1 {
		t.Fatalf("scope notifications=%d,%d want=1,1", p.starts, p.finishes)
	}
	setup := tr.TraceQueryStart(Internal(ctx, ConnectionSetup), nil, pgx.TraceQueryStartData{SQL: "SELECT 'private_setup'"})
	tr.TraceQueryEnd(setup, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "42501", Message: "private_error"}})
	if p.starts != 1 || p.finishes != 1 {
		t.Fatal("setup reached request scope")
	}
	if s.reserved[6].counts.values[cCompleted].Load() != 1 || s.reserved[6].counts.outcomes[classIndex("42")].Load() != 1 {
		t.Fatal("setup class lost")
	}
}
func TestTimeCountsCallerWork(t *testing.T) {
	s := NewStore()
	tr := Tracer{Store: s}
	ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	r := ctx.(*record)
	r.start -= 60_000_000 // A deterministic clock boundary, no sleep.
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
	if got := s.Totals().QueryNS; got < 60_000_000 {
		t.Fatalf("query ns=%d want>=60000000", got)
	}
}
func TestCorePrivacySentinels(t *testing.T) {
	s := NewStore()
	tr := Tracer{Store: s}
	sentinel := "PRIVATE_SENTINEL_9e27"
	ctx := tr.TraceQueryStart(WithLabel(context.Background(), labelTest), nil, pgx.TraceQueryStartData{SQL: "SELECT '" + sentinel + "', $1 FROM " + sentinel + " /* " + sentinel + " */", Args: []any{sentinel}})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "23505", Message: sentinel, Detail: sentinel, Hint: sentinel}})
	data, err := json.Marshal(s.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), sentinel) || strings.Contains(string(data), ".go") {
		t.Fatalf("private snapshot: %s", data)
	}
	// The same predicate must reject a leaking projection.
	if !strings.Contains(`{"sql":"`+sentinel+`"}`, sentinel) {
		t.Fatal("leaking control was accepted")
	}
}
func TestRecordAndConnectionByteBounds(t *testing.T) {
	if n := unsafe.Sizeof(record{}); n > 96 {
		t.Errorf("record bytes=%d want<=96", n)
	}
	if n := unsafe.Sizeof(connectionState{}); n > 256 {
		t.Errorf("connection bytes=%d want<=256", n)
	}
	runtime.GC()
}
func TestNoRowsAndUnavailableForms(t *testing.T) {
	s := NewStore()
	tr := Tracer{Store: s}
	for _, d := range []pgx.TraceQueryStartData{{SQL: "prepared_private"}, {SQL: "SELECT $1", Args: []any{panicRewriter{}}}, {SQL: "SELECT 1; SELECT 2"}} {
		ctx := tr.TraceQueryStart(WithLabel(context.Background(), labelTest), nil, d)
		tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
	}
	got := s.Totals()
	if got.Started != 3 || got.Outcomes[Success] != 3 {
		t.Fatalf("totals=%+v", got)
	}
	for _, r := range s.Snapshot() {
		if r.Identity.Form == Unavailable && r.Counters.Completed > 0 && r.Identity.Fingerprint != 0 {
			t.Fatal("unavailable identity has hash")
		}
	}
}

func TestBatchScopeAndOriginReconciliation(t *testing.T) {
	s := newStore(1, 1, 4)
	tr := Tracer{Store: s}
	p := &statementScope{}
	ctx := WithOwnership(WithLabel(context.Background(), labelTest), RequestOrigin, p)
	b := &pgx.Batch{}
	b.Queue("SELECT 1")
	b.Queue("SELECT 2")
	b.Queue("SELECT 3")
	c := tr.TraceBatchStart(ctx, nil, pgx.TraceBatchStartData{Batch: b})
	tr.TraceBatchQuery(c, nil, pgx.TraceBatchQueryData{SQL: "SELECT 1"})
	tr.TraceBatchEnd(c, nil, pgx.TraceBatchEndData{Err: context.Canceled})
	if p.started != 3 || p.finished != 3 || p.batches != 1 || p.children != 1 || p.tails != 2 {
		t.Fatalf("scope=%+v want starts=3 finishes=3 batches=1 children=1 tails=2", p)
	}
	node := s.Totals()
	origin := s.reserved[2].counts.snapshot()
	if node.Started != origin.Started || node.Completed != origin.Completed || node.QueryNS != origin.QueryNS || node.Unobserved != origin.Unobserved || node.Observed != origin.Observed {
		t.Fatalf("node=%+v request=%+v", node, origin)
	}
	if node.Overflow != 1 {
		t.Fatalf("overflow statements=%d want=1 observed fallback child", node.Overflow)
	}
}

type statementScope struct{ started, finished, batches, children, tails uint64 }

func (p *statementScope) Start(_ Identity, k Kind, n uint64, _ Sizes, _ int64) {
	p.started += n
	if k == Batch {
		p.batches++
	}
}
func (p *statementScope) Finish(o Observation) {
	p.finished += o.Statements
	if o.Child {
		p.children++
	}
	if o.Kind == Batch && !o.Child && o.Unobserved {
		p.tails += o.Statements
	}
}

func TestAcquireStartPanicStillReturnsRecord(t *testing.T) {
	s := NewStore()
	tr := Tracer{Store: s}
	ctx := tr.TraceAcquireStart(panicContext{context.Background()}, nil, pgxpool.TraceAcquireStartData{})
	if ctx == nil {
		t.Fatal("start panic lost acquire record")
	}
	tr.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{Err: context.DeadlineExceeded})
	got := s.Totals()
	if got.Started != 0 || got.Acquires != 1 || got.FailedAcquires != 1 || got.AttributionFailures != 1 {
		t.Fatalf("acquire panic=%+v", got)
	}
}

func TestUnobservedEnvelopeReconcilesSeparately(t *testing.T) {
	s := NewStore()
	tr := Tracer{Store: s}
	batch := &pgx.Batch{}
	batch.Queue("SELECT 1")
	r := tr.TraceBatchStart(context.Background(), nil, pgx.TraceBatchStartData{Batch: batch}).(*record)
	tr.finish(r, nil, Batch, pgconn.CommandTag{}, nil, true)
	got := s.Totals()
	var outcomes uint64
	for _, n := range got.EnvelopeOutcomes {
		outcomes += n
	}
	if got.Envelopes != outcomes+got.UnobservedEnvelopes || got.UnobservedEnvelopes != 1 || got.Observed != 0 || got.Unobserved != 1 {
		t.Fatalf("closed envelope=%+v", got)
	}
}

func TestInitializeKillSwitch(t *testing.T) {
	if os.Getenv("SILO_QUERYATTR_TEST_CHILD") == "1" {
		want := os.Getenv("SILO_QUERYATTR_TEST_WANT") == "true"
		if got := Initialize(); got != want {
			t.Fatalf("enabled=%t want=%t", got, want)
		}
		t.Setenv("SILO_QUERY_ATTRIBUTION_ENABLED", strconv.FormatBool(!want))
		if Initialize() != want || Enabled() != want {
			t.Fatal("kill switch was read more than once")
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"", "false", "unexpected", "true"} {
		t.Run(v, func(t *testing.T) {
			want := v == "" || v == "true"
			cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestInitializeKillSwitch$")
			for _, e := range os.Environ() {
				if !strings.HasPrefix(e, "SILO_QUERY_ATTRIBUTION_ENABLED=") {
					cmd.Env = append(cmd.Env, e)
				}
			}
			cmd.Env = append(cmd.Env, "SILO_QUERYATTR_TEST_CHILD=1", "SILO_QUERYATTR_TEST_WANT="+strconv.FormatBool(want), "SILO_QUERY_ATTRIBUTION_ENABLED="+v)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("child=%v output=%s", err, out)
			}
		})
	}
}
