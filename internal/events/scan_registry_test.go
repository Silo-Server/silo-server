package events

import (
	"strconv"
	"testing"
	"time"
)

func TestScanRegistryListActiveLimit(t *testing.T) {
	registry := NewScanRegistry()
	for i := 0; i < 3; i++ {
		registry.Upsert(ScanRun{ID: "active-" + strconv.Itoa(i), Status: "accepted"})
	}
	registry.Upsert(ScanRun{ID: "completed", Status: "completed"})

	limited := registry.ListActiveLimit(2)
	if len(limited) != 2 {
		t.Fatalf("limited active runs = %d, want 2", len(limited))
	}
	for _, run := range limited {
		if run.Status != "accepted" && run.Status != "running" {
			t.Fatalf("limited run should be active, got %+v", run)
		}
	}

	all := registry.ListActiveLimit(0)
	if len(all) != 3 {
		t.Fatalf("all active runs = %d, want 3", len(all))
	}
}

func TestScanRegistryListActiveLimitSortsBeforeLimiting(t *testing.T) {
	registry := NewScanRegistry()
	newer := time.Date(2026, 6, 26, 12, 1, 0, 0, time.UTC)
	older := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	registry.Upsert(ScanRun{ID: "queued-b", Status: "accepted"})
	registry.Upsert(ScanRun{ID: "running-newer", Status: "running", StartedAt: &newer})
	registry.Upsert(ScanRun{ID: "queued-a", Status: "accepted"})
	registry.Upsert(ScanRun{ID: "running-older", Status: "running", StartedAt: &older})

	limited := registry.ListActiveLimit(3)
	got := make([]string, 0, len(limited))
	for _, run := range limited {
		got = append(got, run.ID)
	}
	want := []string{"running-older", "running-newer", "queued-a"}
	if len(got) != len(want) {
		t.Fatalf("limited IDs = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("limited IDs = %#v, want %#v", got, want)
		}
	}
}
