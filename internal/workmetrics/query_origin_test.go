package workmetrics

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/database/queryattr"
)

type ownershipScope struct{ starts, finishes int }

func (p *ownershipScope) Start(queryattr.Identity, queryattr.Kind, uint64, queryattr.Sizes, int64) {
	p.starts++
}
func (p *ownershipScope) Finish(queryattr.Observation) { p.finishes++ }

func TestWorkTransfersOwnershipOnlyAtStart(t *testing.T) {
	s := queryattr.NewStore()
	tr := queryattr.Tracer{Store: s}
	p := &ownershipScope{}
	parent := queryattr.WithOwnership(t.Context(), queryattr.RequestOrigin, p)
	query := func(ctx context.Context) {
		c := tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
		tr.TraceQueryEnd(c, nil, pgx.TraceQueryEndData{})
	}
	Do(parent, query)
	ctx, run := Start(parent, "scan_libraries", time.Time{})
	defer run.Finish("success")
	restore := Profile(ctx)
	Do(ctx, query)
	Progress("scan_libraries")
	restore()
	query(parent)
	if p.starts != 2 || p.finishes != 2 {
		t.Fatalf("request starts/finishes=%d/%d, want 2/2", p.starts, p.finishes)
	}
	if got := s.Totals(); got.Started != 3 || got.Completed != 3 || got.OriginUntracked != 0 {
		t.Fatalf("totals=%+v, want three completed, no untracked work", got)
	}
	for _, row := range s.Snapshot() {
		if row.Identity.Caller.Function == "background" && row.Counters.Completed != 1 {
			t.Fatalf("background completed=%d, want 1", row.Counters.Completed)
		}
	}
}
