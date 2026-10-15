package queryattr

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestKindsOutcomesAndStaleEnds(t *testing.T) {
	s := NewStore()
	tr := Tracer{Store: s}
	ctx := context.Background()
	a := tr.TraceAcquireStart(ctx, nil, pgxpool.TraceAcquireStartData{})
	tr.TraceAcquireEnd(a, nil, pgxpool.TraceAcquireEndData{Err: context.DeadlineExceeded})
	q := tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	tr.TraceBatchEnd(q, nil, pgx.TraceBatchEndData{}) // Must not consume the query.
	if got := s.Totals(); got.Started != 1 || got.Completed != 0 || got.FailedAcquires != 1 || got.AttributionFailures != 1 {
		t.Fatalf("open totals=%+v", got)
	}
	tr.TraceQueryEnd(q, nil, pgx.TraceQueryEndData{CommandTag: pgconn.NewCommandTag("SELECT 3")})
	b := &pgx.Batch{}
	b.Queue("SELECT 1")
	b.Queue("SELECT 2")
	b.Queue("SELECT 3")
	old := tr.TraceBatchStart(ctx, nil, pgx.TraceBatchStartData{Batch: b})
	tr.TraceBatchQuery(old, nil, pgx.TraceBatchQueryData{SQL: "SELECT 1"})
	tr.TraceBatchQuery(old, nil, pgx.TraceBatchQueryData{SQL: "SELECT 2", Err: &pgconn.PgError{Code: "23505"}})
	tr.TraceBatchEnd(old, nil, pgx.TraceBatchEndData{Err: errors.New("sentinel")})
	newer := tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT 4"})
	tr.TraceBatchEnd(old, nil, pgx.TraceBatchEndData{})
	tr.TraceQueryEnd(newer, nil, pgx.TraceQueryEndData{Err: context.Canceled})
	got := s.Totals()
	if got.Started != 5 || got.Completed != 5 || got.Observed != 4 || got.Unobserved != 1 || got.Batches != 1 || got.Envelopes != 1 || got.DuplicateEnds != 1 || got.Rows != 3 {
		t.Fatalf("totals=%+v", got)
	}
	for _, r := range s.Snapshot() {
		var n uint64
		for _, v := range r.Counters.Outcomes {
			n += v
		}
		if n != r.Counters.Observed {
			t.Errorf("row %s observed=%d outcomes=%d", r.Identity.Caller.Function, r.Counters.Observed, n)
		}
	}
	if got.Outcomes[Success] != 2 || got.Outcomes[Canceled] != 1 || got.Outcomes[classIndex("23")] != 1 {
		t.Fatalf("outcomes=%v", got.Outcomes)
	}
}

func TestOutcomeClasses(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{nil, Success}, {context.Canceled, Canceled}, {context.DeadlineExceeded, Deadline},
		{fmt.Errorf("outer: %w", &pgconn.PgError{Code: "40001"}), classIndex("40")},
		{errors.Join(context.Canceled, &pgconn.PgError{Code: "23505"}), Canceled},
		{&pgconn.PgError{Code: "ZZ999", Message: "private", Detail: "private", Hint: "private"}, PostgresOther},
		{&pgconn.PgError{Code: "2350"}, PostgresOther},
		{&pgconn.PgError{Code: "23!@#"}, PostgresOther}, {errors.New("private"), Client},
		{&net.OpError{Op: "write", Net: "tcp", Err: errors.New("private")}, Network},
	} {
		if got := outcome(tc.err); got != tc.want {
			t.Errorf("outcome=%d want=%d", got, tc.want)
		}
	}
}

func TestConcurrentIndependentReconciliation(t *testing.T) {
	s := NewStore()
	tr := Tracer{Store: s}
	const workers = 16
	const operations = 100
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Go(func() {
			for j := 0; j < operations; j++ {
				ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT $1", Args: []any{j}})
				tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
			}
		})
	}
	wg.Wait()
	got := s.Totals()
	if got.Started != workers*operations || got.Completed != got.Started || got.Observed != got.Completed || got.Outcomes[Success] != got.Observed {
		t.Fatalf("independent expected=%d; got=%+v", workers*operations, got)
	}
}

func TestCapacityClassesAndWarnings(t *testing.T) {
	s := newStore(2, 1, 2)
	tr := Tracer{Store: s}
	ctx := WithLabel(context.Background(), labelTest)
	unavailableName := s.reserved[4].identity.Caller.Function
	for _, sql := range []string{"SELECT 1", "SELECT 1+1"} {
		r := tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: sql})
		tr.TraceQueryEnd(r, nil, pgx.TraceQueryEndData{})
	}
	errorsByClass := []error{nil, context.Canceled, context.DeadlineExceeded, &pgconn.PgError{Code: "23505"}, &pgconn.PgError{Code: "40001"}, &pgconn.PgError{Code: "ZZ999"}, errors.New("private"), net.ErrClosed}
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{labelName(labelTest), ctx},
		{unavailableName, WithLabel(context.Background(), LabelPersonalCollectionCounts)},
	} {
		for _, err := range errorsByClass {
			r := tr.TraceQueryStart(tc.ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT 1+2+3"})
			tr.TraceQueryEnd(r, nil, pgx.TraceQueryEndData{Err: err})
		}
		var found bool
		for _, row := range s.Snapshot() {
			if row.Identity.Caller.Function != tc.name || row.Identity.Form != FunctionOnly && tc.name != unavailableName {
				continue
			}
			found = true
			var want [ClassCount]uint64
			for _, err := range errorsByClass {
				want[outcome(err)]++
			}
			if row.Counters.Started != uint64(len(errorsByClass)) || row.Counters.Completed != uint64(len(errorsByClass)) || row.Counters.Outcomes != want {
				t.Errorf("fallback %s started=%d completed=%d outcomes=%v want=%d/%d/%v", tc.name, row.Counters.Started, row.Counters.Completed, row.Counters.Outcomes, len(errorsByClass), len(errorsByClass), want)
			}
		}
		if !found {
			t.Errorf("missing fallback row %s", tc.name)
		}
	}
	got := s.Totals()
	if got.Started != 18 || got.Completed != 18 || got.Overflow != 16 || got.OutcomeDetailLoss != 0 {
		t.Fatalf("capacity totals=%+v want starts/completions=18 overflow=16 detail_loss=0", got)
	}
	if len(s.identities) != 2 || len(s.functions) != 1 {
		t.Fatalf("tables=%d,%d want=2,1", len(s.identities), len(s.functions))
	}
}

func TestReservedGapRows(t *testing.T) {
	s := NewStore()
	tr := Tracer{Store: s}
	ctx := context.Background()
	a := tr.TraceAcquireStart(ctx, nil, pgxpool.TraceAcquireStartData{})
	tr.TraceAcquireEnd(a, nil, pgxpool.TraceAcquireEndData{Err: context.DeadlineExceeded})
	tr.CountEmptyBatch()
	conn, _ := wireConnection(t, tr, pgx.QueryExecModeSimpleProtocol)
	copyCtx := tr.TraceCopyFromStart(ctx, conn, pgx.TraceCopyFromStartData{})
	if tr.ResolveCopySetup(conn, 0, context.Canceled) != copyCtx {
		t.Fatal("copy setup did not finish its record")
	}
	batch := &pgx.Batch{}
	batch.Queue("SELECT 1")
	batch.Queue("SELECT 2")
	b := tr.TraceBatchStart(ctx, conn, pgx.TraceBatchStartData{Batch: batch})
	tr.TraceBatchEnd(b, conn, pgx.TraceBatchEndData{})
	tr.TraceQueryStart(ctx, conn, pgx.TraceQueryStartData{SQL: "SELECT 3"})
	tr.BeforeClose(conn)
	q := tr.TraceQueryStart(ctx, conn, pgx.TraceQueryStartData{SQL: "SELECT 4"})
	tr.TraceBatchEnd(q, conn, pgx.TraceBatchEndData{})
	tr.TraceQueryEnd(q, conn, pgx.TraceQueryEndData{})
	tr.TraceQueryEnd(q, conn, pgx.TraceQueryEndData{})
	wants := map[string]uint64{
		s.reserved[7].identity.Caller.Function:  1,
		s.reserved[8].identity.Caller.Function:  1,
		s.reserved[9].identity.Caller.Function:  1,
		s.reserved[10].identity.Caller.Function: 2,
		s.reserved[11].identity.Caller.Function: 1,
		s.reserved[12].identity.Caller.Function: 4,
		s.reserved[13].identity.Caller.Function: 1,
		s.reserved[14].identity.Caller.Function: 1,
	}
	for _, r := range s.Snapshot() {
		want, ok := wants[r.Identity.Caller.Function]
		if !ok {
			continue
		}
		var got uint64
		switch r.Identity.Caller.Function {
		case "failed_acquire":
			got = r.Counters.FailedAcquires
		case "empty_batch":
			got = r.Counters.EmptyBatches
		case "copy_setup_failure":
			got = r.Counters.CopySetupFailures
		case "unobserved_batch":
			got = r.Counters.UnobservedBatch
		case "connection_closed_unobserved":
			got = r.Counters.ConnectionClosed
		case "origin_untracked":
			got = r.Counters.OriginUntracked
		case "attribution_failure":
			got = r.Counters.AttributionFailures
		case "duplicate_end":
			got = r.Counters.DuplicateEnds
		}
		if got != want {
			t.Errorf("reserved row %s count=%d want=%d", r.Identity.Caller.Function, got, want)
		}
		if r.Counters.Started != 0 || r.Counters.Completed != 0 || r.Counters.Outcomes != [ClassCount]uint64{} {
			t.Errorf("gap row %s fabricated statements or outcomes: %+v", r.Identity.Caller.Function, r.Counters)
		}
		delete(wants, r.Identity.Caller.Function)
	}
	if len(wants) != 0 {
		t.Fatalf("missing reserved rows: %v", wants)
	}
	if got := s.Totals(); got.Started != 5 || got.Completed != 5 || got.Unobserved != 3 {
		t.Fatalf("node totals=%+v want starts/completions=5 unobserved=3", got)
	}
}
