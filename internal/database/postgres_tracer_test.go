package database

import (
	"context"
	"reflect"
	"runtime"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/Silo-Server/silo-server/internal/database/queryattr"
)

var (
	_ pgx.QueryTracer       = poolTracer{}
	_ pgx.BatchTracer       = poolTracer{}
	_ pgx.CopyFromTracer    = poolTracer{}
	_ pgx.ConnectTracer     = poolTracer{}
	_ pgxpool.AcquireTracer = poolTracer{}
)

func nonRecordingPostgresSDK(tb testing.TB) {
	tb.Helper()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	tb.Cleanup(func() {
		otel.SetTracerProvider(previous)
		if err := provider.Shutdown(context.Background()); err != nil {
			tb.Error(err)
		}
	})
}

func allocatedBytesPerOperation(run func()) int64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 1000 {
		run()
	}
	runtime.ReadMemStats(&after)
	return int64(after.TotalAlloc-before.TotalAlloc) / 1000
}

func TestAttributionRepeatedCopyWrapperRealScopes(t *testing.T) {
	queryattr.Initialize()
	nonRecordingPostgresSDK(t)
	coarse := postgresTracer{role: "checks"}
	combined := poolTracer{coarse: coarse, attribution: queryattr.Tracer{Store: queryattr.NewStore()}}
	table, columns := pgx.Identifier{"table"}, []string{"column"}
	source := &unreadCopySource{}
	var operations [2]func(context.Context)
	for i := range operations {
		var tracer pgx.QueryTracer = coarse
		var copyTracer pgx.CopyFromTracer = coarse
		if i == 1 {
			tracer, copyTracer = combined, combined
		}
		conn := bulkMemoryConn(t, tracer, pgx.QueryExecModeSimpleProtocol)
		tx := &bulkTx{conn: conn, copy: func(ctx context.Context, table pgx.Identifier, columns []string, _ pgx.CopyFromSource) (int64, error) {
			c := copyTracer.TraceCopyFromStart(ctx, conn, pgx.TraceCopyFromStartData{TableName: table, ColumnNames: columns})
			copyTracer.TraceCopyFromEnd(c, conn, pgx.TraceCopyFromEndData{CommandTag: pgconn.NewCommandTag("COPY 2")})
			return 2, nil
		}}
		operations[i] = func(ctx context.Context) {
			if n, err := CopyFrom(ctx, tx, table, columns, source); n != 2 || err != nil {
				t.Fatalf("wrapped copy=%d/%v want=2/nil", n, err)
			}
		}
	}
	for round := range 2 {
		ctx, scope := queryattr.StartRequest(queryattr.WithLabel(t.Context(), queryattr.LabelPersonalCollectionCounts))
		base, attributed := func() { operations[0](ctx) }, func() { operations[1](ctx) }
		for range 10 {
			base()
			attributed()
		}
		runtime.GC()
		baseAllocs := testing.AllocsPerRun(100, base)
		attrAllocs := testing.AllocsPerRun(100, attributed)
		baseBytes, attrBytes := allocatedBytesPerOperation(base), allocatedBytesPerOperation(attributed)
		t.Logf("round=%d copy wrapper coarse_allocs=%.0f attributed_allocs=%.0f coarse_bytes=%d attributed_bytes=%d", round, baseAllocs, attrAllocs, baseBytes, attrBytes)
		if attrAllocs-baseAllocs != 1 || attrBytes-baseBytes > 96 {
			t.Errorf("copy wrapper added allocations=%v bytes=%d want=1/<=96", attrAllocs-baseAllocs, attrBytes-baseBytes)
		}
		if got := scope.Seal(); got.Queries == 0 || got.InFlight != 0 {
			t.Fatalf("copy wrapper scope=%+v want completed statements", got)
		}
	}
}

func TestPoolTracerConfigurationAndKillSwitch(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("host=127.0.0.1 user=qa sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	configurePostgresTracing(cfg, "checks", false)
	if _, ok := cfg.ConnConfig.Tracer.(postgresTracer); !ok {
		t.Fatalf("off tracer=%T", cfg.ConnConfig.Tracer)
	}
	if reflect.ValueOf(cfg.AfterConnect).Pointer() != reflect.ValueOf(disableUnconfiguredJIT).Pointer() || cfg.BeforeClose != nil {
		t.Fatal("off setup must be unwrapped with no teardown hook")
	}
	configurePostgresTracing(cfg, "checks", true)
	if _, ok := cfg.ConnConfig.Tracer.(poolTracer); !ok {
		t.Fatalf("on tracer=%T", cfg.ConnConfig.Tracer)
	}
	if reflect.ValueOf(cfg.AfterConnect).Pointer() == reflect.ValueOf(disableUnconfiguredJIT).Pointer() || cfg.BeforeClose == nil {
		t.Fatal("on setup must be internally marked with a teardown hook")
	}
}

func TestAttributionWarmedAllocationComparison(t *testing.T) {
	queryattr.Initialize()
	nonRecordingPostgresSDK(t)
	s := queryattr.NewStore()
	coarse := postgresTracer{role: "checks"}
	combined := poolTracer{coarse: coarse, attribution: queryattr.Tracer{Store: s}}
	ctx := queryattr.WithLabel(context.Background(), queryattr.LabelPersonalCollectionCounts)
	ctx, scope := queryattr.StartRequest(ctx)
	defer scope.Seal()
	batch := &pgx.Batch{}
	batch.Queue("SELECT $1", 1)
	batch.Queue("SELECT $1", 2)
	childData := pgx.TraceBatchQueryData{SQL: "SELECT $1", Args: []any{1}}
	for _, kind := range []string{"query", "exec", "queryrow", "copy", "batch", "acquire", "child", "connect"} {
		t.Run(kind, func(t *testing.T) {
			var runBase, runAttr func()
			switch kind {
			case "query", "exec", "queryrow":
				d := pgx.TraceQueryStartData{SQL: "SELECT $1", Args: []any{1}}
				runBase = func() {
					c := coarse.TraceQueryStart(ctx, nil, d)
					coarse.TraceQueryEnd(c, nil, pgx.TraceQueryEndData{})
				}
				runAttr = func() {
					c := combined.TraceQueryStart(ctx, nil, d)
					combined.TraceQueryEnd(c, nil, pgx.TraceQueryEndData{})
				}
			case "copy":
				d := pgx.TraceCopyFromStartData{TableName: pgx.Identifier{"private_table"}, ColumnNames: []string{"private_column"}}
				runBase = func() {
					c := coarse.TraceCopyFromStart(ctx, nil, d)
					coarse.TraceCopyFromEnd(c, nil, pgx.TraceCopyFromEndData{})
				}
				runAttr = func() {
					c := combined.TraceCopyFromStart(ctx, nil, d)
					combined.TraceCopyFromEnd(c, nil, pgx.TraceCopyFromEndData{})
				}
			case "batch":
				d := pgx.TraceBatchStartData{Batch: batch}
				runBase = func() {
					c := coarse.TraceBatchStart(ctx, nil, d)
					for range 2 {
						coarse.TraceBatchQuery(c, nil, childData)
					}
					coarse.TraceBatchEnd(c, nil, pgx.TraceBatchEndData{})
				}
				runAttr = func() {
					c := combined.TraceBatchStart(ctx, nil, d)
					for range 2 {
						combined.TraceBatchQuery(c, nil, childData)
					}
					combined.TraceBatchEnd(c, nil, pgx.TraceBatchEndData{})
				}
			case "acquire":
				runBase = func() {
					c := coarse.TraceAcquireStart(ctx, nil, pgxpool.TraceAcquireStartData{})
					coarse.TraceAcquireEnd(c, nil, pgxpool.TraceAcquireEndData{})
				}
				runAttr = func() {
					c := combined.TraceAcquireStart(ctx, nil, pgxpool.TraceAcquireStartData{})
					combined.TraceAcquireEnd(c, nil, pgxpool.TraceAcquireEndData{})
				}
			case "child":
				large := &pgx.Batch{}
				for range 10000 {
					large.Queue("SELECT 1")
				}
				baseCtx := coarse.TraceBatchStart(ctx, nil, pgx.TraceBatchStartData{Batch: large})
				attrCtx := combined.TraceBatchStart(ctx, nil, pgx.TraceBatchStartData{Batch: large})
				defer coarse.TraceBatchEnd(baseCtx, nil, pgx.TraceBatchEndData{})
				defer combined.TraceBatchEnd(attrCtx, nil, pgx.TraceBatchEndData{})
				runBase = func() { coarse.TraceBatchQuery(baseCtx, nil, pgx.TraceBatchQueryData{SQL: "SELECT 1"}) }
				runAttr = func() { combined.TraceBatchQuery(attrCtx, nil, pgx.TraceBatchQueryData{SQL: "SELECT 1"}) }
			case "connect":
				runBase = func() {
					c := coarse.TraceConnectStart(ctx, pgx.TraceConnectStartData{})
					coarse.TraceConnectEnd(c, pgx.TraceConnectEndData{})
				}
				runAttr = func() {
					c := combined.TraceConnectStart(ctx, pgx.TraceConnectStartData{})
					combined.TraceConnectEnd(c, pgx.TraceConnectEndData{})
				}
			}
			for range 10 {
				runBase()
				runAttr()
			}
			runtime.GC()
			base := testing.AllocsPerRun(1000, runBase)
			attr := testing.AllocsPerRun(1000, runAttr)
			// Keep child callbacks inside the 10,000 queued statements above.
			// Calibration would eventually measure the exhausted-batch gap.
			baseBytes := allocatedBytesPerOperation(runBase)
			attrBytes := allocatedBytesPerOperation(runAttr)
			want := float64(1)
			if kind == "child" || kind == "connect" {
				want = 0
			}
			t.Logf("coarse_allocs=%.0f attributed_allocs=%.0f added=%.0f coarse_bytes=%d attributed_bytes=%d added_bytes=%d", base, attr, attr-base, baseBytes, attrBytes, attrBytes-baseBytes)
			if attr-base != want {
				t.Errorf("added allocations=%.0f want=%.0f", attr-base, want)
			}
			if attrBytes-baseBytes > int64(want)*96 {
				t.Errorf("added bytes=%d want<=%.0f", attrBytes-baseBytes, want*96)
			}
		})
	}
}

type setupScope struct {
	queries, acquires  uint64
	queryNS, acquireNS uint64
}

func (p *setupScope) Start(_ queryattr.Identity, k queryattr.Kind, n uint64, _ queryattr.Sizes, _ int64) {
	if k == queryattr.Acquire {
		p.acquires++
	} else {
		p.queries += n
	}
}
func (p *setupScope) Finish(o queryattr.Observation) {
	if o.Kind == queryattr.Acquire {
		p.acquireNS += o.NS
	} else {
		p.queryNS += o.NS
	}
}
func TestSetupTimeStaysInsideAcquireAndOutsideRequest(t *testing.T) {
	s := queryattr.NewStore()
	pool := memoryPool(t, true, s)
	p := &setupScope{}
	ctx := queryattr.WithOwnership(t.Context(), queryattr.RequestOrigin, p)
	c, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c.Release()
	got := s.Totals()
	if got.Started != 1 || got.Completed != 1 || got.Acquires != 1 || p.queries != 0 || p.queryNS != 0 || p.acquires != 1 || p.acquireNS < got.QueryNS {
		t.Fatalf("setup totals=%+v request=%+v", got, p)
	}
	for _, r := range s.Snapshot() {
		if r.Identity.Caller.Function == "connection_setup" && r.Counters.Started != 1 {
			t.Fatalf("setup row=%+v", r)
		}
	}
	held, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = pool.Acquire(canceled)
	held.Release()
	if err == nil {
		t.Fatal("canceled acquire succeeded")
	}
	got = s.Totals()
	if got.Started != 1 || got.FailedAcquires != 1 {
		t.Fatalf("failed acquire totals=%+v", got)
	}
}
