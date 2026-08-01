package tasks

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type fakeVirtualEpisodeReconciler struct {
	limit int
	count int
	err   error
}

func (f *fakeVirtualEpisodeReconciler) ReconcileReleasedCollectionVirtualEpisodes(_ context.Context, limit int) (int, error) {
	f.limit = limit
	return f.count, f.err
}

type virtualEpisodeProgress struct {
	percent float64
	message string
}

func (p *virtualEpisodeProgress) Report(percent float64, message string) {
	p.percent = percent
	p.message = message
}

func (*virtualEpisodeProgress) SetResultData(json.RawMessage) {}

func TestReconcileVirtualEpisodesTaskRunsDailyAndBounded(t *testing.T) {
	reconciler := &fakeVirtualEpisodeReconciler{count: 3}
	task := NewReconcileVirtualEpisodesTask(reconciler)
	triggers := task.DefaultTriggers()
	if len(triggers) != 1 || triggers[0].Type != taskmanager.TriggerTypeInterval {
		t.Fatalf("triggers=%+v", triggers)
	}
	if got := time.Duration(triggers[0].IntervalMs) * time.Millisecond; got != 24*time.Hour {
		t.Fatalf("interval=%s, want 24h", got)
	}
	progress := &virtualEpisodeProgress{}
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute(): %v", err)
	}
	if reconciler.limit != 1000 {
		t.Fatalf("limit=%d, want 1000", reconciler.limit)
	}
	if progress.percent != 100 || progress.message != "Reconciled 3 virtual series" {
		t.Fatalf("progress=%+v", progress)
	}
}
