package nodepool

import "testing"

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
