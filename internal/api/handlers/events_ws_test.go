package handlers

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

type fakeActiveScanSnapshotLister struct {
	runs  []evt.ScanRun
	limit int
}

func (f *fakeActiveScanSnapshotLister) ListActiveSnapshot(_ context.Context, limit int) ([]evt.ScanRun, error) {
	f.limit = limit
	if limit > 0 && len(f.runs) > limit {
		return f.runs[:limit], nil
	}
	return f.runs, nil
}

func TestScanSnapshotCapsActiveRuns(t *testing.T) {
	persisted := &fakeActiveScanSnapshotLister{runs: makeScanRuns("persisted-", 300)}
	registry := evt.NewScanRegistry()
	for _, run := range makeScanRuns("registry-", 250) {
		registry.Upsert(run)
	}
	registry.Upsert(evt.ScanRun{ID: "registry-completed", Status: "completed"})

	handler := &EventsHandler{
		persistedScans: persisted,
		scans:          registry,
	}
	raw, err := handler.snapshotForChannel(httptest.NewRequest("GET", "/", nil), nil, "", evt.ChannelScans)
	if err != nil {
		t.Fatalf("snapshotForChannel: %v", err)
	}

	var runs []evt.ScanRun
	if err := json.Unmarshal(raw, &runs); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(runs) != maxRealtimeScanSnapshotRuns {
		t.Fatalf("snapshot length = %d, want %d", len(runs), maxRealtimeScanSnapshotRuns)
	}
	if persisted.limit != maxRealtimeScanSnapshotRuns {
		t.Fatalf("persisted snapshot limit = %d, want %d", persisted.limit, maxRealtimeScanSnapshotRuns)
	}

	registryRuns := 0
	for _, run := range runs {
		if strings.HasPrefix(run.ID, "registry-") {
			registryRuns++
		}
	}
	if registryRuns != maxRealtimeScanSnapshotRuns-len(persisted.runs) {
		t.Fatalf("registry runs in snapshot = %d, want %d", registryRuns, maxRealtimeScanSnapshotRuns-len(persisted.runs))
	}
}

func makeScanRuns(prefix string, count int) []evt.ScanRun {
	runs := make([]evt.ScanRun, 0, count)
	for i := 0; i < count; i++ {
		runs = append(runs, evt.ScanRun{
			ID:        prefix + strconv.Itoa(i),
			LibraryID: 1,
			Mode:      "file",
			Path:      "/library/file-" + strconv.Itoa(i) + ".mkv",
			Trigger:   "autoscan",
			Status:    "accepted",
		})
	}
	return runs
}
