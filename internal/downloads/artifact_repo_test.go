package downloads

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func newArtifactTestRepo(t *testing.T) (*ArtifactRepository, *pgxpool.Pool, int) {
	t.Helper()
	pool := newDownloadsTestPool(t)
	ctx := context.Background()
	var present *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.download_artifacts')::text`).Scan(&present); err != nil {
		t.Fatalf("check download_artifacts: %v", err)
	}
	if present == nil {
		t.Skip("download_artifacts migration has not been applied")
	}
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.download_artifact_orphans')::text`).Scan(&present); err != nil {
		t.Fatalf("check download_artifact_orphans: %v", err)
	}
	if present == nil {
		t.Skip("download_artifact_orphans migration has not been applied")
	}

	suffix := time.Now().UnixNano()
	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name) VALUES ('movies', $1) RETURNING id`,
		fmt.Sprintf("Artifacts Test %d", suffix),
	).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	var fileID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_files (media_folder_id, file_path) VALUES ($1, $2) RETURNING id`,
		folderID, fmt.Sprintf("/tmp/artifact-%d.mkv", suffix),
	).Scan(&fileID); err != nil {
		t.Fatalf("seed media file: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM download_artifacts WHERE media_file_id = $1`, fileID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE id = $1`, fileID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	return NewArtifactRepository(pool), pool, fileID
}

func newArtifact(t *testing.T, fileID int, hash string) *Artifact {
	t.Helper()
	id, err := idgen.NextID()
	if err != nil {
		t.Fatalf("id: %v", err)
	}
	return &Artifact{
		ID: id, MediaFileID: fileID, Format: "transcode", ParamsHash: hash,
		Container: "mp4", CodecVideo: "h264", CodecAudio: "aac", Resolution: "1080p",
		AudioTrackIndex: -1, OutputPath: "/tmp/" + id + ".mp4", MaxAttempts: 3,
	}
}

func remoteOrphansForArtifact(orphans []RemoteArtifactOrphan, artifactID string) []RemoteArtifactOrphan {
	filtered := make([]RemoteArtifactOrphan, 0, 1)
	for _, orphan := range orphans {
		if orphan.DownloadArtifactID == artifactID {
			filtered = append(filtered, orphan)
		}
	}
	return filtered
}

func TestArtifactEnsureQueuedRoundTripsToneMapRecipe(t *testing.T) {
	repo, _, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	artifact := newArtifact(t, fileID, "hash-tone-map-round-trip")
	artifact.ToneMapPolicy = tonemap.PolicyHardwareThenSoftware
	artifact.ToneMapMode = tonemap.ModeHardware
	artifact.ToneMapSourceKind = tonemap.SourcePQ
	wantRecipeVersion := playback.TransformationHDRToSDRToneMapRecipeVersionV3
	wantSourceRevision := tonemap.SourceRevision{MediaFileID: fileID, FileSize: 100, StreamSignature: "video"}.Encode()
	artifact.ToneMapRecipeVersion = wantRecipeVersion
	artifact.ToneMapSourceRevision = wantSourceRevision

	returned, created, err := repo.EnsureQueued(ctx, artifact)
	if err != nil || !created {
		t.Fatalf("EnsureQueued = (%+v, created=%v, %v), want new tone-mapped artifact", returned, created, err)
	}
	assertToneMapRecipe := func(name string, got *Artifact) {
		t.Helper()
		if got.ToneMapPolicy != tonemap.PolicyHardwareThenSoftware || got.ToneMapMode != tonemap.ModeHardware || got.ToneMapSourceKind != tonemap.SourcePQ || got.ToneMapRecipeVersion != wantRecipeVersion || got.ToneMapSourceRevision != wantSourceRevision {
			t.Fatalf("%s tone-map recipe = policy %q mode %q source %q version %q revision %q; want version %q revision %q", name, got.ToneMapPolicy, got.ToneMapMode, got.ToneMapSourceKind, got.ToneMapRecipeVersion, got.ToneMapSourceRevision, wantRecipeVersion, wantSourceRevision)
		}
	}
	assertToneMapRecipe("returned", returned)
	persisted, err := repo.GetByID(ctx, returned.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertToneMapRecipe("persisted", persisted)
}

// TestArtifactQueueClaimAndLeaseRecovery is the Phase 3 / invariant-3 acceptance
// test: a crash mid-encode (an expired lease) is recovered on the next sweep so
// the job re-enqueues and reaches ready, and concurrent workers never claim the
// same job twice (no double-encode).
func TestArtifactQueueClaimAndLeaseRecovery(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()

	a := newArtifact(t, fileID, "hash-recovery")
	row, created, err := repo.EnsureQueued(ctx, a)
	if err != nil || !created || row.Status != ArtifactQueued {
		t.Fatalf("EnsureQueued = (%+v, created=%v, %v), want new queued row", row, created, err)
	}

	// Dedup: a second ensure for the same key returns the same row, not a new one.
	dup, created2, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, "hash-recovery"))
	if err != nil || created2 || dup.ID != row.ID {
		t.Fatalf("dedup EnsureQueued = (%s, created=%v, %v), want existing %s", dup.ID, created2, err, row.ID)
	}

	// Worker 1 claims the job; worker 2 finds nothing (no double-encode).
	claim, err := repo.ClaimNext(ctx, "worker-1", time.Minute)
	if err != nil || claim.ID != row.ID || claim.Status != ArtifactRunning || claim.Attempts != 1 {
		t.Fatalf("ClaimNext = (%+v, %v), want running attempts=1", claim, err)
	}
	if _, err := repo.ClaimNext(ctx, "worker-2", time.Minute); !errors.Is(err, ErrNoArtifactJob) {
		t.Fatalf("second ClaimNext err = %v, want ErrNoArtifactJob", err)
	}

	// Simulate a crash: expire the lease, then run the startup sweep.
	if _, err := pool.Exec(ctx, `UPDATE download_artifacts SET lease_expires_at = now() - interval '1 minute' WHERE id = $1`, row.ID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	reclaimed, err := repo.ReclaimExpiredLeases(ctx)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].ID != row.ID || reclaimed[0].Terminal {
		t.Fatalf("reclaimed = %+v, want one non-terminal %s", reclaimed, row.ID)
	}
	back, err := repo.GetByID(ctx, row.ID)
	if err != nil || back.Status != ArtifactQueued {
		t.Fatalf("after reclaim status = %v (%v), want queued (no permanent running)", back.Status, err)
	}

	// Another worker reclaims and completes it.
	claim2, err := repo.ClaimNext(ctx, "worker-2", time.Minute)
	if err != nil || claim2.ID != row.ID || claim2.Attempts != 2 {
		t.Fatalf("reclaim ClaimNext = (%+v, %v), want attempts=2", claim2, err)
	}
	if applied, err := repo.MarkReady(ctx, row.ID, "worker-2", claim2.OutputPath, 0, "", "", "", 4242); err != nil || !applied {
		t.Fatalf("MarkReady = (%v, %v), want (true, nil)", applied, err)
	}
	done, err := repo.GetByKey(ctx, fileID, "transcode", "hash-recovery")
	if err != nil || done.Status != ArtifactReady || done.FileSize != 4242 {
		t.Fatalf("final = (%+v, %v), want ready size=4242", done, err)
	}
}

// TestToneMapArtifactQueueRejectsLegacyWorkers verifies that the durable queue
// does not expose a tone-map recipe to workers from before tone-map fields were
// added. Such a worker would otherwise claim the row and encode ordinary SDR
// output while silently ignoring the frozen recipe.
func TestToneMapArtifactQueueRejectsLegacyWorkers(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	// The fence this test exercises is the trigger created by migration
	// 20260815135416_fence_tone_map_artifact_workers; without it the legacy
	// claim below would succeed and the test would fail for the wrong reason.
	var triggerName *string
	if err := pool.QueryRow(ctx, `SELECT tgname::text FROM pg_trigger WHERE tgname = 'download_artifacts_tone_map_worker_status'`).Scan(&triggerName); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("check tone-map worker fence trigger: %v", err)
	}
	if triggerName == nil {
		t.Skip("migration 20260815135416_fence_tone_map_artifact_workers has not been applied")
	}

	a := newArtifact(t, fileID, "hash-tone-map-worker-fence")
	a.ToneMapPolicy = tonemap.PolicySoftwareOnly
	a.ToneMapMode = tonemap.ModeSoftware
	a.ToneMapSourceKind = tonemap.SourcePQ
	a.ToneMapRecipeVersion = playback.TransformationHDRToSDRToneMapRecipeVersionV3
	a.ToneMapSourceRevision = tonemap.SourceRevision{MediaFileID: fileID, FileSize: 123}.Encode()
	row, created, err := repo.EnsureQueued(ctx, a)
	if err != nil || !created || row.Status != ArtifactToneMapQueued {
		t.Fatalf("EnsureQueued = (%+v, created=%v, %v), want new tone-map queued row", row, created, err)
	}

	// This is the claim predicate used by the merge-base worker. It must not see
	// the new queue discriminator, even though additive recipe columns are present.
	var legacyClaimID string
	err = pool.QueryRow(ctx, `
		UPDATE download_artifacts SET status = 'running'
		WHERE id = (
			SELECT id FROM download_artifacts
			WHERE status = 'queued' AND (next_retry_at IS NULL OR next_retry_at <= now())
			ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED
		)
		RETURNING id
	`).Scan(&legacyClaimID)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("legacy worker claim = id %q, err %v; want no row", legacyClaimID, err)
	}

	claim, err := repo.ClaimNext(ctx, "current-worker", time.Minute)
	if err != nil || claim.ID != row.ID || claim.Status != ArtifactToneMapRunning {
		t.Fatalf("current ClaimNext = (%+v, %v), want tone-map running", claim, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE download_artifacts SET lease_expires_at = now() - interval '1 minute' WHERE id = $1`, row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReclaimExpiredLeases(ctx); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := repo.GetByID(ctx, row.ID)
	if err != nil || reclaimed.Status != ArtifactToneMapQueued {
		t.Fatalf("reclaimed = (%+v, %v), want tone-map queued", reclaimed, err)
	}

	_, err = repo.ClaimNext(ctx, "current-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	terminal, applied, err := repo.MarkFailedOrRetry(ctx, row.ID, "current-worker", "retry", time.Second)
	if err != nil || !applied || terminal {
		t.Fatalf("MarkFailedOrRetry = (%v, %v, %v), want retry", terminal, applied, err)
	}
	retried, err := repo.GetByID(ctx, row.ID)
	if err != nil || retried.Status != ArtifactToneMapQueued {
		t.Fatalf("retried = (%+v, %v), want tone-map queued", retried, err)
	}

	if _, err := pool.Exec(ctx, `UPDATE download_artifacts SET next_retry_at = now() - interval '1 second' WHERE id = $1`, row.ID); err != nil {
		t.Fatal(err)
	}
	claim, err = repo.ClaimNext(ctx, "current-worker", time.Minute)
	if err != nil || claim.Status != ArtifactToneMapRunning {
		t.Fatalf("final ClaimNext = (%+v, %v), want tone-map running", claim, err)
	}
	if applied, err := repo.MarkReady(ctx, row.ID, "current-worker", row.OutputPath, 0, "", "", "", 4242); err != nil || !applied {
		t.Fatalf("MarkReady = (%v, %v), want applied", applied, err)
	}
	ready, err := repo.GetByID(ctx, row.ID)
	if err != nil || ready.Status != ArtifactToneMapReady {
		t.Fatalf("tone-map ready row = (%+v, %v), want tone_map_ready", ready, err)
	}
	var legacyReadyID string
	err = pool.QueryRow(ctx, `SELECT id FROM download_artifacts WHERE id = $1 AND status = 'ready'`, row.ID).Scan(&legacyReadyID)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("merge-base ready reader saw tone-map artifact %q, err %v", legacyReadyID, err)
	}
	// A merge-base API process can still requeue a ready row with the legacy
	// status literal. The database fence must normalize that write too, or an old
	// worker could claim it on the next poll.
	if _, err := pool.Exec(ctx, `UPDATE download_artifacts SET status = 'queued' WHERE id = $1`, row.ID); err != nil {
		t.Fatal(err)
	}
	legacyRequeue, err := repo.GetByID(ctx, row.ID)
	if err != nil || legacyRequeue.Status != ArtifactToneMapQueued {
		t.Fatalf("legacy requeue = (%+v, %v), want database-normalized tone-map queued", legacyRequeue, err)
	}
}

// TestAudioV2ArtifactQueueRejectsMergeBaseWorkers proves that the durable
// status family, not the opaque params hash, fences a boosted prepared file.
// The merge-base worker already understands tone_map_* jobs, so audio v2 needs
// a distinct discriminator that its ClaimNext predicate cannot see.
func TestAudioV2ArtifactQueueRejectsMergeBaseWorkers(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	var audioRecipeColumn *string
	if err := pool.QueryRow(ctx, `
		SELECT column_name::text
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'download_artifacts' AND column_name = 'audio_recipe_version'
	`).Scan(&audioRecipeColumn); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("check audio recipe worker fence: %v", err)
	}
	if audioRecipeColumn == nil {
		t.Skip("migration 20260825052554_fence_audio_v2_artifact_workers has not been applied")
	}

	a := newArtifact(t, fileID, "hash-audio-v2-worker-fence")
	a.CodecAudio = "aac"
	a.AudioRecipeVersion = playback.TransformationAudioToAACRecipeVersionV3
	row, created, err := repo.EnsureQueued(ctx, a)
	if err != nil || !created || row.Status != ArtifactAudioV2Queued || row.AudioRecipeVersion != playback.TransformationAudioToAACRecipeVersionV3 {
		t.Fatalf("EnsureQueued = (%+v, created=%v, %v), want new audio-v2 queued row", row, created, err)
	}

	// Exact queue predicate from the merge-base worker: it can claim ordinary
	// and tone-map rows but must not see the new audio-v2 status.
	var legacyClaimID string
	err = pool.QueryRow(ctx, `
		UPDATE download_artifacts
		SET status = CASE WHEN status IN ('tone_map_queued', 'tone_map_running') THEN 'tone_map_running' ELSE 'running' END
		WHERE id = (
			SELECT id FROM download_artifacts
			WHERE (status IN ('queued', 'tone_map_queued') AND (next_retry_at IS NULL OR next_retry_at <= now()))
			   OR (status IN ('running', 'tone_map_running') AND lease_expires_at < now())
			ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED
		)
		RETURNING id
	`).Scan(&legacyClaimID)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("merge-base worker claim = id %q, err %v; want no row", legacyClaimID, err)
	}

	claim, err := repo.ClaimNext(ctx, "current-worker", time.Minute)
	if err != nil || claim.ID != row.ID || claim.Status != ArtifactAudioV2Running {
		t.Fatalf("current ClaimNext = (%+v, %v), want audio-v2 running", claim, err)
	}
	terminal, applied, err := repo.MarkFailedOrRetry(ctx, row.ID, "current-worker", "retry", time.Second)
	if err != nil || !applied || terminal {
		t.Fatalf("MarkFailedOrRetry = (%v, %v, %v), want retry", terminal, applied, err)
	}
	retried, err := repo.GetByID(ctx, row.ID)
	if err != nil || retried.Status != ArtifactAudioV2Queued {
		t.Fatalf("retried = (%+v, %v), want audio-v2 queued", retried, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE download_artifacts SET next_retry_at = now() - interval '1 second' WHERE id = $1`, row.ID); err != nil {
		t.Fatal(err)
	}
	claim, err = repo.ClaimNext(ctx, "current-worker", time.Minute)
	if err != nil || claim.Status != ArtifactAudioV2Running {
		t.Fatalf("final ClaimNext = (%+v, %v), want audio-v2 running", claim, err)
	}
	if applied, err := repo.MarkReady(ctx, row.ID, "current-worker", row.OutputPath, 0, "", "", "", 4242); err != nil || !applied {
		t.Fatalf("MarkReady = (%v, %v), want applied", applied, err)
	}
	ready, err := repo.GetByID(ctx, row.ID)
	if err != nil || ready.Status != ArtifactAudioV2Ready {
		t.Fatalf("audio-v2 ready row = (%+v, %v), want audio-v2 ready", ready, err)
	}

	var legacyReadyID string
	err = pool.QueryRow(ctx, `SELECT id FROM download_artifacts WHERE id = $1 AND status IN ('ready', 'tone_map_ready')`, row.ID).Scan(&legacyReadyID)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("merge-base ready reader saw audio-v2 artifact %q, err %v", legacyReadyID, err)
	}

	// A merge-base API can requeue the row with its plain status expression.
	// The database trigger must restore the audio-v2 fence before another poll.
	if _, err := pool.Exec(ctx, `UPDATE download_artifacts SET status = 'queued' WHERE id = $1`, row.ID); err != nil {
		t.Fatal(err)
	}
	legacyRequeue, err := repo.GetByID(ctx, row.ID)
	if err != nil || legacyRequeue.Status != ArtifactAudioV2Queued {
		t.Fatalf("legacy requeue = (%+v, %v), want database-normalized audio-v2 queued", legacyRequeue, err)
	}
}

// TestArtifactRetryUntilTerminal verifies attempt counting and backoff: a job
// retries behind its backoff gate until max_attempts, then goes terminal-failed.
func TestArtifactRetryUntilTerminal(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()

	row, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, "hash-retry"))
	if err != nil {
		t.Fatalf("EnsureQueued: %v", err)
	}

	for attempt := 1; attempt <= 3; attempt++ {
		claim, err := repo.ClaimNext(ctx, "worker", time.Minute)
		if err != nil {
			t.Fatalf("attempt %d ClaimNext: %v", attempt, err)
		}
		if claim.Attempts != attempt {
			t.Fatalf("attempt %d: attempts = %d", attempt, claim.Attempts)
		}
		terminal, applied, err := repo.MarkFailedOrRetry(ctx, row.ID, "worker", "boom", 30*time.Second)
		if err != nil || !applied {
			t.Fatalf("attempt %d MarkFailedOrRetry = (%v, %v, %v)", attempt, terminal, applied, err)
		}
		if attempt < 3 {
			if terminal {
				t.Fatalf("attempt %d went terminal too early", attempt)
			}
			// Behind the backoff gate the job is not yet claimable.
			if _, err := repo.ClaimNext(ctx, "worker", time.Minute); !errors.Is(err, ErrNoArtifactJob) {
				t.Fatalf("attempt %d: job claimable during backoff", attempt)
			}
			if _, err := pool.Exec(ctx, `UPDATE download_artifacts SET next_retry_at = now() - interval '1 second' WHERE id = $1`, row.ID); err != nil {
				t.Fatalf("clear backoff: %v", err)
			}
		} else if !terminal {
			t.Fatalf("final attempt should be terminal")
		}
	}

	failed, err := repo.GetByID(ctx, row.ID)
	if err != nil || failed.Status != ArtifactFailed {
		t.Fatalf("final status = %v (%v), want failed", failed.Status, err)
	}
}

// TestArtifactMarkFencedByOwner verifies MarkReady/MarkFailedOrRetry only apply
// for the worker that currently holds the lease, so a worker whose lease was
// stolen (e.g. a slow encode reclaimed by another node) cannot flip a job it no
// longer owns — the double-encode guard behind invariant 3.
func TestArtifactMarkFencedByOwner(t *testing.T) {
	repo, _, fileID := newArtifactTestRepo(t)
	ctx := context.Background()

	row, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, "hash-fence"))
	if err != nil {
		t.Fatalf("EnsureQueued: %v", err)
	}
	if _, err := repo.ClaimNext(ctx, "owner-1", time.Minute); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}

	// A non-owner cannot mark the job ready or failed.
	if applied, err := repo.MarkReady(ctx, row.ID, "owner-2", "/tmp/x.mp4", 0, "", "", "", 10); err != nil || applied {
		t.Fatalf("MarkReady(non-owner) = (%v, %v), want (false, nil)", applied, err)
	}
	if _, applied, err := repo.MarkFailedOrRetry(ctx, row.ID, "owner-2", "boom", time.Second); err != nil || applied {
		t.Fatalf("MarkFailedOrRetry(non-owner) applied = %v (%v), want false", applied, err)
	}

	// The job remains claimable-state 'running' and untouched.
	mid, err := repo.GetByID(ctx, row.ID)
	if err != nil || mid.Status != ArtifactRunning {
		t.Fatalf("status after fenced writes = %v (%v), want running", mid.Status, err)
	}

	// The real owner succeeds.
	if applied, err := repo.MarkReady(ctx, row.ID, "owner-1", "/tmp/x.mp4", 0, "", "", "", 10); err != nil || !applied {
		t.Fatalf("MarkReady(owner) = (%v, %v), want (true, nil)", applied, err)
	}
}

func TestArtifactRemoteLocatorRoundTripsAndRequeueClearsIt(t *testing.T) {
	repo, _, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	row, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, "hash-remote-locator"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ClaimNext(ctx, "worker", time.Minute); err != nil {
		t.Fatal(err)
	}
	if applied, err := repo.MarkReady(ctx, row.ID, "worker", "", 17, "http://transcode", "host-a", "artifact-opaque", 4242); err != nil || !applied {
		t.Fatalf("MarkReady = (%v, %v)", applied, err)
	}
	ready, err := repo.GetByID(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ready.OutputPath != "" || ready.OriginNodeID != 17 || ready.OriginNodeURL != "http://transcode" || ready.OriginNodeGroup != "host-a" || ready.OriginArtifactID != "artifact-opaque" || ready.FileSize != 4242 {
		t.Fatalf("ready artifact = %+v", ready)
	}
	ready.OriginNodeURL = "http://transcode-new"
	ready.OriginNodeGroup = "host-new"
	if applied, err := repo.RefreshRemoteLocator(ctx, ready); err != nil || !applied {
		t.Fatalf("RefreshRemoteLocator = (%v, %v)", applied, err)
	}
	refreshed, err := repo.GetByID(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.OriginNodeURL != "http://transcode-new" || refreshed.OriginNodeGroup != "host-new" {
		t.Fatalf("refreshed artifact = %+v", refreshed)
	}
	if err := repo.Requeue(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	queued, err := repo.GetByID(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.OriginNodeID != 0 || queued.OriginNodeURL != "" || queued.OriginNodeGroup != "" || queued.OriginArtifactID != "" {
		t.Fatalf("requeued artifact retained remote locator: %+v", queued)
	}
}

func TestArtifactReadyPersistsRefreshedOriginLocator(t *testing.T) {
	repo, _, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	row, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, "hash-refresh-origin-locator"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ClaimNext(ctx, "worker", time.Minute); err != nil {
		t.Fatal(err)
	}
	if applied, err := repo.MarkReady(ctx, row.ID, "worker", "", 17, "http://old-url", "old-group", "artifact-refresh", 4242); err != nil || !applied {
		t.Fatalf("MarkReady = (%v, %v)", applied, err)
	}
	manager := &ArtifactManager{
		repo: repo,
		preparer: &lifecycleTestPreparer{
			resolvedURL: "http://new-url", resolvedGroup: "new-group",
		},
	}
	resolved, err := manager.Ready(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.OriginNodeURL != "http://new-url" || resolved.OriginNodeGroup != "new-group" {
		t.Fatalf("resolved artifact = %+v", resolved)
	}
	persisted, err := repo.GetByID(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.OriginNodeURL != "http://new-url" || persisted.OriginNodeGroup != "new-group" {
		t.Fatalf("persisted artifact = %+v", persisted)
	}
}

func TestArtifactReadyMapsRemovedOriginToInactiveWhenRequeueLosesFence(t *testing.T) {
	repo, _, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	row, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, "hash-removed-origin-stale-fence"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ClaimNext(ctx, "worker", time.Minute); err != nil {
		t.Fatal(err)
	}
	if applied, err := repo.MarkReady(ctx, row.ID, "worker", "", 17, "http://removed", "host-a", "artifact-removed", 4242); err != nil || !applied {
		t.Fatalf("MarkReady = (%v, %v)", applied, err)
	}
	manager := &ArtifactManager{
		repo: repo,
		preparer: &lifecycleTestPreparer{
			resolvedNode: 18,
			resolveErr:   ErrArtifactOriginRemoved,
		},
	}
	_, err = manager.Ready(ctx, row.ID)
	if !errors.Is(err, ErrDownloadNotActive) || !errors.Is(err, ErrArtifactOriginRemoved) {
		t.Fatalf("Ready error = %v, want inactive removed-origin error", err)
	}
}

func TestArtifactRecoveryContinuesAfterStaleLocatorRefresh(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	ready := make([]*Artifact, 0, 2)
	for i := range 2 {
		row, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, fmt.Sprintf("hash-stale-locator-batch-%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.ClaimNext(ctx, "worker", time.Minute); err != nil {
			t.Fatal(err)
		}
		if applied, err := repo.MarkReady(ctx, row.ID, "worker", "", 17, "http://old-url", "host-a", fmt.Sprintf("artifact-%d", i), 4242); err != nil || !applied {
			t.Fatalf("MarkReady(%d) = (%v, %v)", i, applied, err)
		}
		artifact, err := repo.GetByID(ctx, row.ID)
		if err != nil {
			t.Fatal(err)
		}
		ready = append(ready, artifact)
	}

	// Simulate another worker replacing the first locator after this recovery
	// pass read it. Its refresh must lose the fence without suppressing checks
	// for the remaining artifacts on the still-healthy origin.
	if _, err := pool.Exec(ctx,
		`UPDATE download_artifacts SET origin_artifact_id = 'artifact-replaced' WHERE id = $1`,
		ready[0].ID,
	); err != nil {
		t.Fatal(err)
	}
	preparer := &lifecycleTestPreparer{
		resolvedURL: "http://new-url", resolvedGroup: "host-new",
		stat: downloadprepare.Result{FileSize: 4242},
	}
	manager := &ArtifactManager{repo: repo, preparer: preparer}
	manager.probeRemoteArtifactGroup(ctx, preparer, ready)

	if len(preparer.statIDs) != 1 || preparer.statIDs[0] != ready[1].ID {
		t.Fatalf("probed artifacts = %v, want only continuing artifact %s", preparer.statIDs, ready[1].ID)
	}
	persisted, err := repo.GetByID(ctx, ready[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.OriginNodeURL != "http://new-url" || persisted.OriginNodeGroup != "host-new" {
		t.Fatalf("continued artifact locator = %+v", persisted)
	}
}

func TestArtifactInvalidRemoteResultQueuesCleanup(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	row, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, "hash-invalid-remote-result"))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimNext(ctx, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	preparer := &lifecycleTestPreparer{prepared: PreparedArtifact{
		OriginNodeID: 17, OriginNodeURL: "http://transcode", OriginNodeGroup: "host-a",
		OriginArtifactID: "artifact-zero-byte", FileSize: 0,
	}}
	manager := &ArtifactManager{
		repo: repo, owner: "worker", fileRepo: fakeFileResolver{file: &models.MediaFile{ID: fileID, FilePath: "/media/movie.mkv"}},
		preparer: preparer,
	}
	manager.encodeOne(ctx, claimed)

	queued, err := repo.GetByID(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != ArtifactQueued {
		t.Fatalf("artifact status = %q, want queued retry", queued.Status)
	}
	var orphanID int64
	if err := pool.QueryRow(ctx,
		`SELECT id FROM download_artifact_orphans
		 WHERE download_artifact_id = $1 AND origin_node_id = 17 AND origin_artifact_id = 'artifact-zero-byte'`,
		row.ID,
	).Scan(&orphanID); err != nil {
		t.Fatalf("remote cleanup row: %v", err)
	}
	t.Cleanup(func() { _ = repo.DeleteRemoteOrphan(ctx, orphanID) })
}

func TestArtifactRecoveryDeletesWrongSizedRemoteBeforeRequeue(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	row, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, "hash-remote-size-mismatch"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ClaimNext(ctx, "worker", time.Minute); err != nil {
		t.Fatal(err)
	}
	if applied, err := repo.MarkReady(ctx, row.ID, "worker", row.OutputPath, 17, "http://transcode", "host-a", "artifact-truncated", 4242); err != nil || !applied {
		t.Fatalf("MarkReady = (%v, %v)", applied, err)
	}

	preparer := &lifecycleTestPreparer{stat: downloadprepare.Result{ArtifactID: "artifact-truncated", FileSize: 42}}
	manager := &ArtifactManager{
		repo:      repo,
		downloads: NewRepository(pool),
		preparer:  preparer,
	}
	manager.recover(ctx)

	got, err := repo.GetByID(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ArtifactQueued || got.OriginArtifactID != "" {
		t.Fatalf("recovered artifact = %+v, want queued without remote locator", got)
	}
	orphans, err := repo.ListRemoteOrphansDue(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	orphans = remoteOrphansForArtifact(orphans, row.ID)
	if len(orphans) != 1 || orphans[0].OriginNodeID != 17 || orphans[0].OriginArtifactID != "artifact-truncated" {
		t.Fatalf("remote cleanup queue = %+v", orphans)
	}
	if preparer.deleted != "artifact-truncated" {
		t.Fatalf("deleted remote artifact = %q, want artifact-truncated", preparer.deleted)
	}
	t.Cleanup(func() { _ = repo.DeleteRemoteOrphan(ctx, orphans[0].ID) })
}

func TestRemoteRecoveryBudgetBoundsUnreachableOrigins(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	for i := range 5 {
		row, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, fmt.Sprintf("hash-recovery-budget-%d-%d", time.Now().UnixNano(), i)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx,
			`UPDATE download_artifacts
			 SET status = 'ready', file_size = 42, completed_at = now(),
			     origin_node_id = $2, origin_node_url = $3, origin_artifact_id = $4
			 WHERE id = $1`,
			row.ID, 32000+i, fmt.Sprintf("http://unreachable-recovery-%d", i), fmt.Sprintf("artifact-blocked-%d", i),
		); err != nil {
			t.Fatal(err)
		}
	}
	preparer := &lifecycleTestPreparer{statWait: true}
	manager := &ArtifactManager{repo: repo, preparer: preparer, remoteRecoveryBudget: 50 * time.Millisecond}
	begin := time.Now()
	manager.recoverReadyArtifacts(ctx)
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Fatalf("recovery elapsed = %s, want bounded return", elapsed)
	}
	if started := preparer.statStarted.Load(); started == 0 || started > 4 {
		t.Fatalf("started probes = %d, want one bounded wave of at most four", started)
	}
}

func TestArtifactRemoteRequeueAtomicallyQueuesCleanup(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	row, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, fmt.Sprintf("hash-remote-atomic-requeue-%d", time.Now().UnixNano())))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ClaimNext(ctx, "worker", time.Minute); err != nil {
		t.Fatal(err)
	}
	if applied, err := repo.MarkReady(ctx, row.ID, "worker", row.OutputPath, 23, "http://transcode-old", "host-a", "artifact-abandoned", 4242); err != nil || !applied {
		t.Fatalf("MarkReady = (%v, %v)", applied, err)
	}
	ready, err := repo.GetByID(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	var userID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, role, download_allowed) VALUES ($1, 'user', true) RETURNING id`,
		fmt.Sprintf("requeue-user-%d", time.Now().UnixNano()),
	).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	downloadID := fmt.Sprintf("requeue-download-%d", time.Now().UnixNano())
	if err := NewRepository(pool).Create(ctx, &Download{
		ID: downloadID, UserID: userID, MediaFileID: fileID,
		ContentID: "requeue-content", Kind: KindQueued, Status: StatusCompleted,
		Format: FormatTranscode, ArtifactID: row.ID, FileSize: ready.FileSize,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM downloads WHERE id = $1`, downloadID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})
	// An indeterminate MarkReady result may enqueue the locator even though its
	// database write committed. Cleanup must recognize the winning locator and
	// remove only the stale queue row, never the node-local bytes.
	if err := repo.EnqueueRemoteOrphan(ctx, row.ID, ready.OriginNodeID, ready.OriginNodeURL, ready.OriginArtifactID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE download_artifact_orphans SET next_retry_at = NULL WHERE origin_node_id = $1 AND origin_artifact_id = $2`, ready.OriginNodeID, ready.OriginArtifactID); err != nil {
		t.Fatal(err)
	}
	orphans, err := repo.ListRemoteOrphansDue(ctx, 10)
	orphans = remoteOrphansForArtifact(orphans, row.ID)
	if err != nil || len(orphans) != 1 {
		t.Fatalf("uncertain cleanup queue = %+v (%v)", orphans, err)
	}
	owned, claimed, err := repo.PrepareRemoteOrphanCleanup(ctx, orphans[0])
	if err != nil || !claimed || !owned {
		t.Fatalf("PrepareRemoteOrphanCleanup = (owned=%v claimed=%v err=%v)", owned, claimed, err)
	}
	if left, err := repo.ListRemoteOrphansDue(ctx, 10); err != nil || len(remoteOrphansForArtifact(left, row.ID)) != 0 {
		t.Fatalf("owned cleanup rows left = %+v (%v)", left, err)
	}
	// The in-memory URL may already have followed an administrative edit; the
	// transaction deliberately matches stable node/id fields and stores the
	// refreshed URL as the cleanup target.
	ready.OriginNodeURL = "http://transcode-new"
	linked, applied, err := repo.RequeueRemote(ctx, ready)
	if err != nil || !applied {
		t.Fatalf("RequeueRemote = (%v, %v)", applied, err)
	}
	if len(linked) != 1 || linked[0].ID != downloadID || linked[0].Status != StatusPreparing {
		t.Fatalf("reset linked downloads = %+v", linked)
	}
	queued, err := repo.GetByID(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != ArtifactQueued || queued.OriginArtifactID != "" {
		t.Fatalf("queued artifact = %+v", queued)
	}
	orphans, err = repo.ListRemoteOrphansDue(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	orphans = remoteOrphansForArtifact(orphans, row.ID)
	if len(orphans) != 1 || orphans[0].DownloadArtifactID != row.ID || orphans[0].OriginNodeURL != "http://transcode-new" || orphans[0].OriginArtifactID != "artifact-abandoned" {
		t.Fatalf("remote cleanup queue = %+v", orphans)
	}
	t.Cleanup(func() { _ = repo.DeleteRemoteOrphan(ctx, orphans[0].ID) })
	// A stale caller cannot enqueue a second cleanup after the row changed.
	if _, applied, err := repo.RequeueRemote(ctx, ready); err != nil || applied {
		t.Fatalf("stale RequeueRemote = (%v, %v), want false, nil", applied, err)
	}
}

func TestProxyMissingReportFencesCompleteRemoteLocator(t *testing.T) {
	repo, _, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	row, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, fmt.Sprintf("hash-proxy-missing-report-%d", time.Now().UnixNano())))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ClaimNext(ctx, "worker", time.Minute); err != nil {
		t.Fatal(err)
	}
	if applied, err := repo.MarkReady(ctx, row.ID, "worker", "", 23, "http://transcode-current", "host-a", "artifact-missing", 4242); err != nil || !applied {
		t.Fatalf("MarkReady = (%v, %v)", applied, err)
	}
	manager := NewArtifactManager(repo, nil, nil, nil, "proxy-test", nil, nil)
	stillReady, err := repo.GetByID(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	stale := *stillReady
	stale.OriginNodeURL = "http://transcode-stale"
	if applied, err := manager.requeueRemoteArtifactExactNow(ctx, &stale, "stale API miss"); err != nil || applied {
		t.Fatalf("stale exact requeue = (%v, %v), want false, nil", applied, err)
	}
	if err := manager.ReportRemoteArtifactMissing(ctx, row.ID, "http://transcode-stale", "artifact-missing"); err != nil {
		t.Fatal(err)
	}
	stillReady, err = repo.GetByID(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillReady.Status != ArtifactReady {
		t.Fatalf("stale report changed artifact = %+v", stillReady)
	}
	if err := manager.ReportRemoteArtifactMissing(ctx, row.ID, "http://transcode-current", "artifact-missing"); err != nil {
		t.Fatal(err)
	}
	queued, err := repo.GetByID(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != ArtifactQueued || queued.OriginArtifactID != "" {
		t.Fatalf("artifact after authoritative missing report = %+v", queued)
	}
	orphans, err := repo.ListRemoteOrphansDue(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	orphans = remoteOrphansForArtifact(orphans, row.ID)
	if len(orphans) != 1 || orphans[0].DownloadArtifactID != row.ID || orphans[0].OriginArtifactID != "artifact-missing" {
		t.Fatalf("remote cleanup queue = %+v", orphans)
	}
	t.Cleanup(func() { _ = repo.DeleteRemoteOrphan(ctx, orphans[0].ID) })
}

func TestRemoteCleanupBudgetBoundsUnreachableOrigins(t *testing.T) {
	repo, _, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	row, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, fmt.Sprintf("hash-cleanup-budget-%d", time.Now().UnixNano())))
	if err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UnixNano()
	originArtifactID := fmt.Sprintf("artifact-blocked-%d", suffix)
	originURL := fmt.Sprintf("http://unreachable-%d", suffix)
	if err := repo.EnqueueRemoteOrphan(ctx, row.ID, 31, originURL, originArtifactID); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	preparer := &lifecycleTestPreparer{deleteStarted: started, deleteWait: true}
	manager := &ArtifactManager{repo: repo, preparer: preparer, remoteCleanupBudget: 50 * time.Millisecond}
	begin := time.Now()
	manager.cleanupRemoteOrphans(ctx)
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Fatalf("cleanup elapsed = %s, want bounded return", elapsed)
	}
	select {
	case <-started:
	default:
		t.Fatal("remote deletion was not attempted")
	}
	orphans, err := repo.ListRemoteOrphansDue(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, orphan := range orphans {
		if orphan.DownloadArtifactID == row.ID {
			t.Fatalf("timed-out orphan remained immediately due: %+v", orphan)
		}
	}
}

func TestListRemoteOrphansDueIsFairAcrossOrigins(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	row, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, fmt.Sprintf("hash-orphan-fairness-%d", time.Now().UnixNano())))
	if err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UnixNano()
	artifactIDs := []string{
		fmt.Sprintf("fair-origin-a-first-%d", suffix),
		fmt.Sprintf("fair-origin-a-second-%d", suffix),
		fmt.Sprintf("fair-origin-b-%d", suffix),
	}
	for i, artifactID := range artifactIDs {
		nodeID := 31001
		nodeURL := "http://origin-a"
		if i == 2 {
			nodeID = 31002
			nodeURL = "http://origin-b"
		}
		if err := repo.EnqueueRemoteOrphan(ctx, row.ID, nodeID, nodeURL, artifactID); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM download_artifact_orphans WHERE origin_artifact_id = ANY($1)`, artifactIDs)
	})
	if _, err := pool.Exec(ctx,
		`UPDATE download_artifact_orphans
		 SET next_retry_at = NULL, created_at = '-infinity'
		 WHERE origin_artifact_id = ANY($1)`,
		artifactIDs,
	); err != nil {
		t.Fatal(err)
	}

	due, err := repo.ListRemoteOrphansDue(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[int]int)
	for _, orphan := range due {
		if orphan.OriginNodeID == 31001 || orphan.OriginNodeID == 31002 {
			seen[orphan.OriginNodeID]++
		}
	}
	if seen[31001] != 1 || seen[31002] != 1 {
		t.Fatalf("due candidates by origin = %+v, want one candidate for each origin", seen)
	}
}

// TestHasActiveLinkCoversEphemeralRows pins the eviction guard: an ephemeral
// (device-less web) download row must protect its artifact from LRU cleanup
// exactly like a managed row does, and terminal rows must not.
func TestHasActiveLinkCoversEphemeralRows(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()

	art := newArtifact(t, fileID, fmt.Sprintf("hash-link-%d", time.Now().UnixNano()))
	if _, _, err := repo.EnsureQueued(ctx, art); err != nil {
		t.Fatalf("ensure artifact: %v", err)
	}

	var userID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, role, download_allowed) VALUES ($1, 'user', true) RETURNING id`,
		fmt.Sprintf("linkuser-%d", time.Now().UnixNano()),
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	contentID := fmt.Sprintf("link-content-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM downloads WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})

	dlRepo := NewRepository(pool)
	now := time.Now()
	dlID := fmt.Sprintf("dl-link-%d", now.UnixNano())
	if err := dlRepo.Create(ctx, &Download{
		ID: dlID, UserID: userID, MediaFileID: fileID, ContentID: contentID,
		Kind: KindQueued, Status: StatusReady, Format: FormatTranscode,
		ArtifactID: art.ID, FileSize: 1024, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create ephemeral download: %v", err)
	}

	active, err := repo.HasActiveLink(ctx, art.ID)
	if err != nil {
		t.Fatalf("HasActiveLink: %v", err)
	}
	if !active {
		t.Fatal("ephemeral ready row must protect its artifact from eviction")
	}

	if _, err := pool.Exec(ctx, `UPDATE downloads SET status = 'cancelled' WHERE id = $1`, dlID); err != nil {
		t.Fatalf("cancel download: %v", err)
	}
	active, err = repo.HasActiveLink(ctx, art.ID)
	if err != nil {
		t.Fatalf("HasActiveLink after cancel: %v", err)
	}
	if active {
		t.Fatal("terminal-only links must not protect an artifact")
	}
}
