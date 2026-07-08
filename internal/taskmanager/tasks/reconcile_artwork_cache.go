package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// ArtworkStorageIdentityKey is the server_settings key holding the storage
// identity fingerprint of the public S3 bucket the artwork cache was last
// reconciled against. Machine-managed; not an admin-editable setting.
const ArtworkStorageIdentityKey = "s3.public_storage_identity"

// ArtworkStorageIdentity builds the fingerprint of the public S3 storage the
// cached artwork lives in. Only fields that determine *where objects are
// stored* participate: the read endpoint and URL-auth settings affect how
// objects are served, not where they live, so changing them must not trigger
// a reconcile.
func ArtworkStorageIdentity(endpoint, bucket, keyPrefix string) string {
	normalize := func(v string) string { return strings.ToLower(strings.TrimSpace(v)) }
	return normalize(endpoint) + "|" + normalize(bucket) + "|" + normalize(keyPrefix)
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

// BrandingAssetReconciler clears branding asset refs whose stored objects are
// missing. Satisfied by *branding.Service; may be nil when branding has no
// storage.
type BrandingAssetReconciler interface {
	ReconcileMissingAssets(ctx context.Context) (cleared int, err error)
}

// ReconcileArtworkCacheTask verifies cached artwork against the currently
// configured public object storage and resets whatever is missing so the
// image cache pipeline rebuilds it. Scheduled runs only fire when the storage
// identity changed since the last completed reconcile; manual runs always
// sweep, which doubles as recovery from bucket data loss.
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
	return "Verifies cached artwork against object storage and re-caches anything missing (runs automatically after the storage provider changes)"
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

// ShouldRun suppresses the startup trigger while the storage identity is
// unchanged. Manual RunTask calls bypass this and always sweep.
func (t *ReconcileArtworkCacheTask) ShouldRun(ctx context.Context) (bool, error) {
	if t.runner == nil || t.settings == nil {
		return false, nil
	}
	stored, err := t.settings.Get(ctx, ArtworkStorageIdentityKey)
	if err != nil {
		return false, fmt.Errorf("reading artwork storage identity: %w", err)
	}
	return stored != "" && stored != t.identity, nil
}

func (t *ReconcileArtworkCacheTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.runner == nil || t.settings == nil {
		progress.Report(100, "Artwork reconcile is not configured")
		return nil
	}

	stats, err := t.runner.Run(ctx, progress.Report)
	if err == nil && t.branding != nil {
		var brandingCleared int
		brandingCleared, err = t.branding.ReconcileMissingAssets(ctx)
		stats.Cleared += brandingCleared
		stats.Checked += brandingCleared
	}
	if data, marshalErr := json.Marshal(stats); marshalErr == nil {
		progress.SetResultData(data)
	}
	if err != nil {
		return fmt.Errorf("reconciling artwork cache: %w", err)
	}

	// Only a completed sweep certifies the current storage; an aborted one
	// leaves the old fingerprint so the next startup retries.
	if setErr := t.settings.Set(ctx, ArtworkStorageIdentityKey, t.identity); setErr != nil {
		return fmt.Errorf("persisting artwork storage identity: %w", setErr)
	}

	message := fmt.Sprintf(
		"Verified %d cached images intact, re-queued %d for re-cache, cleared %d without a re-downloadable source",
		stats.Verified, stats.Requeued, stats.Cleared,
	)
	if stats.Mode == "bulk_reset" {
		message = fmt.Sprintf(
			"Storage probe found %d/%d sampled objects missing; reset all cached artwork (re-queued %d, cleared %d)",
			stats.SampleMissing, stats.Sampled, stats.Requeued, stats.Cleared,
		)
	}
	if stats.Errors > 0 {
		message += fmt.Sprintf(", %d rows skipped on storage errors", stats.Errors)
	}
	progress.Report(100, message)
	return nil
}
