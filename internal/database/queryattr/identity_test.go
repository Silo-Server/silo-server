package queryattr

import (
	"context"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/envutil"
)

func genericIdentity[T any](v T) T { return v }
func TestCallerCacheAndRuntimeNames(t *testing.T) {
	s := newStore(4, 4, 2)
	pc := reflect.ValueOf(envutil.Bool).Pointer() + 1
	a := s.resolvePC(pc)
	b := s.resolvePC(pc)
	if a.Function != "envutil.Bool" || a != b || a.Line == 0 {
		t.Fatalf("caller=%+v cached=%+v want envutil.Bool", a, b)
	}
	s.resolvePC(reflect.ValueOf(envutil.Truthy).Pointer() + 1)
	s.resolvePC(reflect.ValueOf(envutil.IsSet).Pointer() + 1)
	if len(s.pcs) > 2 {
		t.Fatalf("PC entries=%d want<=2", len(s.pcs))
	}
	if after := s.resolvePC(pc); after != a {
		t.Fatalf("cache churn caller=%+v want=%+v", after, a)
	}
	pc = reflect.ValueOf(genericIdentity[int]).Pointer()
	fn := runtime.FuncForPC(pc).Name()
	if got := displayName(fn); got != strings.TrimPrefix(strings.TrimPrefix(fn, modulePrefix), "internal/") {
		t.Fatalf("generic display=%q runtime=%q", got, fn)
	}
	if !skipFunction(modulePrefix+"internal/database/postgresTracer.TraceQueryStart") || !skipFunction("runtime.goexit") || !skipFunction("github.com/jackc/pgx/v5.(*Conn).Exec") || skipFunction(modulePrefix+"internal/catalog.queryDistinctStrings") {
		t.Fatal("caller skip policy mismatch")
	}
}
func TestLabelShapesAndWarnings(t *testing.T) {
	s := NewStore()
	tr := Tracer{Store: s}
	ctx := WithLabel(context.Background(), LabelPersonalCollectionCounts)
	for n := 1; n <= 66; n++ {
		q := tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: strings.Repeat("SELECT 1 UNION ALL ", n) + "SELECT 1"})
		tr.TraceQueryEnd(q, nil, pgx.TraceQueryEndData{})
	}
	warns := s.DrainWarnings()
	if len(warns) != 1 || warns[0].Caller != personalCollectionCountsLabel || warns[0].Kind != "shapes" {
		t.Fatalf("warnings=%+v want one label shape warning", warns)
	}
	if got := s.DrainWarnings(); len(got) != 0 {
		t.Fatalf("repeated warnings=%+v", got)
	}
	s = newStore(1, 2, 2)
	for i := uint64(1); i <= 5; i++ {
		s.lookup(Caller{Function: "caller"}, i, Statement)
	}
	warns = s.DrainWarnings()
	if len(warns) != 1 || warns[0].Kind != "overflow" {
		t.Fatalf("overflow warnings=%+v", warns)
	}
	if got := s.DrainWarnings(); len(got) != 0 {
		t.Fatalf("repeated overflow=%+v", got)
	}
}
