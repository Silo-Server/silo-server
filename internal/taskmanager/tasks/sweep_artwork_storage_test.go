package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

type fakeSweepRunner struct {
	stats metadata.ArtworkStorageSweepStats
	err   error
	seen  artworkSweepCheckpoint
}

func (f *fakeSweepRunner) SweepPrefix(_ context.Context, prefix, token string, _ int) (metadata.ArtworkStorageSweepStats, error) {
	f.seen = artworkSweepCheckpoint{Prefix: prefix, Token: token}
	return f.stats, f.err
}

func sweepCheckpointAfter(t *testing.T, start artworkSweepCheckpoint, runner *fakeSweepRunner) artworkSweepCheckpoint {
	t.Helper()
	store := &fakeSettingsStore{values: map[string]string{}}
	start.Identity = "store"
	encoded, err := json.Marshal(start)
	if err != nil {
		t.Fatal(err)
	}
	store.values[ArtworkStorageSweepCheckpointKey] = string(encoded)

	task := NewSweepArtworkStorageTask(runner, store, "store")
	_ = task.Execute(context.Background(), &fakeProgress{})
	if runner.seen.Prefix != start.Prefix || runner.seen.Token != start.Token {
		t.Fatalf("sweep ran from %+v, want the saved checkpoint %+v", runner.seen, start)
	}

	var saved artworkSweepCheckpoint
	if err := json.Unmarshal([]byte(store.values[ArtworkStorageSweepCheckpointKey]), &saved); err != nil {
		t.Fatalf("checkpoint unreadable: %v", err)
	}
	return saved
}

// A refused page would be refused again on every run. Resuming at it pinned
// the sweep to one prefix forever and left the others unswept, so the task
// moves on instead.
func TestSweepArtworkStorageMovesOnAfterAnAnomalyStop(t *testing.T) {
	runner := &fakeSweepRunner{
		stats: metadata.ArtworkStorageSweepStats{StoppedOnAnomaly: true, NextToken: "local/before-the-bad-page"},
		err:   errors.New("refusing to delete"),
	}
	saved := sweepCheckpointAfter(t, artworkSweepCheckpoint{Prefix: "local/", Token: "local/earlier"}, runner)
	if saved.Prefix != "tmdb/" || saved.Token != "" {
		t.Fatalf("checkpoint after an anomaly stop = %+v, want the start of tmdb/", saved)
	}
}

func TestSweepArtworkStorageResumesABoundedRun(t *testing.T) {
	runner := &fakeSweepRunner{stats: metadata.ArtworkStorageSweepStats{NextToken: "local/page-3"}}
	saved := sweepCheckpointAfter(t, artworkSweepCheckpoint{Prefix: "local/", Token: "local/page-1"}, runner)
	if saved.Prefix != "local/" || saved.Token != "local/page-3" {
		t.Fatalf("checkpoint after a bounded run = %+v, want local/ at local/page-3", saved)
	}
}

func TestSweepArtworkStorageLeavesTheCheckpointAloneWhenSkipped(t *testing.T) {
	runner := &fakeSweepRunner{stats: metadata.ArtworkStorageSweepStats{Skipped: true}}
	saved := sweepCheckpointAfter(t, artworkSweepCheckpoint{Prefix: "tvdb/", Token: "tvdb/page-9"}, runner)
	if saved.Prefix != "tvdb/" || saved.Token != "tvdb/page-9" {
		t.Fatalf("checkpoint after a skipped run = %+v; another node's cursor must not be overwritten", saved)
	}
}
