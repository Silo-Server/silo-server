package main

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// TestBuildLiveSessionSyncsSkipsStopReportedSessions covers #1454: a play the
// client reported stopped leaves the live admin snapshot, while other sessions
// on the node stay listed.
func TestBuildLiveSessionSyncsSkipsStopReportedSessions(t *testing.T) {
	syncs := buildLiveSessionSyncs([]*playback.Session{
		{ID: "playing", UserID: 1},
		{ID: "stopped", UserID: 1, IsPaused: true, StopReported: true},
		nil,
	}, "node-a", sessionDelivery{})

	if len(syncs) != 1 || syncs[0].SessionID != "playing" || syncs[0].ReportingNode != "node-a" {
		t.Fatalf("syncs = %+v, want only the playing session on node-a", syncs)
	}
}

// HLS sessions on a transcode node take the node's progress for their
// transport id, one read per node; a node that fails costs only its own
// sessions the values, and Direct Play never asks a node.
func TestLiveDeliveryProgressFromTranscodeNodes(t *testing.T) {
	run := func(start, end, produced float64) playback.HLSDeliveryProgress {
		return playback.HLSDeliveryProgress{Fetched: playback.MediaSpan{Start: start, End: end}, FetchedOK: true, ProducedThrough: produced, ProducedOK: true}
	}
	nodes := map[string]map[string]playback.HLSDeliveryProgress{
		"http://node-a": {"transport-1": run(100, 160.4, 300), "plain": run(0, 30, 60), "elsewhere": run(500, 520, 600)},
	}
	var mu sync.Mutex
	reads := map[string]int{}
	fetch := func(_ context.Context, nodeURL string) (map[string]playback.HLSDeliveryProgress, error) {
		mu.Lock()
		reads[nodeURL]++
		mu.Unlock()
		if listing, ok := nodes[nodeURL]; ok {
			return listing, nil
		}
		return nil, errors.New("node down")
	}
	sessions := []*playback.Session{
		{ID: "rehomed", PlayMethod: playback.PlayTranscode, TranscodeNodeURL: "http://node-a", TranscodeTransportID: "transport-1", Position: 120},
		{ID: "plain", PlayMethod: playback.PlayRemux, TranscodeNodeURL: "http://node-a", Position: 10},
		{ID: "sought-away", PlayMethod: playback.PlayTranscode, TranscodeNodeURL: "http://node-a", TranscodeTransportID: "elsewhere", Position: 10},
		{ID: "node-down", PlayMethod: playback.PlayTranscode, TranscodeNodeURL: "http://node-b", Position: 10},
		{ID: "direct", PlayMethod: playback.PlayDirect, TranscodeNodeURL: "http://node-c"},
		{ID: "local", PlayMethod: playback.PlayTranscode, Position: 10},
	}
	remote := fetchRemoteDelivery(t.Context(), sessions, fetch)
	if reads["http://node-a"] != 1 || reads["http://node-b"] != 1 || reads["http://node-c"] != 0 {
		t.Fatalf("node reads = %v, want node-a and node-b once, node-c never", reads)
	}

	want := map[string][2]any{
		"rehomed":     {160.0, 300.0},
		"plain":       {30.0, 60.0},
		"sought-away": {nil, 600.0},
		"node-down":   {nil, nil},
		"local":       {nil, nil},
	}
	syncs := buildLiveSessionSyncs(sessions, "api-1", sessionDelivery{remote: remote})
	for _, row := range syncs {
		w, ok := want[row.SessionID]
		if !ok {
			continue
		}
		if !sameSeconds(row.DeliveredThroughSeconds, w[0]) || !sameSeconds(row.ProducedThroughSeconds, w[1]) {
			t.Errorf("%s: delivered %v produced %v, want %v %v", row.SessionID, deref(row.DeliveredThroughSeconds), deref(row.ProducedThroughSeconds), w[0], w[1])
		}
	}
}

func sameSeconds(got *float64, want any) bool {
	if want == nil {
		return got == nil
	}
	return got != nil && *got == want.(float64)
}

func deref(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}
