package queryattr

import (
	"context"
	"fmt"
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
