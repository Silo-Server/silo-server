package nodepool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// withJobsByKind gives a node a health report that breaks its jobs down by
// kind, the way CheckNode stores one. A nil breakdown is a node from before job
// kinds that reports only its total.
func withJobsByKind(t *testing.T, n *Node, total int, jobs map[JobKind]int) *Node {
	t.Helper()
	n.ActiveJobs = total
	if jobs != nil {
		stats, err := json.Marshal(map[string]any{"active_jobs_by_kind": jobs})
		if err != nil {
			t.Fatal(err)
		}
		n.LastStats = stats
	}
	return n
}

func TestTranscodeSelectionBalancesEachJobKind(t *testing.T) {
	cases := []struct {
		name   string
		aTotal int
		a      map[JobKind]int
		bTotal int
		b      map[JobKind]int
		kind   JobKind
		want   string
	}{
		{
			name:   "a video encode goes to the node with fewer encodes, though it runs more jobs",
			aTotal: 3, a: map[JobKind]int{JobKindVideoTranscode: 3},
			bTotal: 5, b: map[JobKind]int{JobKindRemux: 5},
			kind: JobKindVideoTranscode, want: "http://b",
		},
		{
			name:   "a remux goes to the node with fewer remuxes",
			aTotal: 3, a: map[JobKind]int{JobKindVideoTranscode: 3},
			bTotal: 5, b: map[JobKind]int{JobKindRemux: 5},
			kind: JobKindRemux, want: "http://a",
		},
		{
			name:   "level on the kind, the node with fewer jobs in all wins",
			aTotal: 3, a: map[JobKind]int{JobKindVideoTranscode: 1, JobKindAudioTranscode: 2},
			bTotal: 1, b: map[JobKind]int{JobKindVideoTranscode: 1},
			kind: JobKindVideoTranscode, want: "http://b",
		},
		{
			name:   "a node that reports no breakdown makes the pick fall back to total jobs",
			aTotal: 3, a: nil,
			bTotal: 5, b: map[JobKind]int{JobKindRemux: 5},
			kind: JobKindVideoTranscode, want: "http://a",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := withJobsByKind(t, transcodeNode(1, "http://a", nil, 0), tc.aTotal, tc.a)
			b := withJobsByKind(t, transcodeNode(2, "http://b", nil, 0), tc.bTotal, tc.b)
			f := newFixture(nil, []*Node{a, b})

			plan := f.planner.PlanRoute(RouteRequest{SessionID: "s", NeedsTranscode: true, JobKind: tc.kind})
			if plan.TranscodeNode == nil || plan.TranscodeNode.URL != tc.want {
				t.Fatalf("picked %+v, want %s", plan.TranscodeNode, tc.want)
			}
		})
	}
}

// Starts arrive faster than health reports. A reservation the node has not
// reported yet must count against its kind, or a burst of encodes all lands on
// the node that last looked idle.
func TestTranscodeSelectionCountsUnreportedReservationsOfTheKind(t *testing.T) {
	a := withJobsByKind(t, transcodeNode(1, "http://a", nil, 0), 2, map[JobKind]int{JobKindRemux: 2})
	b := withJobsByKind(t, transcodeNode(2, "http://b", nil, 0), 0, map[JobKind]int{})
	f := newFixture(nil, []*Node{a, b})

	first := f.planner.PlanRoute(RouteRequest{SessionID: "s1", NeedsTranscode: true, JobKind: JobKindVideoTranscode})
	second := f.planner.PlanRoute(RouteRequest{SessionID: "s2", NeedsTranscode: true, JobKind: JobKindVideoTranscode})
	if first.TranscodeNode == nil || first.TranscodeNode.URL != "http://b" ||
		second.TranscodeNode == nil || second.TranscodeNode.URL != "http://a" {
		t.Fatalf("picked %+v then %+v, want b then a", first.TranscodeNode, second.TranscodeNode)
	}
}

// The breakdown reaches selection only through the stored resource sample, so
// dropping it there would silently return every pick to total jobs.
func TestCheckNodeStoresJobsByKindForSelection(t *testing.T) {
	url := newStatsHealthNode(t, `{"status":"ok","active_jobs":2,
		"active_jobs_by_kind":{"video_transcode":2,"remux":0}}`)

	_, _, _, _, lastStats, _ := CheckNode(context.Background(), &Node{URL: url})
	jobs, ok := reportedJobsByKind(&Node{LastStats: lastStats})
	if !ok || jobs[JobKindVideoTranscode] != 2 || jobs[JobKindRemux] != 0 {
		t.Fatalf("stored breakdown = %v (ok %v), want video_transcode 2 from %s", jobs, ok, lastStats)
	}
}

// An oversized resource sample is dropped, but the breakdown routes work: losing
// it with the sample would return every placement this node is eligible for to
// total jobs, and the busiest node is the one whose sample grows largest.
func TestCheckNodeKeepsJobsByKindWhenTheSampleIsTooLarge(t *testing.T) {
	padding := strings.Repeat("x", maxLastStatsBytes)
	url := newStatsHealthNode(t, `{"status":"ok","active_jobs":2,
		"active_jobs_by_kind":{"video_transcode":2,"remux":0},
		"system":{"cpu_pct":41,"junk":"`+padding+`"}}`)

	_, _, _, _, lastStats, _ := CheckNode(context.Background(), &Node{URL: url})
	if len(lastStats) > maxLastStatsBytes || strings.Contains(string(lastStats), "junk") {
		t.Fatalf("stored the oversized sample (%d bytes)", len(lastStats))
	}
	jobs, ok := reportedJobsByKind(&Node{LastStats: lastStats})
	if !ok || jobs[JobKindVideoTranscode] != 2 {
		t.Fatalf("stored breakdown = %v (ok %v), want video_transcode 2", jobs, ok)
	}
}
