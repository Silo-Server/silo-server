package tasks

import (
	"context"
	"encoding/json"
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
