package nodepool

import "testing"

func TestReservationsPickLeastLoadedWithinCapacity(t *testing.T) {
	nodes := []*Node{
		{URL: "http://a/", Enabled: true, Healthy: true, ActiveJobs: 3},
		{URL: "http://b", Enabled: true, Healthy: true, ActiveJobs: 1},
		{URL: "http://c", Enabled: false, Healthy: true},
		{URL: "http://d", Enabled: true, Healthy: false},
	}
	r := NewReservations(map[string]int{"http://b/": 2})

	node, release, outcome := r.Reserve(nodes, 3)
	if outcome != Reserved || node.URL != "http://a/" && node.URL != "http://b" {
		t.Fatalf("got %v %v", node, outcome)
	}
	// b carries 1 job + 2 reservations, a 3 jobs: equal load keeps the first.
	if node.URL != "http://a/" {
		t.Fatalf("picked %s", node.URL)
	}
	second, releaseSecond, _ := r.Reserve(nodes, 3)
	if second.URL != "http://b" {
		t.Fatalf("second pick %s", second.URL)
	}
	if _, _, outcome := r.Reserve(nodes, 1); outcome != CapacityExhausted {
		t.Fatalf("outcome %v, want capacity exhausted", outcome)
	}
	release()
	release() // idempotent
	releaseSecond()
	if got := r.counts["http://a"]; got != 0 {
		t.Fatalf("a still holds %d", got)
	}
	if got := r.counts["http://b"]; got != 2 {
		t.Fatalf("b holds %d, want 2", got)
	}
	if _, _, outcome := r.Reserve(nodes[2:], 1); outcome != NoHealthyNode {
		t.Fatalf("outcome %v, want no healthy node", outcome)
	}
}
