package jellycompat

import (
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
