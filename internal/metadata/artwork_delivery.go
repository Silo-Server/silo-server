package metadata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkmetrics"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const (
	ArtworkRepairStatePending       = "queued"
	ArtworkRepairStateRepairing     = "repairing"
	ArtworkRepairStateProtectedLoss = "protected_loss"
)

type ArtworkTargetState struct {
	Target              artworkurl.Target
	SelectedPath        string
	SourcePath          string
	ImageType           string
	RepairTargetType    string
	RepairTargetID      string
	RepairLanguage      string
	RepairSeasonNumber  *int
	RepairEpisodeNumber *int
	Recoverable         bool
	Protected           bool
	Missing             bool
}

// ArtworkPublished reconciles revision-level loss and target-level alerts only
// after the conditional catalog publication succeeds. A single readable
// variant is insufficient evidence; normal materialization has completed and
// registered the whole portable manifest before this hook runs.
func (c *ArtworkDeliveryCoordinator) ArtworkPublished(ctx context.Context, job *models.MetadataImageCacheJob, previousPath, publishedPath string) error {
	if c == nil || c.pool == nil || job == nil {
		return nil
	}
	if err := c.revisions.ParkArtworkRevision(ctx, publishedPath, job.ImageType); err != nil {
		return err
	}
	// Only the published revision was proved durable. If upstream bytes changed,
	// the previous revision may still be missing while another target selects it.
	// The CTE captures whether this publication actually cleared loss state:
	// most publications are ordinary materializations of healthy paths, and
	// running the recovered metric plus the rebuild-status aggregate for each
	// of those turns a library scan into a per-image full accounting pass.
	wasLost := false
	if err := c.pool.QueryRow(ctx, `
		WITH prev AS (
			SELECT original_path,
				bool_or(missing_at IS NOT NULL OR repair_state <> '' OR protected_loss_at IS NOT NULL) AS was_lost
			FROM artwork_revision_gc_candidates WHERE original_path = $1
			GROUP BY original_path
		)
		UPDATE artwork_revision_gc_candidates c
		SET missing_at = NULL, repair_state = '', repair_queued_at = NULL,
			protected_loss_at = NULL, last_verified_at = NOW(), updated_at = NOW()
		FROM prev WHERE c.original_path = prev.original_path
		RETURNING prev.was_lost`, publishedPath).Scan(&wasLost); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	if !wasLost && !job.RepairRequested {
		return nil
	}
	target, ok, err := c.artworkTargetForImageCacheJob(ctx, job)
	if err != nil {
		return err
	}
	if ok {
		if _, err := c.pool.Exec(ctx, `
			UPDATE artwork_storage_alerts SET resolved_at = NOW(), last_seen_at = NOW()
			WHERE kind = 'protected_data_loss' AND surface_name = $1
			  AND target_keys = $2 AND image_slot = $3 AND resolved_at IS NULL`,
			target.Surface, target.Keys, target.Slot); err != nil {
			return err
		}
	}
	if wasLost {
		artworkmetrics.Repair("recovered")
	}
	if c.health != nil {
		if status, err := c.RebuildStatus(ctx); err == nil {
			c.health.ReportInventoryMissing(status.MissingReferences)
		}
	}
	return nil
}

func (c *ArtworkDeliveryCoordinator) artworkTargetForImageCacheJob(ctx context.Context, job *models.MetadataImageCacheJob) (artworkurl.Target, bool, error) {
	if c == nil || c.pool == nil || job == nil {
		return artworkurl.Target{}, false, nil
	}
	target := artworkurl.Target{Slot: job.ImageType}
	switch job.TargetType {
	case ImageCacheTargetItem:
		target.Surface, target.Keys = itemSurfaceForImageType(job.ImageType), []string{job.TargetContentID}
	case ImageCacheTargetItemLocalization:
		target.Surface, target.Keys = localizedItemSurfaceForImageType(job.ImageType), []string{job.TargetContentID, job.TargetLanguage}
	case ImageCacheTargetSeason, ImageCacheTargetSeasonLocalization:
		if job.SeasonNumber == nil {
			return artworkurl.Target{}, false, errors.New("season artwork job has no season number")
		}
		var seasonContentID string
		if err := c.pool.QueryRow(ctx, `SELECT content_id FROM seasons WHERE series_id = $1 AND season_number = $2`,
			job.TargetContentID, *job.SeasonNumber).Scan(&seasonContentID); err != nil {
			return artworkurl.Target{}, false, fmt.Errorf("resolve published season artwork target: %w", err)
		}
		if job.TargetType == ImageCacheTargetSeason {
			target.Surface, target.Keys = artworkurl.SurfaceSeasonPosters, []string{seasonContentID}
		} else {
			target.Surface, target.Keys = artworkurl.SurfaceLocalizedSeasonPosters, []string{seasonContentID, job.TargetLanguage}
		}
	case ImageCacheTargetEpisode:
		if job.SeasonNumber == nil || job.EpisodeNumber == nil {
			return artworkurl.Target{}, false, errors.New("episode artwork job has no season or episode number")
		}
		var episodeContentID string
		if err := c.pool.QueryRow(ctx, `SELECT content_id FROM episodes
			WHERE series_id = $1 AND season_number = $2 AND episode_number = $3`,
			job.TargetContentID, *job.SeasonNumber, *job.EpisodeNumber).Scan(&episodeContentID); err != nil {
			return artworkurl.Target{}, false, fmt.Errorf("resolve published episode artwork target: %w", err)
		}
		target.Surface, target.Keys = artworkurl.SurfaceEpisodeStills, []string{episodeContentID}
	case ImageCacheTargetPerson:
		target.Surface, target.Keys = artworkurl.SurfacePersonPhotos, []string{job.TargetContentID}
	default:
		return artworkurl.Target{}, false, nil
	}
	return target, target.Surface != "", nil
}

func itemSurfaceForImageType(imageType string) string {
	switch imageType {
	case artworkkey.ImageTypePoster:
		return artworkurl.SurfaceItemPosters
	case artworkkey.ImageTypeBackdrop:
		return artworkurl.SurfaceItemBackdrops
	case artworkkey.ImageTypeLogo:
		return artworkurl.SurfaceItemLogos
	default:
		return ""
	}
}

func localizedItemSurfaceForImageType(imageType string) string {
	switch imageType {
	case artworkkey.ImageTypePoster:
		return artworkurl.SurfaceLocalizedItemPosters
	case artworkkey.ImageTypeBackdrop:
		return artworkurl.SurfaceLocalizedItemBackdrops
	case artworkkey.ImageTypeLogo:
		return artworkurl.SurfaceLocalizedItemLogos
	default:
		return ""
	}
}

// ArtworkDeliveryCoordinator reloads target state and bridges request-time
// loss detection into the existing durable, deduplicated image-cache queue.
// It never clears a selected catalog path merely because delivery failed.
type ArtworkDeliveryCoordinator struct {
	pool      *pgxpool.Pool
	jobs      *ImageCacheJobRepository
	revisions *catalog.ArtworkRevisionTracker
	direct    *DirectLibraryArtworkResolver
	users     userstore.UserStoreProvider
	health    *artworkstore.Handle
}

func (c *ArtworkDeliveryCoordinator) SetStoreHealth(handle *artworkstore.Handle) {
	if c != nil {
		c.health = handle
	}
}

func (c *ArtworkDeliveryCoordinator) SetUserStoreProvider(users userstore.UserStoreProvider) {
	if c != nil {
		c.users = users
	}
}

func NewArtworkDeliveryCoordinator(pool *pgxpool.Pool, direct *DirectLibraryArtworkResolver, users ...userstore.UserStoreProvider) *ArtworkDeliveryCoordinator {
	if pool == nil {
		return nil
	}
	coordinator := &ArtworkDeliveryCoordinator{
		pool: pool, jobs: NewImageCacheJobRepository(pool), revisions: catalog.NewArtworkRevisionTracker(pool), direct: direct,
	}
	if len(users) > 0 {
		coordinator.users = users[0]
	}
	return coordinator
}

type artworkRecoveryState struct {
	storeHealth       string
	rebuildGeneration string
	enqueueComplete   bool
}

const artworkStoreRootAlertKind = "store_root_missing"

func (c *ArtworkDeliveryCoordinator) syncStoreRootAlert(ctx context.Context, state artworkstore.HealthState) error {
	if c == nil || c.pool == nil || c.health == nil || c.health.Backend != artworkstore.BackendLocal || !c.health.IsPinned() {
		return nil
	}
	if state == artworkstore.HealthUnavailable || state == artworkstore.HealthWrongMount {
		message := "Pinned local artwork store root is unavailable; restore it or explicitly rebuild the artwork store"
		if state == artworkstore.HealthWrongMount {
			message = "Pinned local artwork store markers are absent or mismatched; restore the expected mount or explicitly rebuild the artwork store"
		}
		_, err := c.pool.Exec(ctx, `INSERT INTO artwork_storage_alerts
			(kind, surface_name, target_keys, image_slot, original_path, message)
			VALUES ($1, 'artwork_store', '{}', 'root', '', $2)
			ON CONFLICT (kind, surface_name, target_keys, image_slot) DO UPDATE SET
				message = EXCLUDED.message, last_seen_at = NOW(), resolved_at = NULL`, artworkStoreRootAlertKind, message)
		return err
	}
	if state == artworkstore.HealthHealthy {
		_, err := c.pool.Exec(ctx, `UPDATE artwork_storage_alerts
			SET resolved_at = NOW(), last_seen_at = NOW()
			WHERE kind = $1 AND resolved_at IS NULL`, artworkStoreRootAlertKind)
		return err
	}
	return nil
}

func (c *ArtworkDeliveryCoordinator) loadRecoveryState(ctx context.Context) (artworkRecoveryState, error) {
	var state artworkRecoveryState
	err := c.pool.QueryRow(ctx, `SELECT store_health, rebuild_generation,
		rebuild_enqueued_at IS NOT NULL FROM artwork_storage_accounting_state WHERE singleton`).Scan(
		&state.storeHealth, &state.rebuildGeneration, &state.enqueueComplete,
	)
	if err != nil {
		return artworkRecoveryState{}, fmt.Errorf("load artwork recovery state: %w", err)
	}
	return state, nil
}

func (c *ArtworkDeliveryCoordinator) beginRecoveryGeneration(ctx context.Context, generation string) error {
	_, err := c.pool.Exec(ctx, `UPDATE artwork_storage_accounting_state SET
		store_health = 'empty_rebuilding', health_changed_at = NOW(),
		rebuild_generation = $1, rebuild_surface_name = '', rebuild_enqueued_at = NULL
		WHERE singleton`, generation)
	if err != nil {
		return fmt.Errorf("begin artwork recovery generation: %w", err)
	}
	return nil
}

func (c *ArtworkDeliveryCoordinator) completeRecoveryGeneration(ctx context.Context, health artworkstore.HealthState) error {
	_, err := c.pool.Exec(ctx, `UPDATE artwork_storage_accounting_state SET
		store_health = $1, health_changed_at = NOW() WHERE singleton`, string(health))
	if err != nil {
		return fmt.Errorf("complete artwork recovery generation: %w", err)
	}
	return nil
}

// shouldReenterArtworkRecovery reports whether a persisted rebuild intent must
// be re-adopted by a live store that no longer says it is rebuilding.
//
// The persisted generation is deliberately not required to match the live one.
// An explicit rebuild records its intent, then durably rotates the store's
// marker and pin, then writes the new generation; a crash in that last gap
// leaves an empty store on a fresh generation whose accounting row still names
// the old one. Requiring a match there means the store probes healthy, bulk
// recovery never runs, and the emptiness is never repaired. A mismatch is
// adopted by beginRecoveryGeneration, which restarts the enqueue against the
// live generation.
func shouldReenterArtworkRecovery(persisted artworkRecoveryState, live artworkstore.HealthState) bool {
	if persisted.storeHealth != string(artworkstore.HealthEmptyRebuilding) {
		return false
	}
	// An unreachable or wrong-mount root is not evidence of an empty store, and
	// a handle already rebuilding needs no nudge.
	return live != artworkstore.HealthEmptyRebuilding &&
		live != artworkstore.HealthUnavailable && live != artworkstore.HealthWrongMount
}

// RunRecovery coordinates authoritative-empty store recovery until ctx is
// canceled. Durable generation/checkpoint state makes restart re-entry and
// enqueue idempotence converge through the ordinary image-cache queue.
func (c *ArtworkDeliveryCoordinator) RunRecovery(ctx context.Context) {
	if c == nil || c.pool == nil || c.health == nil {
		return
	}
	backoff := 5 * time.Second
	for {
		state, _ := c.health.Health()
		persisted, err := c.loadRecoveryState(ctx)
		if alertErr := c.syncStoreRootAlert(ctx, state); alertErr != nil && err == nil {
			err = fmt.Errorf("sync artwork store root alert: %w", alertErr)
		}
		generation := c.health.GenerationID()
		if err == nil && shouldReenterArtworkRecovery(persisted, state) {
			if probeErr := c.health.ProbeNow(ctx); probeErr == nil {
				c.health.BeginRebuild()
				state = artworkstore.HealthEmptyRebuilding
			}
		}
		if err == nil && state == artworkstore.HealthEmptyRebuilding {
			if persisted.rebuildGeneration != generation {
				err = c.beginRecoveryGeneration(ctx, generation)
				persisted.enqueueComplete = false
			}
			if err == nil && !persisted.enqueueComplete {
				queued, enqueueErr := c.EnqueueBulkRecovery(ctx)
				if enqueueErr != nil {
					err = enqueueErr
				} else {
					persisted.enqueueComplete = true
					slog.InfoContext(ctx, "artwork bulk recovery queued", "targets", queued, "generation", generation)
				}
			}
			if err == nil && persisted.enqueueComplete {
				var rebuildStatus ArtworkRebuildStatus
				rebuildStatus, err = c.RebuildStatus(ctx)
				if err == nil && rebuildStatus.OutstandingJobs == 0 {
					degraded := rebuildStatus.ProtectedLosses > 0 || rebuildStatus.MissingReferences > 0
					c.health.CompleteRebuild(degraded)
					finalHealth := artworkstore.HealthHealthy
					if degraded {
						finalHealth = artworkstore.HealthDegraded
					}
					err = c.completeRecoveryGeneration(ctx, finalHealth)
				}
			}
		}
		if err != nil {
			slog.WarnContext(ctx, "artwork recovery coordinator iteration failed", "error", err)
		} else {
			backoff = 5 * time.Second
		}
		wait := 30 * time.Second
		if state == artworkstore.HealthEmptyRebuilding || err != nil {
			wait = backoff
			if backoff < 5*time.Minute {
				backoff *= 2
				if backoff > 5*time.Minute {
					backoff = 5 * time.Minute
				}
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// ArtworkRebuildStatus is the durable completion gate for an authoritative
// empty-store rebuild. Missing and protected counts include only revisions a
// current catalog surface still selects; orphaned inventory remains visible to
// accounting and GC without keeping the store in empty_rebuilding.
type ArtworkRebuildStatus struct {
	OutstandingJobs   int64
	ProtectedLosses   int64
	MissingReferences int64
}

func artworkRebuildStatusSQL() string {
	return `WITH loss_paths AS (
		SELECT original_path FROM artwork_revision_gc_candidates
		WHERE tombstoned_at IS NULL
		  AND (missing_at IS NOT NULL OR repair_state = 'protected_loss')
	), referenced_paths AS (
		SELECT DISTINCT path FROM (` + artworkLossReferenceUnionSQL() + `) refs
	) SELECT
		(SELECT count(*) FROM metadata_image_cache_jobs
		 WHERE repair_requested AND status IN ('queued', 'running')),
		(SELECT count(*) FROM artwork_revision_gc_candidates i
		 WHERE i.repair_state = 'protected_loss' AND i.tombstoned_at IS NULL
		   AND i.original_path IN (SELECT path FROM referenced_paths)),
		(SELECT count(*) FROM artwork_revision_gc_candidates i
		 WHERE i.missing_at IS NOT NULL AND i.tombstoned_at IS NULL
		   AND i.original_path IN (SELECT path FROM referenced_paths))`
}

func (c *ArtworkDeliveryCoordinator) RebuildStatus(ctx context.Context) (ArtworkRebuildStatus, error) {
	var status ArtworkRebuildStatus
	if c == nil || c.pool == nil {
		return status, errors.New("artwork delivery coordinator is not configured")
	}
	err := c.pool.QueryRow(ctx, artworkRebuildStatusSQL()).Scan(
		&status.OutstandingJobs, &status.ProtectedLosses, &status.MissingReferences,
	)
	if err != nil {
		return ArtworkRebuildStatus{}, fmt.Errorf("load artwork rebuild status: %w", err)
	}
	return status, nil
}

func (c *ArtworkDeliveryCoordinator) LoadTarget(ctx context.Context, target artworkurl.Target) (ArtworkTargetState, error) {
	if c == nil || c.pool == nil {
		return ArtworkTargetState{}, errors.New("artwork delivery coordinator is not configured")
	}
	if err := target.Validate(); err != nil {
		return ArtworkTargetState{}, err
	}
	if target.Surface == artworkurl.SurfaceProfileAvatars || target.Surface == artworkurl.SurfaceUserCollectionPosters {
		return c.loadUserTarget(ctx, target)
	}
	if target.Surface == artworkurl.SurfaceChapterThumbnails {
		return c.loadChapterTarget(ctx, target)
	}
	surface, ok := artworkSweepSurfaceByName(target.Surface)
	if !ok || len(target.Keys) != len(surface.keyCols) || target.Slot != surface.imageType {
		return ArtworkTargetState{}, errors.New("artwork target does not name a supported catalog surface")
	}
	values, err := surface.parseKeys(target.Keys)
	if err != nil {
		return ArtworkTargetState{}, err
	}
	where := make([]string, len(surface.keyCols))
	for i, key := range surface.keyCols {
		where[i] = fmt.Sprintf("%s = $%d", key.column, i+1)
	}
	sourceExpr := "''"
	if surface.sourceCol != "" {
		sourceExpr = "COALESCE(" + surface.sourceCol + ", '')"
	}
	repairExprs := surface.repairIdentitySelectExpressions()
	query := fmt.Sprintf(`SELECT COALESCE(%[1]s, ''), %[2]s,
		EXISTS (SELECT 1 FROM artwork_revision_gc_candidates inventory
			WHERE inventory.original_path = COALESCE(%[1]s, '') AND inventory.missing_at IS NOT NULL),
		%[5]s
		FROM %[3]s WHERE %[4]s`, surface.pathCol, sourceExpr, surface.table, strings.Join(where, " AND "), strings.Join(repairExprs, ", "))
	var selected, source, repairTargetID, repairLanguage string
	var repairSeason, repairEpisode *int
	var missing bool
	if err := c.pool.QueryRow(ctx, query, values...).Scan(
		&selected, &source, &missing, &repairTargetID, &repairLanguage, &repairSeason, &repairEpisode,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ArtworkTargetState{}, errors.New("artwork target no longer exists")
		}
		return ArtworkTargetState{}, fmt.Errorf("load artwork target: %w", err)
	}
	selected = strings.TrimSpace(selected)
	source = strings.TrimSpace(source)
	if source == "" && strings.Contains(selected, "://") {
		source = selected
	}
	recoverable := isRequestRecoverableArtworkSource(source)
	return ArtworkTargetState{
		Target: target, SelectedPath: selected, SourcePath: source, ImageType: surface.imageType,
		RepairTargetType: surface.repairTarget.targetType, RepairTargetID: repairTargetID,
		RepairLanguage: repairLanguage, RepairSeasonNumber: repairSeason, RepairEpisodeNumber: repairEpisode,
		Recoverable: recoverable, Protected: !recoverable && selected != "" && selected != "-", Missing: missing,
	}, nil
}

func (c *ArtworkDeliveryCoordinator) loadChapterTarget(ctx context.Context, target artworkurl.Target) (ArtworkTargetState, error) {
	if len(target.Keys) != 2 || target.Slot != ImageCacheImageStill {
		return ArtworkTargetState{}, errors.New("chapter artwork target is invalid")
	}
	fileID, err := strconv.Atoi(target.Keys[0])
	if err != nil || fileID <= 0 {
		return ArtworkTargetState{}, errors.New("chapter artwork target has an invalid file key")
	}
	chapterIndex, err := strconv.Atoi(target.Keys[1])
	if err != nil || chapterIndex < 0 {
		return ArtworkTargetState{}, errors.New("chapter artwork target has an invalid chapter key")
	}
	var selected string
	var missing bool
	err = c.pool.QueryRow(ctx, `
		SELECT COALESCE(chapter->>'thumbnail_path', ''),
			EXISTS (SELECT 1 FROM artwork_revision_gc_candidates inventory
				WHERE inventory.original_path = COALESCE(chapter->>'thumbnail_path', '')
				  AND inventory.missing_at IS NOT NULL)
		FROM media_files mf
		CROSS JOIN LATERAL jsonb_array_elements(COALESCE(mf.chapters, '[]'::jsonb)) chapter
		WHERE mf.id = $1 AND (chapter->>'index')::integer = $2`, fileID, chapterIndex).Scan(&selected, &missing)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ArtworkTargetState{}, errors.New("chapter artwork target no longer exists")
		}
		return ArtworkTargetState{}, fmt.Errorf("load chapter artwork target: %w", err)
	}
	selected = strings.TrimSpace(selected)
	return ArtworkTargetState{
		Target: target, SelectedPath: selected, ImageType: ImageCacheImageStill,
		Protected: selected != "" && selected != "-", Missing: missing,
	}, nil
}

func (c *ArtworkDeliveryCoordinator) loadUserTarget(ctx context.Context, target artworkurl.Target) (ArtworkTargetState, error) {
	if c.users == nil || len(target.Keys) != 2 {
		return ArtworkTargetState{}, errors.New("user artwork target is not available")
	}
	userID, err := strconv.Atoi(target.Keys[0])
	if err != nil || userID <= 0 {
		return ArtworkTargetState{}, errors.New("user artwork target has an invalid account key")
	}
	store, err := c.users.ForUser(ctx, userID)
	if err != nil {
		return ArtworkTargetState{}, err
	}
	selected := ""
	imageType := ""
	switch target.Surface {
	case artworkurl.SurfaceProfileAvatars:
		if target.Slot != "avatar" {
			return ArtworkTargetState{}, errors.New("profile artwork target has an invalid slot")
		}
		profile, err := store.GetProfile(ctx, target.Keys[1])
		if err != nil || profile == nil {
			return ArtworkTargetState{}, errors.New("profile artwork target no longer exists")
		}
		selected = strings.TrimPrefix(strings.TrimSpace(profile.Avatar), "upload:")
		imageType = "avatar"
	case artworkurl.SurfaceUserCollectionPosters:
		if target.Slot != artworkkey.ImageTypeCollectionPoster {
			return ArtworkTargetState{}, errors.New("personal collection artwork target has an invalid slot")
		}
		collection, err := store.GetCollection(ctx, target.Keys[1])
		if err != nil || collection == nil {
			return ArtworkTargetState{}, errors.New("personal collection artwork target no longer exists")
		}
		selected = strings.TrimSpace(collection.PosterURL)
		imageType = "collection-poster"
	}
	if selected == "" || strings.Contains(selected, "://") {
		return ArtworkTargetState{}, errors.New("user artwork target has no stored selection")
	}
	var missing bool
	_ = c.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM artwork_revision_gc_candidates WHERE original_path = $1 AND missing_at IS NOT NULL)`, selected).Scan(&missing)
	return ArtworkTargetState{
		Target: target, SelectedPath: selected, SourcePath: "upload://protected", ImageType: imageType,
		Protected: true, Missing: missing,
	}, nil
}

func isRequestRecoverableArtworkSource(source string) bool {
	source = strings.ToLower(strings.TrimSpace(source))
	if source == "" || strings.HasPrefix(source, "upload://") || strings.HasPrefix(source, "embedded://") || strings.HasPrefix(source, "generated://") {
		return false
	}
	if strings.HasPrefix(source, "file://") || strings.HasPrefix(source, artworkurl.LibraryReferencePrefix) ||
		strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		return true
	}
	// Any remaining scheme is treated as a provider/plugin reference — but only
	// if the repair queue would actually accept it. Legacy s3:// and local://
	// sources cannot be re-fetched; classifying them recoverable marks the
	// revision repair-queued while no job is ever admitted, so the loss stays
	// silent forever instead of raising the protected-loss alert.
	return strings.Contains(source, "://") && !isNonProviderImageScheme(source)
}

// StoredVariant returns the current immutable key for variant. A stale signed
// expected revision never selects displaced bytes: the current target wins.
func (s ArtworkTargetState) StoredVariant(variant string) string {
	if s.SelectedPath == "" || strings.Contains(s.SelectedPath, "://") {
		return ""
	}
	return artworkkey.Variant(s.SelectedPath, variant)
}

func (c *ArtworkDeliveryCoordinator) ReadSidecar(ctx context.Context, state ArtworkTargetState) (ConfinedLocalArtwork, error) {
	if c == nil || c.direct == nil || !strings.HasPrefix(strings.ToLower(state.SourcePath), "file://") {
		return ConfinedLocalArtwork{}, errors.New("artwork target has no confined sidecar source")
	}
	return c.direct.ReadSource(ctx, state.Target.Surface, state.Target.Keys, state.SourcePath)
}

// SignalMissing records authoritative absence and emits one deduplicated
// repair signal per target. Protected selections remain selected and receive a
// persistent alert instead of a catalog reset.
func (c *ArtworkDeliveryCoordinator) SignalMissing(ctx context.Context, state ArtworkTargetState) error {
	if c == nil || c.pool == nil || state.SelectedPath == "" {
		return nil
	}
	if state.Target.Surface == artworkurl.SurfaceChapterThumbnails {
		return c.signalMissingChapterThumbnail(ctx, state)
	}
	repairState := ArtworkRepairStateProtectedLoss
	if state.Recoverable {
		repairState = ArtworkRepairStatePending
	}
	_, err := c.pool.Exec(ctx, `
		UPDATE artwork_revision_gc_candidates
		SET missing_at = COALESCE(missing_at, NOW()), repair_state = $2,
			repair_queued_at = CASE WHEN $2 = 'queued' THEN COALESCE(repair_queued_at, NOW()) ELSE repair_queued_at END,
			protected_loss_at = CASE WHEN $2 = 'protected_loss' THEN COALESCE(protected_loss_at, NOW()) ELSE protected_loss_at END,
			updated_at = NOW()
		WHERE original_path = $1`, state.SelectedPath, repairState)
	if err != nil {
		return fmt.Errorf("mark missing artwork revision: %w", err)
	}
	if state.Recoverable {
		artworkmetrics.Repair("missing")
		if input, ok := repairJobForTarget(state); ok {
			_, err := c.jobs.EnqueueRepair(ctx, input)
			if err == nil {
				artworkmetrics.Repair("queued")
			}
			return err
		}
		return nil
	}
	_, err = c.pool.Exec(ctx, `
		INSERT INTO artwork_storage_alerts (kind, surface_name, target_keys, image_slot, original_path, message)
		VALUES ('protected_data_loss', $1, $2, $3, $4, 'Selected artwork bytes are missing and no verified fallback exists')
		ON CONFLICT (kind, surface_name, target_keys, image_slot) DO UPDATE SET
			original_path = EXCLUDED.original_path, message = EXCLUDED.message,
			last_seen_at = NOW(), resolved_at = NULL`,
		state.Target.Surface, state.Target.Keys, state.Target.Slot, state.SelectedPath)
	if err == nil {
		artworkmetrics.Repair("protected_loss")
	}
	return err
}

// signalMissingChapterThumbnail treats a lost chapter still as regenerable
// rather than as permanent loss. A chapter thumbnail is extracted from the
// media file, and internal/chapterthumbs re-extracts a chapter exactly when its
// thumbnail_path is empty — so clearing the selection is the repair signal.
// There is no image-cache job to enqueue and nothing to alert about: the same
// clear is what the manual artwork reconciler performs when it finds the bytes
// gone.
func (c *ArtworkDeliveryCoordinator) signalMissingChapterThumbnail(ctx context.Context, state ArtworkTargetState) error {
	if len(state.Target.Keys) != 2 {
		return errors.New("chapter artwork target is invalid")
	}
	fileID, err := strconv.Atoi(state.Target.Keys[0])
	if err != nil || fileID <= 0 {
		return errors.New("chapter artwork target has an invalid file key")
	}
	chapterIndex, err := strconv.Atoi(state.Target.Keys[1])
	if err != nil || chapterIndex < 0 {
		return errors.New("chapter artwork target has an invalid chapter key")
	}
	artworkmetrics.Repair("missing")
	if _, err := c.pool.Exec(ctx, `
		UPDATE artwork_revision_gc_candidates
		SET missing_at = COALESCE(missing_at, NOW()), repair_state = $2,
			repair_queued_at = COALESCE(repair_queued_at, NOW()), updated_at = NOW()
		WHERE original_path = $1`, state.SelectedPath, ArtworkRepairStatePending); err != nil {
		return fmt.Errorf("mark missing chapter thumbnail revision: %w", err)
	}
	// Guarded on the selection that was actually found missing, so a concurrent
	// re-extraction that already wrote a new thumbnail wins over this clear.
	tag, err := c.pool.Exec(ctx, `
		UPDATE media_files mf
		SET chapters = rebuilt.chapters, chapter_thumbnail_retry_after = NULL
		FROM (
			SELECT jsonb_agg(
				CASE WHEN (entry.chapter->>'index')::integer = $2
					AND COALESCE(entry.chapter->>'thumbnail_path', '') = $3
				THEN (entry.chapter - 'thumbnail_retry_after' - 'thumbnail_failed_at' - 'thumbnail_last_error')
					|| jsonb_build_object('thumbnail_path', '', 'thumbnail_thumbhash', '')
				ELSE entry.chapter END
				ORDER BY entry.ordinality) AS chapters
			FROM media_files source
			CROSS JOIN LATERAL jsonb_array_elements(COALESCE(source.chapters, '[]'::jsonb))
				WITH ORDINALITY AS entry(chapter, ordinality)
			WHERE source.id = $1
		) rebuilt
		WHERE mf.id = $1
		  AND EXISTS (
			SELECT 1 FROM jsonb_array_elements(COALESCE(mf.chapters, '[]'::jsonb)) chapter
			WHERE (chapter->>'index')::integer = $2
			  AND COALESCE(chapter->>'thumbnail_path', '') = $3)`,
		fileID, chapterIndex, state.SelectedPath)
	if err != nil {
		return fmt.Errorf("clear missing chapter thumbnail: %w", err)
	}
	if tag.RowsAffected() > 0 {
		artworkmetrics.Repair("queued")
	}
	return nil
}

func repairJobForTarget(state ArtworkTargetState) (EnqueueImageCacheJobInput, bool) {
	if state.RepairTargetType == "" || state.RepairTargetID == "" {
		return EnqueueImageCacheJobInput{}, false
	}
	input := EnqueueImageCacheJobInput{
		TargetType: state.RepairTargetType, TargetContentID: state.RepairTargetID,
		TargetLanguage: state.RepairLanguage, SeriesID: state.RepairTargetID,
		SourcePath: state.SourcePath, ImageType: state.ImageType,
		SeasonNumber: state.RepairSeasonNumber, EpisodeNumber: state.RepairEpisodeNumber,
	}
	if input.TargetType == ImageCacheTargetPerson {
		input.SeriesID = ""
	}
	return input, true
}

// EnqueueBulkRecovery walks recoverable selected artwork in recency order and
// feeds it into the ordinary durable image-cache queue. Catalog selections are
// never cleared: until a conditional publication succeeds they continue to
// render source fallback or a placeholder. Request misses use the same unique
// queue key and therefore safely jump a target back to due-now work.
func (c *ArtworkDeliveryCoordinator) EnqueueBulkRecovery(ctx context.Context) (int, error) {
	if c == nil || c.pool == nil || c.jobs == nil {
		return 0, errors.New("artwork delivery coordinator is not configured")
	}
	var checkpoint string
	if err := c.pool.QueryRow(ctx, `SELECT rebuild_surface_name FROM artwork_storage_accounting_state WHERE singleton`).Scan(&checkpoint); err != nil {
		return 0, fmt.Errorf("load artwork rebuild checkpoint: %w", err)
	}
	resume := checkpoint == ""
	total := 0
	for _, surface := range artworkSweepSurfaces() {
		if !resume {
			if surface.name == checkpoint {
				resume = true
			}
			continue
		}
		sourceExpr := "''"
		if surface.sourceCol != "" {
			sourceExpr = "COALESCE(" + surface.sourceCol + ", '')"
		}
		selects := append(surface.keySelectExpressions(), surface.pathCol, sourceExpr)
		selects = append(selects, surface.repairIdentitySelectExpressions()...)
		order := "updated_at DESC, " + strings.Join(surface.keyColumnNames(), ", ")
		if surface.noUpdatedAt {
			order = strings.Join(surface.keyColumnNames(), ", ")
		}
		query := fmt.Sprintf(`SELECT %s FROM %s
			WHERE %s ORDER BY %s`, strings.Join(selects, ", "), surface.table, surface.cachedPredicate(), order)
		rows, err := c.pool.Query(ctx, query)
		if err != nil {
			return total, fmt.Errorf("scan %s recovery targets: %w", surface.name, err)
		}
		batch := make([]EnqueueImageCacheJobInput, 0, 250)
		missingPaths := make([]string, 0, 250)
		protected := make([]ArtworkTargetState, 0, 250)
		for rows.Next() {
			keys := make([]string, len(surface.keyCols))
			dest := make([]any, 0, len(keys)+6)
			for i := range keys {
				dest = append(dest, &keys[i])
			}
			var selected, source, repairTargetID, repairLanguage string
			var repairSeason, repairEpisode *int
			dest = append(dest, &selected, &source, &repairTargetID, &repairLanguage, &repairSeason, &repairEpisode)
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				return total, fmt.Errorf("read %s recovery target: %w", surface.name, err)
			}
			state := ArtworkTargetState{Target: artworkurl.Target{
				Surface: surface.name, Keys: keys, Slot: surface.imageType,
			}, SelectedPath: selected, SourcePath: source, ImageType: surface.imageType,
				RepairTargetType: surface.repairTarget.targetType, RepairTargetID: repairTargetID,
				RepairLanguage: repairLanguage, RepairSeasonNumber: repairSeason, RepairEpisodeNumber: repairEpisode,
				Recoverable: isRequestRecoverableArtworkSource(source)}
			if input, ok := repairJobForTarget(state); ok && state.Recoverable {
				batch = append(batch, input)
				missingPaths = append(missingPaths, selected)
			} else if !state.Recoverable {
				state.Protected = true
				protected = append(protected, state)
			}
			if len(batch) == cap(batch) {
				if err := c.markBulkMissing(ctx, missingPaths); err != nil {
					rows.Close()
					return total, err
				}
				queued, err := c.jobs.EnqueueRepairBatch(ctx, batch)
				if err != nil {
					rows.Close()
					return total, err
				}
				total += queued
				batch = batch[:0]
				missingPaths = missingPaths[:0]
			}
			if len(protected) == cap(protected) {
				if err := c.markBulkProtected(ctx, protected); err != nil {
					rows.Close()
					return total, err
				}
				protected = protected[:0]
			}
		}
		rowsErr := rows.Err()
		rows.Close()
		if rowsErr != nil {
			return total, fmt.Errorf("iterate %s recovery targets: %w", surface.name, rowsErr)
		}
		if len(batch) > 0 {
			if err := c.markBulkMissing(ctx, missingPaths); err != nil {
				return total, err
			}
			queued, err := c.jobs.EnqueueRepairBatch(ctx, batch)
			if err != nil {
				return total, err
			}
			total += queued
		}
		if len(protected) > 0 {
			if err := c.markBulkProtected(ctx, protected); err != nil {
				return total, err
			}
		}
		if _, err := c.pool.Exec(ctx, `UPDATE artwork_storage_accounting_state
			SET rebuild_surface_name = $1, rebuild_enqueued_at = NULL WHERE singleton`, surface.name); err != nil {
			return total, fmt.Errorf("save artwork rebuild checkpoint: %w", err)
		}
	}
	if checkpoint != "" && !resume {
		return total, fmt.Errorf("artwork rebuild checkpoint names unknown surface %q", checkpoint)
	}
	if err := c.markBulkProtectedProfileAvatars(ctx); err != nil {
		return total, err
	}
	if _, err := c.pool.Exec(ctx, `UPDATE artwork_storage_accounting_state SET rebuild_enqueued_at = NOW() WHERE singleton`); err != nil {
		return total, fmt.Errorf("finish artwork rebuild enqueue: %w", err)
	}
	return total, nil
}

// markBulkProtectedProfileAvatars covers the upload-only reference surface that
// deliberately stays out of artworkSweepSurfaces: an uploaded avatar has no
// source from which a repair job could reconstruct it. Legacy avatar bucket
// keys share the upload: prefix but are not canonical portable revisions, so
// only portable keys participate in canonical-store loss accounting.
func (c *ArtworkDeliveryCoordinator) markBulkProtectedProfileAvatars(ctx context.Context) error {
	surface := profileAvatarReferenceSurface()
	rows, err := c.pool.Query(ctx, fmt.Sprintf(`
		SELECT user_id::text, id, %s
		FROM %s WHERE %s
		ORDER BY updated_at DESC, user_id, id`, surface.pathExpr, surface.table, surface.filter))
	if err != nil {
		return fmt.Errorf("scan profile avatar recovery targets: %w", err)
	}
	defer rows.Close()

	protected := make([]ArtworkTargetState, 0, 250)
	for rows.Next() {
		var userID, profileID, selected string
		if err := rows.Scan(&userID, &profileID, &selected); err != nil {
			return fmt.Errorf("read profile avatar recovery target: %w", err)
		}
		selected = strings.TrimSpace(selected)
		if !artworkkey.IsPortableKey(selected) {
			continue
		}
		protected = append(protected, ArtworkTargetState{
			Target: artworkurl.Target{
				Surface: artworkurl.SurfaceProfileAvatars,
				Keys:    []string{userID, profileID},
				Slot:    artworkkey.ImageTypeAvatar,
			},
			SelectedPath: selected,
			ImageType:    artworkkey.ImageTypeAvatar,
			Protected:    true,
		})
		if len(protected) == cap(protected) {
			if err := c.markBulkProtected(ctx, protected); err != nil {
				return err
			}
			protected = protected[:0]
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate profile avatar recovery targets: %w", err)
	}
	return c.markBulkProtected(ctx, protected)
}

func (c *ArtworkDeliveryCoordinator) markBulkProtected(ctx context.Context, states []ArtworkTargetState) error {
	if len(states) == 0 {
		return nil
	}
	paths := make([]string, 0, len(states))
	for _, state := range states {
		paths = append(paths, state.SelectedPath)
	}
	if _, err := c.pool.Exec(ctx, `UPDATE artwork_revision_gc_candidates SET
		missing_at = COALESCE(missing_at, NOW()), repair_state = 'protected_loss',
		protected_loss_at = COALESCE(protected_loss_at, NOW()), updated_at = NOW()
		WHERE original_path = ANY($1)`, paths); err != nil {
		return fmt.Errorf("mark protected artwork losses: %w", err)
	}
	batch := &pgx.Batch{}
	for _, state := range states {
		batch.Queue(`INSERT INTO artwork_storage_alerts
			(kind, surface_name, target_keys, image_slot, original_path, message)
			VALUES ('protected_data_loss', $1, $2, $3, $4, 'Selected artwork bytes are missing and no verified fallback exists')
			ON CONFLICT (kind, surface_name, target_keys, image_slot) DO UPDATE SET
				original_path = EXCLUDED.original_path, message = EXCLUDED.message,
				last_seen_at = NOW(), resolved_at = NULL`,
			state.Target.Surface, state.Target.Keys, state.Target.Slot, state.SelectedPath)
	}
	results := c.pool.SendBatch(ctx, batch)
	defer func() { _ = results.Close() }()
	for range states {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("record protected artwork loss: %w", err)
		}
	}
	artworkmetrics.Repair("protected_loss")
	return nil
}

func (c *ArtworkDeliveryCoordinator) markBulkMissing(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	_, err := c.pool.Exec(ctx, `
		UPDATE artwork_revision_gc_candidates
		SET missing_at = COALESCE(missing_at, NOW()), repair_state = 'queued',
			repair_queued_at = COALESCE(repair_queued_at, NOW()), updated_at = NOW()
		WHERE original_path = ANY($1)`, paths)
	if err != nil {
		return fmt.Errorf("mark bulk recovery revisions missing: %w", err)
	}
	return nil
}
