package nodepool

import "testing"

func TestMarkTranscodeNodeUnreachableStopsSelectionUntilHealthRecovers(t *testing.T) {
	f := newFixture(nil, []*Node{
		transcodeNode(1, "http://dead:8080/", nil, 0),
		transcodeNode(2, "http://alive:8080", nil, 5),
	})

	if !f.planner.MarkTranscodeNodeUnreachable("http://dead:8080") {
		t.Fatal("marking a healthy node reported no change")
	}
	if f.planner.MarkTranscodeNodeUnreachable("http://dead:8080") {
		t.Fatal("marking an already unhealthy node reported a change")
	}
	if f.planner.TranscodeNodeHealthy("http://dead:8080") {
		t.Fatal("marked node still reported healthy")
	}
	// The dead node carries fewer jobs, so only the mark keeps it from winning.
	plan := f.planner.PlanTranscodeSessionWithLocalEgress("session-1", "", nil)
	if plan.TranscodeNode == nil || plan.TranscodeNode.ID != 2 {
		t.Fatalf("selected %+v, want the reachable node", plan.TranscodeNode)
	}

	// The next health sweep is authoritative: a node that answers again is
	// selectable again without any other intervention.
	f.transcodes.ApplyHealth(1, "http://dead:8080/", true, 0, 0, "", nil, nil, f.now)
	if !f.planner.TranscodeNodeHealthy("http://dead:8080") {
		t.Fatal("a healthy sweep result did not clear the mark")
	}
	if f.planner.MarkTranscodeNodeUnreachable("http://unknown:8080") {
		t.Fatal("marking an unpooled node reported a change")
	}
}

// A move that lost its session to a replan releases only its own
// reservation: the replan's reservation, stored under the same session id,
// stands even when it charges the same node.
func TestReleaseReservationKeepsANewerReservationOnTheSameNode(t *testing.T) {
	f := newFixture(nil, []*Node{transcodeNode(1, "http://only:8080", nil, 0)})
	move := f.planner.PlanTranscodeSessionWithLocalEgress("session-1", "", nil)
	replan := f.planner.PlanTranscodeSessionWithLocalEgress("session-1", "", nil)
	if move.TranscodeNode == nil || replan.TranscodeNode == nil || move.TranscodeNode.ID != replan.TranscodeNode.ID {
		t.Fatalf("selections %+v and %+v, want the same node twice", move.TranscodeNode, replan.TranscodeNode)
	}
	f.planner.ReleaseReservation("session-1", move.Reservation)
	if f.planner.reserved["session-1"] != replan.Reservation.held {
		t.Fatal("releasing the replaced reservation dropped the replan's")
	}
	f.planner.ReleaseReservation("session-1", replan.Reservation)
	if _, ok := f.planner.reserved["session-1"]; ok {
		t.Fatal("releasing the current reservation kept it")
	}
	f.planner.ReleaseReservation("session-1", Reservation{})
}
