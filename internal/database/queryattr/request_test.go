package queryattr

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestRequestPGXRowsSeal(t *testing.T) {
	ctx, scope, s := requestFixture(t)
	tr := Tracer{Store: s}
	c, _ := wireConnection(t, tr, pgx.QueryExecModeSimpleProtocol)
	rows, err := c.Query(ctx, "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("first row absent: %v", rows.Err())
	}
	sealed := scope.Seal()
	if sealed.Queries != 1 || sealed.InFlight != 1 || !sealed.Incomplete {
		t.Fatalf("open pgx rows seal=%+v want one unfinished statement", sealed)
	}
	before := RequestCoverage()
	rows.Close()
	if got := scope.Seal(); got != sealed {
		t.Fatalf("row close changed seal: got=%+v want=%+v", got, sealed)
	}
	if got := s.Totals(); got.Started != 1 || got.Completed != 1 || got.Outcomes[Success] != 1 || RequestCoverage().LateCompletions-before.LateCompletions != 1 {
		t.Fatalf("closed pgx rows totals=%+v want one late successful completion", got)
	}
}

func TestRequestLogPrivacy(t *testing.T) {
	ctx, scope, s := requestFixture(t)
	ctx = WithLabel(ctx, labelTest)
	tr := Tracer{Store: s}
	const sentinel = "PRIVATE_REQUEST_SENTINEL"
	args := make([]any, 1001)
	for i := range args {
		args[i] = sentinel
	}
	args[0] = []string{sentinel}
	data := pgx.TraceQueryStartData{SQL: "SELECT '" + sentinel + "', $$" + sentinel + "$$, $1 FROM " + sentinel + " /* " + sentinel + " */", Args: args}
	for range 11 {
		q := tr.TraceQueryStart(ctx, nil, data)
		q.(*record).start -= 100_000_000
		tr.TraceQueryEnd(q, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "23505", Message: sentinel, Detail: sentinel, Hint: sentinel}})
	}
	for _, sql := range []string{"SELECT '" + sentinel, "SELECT \"" + sentinel + "\"", "SELECT E'" + sentinel + "'"} {
		q := tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: sql})
		tr.TraceQueryEnd(q, nil, pgx.TraceQueryEndData{})
	}
	batch := &pgx.Batch{}
	batch.Queue(data.SQL, args...)
	batch.Queue(data.SQL, args...)
	b := tr.TraceBatchStart(ctx, nil, pgx.TraceBatchStartData{Batch: batch})
	tr.TraceBatchQuery(b, nil, pgx.TraceBatchQueryData{SQL: data.SQL, Args: args})
	tr.TraceBatchEnd(b, nil, pgx.TraceBatchEndData{})
	copyCtx := tr.TraceCopyFromStart(ctx, nil, pgx.TraceCopyFromStartData{TableName: pgx.Identifier{sentinel}, ColumnNames: []string{sentinel}})
	tr.TraceCopyFromEnd(copyCtx, nil, pgx.TraceCopyFromEndData{Err: &pgconn.PgError{Code: "23505", Message: sentinel}})
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	logger.InfoContext(ctx, "request fixture", scope.Seal().LogAttrs()...)
	unsafeOutput := func(text string) bool {
		return strings.Contains(text, sentinel) || strings.Contains(text, ".go") || strings.Contains(text, "SELECT ")
	}
	if unsafeOutput(output.String()) {
		t.Fatalf("private request projection=%s", output.String())
	}
	if !unsafeOutput(`{"sql":"` + sentinel + `"}`) {
		t.Fatal("leaking projection accepted by privacy predicate")
	}
}

func requestFixture(t *testing.T) (context.Context, *RequestScope, *Store) {
	t.Helper()
	previous := enabled.Swap(true)
	t.Cleanup(func() { enabled.Store(previous) })
	ctx, scope := StartRequest(t.Context())
	return ctx, scope, NewStore()
}

func requestIdentity(s *Store, name string, fp uint64) Identity {
	return s.lookup(Caller{Function: name}, fp, Statement).identity
}

func TestRequestSealTotalsAndTies(t *testing.T) {
	ctx, scope, s := requestFixture(t)
	if reused, got := StartRequest(ctx); reused != ctx || got != scope {
		t.Fatalf("unsealed reuse: context=%v scope=%p want=%p", reused == ctx, got, scope)
	}
	a := requestIdentity(s, "a", 2)
	b := requestIdentity(s, "b", 1)
	a1 := requestIdentity(s, "a", 1)
	for _, id := range []Identity{b, a, a1} {
		scope.Start(id, Query, 1, Sizes{}, 1)
	}
	// Three parallel operations each spend 100 ns; their sum is 300 ns.
	for _, id := range []Identity{b, a, a1} {
		scope.Finish(Observation{Identity: id, Kind: Query, Statements: 1, NS: 100})
	}
	got := scope.Seal()
	if got.Queries != 3 || got.QueryNS != 300 || got.InFlight != 0 || got.Incomplete || got.Slowest.Caller.Function != "a" || got.Slowest.Fingerprint != 1 || got.SlowestNS != 100 {
		t.Fatalf("seal=%+v want three complete queries, 300 ns, a/1/100 slowest", got)
	}
	before := RequestCoverage()
	tr := Tracer{Store: s}
	late := tr.TraceQueryStart(WithLabel(ctx, labelTest), nil, pgx.TraceQueryStartData{SQL: "SELECT late"})
	tr.TraceQueryEnd(late, nil, pgx.TraceQueryEndData{})
	if totals := s.Totals(); totals.Started != 1 || totals.Completed != 1 || totals.Outcomes[Success] != 1 {
		t.Fatalf("late start node=%+v want one completed successful statement", totals)
	}
	if again := scope.Seal(); !reflect.DeepEqual(again, got) {
		t.Fatalf("seal changed: got=%+v want=%+v", again, got)
	}
	after := RequestCoverage()
	if after.LateRequestStarts-before.LateRequestStarts != 1 || after.LateCompletions-before.LateCompletions != 1 {
		t.Fatalf("late coverage before=%+v after=%+v want one start and completion", before, after)
	}
	_, next := StartRequest(ctx)
	if next == scope || next == nil {
		t.Fatalf("sealed scope reused: next=%p old=%p", next, scope)
	}
}

func TestRequestInFlightAcquiresAndCancellation(t *testing.T) {
	ctx, scope, s := requestFixture(t)
	tr := Tracer{Store: s}
	q := tr.TraceQueryStart(WithLabel(ctx, labelTest), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	start := NowNS() - 1000
	scope.Start(Identity{}, Acquire, 0, Sizes{}, start)
	scope.Start(Identity{}, Acquire, 0, Sizes{}, start-2000)
	scope.Finish(Observation{Kind: Acquire, StartedNS: start, NS: 700})
	before := RequestCoverage()
	seal := scope.Seal()
	if seal.Queries != 1 || seal.InFlight != 1 || !seal.Incomplete || seal.AcquireWaitNS < 3700 || seal.Slowest.Caller.Function != "" {
		t.Fatalf("in-flight seal=%+v want query=1 in-flight=1 incomplete wait>=3700 no completed query", seal)
	}
	tr.TraceQueryEnd(q, nil, pgx.TraceQueryEndData{Err: context.Canceled})
	scope.Finish(Observation{Kind: Acquire, StartedNS: start - 2000, NS: 9000})
	if got := scope.Seal(); got != seal {
		t.Fatalf("late cancellation mutated seal: got=%+v want=%+v", got, seal)
	}
	if got := s.Totals(); got.Outcomes[Canceled] != 1 || got.Completed != 1 || got.Started != 1 {
		t.Fatalf("node totals=%+v want one canceled completion", got)
	}
	after := RequestCoverage()
	if after.IncompleteRequests-before.IncompleteRequests != 1 || after.LateCompletions-before.LateCompletions != 2 {
		t.Fatalf("coverage before=%+v after=%+v want one incomplete and two late ends", before, after)
	}
}

func TestRequestBatchAndRepeatRules(t *testing.T) {
	for _, hijacked := range []bool{false, true} {
		t.Run(fmt.Sprint(hijacked), func(t *testing.T) {
			_, scope, s := requestFixture(t)
			id := requestIdentity(s, "fixture", 1)
			for range 11 {
				scope.Start(id, Query, 1, Sizes{}, 1)
				scope.Finish(Observation{Identity: id, Kind: Query, Statements: 1, NS: 1})
			}
			envelope := s.lookup(Caller{Function: "fixture"}, 0, BatchEnvelope).identity
			scope.Start(envelope, Batch, 12, Sizes{Batch: 12}, 1)
			for range 11 {
				scope.Finish(Observation{Identity: id, Kind: Batch, Child: true, Statements: 1})
			}
			scope.Finish(Observation{Identity: envelope, Kind: Batch, Statements: 1, NS: 80, Unobserved: true})
			if hijacked {
				scope.Hijack()
				// Statements after a successful hijack still belong to this request.
				scope.Start(id, CopyFrom, 1, Sizes{}, 1)
				scope.Finish(Observation{Identity: id, Kind: CopyFrom, Statements: 1, NS: 3})
			}
			got := scope.Seal()
			wantQueries, wantNS, wantRepeated := uint64(23), uint64(91), uint64(1)
			if hijacked {
				wantQueries, wantNS, wantRepeated = 24, 94, 0
			}
			if got.Queries != wantQueries || got.Batches != 1 || got.QueryNS != wantNS || got.InFlight != 0 || got.Repeated != wantRepeated {
				t.Fatalf("batch/hijack seal=%+v want queries=%d batches=1 ns=%d in-flight=0 repeated=%d", got, wantQueries, wantNS, wantRepeated)
			}
		})
	}
}

func TestRequestRepeatBoundaryAndReleasedIdentities(t *testing.T) {
	for _, calls := range []int{10, 11} {
		t.Run(fmt.Sprint(calls), func(t *testing.T) {
			_, scope, s := requestFixture(t)
			id := requestIdentity(s, "fixture", 1)
			for range calls {
				scope.Start(id, Query, 1, Sizes{}, 1)
				scope.Finish(Observation{Identity: id, Kind: Query, Statements: 1, NS: 1})
			}
			got := scope.Seal()
			want := uint64(0)
			if calls > 10 {
				want = 1
			}
			if got.Repeated != want {
				t.Fatalf("calls=%d repeated=%d want=%d", calls, got.Repeated, want)
			}
			for _, entry := range scope.entries {
				if entry.identity != nil || got.Slowest.canonical != nil {
					t.Fatal("sealed scope retains row references; want nil entries and detached slowest")
				}
			}
		})
	}
}

func TestRequestSealRacesWithIndependentRecorder(t *testing.T) {
	_, scope, s := requestFixture(t)
	id := requestIdentity(s, "concurrent", 1)
	const count = 64
	started := make(chan struct{}, count)
	finish := make(chan struct{})
	late := make(chan struct{})
	var wg sync.WaitGroup
	for i := range count {
		wg.Go(func() {
			scope.Start(id, Query, 1, Sizes{}, 1)
			started <- struct{}{}
			if i%2 == 0 {
				<-late
			} else {
				<-finish
			}
			scope.Finish(Observation{Identity: id, Kind: Query, Statements: 1, NS: 100})
		})
	}
	for range count {
		<-started
	}
	// Seal and all finishes contend on the same lock. Every finish must land
	// either in this snapshot or in the complete late-completion counter.
	before := RequestCoverage()
	close(finish)
	sealed := scope.Seal()
	close(late)
	wg.Wait()
	after := RequestCoverage()
	if sealed.Queries != count || sealed.QueryNS/100+sealed.InFlight != count || sealed.InFlight != after.LateCompletions-before.LateCompletions || scope.Seal() != sealed {
		t.Fatalf("seal=%+v coverage before=%+v after=%+v want independent %d-statement reconciliation", sealed, before, after, count)
	}
}

func TestRequestOverflowAndConcurrentSeal(t *testing.T) {
	ctx, scope, _ := requestFixture(t)
	s := newStore(64, 1, 8)
	tr := Tracer{Store: s}
	labeled := WithLabel(ctx, labelTest)
	failed := pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "23505"}}
	for i := range 66 {
		owner := labeled
		if i == 65 {
			owner = WithLabel(ctx, LabelPersonalCollectionCounts)
		}
		q := tr.TraceQueryStart(owner, nil, pgx.TraceQueryStartData{SQL: fmt.Sprintf("SELECT column%d", i)})
		if got := s.Totals(); got.Started != got.Completed+1 {
			t.Fatalf("open statement %d: totals=%+v want started=completed+1", i, got)
		}
		tr.TraceQueryEnd(q, nil, failed)
		if got := s.Totals(); got.Started != got.Completed {
			t.Fatalf("closed statement %d: totals=%+v want started=completed", i, got)
		}
	}
	if len(s.identities) != 64 || len(s.functions) != 1 || scope.used != 64 {
		t.Fatalf("caps: identities=%d functions=%d request identities=%d want 64/1/64", len(s.identities), len(s.functions), scope.used)
	}
	q := tr.TraceQueryStart(labeled, nil, pgx.TraceQueryStartData{SQL: "SELECT overflow"})
	got := scope.Seal()
	if got.Queries != 67 || got.QueryNS == 0 || !got.Incomplete || got.InFlight != 1 {
		t.Fatalf("overflow seal=%+v want totals preserved at all caps", got)
	}
	tr.TraceQueryEnd(q, nil, failed)
	if totals := s.Totals(); totals.Started != 67 || totals.Completed != 67 || totals.Outcomes[classIndex("23")] != 67 || totals.DuplicateEnds != 0 || totals.Overflow < 3 {
		t.Fatalf("overflow node=%+v want 67 completed class-23 statements, no duplicate ends, three overflows", totals)
	}
	_, parallel, s2 := requestFixture(t)
	ids := make([]Identity, 70)
	for i := range ids {
		ids[i] = requestIdentity(s2, "parallel", uint64(i+1))
	}
	var wg sync.WaitGroup
	ready := make(chan struct{})
	for _, id := range ids {
		wg.Go(func() {
			parallel.Start(id, Query, 1, Sizes{}, 1)
			<-ready
			parallel.Finish(Observation{Identity: id, Kind: Query, Statements: 1, NS: 7})
		})
	}
	close(ready)
	wg.Wait()
	if got := parallel.Seal(); got.Queries != 70 || got.QueryNS != 490 || got.InFlight != 0 || !got.Incomplete {
		t.Fatalf("parallel seal=%+v want queries=70 ns=490 in-flight=0 incomplete", got)
	}
}

func TestRequestNoQueryDisabledAndScopeHeap(t *testing.T) {
	ctx, scope, _ := requestFixture(t)
	got := scope.Seal()
	attrs := got.LogAttrs()
	if got.Queries != 0 || got.Incomplete || len(attrs) != 22 {
		t.Fatalf("no-query snapshot=%+v attrs=%v want zero, complete, eleven fields", got, attrs)
	}
	values := make(map[string]any)
	for i := 0; i < len(attrs); i += 2 {
		values[attrs[i].(string)] = attrs[i+1]
	}
	if values["db_slowest_site"] != "none" || values["db_slowest_fp"] != "none" {
		t.Fatalf("no-query values=%v want explicit none", values)
	}
	enabled.Store(false)
	if next, off := StartRequest(ctx); next != ctx || off != nil || len(off.Seal().LogAttrs()) != 0 {
		t.Fatalf("disabled request allocated or emitted fields: context=%v scope=%p", next == ctx, off)
	}
	if got := testing.AllocsPerRun(100, func() { _, _ = StartRequest(context.Background()) }); got != 0 {
		t.Fatalf("disabled allocations=%v want=0", got)
	}
	enabled.Store(true)
	if n := unsafe.Sizeof(RequestScope{}); n > 1280 {
		t.Fatalf("scope bytes=%d want<=1280", n)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	const count = 1000
	scopes := make([]*RequestScope, count)
	for i := range scopes {
		_, scopes[i] = StartRequest(context.Background())
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	growth := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if growth > count*1280 {
		t.Fatalf("scope heap=%d want<=%d", growth, count*1280)
	}
	t.Logf("scope bytes=%d heap=%d scopes=%d ceiling=%d", unsafe.Sizeof(RequestScope{}), growth, count, count*1280)
	runtime.KeepAlive(scopes)
}

func TestRequestScopeAllocations(t *testing.T) {
	requestFixture(t)
	for round := range 2 {
		runtime.GC()
		got := testing.AllocsPerRun(100, func() {
			ctx, scope := StartRequest(context.Background())
			if again, reused := StartRequest(ctx); again != ctx || reused != scope {
				panic("unsealed request scope was not reused")
			}
			scope.Seal()
		})
		if got != 1 {
			t.Fatalf("round=%d request allocations=%v want one allocation including owner and scope reuse", round, got)
		}
	}
}
