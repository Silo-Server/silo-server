package main

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestBuildLiveSessionSyncPreservesSessionGeneration(t *testing.T) {
	startedAt := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	sync := buildLiveSessionSync(&playback.Session{
		ID:         "session-1",
		Generation: "32a4e124-9df7-4cfa-be49-e8e503316714",
		StartedAt:  startedAt,
	}, "api-1")
	if sync.SessionGeneration != "32a4e124-9df7-4cfa-be49-e8e503316714" {
		t.Fatalf("generation = %q", sync.SessionGeneration)
	}
	if !sync.StartedAt.Equal(startedAt) {
		t.Fatalf("started_at = %s, want %s", sync.StartedAt, startedAt)
	}
}

func TestBuildLiveSessionSyncMapsLegacyEmptyGenerationToInternalSentinel(t *testing.T) {
	sync := buildLiveSessionSync(&playback.Session{ID: "legacy", Generation: ""}, "api-1")
	if sync.SessionGeneration != playback.LegacySessionGenerationSentinel {
		t.Fatalf("generation = %q, want internal sentinel", sync.SessionGeneration)
	}
}
