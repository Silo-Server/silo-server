package queryattr

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"strconv"
	"time"
)

type summary struct {
	run, build string
	previous   time.Time
	suppressed uint64
}

type deltaRow struct {
	site, shape string
	totals
}

// RunSummary writes a delta summary every 15 minutes and a final summary on
// cancellation, then returns. One caller owns summary snapshots per process.
// Off returns immediately. run and build identify the process and binary.
func RunSummary(ctx context.Context, run, build string) {
	if !Enabled() {
		return
	}
	s := summary{run: run, build: build, previous: time.Now()}
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			s.summarize(now)
		case <-ctx.Done():
			s.summarize(time.Now())
			return
		}
	}
}

func (s *summary) summarize(now time.Time) {
	var rows []deltaRow
	var total totals
	sites.Range(func(_, value any) bool {
		site, _ := value.(*site)
		site.mu.Lock()
		entries := make([]*row, 0, len(site.rows))
		for _, r := range site.rows {
			entries = append(entries, r)
		}
		site.mu.Unlock()
		for _, r := range entries {
			current := totals{r.calls.Load(), r.ns.Load(), r.errors.Load()}
			delta := totals{current.calls - r.previous.calls, current.ns - r.previous.ns, current.errors - r.previous.errors}
			r.previous = current
			if delta == (totals{}) {
				continue
			}
			rows = append(rows, deltaRow{site.name, r.shape, delta})
			total.calls += delta.calls
			total.ns += delta.ns
			total.errors += delta.errors
		}
		return true
	})
	suppressed := suppressedTotal.Load()
	attrs := []slog.Attr{
		slog.String("component", "database"), slog.String("run", s.run), slog.String("build", s.build), slog.Int64("interval_ns", now.Sub(s.previous).Nanoseconds()),
		slog.Uint64("queries", total.calls), slog.Uint64("query_ns", total.ns), slog.Uint64("errors", total.errors), slog.Uint64("events_suppressed", suppressed-s.suppressed),
	}
	slices.SortFunc(rows, func(a, b deltaRow) int {
		if n := cmp.Compare(b.ns, a.ns); n != 0 {
			return n
		}
		if n := cmp.Compare(b.calls, a.calls); n != 0 {
			return n
		}
		if n := cmp.Compare(a.site, b.site); n != 0 {
			return n
		}
		return cmp.Compare(a.shape, b.shape)
	})
	attrs = append(attrs, rankList("top_time", rows))
	slices.SortFunc(rows, func(a, b deltaRow) int {
		if n := cmp.Compare(b.calls, a.calls); n != 0 {
			return n
		}
		if n := cmp.Compare(b.ns, a.ns); n != 0 {
			return n
		}
		if n := cmp.Compare(a.site, b.site); n != 0 {
			return n
		}
		return cmp.Compare(a.shape, b.shape)
	})
	attrs = append(attrs, rankList("top_count", rows))
	s.previous = now
	s.suppressed = suppressed
	write(context.Background(), slog.LevelInfo, "database query summary", attrs...)
}

func rankList(key string, rows []deltaRow) slog.Attr {
	attrs := make([]slog.Attr, 0, min(len(rows), 10))
	for i, r := range rows[:min(len(rows), 10)] {
		attrs = append(attrs, slog.GroupAttrs(strconv.Itoa(i+1),
			slog.String(siteLabel, r.site), slog.String("shape", r.shape), slog.Uint64("calls", r.calls), slog.Uint64("ns", r.ns), slog.Uint64("errors", r.errors)))
	}
	return slog.GroupAttrs(key, attrs...)
}
