package metadata

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type alertTestSettings map[string]string

func (s alertTestSettings) Get(_ context.Context, key string) (string, error) {
	return s[key], nil
}

func (s alertTestSettings) SetIfAbsent(_ context.Context, key, value string) (bool, error) {
	if s[key] != "" {
		return false, nil
	}
	s[key] = value
	return true, nil
}

func (s alertTestSettings) Set(_ context.Context, key, value string) error {
	s[key] = value
	return nil
}

func TestStoreRootAlertLifecycle(t *testing.T) {
	pool := localArtworkTestPool(t)
	ctx := t.Context()
	handle, err := artworkstore.Open(ctx, artworkstore.Options{
		Backend: artworkstore.BackendLocal, LocalPath: t.TempDir(), Settings: alertTestSettings{},
	})
	if err != nil {
		t.Fatalf("open alert test store: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err := handle.Store.WriteImmutable(ctx, "artwork/v1/alert-test.webp", []byte("pinned"), artworkstore.ObjectMetadata{}); err != nil {
		t.Fatalf("pin alert test store: %v", err)
	}
	coordinator := &ArtworkDeliveryCoordinator{pool: pool, health: handle}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_storage_alerts WHERE kind = $1`, artworkStoreRootAlertKind)
	})

	if err := coordinator.syncStoreRootAlert(ctx, artworkstore.HealthUnavailable); err != nil {
		t.Fatalf("raise root alert: %v", err)
	}
	var active int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM artwork_storage_alerts WHERE kind = $1 AND resolved_at IS NULL`, artworkStoreRootAlertKind).Scan(&active); err != nil {
		t.Fatalf("read active root alert: %v", err)
	}
	if active != 1 {
		t.Fatalf("active root alerts = %d, want 1", active)
	}
	if err := coordinator.syncStoreRootAlert(ctx, artworkstore.HealthHealthy); err != nil {
		t.Fatalf("resolve root alert: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM artwork_storage_alerts WHERE kind = $1 AND resolved_at IS NULL`, artworkStoreRootAlertKind).Scan(&active); err != nil {
		t.Fatalf("read resolved root alert: %v", err)
	}
	if active != 0 {
		t.Fatalf("active root alerts after recovery = %d, want 0", active)
	}
}

func localArtworkTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestCurrentTargetCachedPathItem(t *testing.T) {
	pool := localArtworkTestPool(t)
	ctx := context.Background()
	contentID := fmt.Sprintf("local-art-%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, poster_path, poster_source_path)
		VALUES ($1, 'movie', 'Local Art Test', 'matched', '{}'::text[], $2, $3)
	`, contentID, "local/movies/"+contentID+"/deadbeef/poster/original.webp", "file:///media/movies/Film/poster.jpg"); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})

	repo := NewImageCacheJobRepository(pool)
	job := &models.MetadataImageCacheJob{
		TargetType:      ImageCacheTargetItem,
		TargetContentID: contentID,
		ImageType:       ImageCacheImagePoster,
	}
	cached, err := repo.CurrentTargetCachedPath(ctx, job)
	if err != nil {
		t.Fatalf("CurrentTargetCachedPath: %v", err)
	}
	if want := "local/movies/" + contentID + "/deadbeef/poster/original.webp"; cached != want {
		t.Fatalf("cached = %q, want %q", cached, want)
	}
	source, found, err := repo.CurrentTargetSourcePath(ctx, job)
	if err != nil {
		t.Fatalf("CurrentTargetSourcePath: %v", err)
	}
	if !found {
		t.Fatal("CurrentTargetSourcePath did not find the seeded item")
	}
	if source != "file:///media/movies/Film/poster.jpg" {
		t.Fatalf("source = %q", source)
	}

	// Missing rows report empty, not an error.
	missing, err := repo.CurrentTargetCachedPath(ctx, &models.MetadataImageCacheJob{
		TargetType:      ImageCacheTargetItem,
		TargetContentID: contentID + "-missing",
		ImageType:       ImageCacheImagePoster,
	})
	if err != nil || missing != "" {
		t.Fatalf("missing row: cached=%q err=%v", missing, err)
	}
}

func TestEnqueueBatchAcceptsLocalSourceDB(t *testing.T) {
	pool := localArtworkTestPool(t)
	ctx := context.Background()
	contentID := fmt.Sprintf("local-art-enq-%d", time.Now().UnixNano())
	repo := NewImageCacheJobRepository(pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM metadata_image_cache_jobs WHERE target_content_id = $1`, contentID)
	})

	n, err := repo.EnqueueBatch(ctx, []EnqueueImageCacheJobInput{{
		TargetType:      ImageCacheTargetItem,
		TargetContentID: contentID,
		SeriesID:        contentID,
		SourcePath:      "file:///media/movies/Film/poster.jpg",
		ContentType:     "movies",
		ImageType:       ImageCacheImagePoster,
	}})
	if err != nil {
		t.Fatalf("EnqueueBatch: %v", err)
	}
	if n != 1 {
		t.Fatalf("enqueued %d, want 1", n)
	}
	var providerID string
	if err := pool.QueryRow(ctx,
		`SELECT provider_id FROM metadata_image_cache_jobs WHERE target_content_id = $1`, contentID,
	).Scan(&providerID); err != nil {
		t.Fatalf("read job: %v", err)
	}
	if providerID != "local" {
		t.Fatalf("provider_id = %q, want local", providerID)
	}
}

func TestBulkRecoverySeasonJobUsesNaturalTargetAndPublishes(t *testing.T) {
	pool := localArtworkTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	seriesID := fmt.Sprintf("series-repair-season-%d", suffix)
	seasonID := fmt.Sprintf("season-repair-season-%d-0", suffix)
	sourcePath := fmt.Sprintf("tvdb://series/%d/seasons/0/poster.jpg", suffix)
	selectedPath := fmt.Sprintf("tvdb/series/%d/seasons/0/poster/original.webp", suffix)
	publishedPath := fmt.Sprintf("tvdb/series/%d/seasons/0/poster/repaired.webp", suffix)
	workerID := fmt.Sprintf("season-repair-worker-%d", suffix)

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM metadata_image_cache_jobs WHERE series_id = $1`, seriesID)
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, selectedPath)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(ctx, `UPDATE artwork_storage_accounting_state
			SET rebuild_surface_name = '', rebuild_enqueued_at = NULL WHERE singleton`)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'series', 'Repair Season', 'matched', '{}'::text[])`, seriesID); err != nil {
		t.Fatalf("seed season recovery item: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO seasons (
			content_id, series_id, season_number, title, poster_path, poster_source_path
		) VALUES ($2, $1, 0, 'Specials', $3, $4)`, seriesID, seasonID, selectedPath, sourcePath); err != nil {
		t.Fatalf("seed season recovery target: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE artwork_storage_accounting_state
		SET rebuild_surface_name = '', rebuild_enqueued_at = NULL WHERE singleton
	`); err != nil {
		t.Fatalf("reset season recovery accounting: %v", err)
	}

	coordinator := NewArtworkDeliveryCoordinator(pool, nil)
	if _, err := coordinator.EnqueueBulkRecovery(ctx); err != nil {
		t.Fatalf("enqueue bulk recovery: %v", err)
	}
	repo := NewImageCacheJobRepository(pool)
	jobs, err := repo.claimDueForTarget(ctx, workerID, seriesID, 10)
	if err != nil {
		t.Fatalf("claim season repair: %v", err)
	}
	var job *models.MetadataImageCacheJob
	for _, candidate := range jobs {
		if candidate.TargetType == ImageCacheTargetSeason && candidate.SeasonNumber != nil && *candidate.SeasonNumber == 0 {
			job = candidate
			break
		}
	}
	if job == nil {
		t.Fatalf("claimed jobs = %#v, want season 0 repair", jobs)
	}
	if job.TargetContentID != seriesID || job.SeriesID != seriesID || !job.RepairRequested {
		t.Fatalf("season repair tuple = target=%q series=%q repair=%v, want %q/%q/true",
			job.TargetContentID, job.SeriesID, job.RepairRequested, seriesID, seriesID)
	}
	current, found, err := repo.CurrentTargetSourcePath(ctx, job)
	if err != nil || !found || current != sourcePath {
		t.Fatalf("round-trip target lookup = source=%q found=%v err=%v", current, found, err)
	}

	cacher := &fakeImageCacher{result: &CacheImageResult{BasePath: strings.TrimSuffix(publishedPath, "/repaired.webp"), Ext: ".webp"}}
	processor := NewImageCacheProcessorWithTargets(repo, cacher, &fakeImageResolver{url: "https://artworks.thetvdb.com/season.jpg"}, ImageCacheProcessorTargets{
		Seasons: catalog.NewSeasonRepository(pool),
	})
	result := processor.processOne(ctx, job)
	if result.outcome != "succeeded" {
		t.Fatalf("process season repair outcome = %q", result.outcome)
	}

	var storedPath string
	if err := pool.QueryRow(ctx, `SELECT poster_path FROM seasons WHERE series_id = $1 AND season_number = 0`, seriesID).Scan(&storedPath); err != nil {
		t.Fatalf("read published season: %v", err)
	}
	wantPath := strings.TrimSuffix(publishedPath, "/repaired.webp") + "/original.webp"
	if storedPath != wantPath {
		t.Fatalf("published season path = %q, want %q", storedPath, wantPath)
	}
}

func TestArtworkBulkRecoveryMarksUploadedProfileAvatarAsProtectedLoss(t *testing.T) {
	pool := localArtworkTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	username := fmt.Sprintf("artwork-avatar-loss-%d", suffix)
	profileID := fmt.Sprintf("avatar-loss-profile-%d", suffix)
	revision := fmt.Sprintf("%064x", suffix)
	avatarPath := artworkkey.PortableKey(artworkkey.ImageTypeAvatar, revision, artworkkey.OriginalVariant, ".webp")
	if avatarPath == "" {
		t.Fatal("build portable avatar key")
	}

	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, username).Scan(&userID); err != nil {
		t.Fatalf("seed avatar owner: %v", err)
	}
	targetKeys := []string{fmt.Sprint(userID), profileID}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_storage_alerts WHERE surface_name = $1 AND target_keys = $2`, artworkurl.SurfaceProfileAvatars, targetKeys)
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, avatarPath)
		_, _ = pool.Exec(ctx, `DELETE FROM user_profiles WHERE id = $1 AND user_id = $2`, profileID, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `UPDATE artwork_storage_accounting_state
			SET rebuild_surface_name = '', rebuild_enqueued_at = NULL WHERE singleton`)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_profiles (id, user_id, name, avatar)
		VALUES ($1, $2, 'Avatar loss profile', $3)`, profileID, userID, profileAvatarUploadPrefix+avatarPath); err != nil {
		t.Fatalf("seed uploaded avatar owner: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO artwork_revision_gc_candidates (original_path, object_keys, not_before)
		VALUES ($1, ARRAY[$1], NOW())`, avatarPath); err != nil {
		t.Fatalf("seed uploaded avatar inventory: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE artwork_storage_accounting_state
		SET rebuild_surface_name = $1, rebuild_enqueued_at = NULL WHERE singleton`,
		artworkSweepSurfaces()[len(artworkSweepSurfaces())-1].name); err != nil {
		t.Fatalf("reset uploaded avatar recovery accounting: %v", err)
	}

	coordinator := NewArtworkDeliveryCoordinator(pool, nil)
	if _, err := coordinator.EnqueueBulkRecovery(ctx); err != nil {
		t.Fatalf("enqueue bulk recovery: %v", err)
	}

	var repairState string
	var protectedAt bool
	if err := pool.QueryRow(ctx, `SELECT repair_state, protected_loss_at IS NOT NULL
		FROM artwork_revision_gc_candidates WHERE original_path = $1`, avatarPath).Scan(&repairState, &protectedAt); err != nil {
		t.Fatalf("read avatar inventory loss state: %v", err)
	}
	if repairState != "protected_loss" || !protectedAt {
		t.Fatalf("avatar loss state = %q protected_at=%v", repairState, protectedAt)
	}
	var alerts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM artwork_storage_alerts
		WHERE kind = 'protected_data_loss' AND surface_name = $1 AND target_keys = $2
		  AND image_slot = $3 AND original_path = $4 AND resolved_at IS NULL`,
		artworkurl.SurfaceProfileAvatars, targetKeys, artworkkey.ImageTypeAvatar, avatarPath).Scan(&alerts); err != nil {
		t.Fatalf("read avatar loss alert: %v", err)
	}
	if alerts != 1 {
		t.Fatalf("avatar protected-loss alerts = %d, want one", alerts)
	}
	status, err := coordinator.RebuildStatus(ctx)
	if err != nil {
		t.Fatalf("load rebuild status: %v", err)
	}
	if status.ProtectedLosses < 1 {
		t.Fatalf("rebuild protected losses = %d, want uploaded avatar included", status.ProtectedLosses)
	}
}

func TestArtworkRepairPublicationDrainsRebuildState(t *testing.T) {
	pool := localArtworkTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	contentID := fmt.Sprintf("artwork-repair-%d", suffix)
	oldPath := fmt.Sprintf("tmdb/movies/%s/poster/original.old.webp", contentID)
	revision := fmt.Sprintf("%064x", suffix)
	newPath := fmt.Sprintf("artwork/v1/objects/poster/%s/%s/original.webp", revision[:2], revision)
	workerID := fmt.Sprintf("repair-worker-%d", suffix)
	targetKeys := []string{contentID}

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_storage_alerts WHERE target_keys = $1`, targetKeys)
		_, _ = pool.Exec(ctx, `DELETE FROM metadata_image_cache_jobs WHERE target_content_id = $1`, contentID)
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path = ANY($1)`, []string{oldPath, newPath})
	})

	for _, path := range []string{oldPath, newPath} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO artwork_revision_gc_candidates (
				original_path, object_keys, missing_at, repair_state,
				repair_queued_at, protected_loss_at, not_before, next_attempt_at
			) VALUES ($1, ARRAY[$1], NOW(), 'protected_loss', NOW(), NOW(),
				NOW() + interval '24 hours', NOW() + interval '24 hours')`, path); err != nil {
			t.Fatalf("seed missing revision %q: %v", path, err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO artwork_storage_alerts (
			kind, surface_name, target_keys, image_slot, original_path, message
		) VALUES ('protected_data_loss', $1, $2, 'poster', $3, 'test loss')`,
		artworkurl.SurfaceItemPosters, targetKeys, oldPath); err != nil {
		t.Fatalf("seed protected-loss alert: %v", err)
	}

	repo := NewImageCacheJobRepository(pool)
	if _, err := repo.EnqueueRepair(ctx, EnqueueImageCacheJobInput{
		TargetType:        ImageCacheTargetItem,
		TargetContentID:   contentID,
		SeriesID:          contentID,
		SourcePath:        "tmdb://movie/repair-test",
		ProviderID:        "tmdb",
		ProviderContentID: contentID,
		ContentType:       "movie",
		ImageType:         ImageCacheImagePoster,
	}); err != nil {
		t.Fatalf("enqueue repair: %v", err)
	}
	jobs, err := repo.claimDue(ctx, workerID, contentID, 1, false)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim repair: jobs=%d err=%v", len(jobs), err)
	}

	coordinator := NewArtworkDeliveryCoordinator(pool, nil)
	if err := coordinator.ArtworkPublished(ctx, jobs[0], oldPath, newPath); err != nil {
		t.Fatalf("record publication: %v", err)
	}
	if err := repo.MarkSucceeded(ctx, jobs[0].ID, workerID); err != nil {
		t.Fatalf("complete repair: %v", err)
	}

	var outstanding, missing, unresolved int64
	var publishedNextAttempt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM metadata_image_cache_jobs
			 WHERE target_content_id = $1 AND repair_requested AND status IN ('queued', 'running')),
			(SELECT count(*) FROM artwork_revision_gc_candidates
			 WHERE original_path = ANY($2) AND missing_at IS NOT NULL AND tombstoned_at IS NULL),
			(SELECT count(*) FROM artwork_storage_alerts
			 WHERE target_keys = $3 AND resolved_at IS NULL),
			(SELECT next_attempt_at FROM artwork_revision_gc_candidates WHERE original_path = $4)`,
		contentID, []string{oldPath, newPath}, targetKeys, newPath).Scan(&outstanding, &missing, &unresolved, &publishedNextAttempt); err != nil {
		t.Fatalf("read rebuild completion gate: %v", err)
	}
	if outstanding != 0 || unresolved != 0 {
		t.Fatalf("repair queue or alert did not drain: outstanding=%d unresolved_alerts=%d", outstanding, unresolved)
	}
	if missing != 1 {
		t.Fatalf("missing revision rows = %d, want only the unrepaired previous revision", missing)
	}
	if publishedNextAttempt != nil {
		t.Fatalf("published revision remained armed for GC at %v", *publishedNextAttempt)
	}
}

func TestArtworkRepairPublicationKeepsChangedSharedRevisionMissing(t *testing.T) {
	pool := localArtworkTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	repairedContentID := fmt.Sprintf("artwork-repair-changed-%d", suffix)
	sharedContentID := fmt.Sprintf("artwork-repair-shared-%d", suffix)
	oldPath := fmt.Sprintf("tmdb/movies/%d/poster/original.old.webp", suffix)
	revision := fmt.Sprintf("%064x", suffix)
	newPath := fmt.Sprintf("artwork/v1/objects/poster/%s/%s/original.webp", revision[:2], revision)

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{repairedContentID, sharedContentID})
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path = ANY($1)`, []string{oldPath, newPath})
	})
	for _, contentID := range []string{repairedContentID, sharedContentID} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, status, genres, poster_path, poster_source_path)
			VALUES ($1, 'movie', 'Shared repair revision', 'matched', '{}'::text[], $2, 'tmdb://movie/shared-repair')`,
			contentID, oldPath); err != nil {
			t.Fatalf("seed shared artwork owner %q: %v", contentID, err)
		}
	}
	for _, path := range []string{oldPath, newPath} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO artwork_revision_gc_candidates (
				original_path, image_type, object_keys, missing_at, repair_state,
				repair_queued_at, protected_loss_at, not_before, next_attempt_at
			) VALUES ($1, 'poster', ARRAY[$1], NOW(), 'protected_loss', NOW(), NOW(), NOW(), NOW())`, path); err != nil {
			t.Fatalf("seed missing revision %q: %v", path, err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE media_items SET poster_path = $2 WHERE content_id = $1`, repairedContentID, newPath); err != nil {
		t.Fatalf("publish changed repaired revision: %v", err)
	}

	coordinator := NewArtworkDeliveryCoordinator(pool, nil)
	if err := coordinator.ArtworkPublished(ctx, &models.MetadataImageCacheJob{
		TargetType: ImageCacheTargetItem, TargetContentID: repairedContentID, ImageType: ImageCacheImagePoster,
	}, oldPath, newPath); err != nil {
		t.Fatalf("record changed repair publication: %v", err)
	}

	var oldMissing, newMissing bool
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT missing_at IS NOT NULL FROM artwork_revision_gc_candidates WHERE original_path = $1),
			(SELECT missing_at IS NOT NULL FROM artwork_revision_gc_candidates WHERE original_path = $2)`,
		oldPath, newPath).Scan(&oldMissing, &newMissing); err != nil {
		t.Fatalf("read repaired revision loss state: %v", err)
	}
	if !oldMissing || newMissing {
		t.Fatalf("loss state after changed repair = old:%t new:%t, want old:true new:false", oldMissing, newMissing)
	}
	status, err := coordinator.RebuildStatus(ctx)
	if err != nil {
		t.Fatalf("load shared repair rebuild status: %v", err)
	}
	if status.MissingReferences != 1 {
		t.Fatalf("shared old revision missing references = %d, want 1", status.MissingReferences)
	}
}

func TestArtworkRebuildStatusIgnoresUnreferencedMissingRevisions(t *testing.T) {
	pool := localArtworkTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	contentID := fmt.Sprintf("artwork-rebuild-gate-%d", suffix)
	referencedPath := fmt.Sprintf("tmdb/movies/%s/poster/original.missing.webp", contentID)
	orphanPath := fmt.Sprintf("tmdb/movies/%s/poster/original.orphan.webp", contentID)
	healthyPath := fmt.Sprintf("tmdb/movies/%s/poster/original.healthy.webp", contentID)

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path = ANY($1)`, []string{referencedPath, orphanPath, healthyPath})
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, poster_path, poster_source_path)
		VALUES ($1, 'movie', 'Artwork rebuild gate', 'matched', '{}'::text[], $2, 'tmdb://movie/rebuild-gate')`,
		contentID, referencedPath); err != nil {
		t.Fatalf("seed referenced owner: %v", err)
	}
	for _, path := range []string{referencedPath, orphanPath} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO artwork_revision_gc_candidates (original_path, object_keys, not_before)
			VALUES ($1, ARRAY[$1], NOW())`, path); err != nil {
			t.Fatalf("seed inventory %q: %v", path, err)
		}
	}

	coordinator := NewArtworkDeliveryCoordinator(pool, nil)
	if err := coordinator.markBulkMissing(ctx, []string{referencedPath, orphanPath}); err != nil {
		t.Fatalf("bulk-mark missing: %v", err)
	}
	status, err := coordinator.RebuildStatus(ctx)
	if err != nil {
		t.Fatalf("load initial rebuild status: %v", err)
	}
	if status.MissingReferences != 1 {
		t.Fatalf("referenced missing revisions = %d, want 1", status.MissingReferences)
	}

	if _, err := pool.Exec(ctx, `UPDATE media_items SET poster_path = $2 WHERE content_id = $1`, contentID, healthyPath); err != nil {
		t.Fatalf("repoint owner: %v", err)
	}
	if err := coordinator.ArtworkPublished(ctx, &models.MetadataImageCacheJob{
		TargetType: ImageCacheTargetItem, TargetContentID: contentID, ImageType: ImageCacheImagePoster,
	}, referencedPath, healthyPath); err != nil {
		t.Fatalf("record replacement publication: %v", err)
	}
	status, err = coordinator.RebuildStatus(ctx)
	if err != nil {
		t.Fatalf("load completed rebuild status: %v", err)
	}
	if status.MissingReferences != 0 || status.ProtectedLosses != 0 {
		t.Fatalf("orphan held rebuild open: missing=%d protected=%d", status.MissingReferences, status.ProtectedLosses)
	}
	var orphanStillMissing bool
	if err := pool.QueryRow(ctx, `SELECT missing_at IS NOT NULL FROM artwork_revision_gc_candidates WHERE original_path = $1`, orphanPath).Scan(&orphanStillMissing); err != nil {
		t.Fatalf("read orphan inventory: %v", err)
	}
	if !orphanStillMissing {
		t.Fatal("completion gate mutated the orphaned missing row")
	}
}
