package logstream

import (
	"context"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/database/queryattr"
)

type insertionScope struct{ starts int }

func (p *insertionScope) Start(queryattr.Identity, queryattr.Kind, uint64, queryattr.Sizes, int64) {
	p.starts++
}
func (*insertionScope) Finish(queryattr.Observation) {}

func TestDrainAttemptHasInternalOrigin(t *testing.T) {
	s := queryattr.NewStore()
	tr := queryattr.Tracer{Store: s}
	p := &insertionScope{}
	parent := queryattr.WithOwnership(t.Context(), queryattr.RequestOrigin, p)
	want := &pgconn.PgError{Code: "23505", Message: "PRIVATE_ERROR", Detail: "PRIVATE_DETAIL", Hint: "PRIVATE_HINT"}
	d := Drain[int]{Insert: func(ctx context.Context, batch []int) error {
		if len(batch) != 1 || batch[0] != 7 {
			t.Fatalf("batch=%v, want [7]", batch)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("insert has no deadline")
		}
		c := tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "INSERT INTO private_table VALUES ('PRIVATE_SQL')", Args: []any{"PRIVATE_ARG"}})
		tr.TraceQueryEnd(c, nil, pgx.TraceQueryEndData{Err: want})
		return want
	}}
	if got := d.attempt(parent, []int{7}); !reflect.ValueOf(got).Equal(reflect.ValueOf(want)) {
		t.Fatalf("error=%v, want original error", got)
	}
	if p.starts != 0 {
		t.Fatalf("request starts=%d, want 0", p.starts)
	}
	for _, r := range s.Snapshot() {
		if r.Identity.Caller.Function == "telemetry_persistence" && (r.Counters.Completed != 1 || r.Counters.Observed != 1 || r.Counters.QueryNS == 0) {
			t.Fatalf("internal row=%+v, want one observed timed statement", r)
		}
	}
}
