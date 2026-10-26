package main

import (
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
	}, "node-a")

	if len(syncs) != 1 || syncs[0].SessionID != "playing" || syncs[0].ReportingNode != "node-a" {
		t.Fatalf("syncs = %+v, want only the playing session on node-a", syncs)
	}
}
