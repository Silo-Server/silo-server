package handlers

import (
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type urlsOnlyEnumerator []string

func (e urlsOnlyEnumerator) TranscodeNodeURLs() []string { return e }

// A new v3 plan counts no draining node's capabilities: the node refuses every
// new transport, so a recipe only it advertises could never be placed.
func TestNewPlanCapabilitiesSkipDrainingNodes(t *testing.T) {
	transcodes := nodepool.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{
		{URL: "http://draining", Enabled: true, Healthy: true, DrainRequested: true},
		{URL: "http://stopping", Enabled: true, Healthy: true, DrainState: nodepool.DrainStateDraining},
		{URL: "http://open", Enabled: true, Healthy: true},
		{URL: "http://down", Enabled: true},
	})
	planner := nodepool.NewPlanner(nodepool.NewProxyPool(), transcodes)

	got := transcodeNodeURLsForNewPlanV3(planner)
	if want := []string{"http://open", "http://down"}; !slices.Equal(got, want) {
		t.Fatalf("new-plan nodes = %v, want %v (unreachable nodes still drop out at their capability fetch)", got, want)
	}
	if got := transcodeNodeURLsForNewPlanV3(urlsOnlyEnumerator{"http://a"}); !slices.Equal(got, []string{"http://a"}) {
		t.Fatalf("plain enumerator = %v", got)
	}
}
