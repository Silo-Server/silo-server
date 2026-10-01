package nodepool

import (
	"encoding/json"
	"fmt"
	"testing"
)

// scratchStats builds the last_stats blob a node's health response produces for
// a scratch volume at the given fill, in the shape nodemetrics publishes.
func scratchStats(usedGB, totalGB float64, flags ...string) json.RawMessage {
	extra := ""
	for _, flag := range flags {
		extra += fmt.Sprintf(`,"%s":true`, flag)
	}
	return json.RawMessage(fmt.Sprintf(
		`{"system":{"disks":[{"path":"/transcode","used_gb":%g,"total_gb":%g,"scratch":true%s},`+
			`{"path":"/media","used_gb":10,"total_gb":100}]}}`, usedGB, totalGB, extra))
}

func TestScratchDiskFillPercent(t *testing.T) {
	tests := []struct {
		name    string
		stats   json.RawMessage
		wantPct int
		wantOK  bool
	}{
		{name: "measured", stats: scratchStats(50, 100), wantPct: 50, wantOK: true},
		{name: "at threshold", stats: scratchStats(95, 100), wantPct: 95, wantOK: true},
		// Floored rather than rounded, so "95%" means at least 95% used.
		{name: "just under threshold", stats: scratchStats(94.99, 100), wantPct: 94, wantOK: true},
		{name: "full", stats: scratchStats(100, 100), wantPct: 100, wantOK: true},
		{name: "no stats at all", stats: nil},
		{name: "unparseable", stats: json.RawMessage(`not json`)},
		{name: "stale numbers", stats: scratchStats(99, 100, "stale")},
		{name: "unmeasurable path", stats: scratchStats(0, 0, "unavailable")},
		{name: "zero capacity", stats: scratchStats(0, 0)},
		{
			name:  "no scratch entry",
			stats: json.RawMessage(`{"system":{"disks":[{"path":"/media","used_gb":99,"total_gb":100}]}}`),
		},
		{name: "no system section", stats: json.RawMessage(`{"gpu":[]}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pct, ok := scratchDiskFillPercent(&Node{LastStats: test.stats})
			if ok != test.wantOK {
				t.Fatalf("ok = %v, want %v (pct = %d)", ok, test.wantOK, pct)
			}
			if ok && pct != test.wantPct {
				t.Fatalf("pct = %d, want %d", pct, test.wantPct)
			}
		})
	}
	if _, ok := scratchDiskFillPercent(nil); ok {
		t.Fatal("a nil node reported a readable scratch fill")
	}
}

// The threshold is the whole contract of the guard, so the boundary is asserted
// on the predicate the planner actually calls.
func TestScratchPressuredThresholdBoundary(t *testing.T) {
	for _, test := range []struct {
		usedGB float64
		want   bool
	}{
		{usedGB: 94, want: false},
		{usedGB: 94.99, want: false},
		{usedGB: 95, want: true},
		{usedGB: 99.5, want: true},
	} {
		if got := scratchPressured(&Node{LastStats: scratchStats(test.usedGB, 100)}); got != test.want {
			t.Fatalf("scratchPressured(%g%% used) = %v, want %v", test.usedGB, got, test.want)
		}
	}
	// Missing evidence is never pressure: excluding a node on a fill we cannot
	// read would take capacity away for nothing.
	if scratchPressured(&Node{}) {
		t.Fatal("a node with no resource sample was treated as under scratch pressure")
	}
}
