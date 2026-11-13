package queryattr

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"
)

func groupRows(t *testing.T, m map[string]any, key string) []map[string]any {
	t.Helper()
	if m[key] == nil {
		return nil
	}
	g := m[key].(map[string]any)
	rows := make([]map[string]any, 0, len(g))
	for i := 1; i <= len(g); i++ {
		rows = append(rows, g[strconv.Itoa(i)].(map[string]any))
	}
	return rows
}
func TestSummaryWindows(t *testing.T) {
	setup(t)
	resetEvents()
	buf := capture(t)
	now := time.Unix(100, 0)
	s := summary{run: "run-id", build: "build-id", previous: now}
	ctx := context.Background()
	a := namedOp(ctx, "a", "x")
	a.end(ctx, nil, 5, now)
	a.end(ctx, nil, 2, now)
	a.end(ctx, nil, 2, now)
	b := namedOp(ctx, "b", "y")
	b.end(ctx, context.Canceled, 7, now)
	b.end(ctx, nil, 7, now)
	c := namedOp(ctx, "a", "z")
	c.end(ctx, nil, 7, now)
	c.end(ctx, nil, 7, now)
	for _, tc := range []struct {
		site, shape string
		durations   []int64
	}{
		{"c", "x", []int64{20}},
		{"d", "x", []int64{2, 2, 2}},
		{"a", "y", []int64{7, 7}},
		{"e", "x", []int64{14}},
	} {
		op := namedOp(ctx, tc.site, tc.shape)
		for _, ns := range tc.durations {
			op.end(ctx, nil, ns, now)
		}
	}
	s.summarize(now.Add(time.Second))
	a.end(ctx, nil, 2, now)
	a.end(ctx, nil, 3, now)
	s.summarize(now.Add(3 * time.Second))
	lines := logLines(t, buf)
	if len(lines) != 2 {
		t.Fatalf("summary lines = %d, want 2", len(lines))
	}
	for k, v := range map[string]any{"run": "run-id", "build": "build-id", "interval_ns": float64(time.Second), "queries": float64(14), "query_ns": float64(91), "errors": float64(1)} {
		requireAttr(t, lines[0], k, v)
	}
	for key, order := range map[string][]string{
		"top_time":  {"c/x", "a/y", "a/z", "b/y", "e/x", "a/x", "d/x"},
		"top_count": {"a/x", "d/x", "a/y", "a/z", "b/y", "c/x", "e/x"},
	} {
		rows := groupRows(t, lines[0], key)
		if len(rows) != len(order) {
			t.Fatalf("%s rows = %d, want %d", key, len(rows), len(order))
		}
		for i, want := range order {
			got := rows[i]["site"].(string) + "/" + rows[i]["shape"].(string)
			if got != want {
				t.Errorf("%s[%d] = %s, want %s", key, i, got, want)
			}
		}
	}
	for k, v := range map[string]any{"interval_ns": float64(2 * time.Second), "queries": float64(2), "query_ns": float64(5), "errors": float64(0)} {
		requireAttr(t, lines[1], k, v)
	}
	rows := groupRows(t, lines[1], "top_count")
	if len(rows) != 1 {
		t.Fatalf("second rows = %d, want 1", len(rows))
	}
	requireAttr(t, rows[0], "site", "a")
	requireAttr(t, rows[0], "calls", float64(2))
}

func TestSummaryTopTen(t *testing.T) {
	setup(t)
	resetEvents()
	buf := capture(t)
	now := time.Unix(100, 0)
	s := summary{previous: now}
	ctx := context.Background()
	for i := range 12 {
		op := namedOp(ctx, fmt.Sprintf("site%02d", i), "x")
		op.end(ctx, nil, int64(i+1), now)
	}
	s.summarize(now.Add(time.Second))
	lines := logLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("summary lines = %d, want 1", len(lines))
	}
	requireAttr(t, lines[0], "queries", float64(12))
	requireAttr(t, lines[0], "query_ns", float64(78))
	for _, key := range []string{"top_time", "top_count"} {
		rows := groupRows(t, lines[0], key)
		if len(rows) != 10 {
			t.Fatalf("%s rows = %d, want 10", key, len(rows))
		}
		for i, row := range rows {
			requireAttr(t, row, "site", fmt.Sprintf("site%02d", 11-i))
		}
	}
}
func TestSummaryKeys(t *testing.T) {
	setup(t)
	resetEvents()
	buf := capture(t)
	now := time.Unix(100, 0)
	s := summary{previous: now}
	op := namedOp(context.Background(), "a", "x")
	op.end(context.Background(), nil, 1, now)
	s.summarize(now.Add(time.Second))
	lines := logLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("summary lines = %d, want 1", len(lines))
	}
	keys := func(m map[string]any) []string {
		ks := make([]string, 0, len(m))
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return ks
	}
	want := []string{"time", "level", "msg", "component", "run", "build", "interval_ns", "queries", "query_ns", "errors", "events_suppressed", "top_time", "top_count"}
	sort.Strings(want)
	if got := keys(lines[0]); !reflect.DeepEqual(got, want) {
		t.Errorf("summary keys = %v, want %v", got, want)
	}
	for _, key := range []string{"top_time", "top_count"} {
		for _, r := range groupRows(t, lines[0], key) {
			want := []string{"calls", "errors", "ns", "shape", "site"}
			if got := keys(r); !reflect.DeepEqual(got, want) {
				t.Errorf("row keys = %v, want %v", got, want)
			}
		}
	}
}
func TestSummaryConcurrentSites(t *testing.T) {
	setup(t)
	resetEvents()
	buf := capture(t)
	now := time.Unix(100, 0)
	s := summary{previous: now}
	s.summarize(now)
	var wg sync.WaitGroup
	done := make(chan struct{})
	start := make(chan struct{})
	for i := range 4 {
		wg.Go(func() {
			<-start
			for range 100 {
				op := namedOp(context.Background(), fmt.Sprintf("site%d", i), "x")
				op.end(context.Background(), nil, 1, now)
			}
		})
	}
	go func() { wg.Wait(); close(done) }()
	close(start)
loop:
	for {
		select {
		case <-done:
			break loop
		default:
			s.summarize(now.Add(time.Second))
		}
	}
	s.summarize(now.Add(2 * time.Second))
	counts := map[string]float64{}
	var queries float64
	for _, line := range logLines(t, buf) {
		queries += line["queries"].(float64)
		for _, r := range groupRows(t, line, "top_count") {
			counts[r["site"].(string)] += r["calls"].(float64)
		}
	}
	if queries != 400 {
		t.Errorf("window queries = %v, want 400", queries)
	}
	for i := range 4 {
		if got := counts[fmt.Sprintf("site%d", i)]; got != 100 {
			t.Errorf("site%d calls = %v, want 100", i, got)
		}
	}
}
