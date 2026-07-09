package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/s3client"
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

// BrandingAssetReconciler clears branding asset refs whose stored objects are
// missing. Satisfied by *branding.Service; may be nil when branding has no
// storage.
type BrandingAssetReconciler interface {
	ReconcileMissingAssets(ctx context.Context) (checked, cleared int, err error)
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
//
// The startup trigger fires exactly once per process, so a transient settings
// read failure here would postpone a needed reconcile until the next restart;
// retry briefly before giving up. (The task manager skips the run on a
// preflight error rather than failing open into a full sweep.)
func (t *ReconcileArtworkCacheTask) ShouldRun(ctx context.Context) (bool, error) {
	if t.runner == nil || t.settings == nil {
		return false, nil
	}
	var stored string
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		stored, err = t.settings.Get(ctx, ArtworkStorageIdentityKey)
		if err == nil {
			return stored != "" && stored != t.identity, nil
		}
		timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		}
	}
	return false, fmt.Errorf("reading artwork storage identity: %w", err)
}

func (t *ReconcileArtworkCacheTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.runner == nil || t.settings == nil {
		progress.Report(100, "Artwork reconcile is not configured")
		return nil
	}

	stats, err := t.runner.Run(ctx, progress.Report)
	if err != nil {
		if data, marshalErr := json.Marshal(stats); marshalErr == nil {
			progress.SetResultData(data)
		}
		return fmt.Errorf("reconciling artwork cache: %w", err)
	}

	// Only a clean, completed sweep certifies the current storage. Sweep
	// errors mean rows were skipped unverified, so the fingerprint stays
	// stale and the next startup retries; resets already applied this run
	// are durable either way.
	if stats.SweepErrors > 0 {
		if data, marshalErr := json.Marshal(stats); marshalErr == nil {
			progress.SetResultData(data)
		}
		return fmt.Errorf(
			"artwork reconcile: %d rows skipped on storage errors (verified %d, re-queued %d, cleared %d); storage identity left uncertified so the next startup retries",
			stats.SweepErrors, stats.Verified, stats.Requeued, stats.Cleared,
		)
	}
	// Certify before the branding check: a transient failure on that
	// 4-object pass must not discard a completed catalog sweep and force it
	// to repeat every boot.
	if setErr := t.settings.Set(ctx, ArtworkStorageIdentityKey, t.identity); setErr != nil {
		return fmt.Errorf("persisting artwork storage identity: %w", setErr)
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
		// SweepErrors is zero here (checked above), so these are probe or
		// branding errors — reported, but they don't reduce sweep coverage.
		message += fmt.Sprintf(", %d storage errors during probing", stats.Errors)
	}
	progress.Report(100, message+brandingNote)
	return nil
}
