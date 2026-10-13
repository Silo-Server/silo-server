package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

type fakeDeliveryReconciler struct {
	stats metadata.ArtworkDeliveryStats
	err   error
}

func (f fakeDeliveryReconciler) Reconcile(context.Context, metadata.ArtworkDeliveryChecker) (metadata.ArtworkDeliveryStats, error) {
	return f.stats, f.err
}

func runVerifyArtworkDelivery(t *testing.T, reconciler fakeDeliveryReconciler) (metadata.ArtworkDeliveryStats, error) {
	t.Helper()
	progress := &fakeProgress{}
	err := NewVerifyArtworkDeliveryTask(reconciler, nil).Execute(t.Context(), progress)
	var saved metadata.ArtworkDeliveryStats
	if err := json.Unmarshal(progress.resultData, &saved); err != nil {
		t.Fatalf("result data %q: %v", progress.resultData, err)
	}
	return saved, err
}

// Progress text is cleared when a run completes, so probe outcomes must reach
// task history as result data.
func TestVerifyArtworkDeliveryPersistsProbeErrors(t *testing.T) {
	stats := metadata.ArtworkDeliveryStats{Checked: 100, Pending: 3, Errors: 1, LastError: "probe returned status 503", Overdue: 42}
	saved, err := runVerifyArtworkDelivery(t, fakeDeliveryReconciler{stats: stats})
	if err != nil {
		t.Fatalf("partial probe failure failed the run: %v", err)
	}
	if saved != stats {
		t.Fatalf("saved %+v, want %+v", saved, stats)
	}
}

func TestVerifyArtworkDeliveryFailsWhenEveryProbeFails(t *testing.T) {
	stats := metadata.ArtworkDeliveryStats{Checked: 7, Errors: 7, LastError: "probe returned status 403"}
	saved, err := runVerifyArtworkDelivery(t, fakeDeliveryReconciler{stats: stats})
	if err == nil || !strings.Contains(err.Error(), "status 403") {
		t.Fatalf("err = %v, want the probe failure", err)
	}
	if saved != stats {
		t.Fatalf("saved %+v, want %+v", saved, stats)
	}
}

func TestVerifyArtworkDeliveryKeepsStatsWhenRunFails(t *testing.T) {
	stats := metadata.ArtworkDeliveryStats{Checked: 100, Incomplete: 2, Missing: 3}
	saved, err := runVerifyArtworkDelivery(t, fakeDeliveryReconciler{stats: stats, err: errors.New("database unavailable")})
	if err == nil {
		t.Fatal("reconcile error was swallowed")
	}
	if saved != stats {
		t.Fatalf("saved %+v, want %+v", saved, stats)
	}
}
