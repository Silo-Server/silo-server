package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/database/queryattr"
)

type partitionScope struct{ started int }

func (p *partitionScope) Start(queryattr.Identity, queryattr.Kind, uint64, queryattr.Sizes, int64) {
	p.started++
}
func (*partitionScope) Finish(queryattr.Observation) {}

type partitionFixture struct {
	tr  queryattr.Tracer
	err error
}

func (p partitionFixture) EnsureFuturePartitions(ctx context.Context) error {
	c := p.tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "CREATE TABLE private_partition (private_column text)"})
	p.tr.TraceQueryEnd(c, nil, pgx.TraceQueryEndData{Err: p.err})
	return p.err
}

func TestStartupPartitionMarkerSites(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range []struct{ receiver, parent string }{{"opsPM", "ctx"}, {"activityPM", "appCtx"}} {
		t.Run(site.receiver, func(t *testing.T) {
			bound := false
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok || id.Name != "ensureTelemetryPartitions" || len(call.Args) != 2 {
					return true
				}
				parent, ok := call.Args[0].(*ast.Ident)
				if !ok {
					return true
				}
				receiver, ok := call.Args[1].(*ast.Ident)
				if ok && receiver.Name == site.receiver && parent.Name == site.parent {
					bound = true
				}
				return true
			})
			if !bound {
				t.Errorf("%s startup call does not use telemetry partition boundary", site.receiver)
			}
			s := queryattr.NewStore()
			p := &partitionScope{}
			ctx := queryattr.WithOwnership(t.Context(), queryattr.RequestOrigin, p)
			want := &pgconn.PgError{Code: "42501", Message: "PRIVATE_ERROR", Detail: "PRIVATE_DETAIL"}
			if got := ensureTelemetryPartitions(ctx, partitionFixture{queryattr.Tracer{Store: s}, want}); !reflect.ValueOf(got).Equal(reflect.ValueOf(want)) {
				t.Fatalf("partition error=%v, want original", got)
			}
			if p.started != 0 {
				t.Fatalf("request starts=%d, want 0", p.started)
			}
			for _, row := range s.Snapshot() {
				if row.Identity.Caller.Function == "telemetry_persistence" && row.Counters.Completed != 1 {
					t.Fatalf("internal completed=%d, want 1", row.Counters.Completed)
				}
			}
		})
	}
}

type warningHandler struct {
	entered chan slog.Record
	release chan struct{}
	calls   atomic.Int32
}

func (*warningHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *warningHandler) Handle(_ context.Context, r slog.Record) error {
	h.calls.Add(1)
	h.entered <- r.Clone()
	<-h.release
	return errors.New("fixture output failure")
}
func (h *warningHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *warningHandler) WithGroup(string) slog.Handler      { return h }

func warnShapes(tr queryattr.Tracer) {
	ctx := queryattr.WithLabel(context.Background(), queryattr.LabelPersonalCollectionCounts)
	for n := 1; n <= queryattr.ShapeThreshold+1; n++ {
		c := tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: strings.Repeat("SELECT 'PRIVATE_SQL' UNION ALL ", n) + "SELECT $1", Args: []any{"PRIVATE_ARG"}})
		tr.TraceQueryEnd(c, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "23505", Message: "PRIVATE_ERROR", Detail: "PRIVATE_DETAIL", Hint: "PRIVATE_HINT"}})
	}
}

func TestQueryWarningsDrainAwayFromCallbacks(t *testing.T) {
	s := queryattr.NewStore()
	tr := queryattr.Tracer{Store: s}
	h := &warningHandler{entered: make(chan slog.Record, 2), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ticks := make(chan time.Time, 2)
	done := make(chan struct{})
	go func() { defer close(done); runQueryAttributionWarnings(ctx, ticks, s, slog.New(h)) }()
	defer func() { close(h.release); cancel(); <-done }()
	warnShapes(tr)
	if h.calls.Load() != 0 {
		t.Fatal("warning logged from a callback")
	}
	ticks <- time.Now()
	var r slog.Record
	select {
	case r = <-h.entered:
	case <-time.After(time.Second):
		t.Fatal("service drain did not log pending shape warning")
	}
	if r.Level != slog.LevelWarn {
		t.Fatalf("level=%v, want WARN", r.Level)
	}
	attrs := map[string]any{}
	r.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value.Any(); return true })
	if attrs["caller"] != "catalog.personal_collection_counts" || attrs["scheme"] != int64(queryattr.Scheme) {
		t.Fatalf("warning attrs=%v, want reviewed label and scheme", attrs)
	}
	callbackDone := make(chan struct{})
	go func() { warnShapes(tr); close(callbackDone) }()
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("blocked warning sink blocked pgx callbacks")
	}
	if got := s.DrainWarnings(); len(got) != 0 {
		t.Fatalf("warnings=%v, want no retry after failing sink", got)
	}
	if h.calls.Load() != 1 {
		t.Fatalf("sink calls=%d, want 1", h.calls.Load())
	}
	for key, value := range attrs {
		if strings.Contains(key, "PRIVATE_") || strings.Contains(strings.TrimSpace(r.Message), "PRIVATE_") || strings.Contains(strings.TrimSpace(strings.Join([]string{key, slog.AnyValue(value).String()}, " ")), "PRIVATE_") {
			t.Fatalf("warning leaked sentinel: %s=%v", key, value)
		}
	}
}

func TestQueryWarningsDrainBothFlagsOnShutdown(t *testing.T) {
	s := queryattr.NewStore()
	tr := queryattr.Tracer{Store: s}
	ctx := queryattr.WithLabel(t.Context(), queryattr.LabelPersonalCollectionCounts)
	for n := 0; n <= queryattr.IdentityCap; n++ {
		c := tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: fmt.Sprintf("SELECT column%d", n)})
		tr.TraceQueryEnd(c, nil, pgx.TraceQueryEndData{})
	}
	h := &warningHandler{entered: make(chan slog.Record, 2), release: make(chan struct{})}
	close(h.release)
	stop := startLogDrain(func(ctx context.Context) { runQueryAttributionWarnings(ctx, nil, s, slog.New(h)) })
	stop()
	if got := h.calls.Load(); got != 2 {
		t.Fatalf("shutdown warning lines=%d, want shape and overflow", got)
	}
	if next := s.DrainWarnings(); len(next) != 0 {
		t.Fatalf("pending warnings=%v, want drained", next)
	}
}

func TestQueryAttributionStartupState(t *testing.T) {
	queryattr.Initialize()
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	var output bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	stop := startQueryAttributionWarnings()
	stop()
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	wantEnabled := os.Getenv("SILO_QUERYATTR_WARN_CHILD") != "1"
	if record["enabled"] != wantEnabled || record["scheme"] != float64(queryattr.Scheme) || record["component"] != "database" {
		t.Fatalf("startup record=%v, want enabled=%v and scheme", record, wantEnabled)
	}
	if wantEnabled {
		cmd := exec.Command(os.Args[0], "-test.run=^TestQueryAttributionStartupState$")
		cmd.Env = append(os.Environ(), "SILO_QUERYATTR_WARN_CHILD=1", "SILO_QUERY_ATTRIBUTION_ENABLED=false")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("disabled startup: %v\n%s", err, out)
		}
	}
}
