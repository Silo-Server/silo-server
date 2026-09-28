package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/database/pglock"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/s3client"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// ErrArtworkReconcileManualRunRequired prevents a storage-location change from
// mutating artwork records on a scheduler trigger. An administrator must first
// migrate the existing objects, then explicitly run the task if they intend
// missing records to be reset for an explicit backfill or cleared.
var ErrArtworkReconcileManualRunRequired = errors.New("artwork storage changed; manual reconcile required")

var ErrArtworkReconcileManagedTransition = errors.New("artwork reconcile is reserved by a managed storage transition")

var ErrArtworkReconcileIdentityChanged = errors.New("artwork storage identity changed during reconcile")

var ErrArtworkReconcileStaleStore = errors.New("artwork reconcile store differs from configured storage")

// ArtworkStorageIdentityKey records the storage the catalog's artwork keys
// belong to. blobstore.Open records it on the first write and refuses a
// different store at startup; this task certifies it after a manual reconcile
// so a deliberate move (copy the tree, clear the row, restart) has one record
// to clear. Machine-managed; not an admin-editable setting.
const (
	ArtworkStorageIdentityKey = blobstore.IdentitySettingKey
	// ArtworkStorageReconcileCheckpointKey holds a machine-managed verify
	// cursor. It is scoped to both the stored and target identities so a later
	// storage move can never resume an older location's sweep.
	ArtworkStorageReconcileCheckpointKey = config.ArtworkStorageReconcileCheckpointKey
)

const (
	artworkStorageBackendSettingKey = "artwork.storage_backend"
	artworkLocalPathSettingKey      = "artwork.local_path"
)

// ArtworkReconcileSettingsStore is the server-settings surface the task needs.
// Satisfied by *catalog.ServerSettingsRepo and its encrypting decorator.
type ArtworkReconcileSettingsStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
	UpdateAtomic(ctx context.Context, update func(map[string]string) (map[string]string, error)) error
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
	pool     *pgxpool.Pool
}

func NewReconcileArtworkCacheTask(runner ArtworkReconcileRunner, settings ArtworkReconcileSettingsStore, branding BrandingAssetReconciler, identity string, pools ...*pgxpool.Pool) *ReconcileArtworkCacheTask {
	var pool *pgxpool.Pool
	if len(pools) > 0 {
		pool = pools[0]
	}
	return &ReconcileArtworkCacheTask{runner: runner, settings: settings, branding: branding, identity: identity, pool: pool}
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

func (t *ReconcileArtworkCacheTask) DefaultTriggers() []taskmanager.TriggerConfig { return nil }

// ManualOnly keeps the mutating sweep an explicit administrator action.
func (t *ReconcileArtworkCacheTask) ManualOnly() bool { return true }

// ShouldRun fails closed for every scheduler trigger, including a startup
// trigger an older installation persisted. Manual RunTask calls bypass this
// gate and remain the explicit recovery path.
func (t *ReconcileArtworkCacheTask) ShouldRun(ctx context.Context) (bool, error) {
	return false, t.CheckStorageIdentity(ctx)
}

// CheckStorageIdentity returns an actionable error when the configured artwork
// storage differs from the one the catalog was last reconciled against. The
// server calls it once at startup so the move is visible in logs; it never
// starts a sweep.
//
// It runs once per process, so a transient settings read failure would hide a
// needed reconcile until the next restart; retry briefly before giving up.
func (t *ReconcileArtworkCacheTask) CheckStorageIdentity(ctx context.Context) error {
	if t.runner == nil || t.settings == nil {
		return nil
	}
	stored, err := t.readStorageIdentity(ctx)
	if err != nil {
		return fmt.Errorf("reading artwork storage identity: %w", err)
	}
	if stored == "" || stored == t.identity {
		return nil
	}
	return fmt.Errorf(
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

	if err := t.rejectManagedTransition(ctx); err != nil {
		return err
	}
	lock, acquired, err := pglock.TryAcquire(ctx, t.pool, pglock.ArtworkReconcileLockKey)
	if err != nil {
		return fmt.Errorf("acquiring artwork reconcile lock: %w", err)
	}
	if t.pool != nil && !acquired {
		return fmt.Errorf("%w: wait for the managed reconcile to finish", ErrArtworkReconcileManagedTransition)
	}
	if lock != nil {
		defer func() {
			if releaseErr := lock.Release(context.Background()); releaseErr != nil {
				slog.WarnContext(ctx, "artwork reconcile: releasing advisory lock failed", "error", releaseErr)
			}
		}()
		if err := t.rejectManagedTransition(ctx); err != nil {
			return err
		}
	}
	if err := t.settings.UpdateAtomic(ctx, func(current map[string]string) (map[string]string, error) {
		return nil, t.checkConfiguredStore(current)
	}); err != nil {
		return fmt.Errorf("checking configured artwork storage: %w", err)
	}

	baseline, err := t.readStorageIdentity(ctx)
	if err != nil {
		return fmt.Errorf("reading artwork reconcile baseline identity: %w", err)
	}
	stats, err := t.run(ctx, baseline, progress.Report)
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
	// A managed transition can commit while this manual sweep is running.
	// Certify only the identity observed before the sweep, under the same
	// settings mutation lock used by the transition commit. Clear the old
	// checkpoint in that transaction so it cannot erase recovery state that
	// the committed transition writes afterward.
	if err := t.settings.UpdateAtomic(ctx, func(current map[string]string) (map[string]string, error) {
		blocked, err := managedTransitionBlocksReconcile(current[config.StorageTransitionTargetKey], t.identity)
		if err != nil {
			return nil, err
		}
		if blocked {
			return nil, ErrArtworkReconcileManagedTransition
		}
		if err := t.checkConfiguredStore(current); err != nil {
			return nil, err
		}
		if current[ArtworkStorageIdentityKey] != baseline {
			return nil, ErrArtworkReconcileIdentityChanged
		}
		return map[string]string{
			ArtworkStorageIdentityKey:            t.identity,
			ArtworkStorageReconcileCheckpointKey: "",
		}, nil
	}); err != nil {
		return fmt.Errorf("certifying artwork storage identity: %w", err)
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

func (t *ReconcileArtworkCacheTask) rejectManagedTransition(ctx context.Context) error {
	raw, err := t.settings.Get(ctx, config.StorageTransitionTargetKey)
	if err != nil {
		return fmt.Errorf("reading managed storage transition state: %w", err)
	}
	blocked, err := managedTransitionBlocksReconcile(raw, t.identity)
	if err != nil {
		return err
	}
	if blocked {
		return fmt.Errorf("%w: wait for the storage transition restart to finish", ErrArtworkReconcileManagedTransition)
	}
	return nil
}

func managedTransitionBlocksReconcile(raw, identity string) (bool, error) {
	if strings.TrimSpace(raw) == "" {
		return false, nil
	}
	var staged struct {
		Phase           string `json:"phase"`
		PublicReconcile bool   `json:"public_reconcile"`
		TargetIdentity  string `json:"target_identity"`
	}
	if err := json.Unmarshal([]byte(raw), &staged); err != nil {
		return false, fmt.Errorf("decoding managed storage transition state: %w", err)
	}
	return staged.Phase == "restart_pending" &&
		(staged.PublicReconcile || staged.TargetIdentity == "" || staged.TargetIdentity != identity), nil
}

// A process can still hold the old blob store after another API node restarts
// and clears the transition receipt. Compare its store with the active
// location settings before sweeping and again when certifying the result.
func (t *ReconcileArtworkCacheTask) checkConfiguredStore(current map[string]string) error {
	identity, known, err := configuredArtworkIdentity(current)
	if err != nil {
		return fmt.Errorf("reading configured artwork location: %w", err)
	}
	if known && identity != t.identity {
		return ErrArtworkReconcileStaleStore
	}
	return nil
}

func configuredArtworkIdentity(current map[string]string) (string, bool, error) {
	// Older installations may rely on the runtime defaults with no location
	// rows yet. A managed transition always persists the location keys.
	known := false
	for _, key := range [...]string{
		artworkStorageBackendSettingKey, artworkLocalPathSettingKey,
		"s3.public_endpoint", "s3.public_bucket", "s3.public_key_prefix",
		"s3.operational_endpoint", "s3.operational_bucket", "s3.operational_key_prefix",
	} {
		if _, ok := current[key]; ok {
			known = true
			break
		}
	}
	if !known {
		return "", false, nil
	}
	values := config.EffectiveAdminSettings(current)
	backend := strings.ToLower(strings.TrimSpace(values[artworkStorageBackendSettingKey]))
	if backend == "" || backend == config.ArtworkBackendAuto {
		backend = blobstore.BackendLocal
		if values["s3.public_bucket"] != "" {
			backend = blobstore.BackendS3
		}
	}
	switch backend {
	case blobstore.BackendLocal:
		identity, err := blobstore.LocalIdentity(values[artworkLocalPathSettingKey])
		return identity, true, err
	case blobstore.BackendS3:
		client := s3client.NewClient(s3client.BucketConfig{
			Endpoint:  values["s3.public_endpoint"],
			Bucket:    values["s3.public_bucket"],
			KeyPrefix: values["s3.public_key_prefix"],
		})
		return blobstore.NewS3(client).Identity(), true, nil
	default:
		return "", true, fmt.Errorf("unsupported artwork backend %q", backend)
	}
}

func (t *ReconcileArtworkCacheTask) run(
	ctx context.Context,
	baseline string,
	progress func(percent float64, message string),
) (metadata.ArtworkReconcileStats, error) {
	runner, ok := t.runner.(resumableArtworkReconcileRunner)
	if !ok {
		return t.runner.Run(ctx, progress)
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
