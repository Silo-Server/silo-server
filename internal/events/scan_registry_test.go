package events

import (
	"testing"
	"time"
)

func TestScanRegistryCancelLibraryRetainsCancelledRun(t *testing.T) {
	registry := NewScanRegistry()
	completedAt := time.Date(2026, 7, 11, 16, 45, 0, 0, time.UTC)
	registry.Upsert(ScanRun{ID: "scan-1", LibraryID: 12, Status: "running"})

	cancelled := registry.CancelLibrary(12, completedAt)
	if len(cancelled) != 1 {
		t.Fatalf("cancelled runs = %d, want 1", len(cancelled))
	}

	run, ok := registry.Get("scan-1")
	if !ok {
		t.Fatal("cancelled run should remain available for async terminal update")
	}
	if run.Status != "cancelled" {
		t.Fatalf("cancelled run status = %q, want cancelled", run.Status)
	}
	if run.CompletedAt == nil || !run.CompletedAt.Equal(completedAt) {
		t.Fatalf("cancelled run CompletedAt = %v, want %v", run.CompletedAt, completedAt)
	}

	registry.MarkTerminal(run)
	if _, ok := registry.Get("scan-1"); ok {
		t.Fatal("terminal run should be removed after final event")
	}
}
