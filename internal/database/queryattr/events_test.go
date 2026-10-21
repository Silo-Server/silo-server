package queryattr

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/time/rate"
)

func resetEvents() {
	eventLimiter = rate.NewLimiter(rate.Every(eventInterval), eventBurst)
	suppressedTotal.Store(0)
	pending.Store(0)
}
func TestEventThresholds(t *testing.T) {
	cases := []struct {
		name       string
		ns         int64
		args       []any
		kind       Kind
		statements int
		cause      string
	}{
		{"slow_equal", int64(100 * time.Millisecond), nil, KindQuery, 1, ""},
		{"slow_above", int64(100*time.Millisecond) + 1, nil, KindQuery, 1, "slow"},
		{"args_equal", 1, make([]any, 1000), KindQuery, 1, ""}, {"args_above", 1, make([]any, 1001), KindQuery, 1, "large"},
		{"array_equal", 1, []any{make([]int, 1000)}, KindQuery, 1, ""}, {"array_above", 1, []any{make([]int, 1001)}, KindQuery, 1, "large"},
		{"batch_equal", 1, nil, KindBatch, 100, ""}, {"batch_above", 1, nil, KindBatch, 101, "large"},
		{"both", int64(time.Second), make([]any, 1001), KindQuery, 1, "slow,large"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setup(t)
			resetEvents()
			buf := capture(t)
			ctx := context.Background()
			op := Begin(ctx, tc.kind, "SELECT 1", tc.args, tc.statements)
			op.end(ctx, nil, tc.ns, time.Unix(100, 0))
			lines := logLines(t, buf)
			want := 0
			if tc.cause != "" {
				want = 1
			}
			if len(lines) != want {
				t.Fatalf("events = %d, want %d", len(lines), want)
			}
			if want == 1 {
				requireAttr(t, lines[0], "cause", tc.cause)
			}
		})
	}
}
func TestEventFields(t *testing.T) {
	setup(t)
	resetEvents()
	buf := capture(t)
	ctx, _ := StartRequest(context.Background())
	op := Begin(ctx, KindQuery, "SELECT 1", []any{[]string{"value"}, pgtype.Int4{Int32: 1, Valid: true}}, 7)
	op.end(ctx, context.Canceled, int64(100*time.Millisecond)+1, time.Unix(100, 0))
	lines := logLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("events = %d, want 1", len(lines))
	}
	m := lines[0]
	for k, v := range map[string]any{"msg": "slow or large database query", "level": "INFO", "component": "database", "cause": "slow", "max_array": float64(-1), "args": float64(2), "outcome": "canceled", "request": true, "statements": float64(1), "site": "unknown", "suppressed": float64(0)} {
		requireAttr(t, m, k, v)
	}
	if shape, ok := m["shape"].(string); !ok || len(shape) != 16 {
		t.Errorf("shape = %v, want 16 hex digits", m["shape"])
	}
	if len(m) != 14 {
		t.Errorf("event keys = %d, want 14", len(m))
	}
}
func TestEventSuppression(t *testing.T) {
	setup(t)
	resetEvents()
	buf := capture(t)
	now := time.Unix(100, 0)
	s := summary{previous: now}
	s.summarize(now)
	buf.Reset()
	ctx := context.Background()
	for range 12 {
		op := Begin(ctx, KindBatch, "", nil, 101)
		op.end(ctx, nil, 1, now)
	}
	if got := len(logLines(t, buf)); got != 10 {
		t.Errorf("initial events = %d, want 10", got)
	}
	op := Begin(ctx, KindBatch, "", nil, 101)
	op.end(ctx, nil, 1, now.Add(time.Second))
	s.summarize(now.Add(time.Second))
	lines := logLines(t, buf)
	if len(lines) != 12 {
		t.Fatalf("lines = %d, want 11 events plus summary", len(lines))
	}
	requireAttr(t, lines[10], "suppressed", float64(2))
	requireAttr(t, lines[11], "events_suppressed", float64(2))
	buf.Reset()
	s.summarize(now.Add(2 * time.Second))
	lines = logLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("idle window lines = %d, want 1 summary", len(lines))
	}
	requireAttr(t, lines[0], "events_suppressed", float64(0))
}

func TestEventArgumentSizes(t *testing.T) {
	for _, tc := range []struct {
		name           string
		args           []any
		count, largest int
	}{
		{"pgx_options", []any{pgx.QueryExecModeDescribeExec, pgx.QueryResultFormats{pgx.BinaryFormatCode}, pgx.QueryResultFormatsByOID{23: pgx.BinaryFormatCode}, []string{"value"}}, 1, 1},
		{"nested_slice", []any{[][]string{{"value"}}, []string{"a", "b"}}, 2, -1},
		{"query_rewriter", []any{pgx.NamedArgs{"value": "private"}}, -1, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setup(t)
			resetEvents()
			buf := capture(t)
			ctx := context.Background()
			op := Begin(ctx, KindQuery, "SELECT 1", tc.args, 1)
			op.end(ctx, nil, int64(time.Second), time.Unix(100, 0))
			lines := logLines(t, buf)
			if len(lines) != 1 {
				t.Fatalf("events = %d, want 1", len(lines))
			}
			requireAttr(t, lines[0], "args", float64(tc.count))
			requireAttr(t, lines[0], "max_array", float64(tc.largest))
		})
	}
}
func TestQuietWritesNoEvent(t *testing.T) {
	setup(t)
	resetEvents()
	buf := capture(t)
	ctx := Quiet(context.Background())
	op := Begin(ctx, KindBatch, "", nil, 101)
	op.end(ctx, nil, 1, time.Unix(100, 0))
	if buf.Len() != 0 || suppressedTotal.Load() != 0 {
		t.Error("quiet operation wrote or suppressed an event")
	}
	if op.row == nil || op.row.calls.Load() != 1 {
		t.Error("quiet row calls must rise by one")
	}
}
