package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// ArtworkStorageSweepCheckpointKey is the machine-managed sweep cursor. It is
// scoped to the storage identity for the same reason the reconcile checkpoint
// is: a storage move must never resume an older location's walk.
const ArtworkStorageSweepCheckpointKey = config.ArtworkStorageSweepCheckpointKey

// artworkSweepPrefixes are the artwork namespaces the sweep owns. Listing them
// explicitly rather than walking the bucket root is deliberate: the bucket also
// holds namespaces this task does not model (book-provider caches, diagnostics,
// collection images), and a sweep that deleted from a namespace whose reference
// rules it does not know would be destroying data on an assumption.
var artworkSweepPrefixes = []string{"local/", "tmdb/", "tvdb/"}

// artworkSweepPagesPerRun bounds one scheduled run. The sweep is a background
// reclaim, not a deadline: capping the work keeps a run's storage cost
// predictable and lets a large bucket be walked across many runs instead of one
// long one holding a connection.
const artworkSweepPagesPerRun = 200

type artworkSweepCheckpoint struct {
	Identity string `json:"identity"`
	Prefix   string `json:"prefix"`
	Token    string `json:"token"`
}

// ArtworkStorageSweepRunner is the sweep surface. Satisfied by
// *metadata.ArtworkStorageSweeper; nil when storage is not configured.
type ArtworkStorageSweepRunner interface {
	SweepPrefix(ctx context.Context, prefix, token string, maxPages int) (metadata.ArtworkStorageSweepStats, error)
}

// SweepArtworkStorageTask deletes stored artwork objects no catalog surface
// references.
//
// The artwork revision GC collects displaced revisions it was told about. This
// is the backstop for the ones it was not: a revision whose candidate row never
// got enqueued is invisible to the GC forever, and without this nothing ever
// reads the bucket back to notice. On the deployment this was built against,
// that leak had reached 27% of the artwork bucket.
type SweepArtworkStorageTask struct {
	runner   ArtworkStorageSweepRunner
	settings ArtworkReconcileSettingsStore
	identity string
}

func NewSweepArtworkStorageTask(runner ArtworkStorageSweepRunner, settings ArtworkReconcileSettingsStore, identity string) *SweepArtworkStorageTask {
	return &SweepArtworkStorageTask{runner: runner, settings: settings, identity: identity}
}

func (t *SweepArtworkStorageTask) Key() string  { return "sweep_artwork_storage" }
func (t *SweepArtworkStorageTask) Name() string { return "Sweep Artwork Storage" }
func (t *SweepArtworkStorageTask) Description() string {
	return "Deletes cached artwork objects that no catalog record references, reclaiming revisions the artwork garbage collector never saw"
}
func (t *SweepArtworkStorageTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *SweepArtworkStorageTask) IsHidden() bool { return false }

func (t *SweepArtworkStorageTask) DefaultTriggers() []taskmanager.TriggerConfig {
	// Daily. The sweep only reclaims space, so running it often buys nothing;
	// running it rarely lets the leak grow between passes.
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64((24 * time.Hour) / time.Millisecond)},
	}
}

// ShouldRun skips scheduled runs when the sweep cannot work safely.
func (t *SweepArtworkStorageTask) ShouldRun(ctx context.Context) (bool, error) {
	if t.runner == nil || t.settings == nil {
		return false, nil
	}
	return true, nil
}

func (t *SweepArtworkStorageTask) readCheckpoint(ctx context.Context) artworkSweepCheckpoint {
	start := artworkSweepCheckpoint{Identity: t.identity, Prefix: artworkSweepPrefixes[0]}
	raw, err := t.settings.Get(ctx, ArtworkStorageSweepCheckpointKey)
	if err != nil || raw == "" {
		return start
	}
	var saved artworkSweepCheckpoint
	if json.Unmarshal([]byte(raw), &saved) != nil {
		return start
	}
	// A checkpoint from a different bucket carries a continuation token that
	// is meaningless here; restart rather than resume into nowhere.
	if saved.Identity != t.identity {
		return start
	}
	for _, prefix := range artworkSweepPrefixes {
		if saved.Prefix == prefix {
			return saved
		}
	}
	return start
}

func (t *SweepArtworkStorageTask) saveCheckpoint(ctx context.Context, cp artworkSweepCheckpoint) {
	encoded, err := json.Marshal(cp)
	if err != nil {
		return
	}
	if err := t.settings.Set(ctx, ArtworkStorageSweepCheckpointKey, string(encoded)); err != nil {
		// Losing the cursor costs re-walking a prefix, which is wasted listing
		// but never wrong: the sweep is idempotent.
		slog.WarnContext(ctx, "artwork storage sweep: saving checkpoint failed",
			"component", "taskmanager", "error", err)
	}
}

// nextPrefix returns the prefix after the given one, wrapping to the first so
// the sweep cycles continuously.
func nextPrefix(current string) string {
	for i, prefix := range artworkSweepPrefixes {
		if prefix == current {
			return artworkSweepPrefixes[(i+1)%len(artworkSweepPrefixes)]
		}
	}
	return artworkSweepPrefixes[0]
}

func (t *SweepArtworkStorageTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.runner == nil || t.settings == nil {
		progress.Report(100, "Artwork storage sweep is not configured")
		return nil
	}

	cp := t.readCheckpoint(ctx)
	progress.Report(5, fmt.Sprintf("Sweeping %s", cp.Prefix))

	stats, err := t.runner.SweepPrefix(ctx, cp.Prefix, cp.Token, artworkSweepPagesPerRun)

	// Persist whatever progress was made before reporting an error: the work
	// already done is real, and re-walking it on the next run is pure waste.
	//
	// A skipped run did none, and another node is mid-sweep holding the lock:
	// writing our stale cursor back would drag that node's progress backwards.
	if !stats.StoppedOnAnomaly && !stats.Skipped {
		if stats.PrefixDone {
			t.saveCheckpoint(ctx, artworkSweepCheckpoint{Identity: t.identity, Prefix: nextPrefix(cp.Prefix)})
		} else {
			t.saveCheckpoint(ctx, artworkSweepCheckpoint{Identity: t.identity, Prefix: cp.Prefix, Token: stats.NextToken})
		}
	}

	if data, marshalErr := json.Marshal(stats); marshalErr == nil {
		progress.SetResultData(data)
	}
	if err != nil {
		return fmt.Errorf("sweeping artwork storage: %w", err)
	}

	if stats.Skipped {
		progress.Report(100, "Another node is already sweeping artwork storage")
		return nil
	}

	progress.Report(100, fmt.Sprintf(
		"Swept %d objects in %s: %d referenced, %d deleted, %d too new, %d unrecognized",
		stats.Scanned, cp.Prefix, stats.Referenced, stats.Deleted, stats.TooNew, stats.Unparsable,
	))
	return nil
}
