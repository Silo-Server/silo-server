package jellycompat

import (
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

// nodeLookupPlannerStub is a planner that can also resolve a pooled node from
// its URL, which is what carries the node's own acceleration override.
type nodeLookupPlannerStub struct {
	node *nodepool.Node
}

func (s *nodeLookupPlannerStub) PlanSession(string, string, bool, int) nodepool.Plan {
	return nodepool.Plan{TranscodeNode: s.node}
}

func (s *nodeLookupPlannerStub) TranscodeNodeByURL(nodeURL string) (*nodepool.Node, bool) {
	if s.node == nil || s.node.URL != nodeURL {
		return nil, false
	}
	return s.node, true
}

// planner without a lookup, standing in for a test fake or a future planner
// that cannot resolve nodes.
type plainPlannerStub struct{}

func (plainPlannerStub) PlanSession(string, string, bool, int) nodepool.Plan { return nodepool.Plan{} }

// The Jellyfin surface dispatches to the same nodes as the native API, so it
// has to name the same backend: the node's own override when it has one, and
// the cluster value untouched otherwise.
func TestRemoteDispatchHWAccelPrefersTheNodesOverride(t *testing.T) {
	override := func(value string) *string { return &value }
	overriddenNode := func(cluster string, node *nodepool.Node) *PlaybackHandler {
		return &PlaybackHandler{HWAccel: cluster, NodePlanner: &nodeLookupPlannerStub{node: node}}
	}
	tests := []struct {
		name    string
		handler *PlaybackHandler
		nodeURL string
		want    string
	}{
		{
			name: "node overridden to software wins over a qsv cluster",
			handler: overriddenNode("qsv", &nodepool.Node{
				URL: "http://node-1", HWAccelOverride: override("none")}),
			nodeURL: "http://node-1",
			want:    "none",
		},
		{
			name: "an override beats the stale report it contradicts",
			handler: overriddenNode("qsv", &nodepool.Node{
				URL:             "http://node-1",
				HWAccelOverride: override("none"),
				Capabilities:    json.RawMessage(`{"resolved":"qsv"}`)}),
			nodeURL: "http://node-1",
			want:    "none",
		},
		{
			name:    "a node with no override keeps the cluster value",
			handler: overriddenNode("qsv", &nodepool.Node{URL: "http://node-1"}),
			nodeURL: "http://node-1",
			want:    "qsv",
		},
		{
			// The node re-resolves auto against live hardware at session start;
			// a report from its last snapshot must not stand in for that.
			name: "auto reaches the node even when the last report says otherwise",
			handler: overriddenNode("auto", &nodepool.Node{
				URL: "http://node-1", Capabilities: json.RawMessage(`{"resolved":"none"}`)}),
			nodeURL: "http://node-1",
			want:    "auto",
		},
		{
			name: "unknown node keeps the cluster value",
			handler: overriddenNode("qsv", &nodepool.Node{
				URL: "http://node-1", HWAccelOverride: override("none")}),
			nodeURL: "http://node-2",
			want:    "qsv",
		},
		{
			name:    "planner without a lookup keeps the cluster value",
			handler: &PlaybackHandler{HWAccel: "qsv", NodePlanner: plainPlannerStub{}},
			nodeURL: "http://node-1",
			want:    "qsv",
		},
		{
			name:    "no planner at all keeps the cluster value",
			handler: &PlaybackHandler{HWAccel: "qsv"},
			nodeURL: "http://node-1",
			want:    "qsv",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.handler.remoteDispatchHWAccel(test.nodeURL); got != test.want {
				t.Fatalf("remoteDispatchHWAccel(%q) = %q, want %q", test.nodeURL, got, test.want)
			}
		})
	}
}
