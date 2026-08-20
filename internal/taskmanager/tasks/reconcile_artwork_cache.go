package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/s3client"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// ErrArtworkReconcileManualRunRequired prevents a storage-location change from
// mutating artwork records on a scheduler trigger. An administrator must first
// migrate the existing objects, then explicitly run the task if they intend
// missing records to be reset for an explicit backfill or cleared.
var ErrArtworkReconcileManualRunRequired = errors.New("artwork storage changed; manual reconcile required")

// ArtworkStorageIdentityKey is the server_settings key holding the storage
// identity fingerprint of the public S3 bucket the artwork cache was last
// reconciled against. Machine-managed; not an admin-editable setting.
const (
	ArtworkStorageIdentityKey = "s3.public_storage_identity"
	// ArtworkStorageReconcileCheckpointKey holds a machine-managed verify
	// cursor. It is scoped to both the stored and target identities so a later
	// storage move can never resume an older bucket's sweep.
	ArtworkStorageReconcileCheckpointKey = config.ArtworkStorageReconcileCheckpointKey
)

// ArtworkStorageIdentity builds the fingerprint of the public S3 storage the
// cached artwork lives in. Only fields that determine *where objects are
// stored* participate: the read endpoint and URL-auth settings affect how
// objects are served, not where they live, so changing them must not trigger
// a reconcile.
//
// Normalization mirrors how each field is actually used: endpoints (hostnames)
// and bucket names are case-insensitive, but the key prefix feeds into
// case-sensitive object keys, so it keeps its case and is normalized exactly
// like s3client applies it (slash- and whitespace-trimmed). A case-only prefix
// edit is a real storage move and must change the fingerprint; a slash-only
// edit is not and must not.
func ArtworkStorageIdentity(endpoint, bucket, keyPrefix string) string {
	insensitive := func(v string) string { return strings.ToLower(strings.TrimSpace(v)) }
	return insensitive(endpoint) + "|" + insensitive(bucket) + "|" + s3client.NormalizeKeyPrefix(keyPrefix)
}

// ArtworkReconcileSettingsStore is the server-settings surface the task needs.
// Satisfied by *catalog.ServerSettingsRepo and its encrypting decorator.
type ArtworkReconcileSettingsStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

// ArtworkReconcileRunner runs a reconcile sweep. Satisfied by
// *metadata.ArtworkCacheReconciler.
type ArtworkReconcileRunner interface {
	Run(ctx context.Context, progress func(percent float64, message string)) (metadata.ArtworkReconcileStats, error)
}

type resumableArtworkReconcileRunner interface {
	RunResumable(
		ctx context.Context,
		checkpoint *metadata.ArtworkReconcileCheckpoint,
		save func(metadata.ArtworkReconcileCheckpoint) error,
		progress func(percent float64, message string),
	) (metadata.ArtworkReconcileStats, error)
}

type artworkReconcileCheckpointEnvelope struct {
	BaselineIdentity string                              `json:"baseline_identity"`
	TargetIdentity   string                              `json:"target_identity"`
	Checkpoint       metadata.ArtworkReconcileCheckpoint `json:"checkpoint"`
}

// BrandingAssetReconciler clears branding asset refs whose stored objects are
// missing. Satisfied by *branding.Service; may be nil when branding has no
// storage.
type BrandingAssetReconciler interface {
	ReconcileMissingAssets(ctx context.Context) (checked, cleared int, err error)
}

// ReconcileArtworkCacheTask verifies cached artwork against the currently
// configured public object storage and resets whatever is missing so the
// image cache pipeline rebuilds it. Scheduled triggers never start this
// mutating sweep after a storage change; an administrator must run it manually
// after migrating objects or when intentionally recovering from bucket loss.
type ReconcileArtworkCacheTask struct {
	runner   ArtworkReconcileRunner
	settings ArtworkReconcileSettingsStore
	branding BrandingAssetReconciler
	identity string
}

func NewReconcileArtworkCacheTask(runner ArtworkReconcileRunner, settings ArtworkReconcileSettingsStore, branding BrandingAssetReconciler, identity string) *ReconcileArtworkCacheTask {
	return &ReconcileArtworkCacheTask{runner: runner, settings: settings, branding: branding, identity: identity}
}

func (t *ReconcileArtworkCacheTask) Key() string  { return "reconcile_artwork_cache" }
func (t *ReconcileArtworkCacheTask) Name() string { return "Reconcile Artwork Cache" }
func (t *ReconcileArtworkCacheTask) Description() string {
	return "Manually verifies cached artwork against object storage; missing records may be reset across the full library and require an explicit metadata image backfill"
}
func (t *ReconcileArtworkCacheTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *ReconcileArtworkCacheTask) IsHidden() bool { return false }

func (t *ReconcileArtworkCacheTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
	}
}

// ShouldRun suppresses scheduled execution in every case. A changed storage
// identity returns an actionable preflight error so the event is visible in
// logs, but it must never launch a mutating sweep automatically. Manual
// RunTask calls bypass this gate and remain the explicit recovery path.
//
// The startup trigger fires exactly once per process, so a transient settings
// read failure here would postpone a needed reconcile until the next restart;
// retry briefly before giving up. (The task manager skips the run on a
// preflight error rather than failing open into a full sweep.)
func (t *ReconcileArtworkCacheTask) ShouldRun(ctx context.Context) (bool, error) {
	if t.runner == nil || t.settings == nil {
		return false, nil
	}
	stored, err := t.readStorageIdentity(ctx)
	if err != nil {
		return false, fmt.Errorf("reading artwork storage identity: %w", err)
	}
	if stored == "" || stored == t.identity {
		return false, nil
	}
	return false, fmt.Errorf(
		"%w: migrate or copy the existing public artwork objects before running Reconcile Artwork Cache manually; a manual run may reset or clear the full artwork library, and re-downloading requires a separate manual Backfill Metadata Images run",
		ErrArtworkReconcileManualRunRequired,
	)
}

func (t *ReconcileArtworkCacheTask) readStorageIdentity(ctx context.Context) (string, error) {
	var stored string
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		stored, err = t.settings.Get(ctx, ArtworkStorageIdentityKey)
		if err == nil {
			return stored, nil
		}
		if attempt == 2 {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		}
	}
	return "", err
}

func (t *ReconcileArtworkCacheTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.runner == nil || t.settings == nil {
		progress.Report(100, "Artwork reconcile is not configured")
		return nil
	}

	stats, err := t.run(ctx, progress.Report)
	if err != nil {
		if data, marshalErr := json.Marshal(stats); marshalErr == nil {
			progress.SetResultData(data)
		}
		return fmt.Errorf("reconciling artwork cache: %w", err)
	}

	// Only a clean, completed sweep certifies the current storage. Sweep
	// errors mean rows were skipped unverified, so the fingerprint and saved
	// checkpoint stay in place for an explicit manual retry; resets already
	// applied this run are durable either way.
	if stats.SweepErrors > 0 {
		if data, marshalErr := json.Marshal(stats); marshalErr == nil {
			progress.SetResultData(data)
		}
		return fmt.Errorf(
			"artwork reconcile: %d rows skipped on storage errors (verified %d, reset for backfill %d, cleared %d); storage identity left uncertified; run Reconcile Artwork Cache manually to resume",
			stats.SweepErrors, stats.Verified, stats.Requeued, stats.Cleared,
		)
	}
	// Certify before the branding check: a transient failure on that
	// 4-object pass must not discard a completed catalog sweep and force it
	// to repeat every boot.
	if setErr := t.settings.Set(ctx, ArtworkStorageIdentityKey, t.identity); setErr != nil {
		return fmt.Errorf("persisting artwork storage identity: %w", setErr)
	}
	if clearErr := t.settings.Set(ctx, ArtworkStorageReconcileCheckpointKey, ""); clearErr != nil {
		// The certified identity suppresses automatic reruns, and checkpoint
		// envelopes are tied to their pre-run baseline, so stale state is safe.
		// Surface the cleanup problem without turning a completed sweep into a
		// failed task that an admin might unnecessarily repeat.
		slog.WarnContext(ctx, "artwork reconcile: clearing completed checkpoint failed", "error", clearErr)
	}

	brandingNote := ""
	if t.branding != nil {
		brandingChecked, brandingCleared, brandingErr := t.branding.ReconcileMissingAssets(ctx)
		stats.Cleared += brandingCleared
		stats.Checked += brandingChecked
		if brandingErr != nil {
			stats.Errors++
			brandingNote = fmt.Sprintf("; branding asset check failed: %v (re-run the task to retry)", brandingErr)
			slog.Warn("artwork reconcile: branding asset check failed", "error", brandingErr)
		}
	}

	if data, marshalErr := json.Marshal(stats); marshalErr == nil {
		progress.SetResultData(data)
	}

	message := fmt.Sprintf(
		"Verified %d cached images intact, reset %d for an optional manual backfill, cleared %d without a re-downloadable source",
		stats.Verified, stats.Requeued, stats.Cleared,
	)
	if stats.Mode == metadata.ArtworkReconcileModeBulkReset {
		message = fmt.Sprintf(
			"Storage probe found %d/%d sampled objects missing; reset all cached artwork (%d provider records ready for an optional manual backfill, cleared %d)",
			stats.SampleMissing, stats.Sampled, stats.Requeued, stats.Cleared,
		)
	}
	if stats.Errors > 0 {
		// SweepErrors is zero here (checked above), so these are probe or
		// branding errors — reported, but they don't reduce sweep coverage.
		message += fmt.Sprintf(", %d storage errors during probing", stats.Errors)
	}
	progress.Report(100, message+brandingNote)
	return nil
}

func (t *ReconcileArtworkCacheTask) run(
	ctx context.Context,
	progress func(percent float64, message string),
) (metadata.ArtworkReconcileStats, error) {
	runner, ok := t.runner.(resumableArtworkReconcileRunner)
	if !ok {
		return t.runner.Run(ctx, progress)
	}

	baseline, err := t.readStorageIdentity(ctx)
	if err != nil {
		return metadata.ArtworkReconcileStats{Mode: metadata.ArtworkReconcileModeVerify}, fmt.Errorf("reading artwork reconcile baseline identity: %w", err)
	}
	// A same-identity run is a manual recovery sweep. It must cover the whole
	// catalog as it exists now rather than inheriting a cursor from an older
	// attempt, because objects may have disappeared anywhere in the meantime.
	if baseline == t.identity {
		return runner.RunResumable(ctx, nil, nil, progress)
	}
	rawCheckpoint, err := t.settings.Get(ctx, ArtworkStorageReconcileCheckpointKey)
	if err != nil {
		return metadata.ArtworkReconcileStats{Mode: metadata.ArtworkReconcileModeVerify}, fmt.Errorf("reading artwork reconcile checkpoint: %w", err)
	}

	var checkpoint *metadata.ArtworkReconcileCheckpoint
	if strings.TrimSpace(rawCheckpoint) != "" {
		var envelope artworkReconcileCheckpointEnvelope
		if unmarshalErr := json.Unmarshal([]byte(rawCheckpoint), &envelope); unmarshalErr != nil {
			slog.WarnContext(ctx, "artwork reconcile: ignoring invalid checkpoint", "error", unmarshalErr)
		} else if envelope.BaselineIdentity == baseline && envelope.TargetIdentity == t.identity {
			checkpoint = &envelope.Checkpoint
		}
	}

	save := func(next metadata.ArtworkReconcileCheckpoint) error {
		envelope := artworkReconcileCheckpointEnvelope{
			BaselineIdentity: baseline,
			TargetIdentity:   t.identity,
			Checkpoint:       next,
		}
		encoded, marshalErr := json.Marshal(envelope)
		if marshalErr != nil {
			return fmt.Errorf("encoding artwork reconcile checkpoint: %w", marshalErr)
		}
		if setErr := t.settings.Set(ctx, ArtworkStorageReconcileCheckpointKey, string(encoded)); setErr != nil {
			return fmt.Errorf("persisting artwork reconcile checkpoint: %w", setErr)
		}
		return nil
	}

	return runner.RunResumable(ctx, checkpoint, save, progress)
}
