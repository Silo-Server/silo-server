package downloads

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/idgen"
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
	if applied, err := repo.MarkReady(ctx, row.ID, "worker-2", claim2.OutputPath, 4242); err != nil || !applied {
		t.Fatalf("MarkReady = (%v, %v), want (true, nil)", applied, err)
	}
	done, err := repo.GetByKey(ctx, fileID, "transcode", "hash-recovery")
	if err != nil || done.Status != ArtifactReady || done.FileSize != 4242 {
		t.Fatalf("final = (%+v, %v), want ready size=4242", done, err)
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
	if applied, err := repo.MarkReady(ctx, row.ID, "owner-2", "/tmp/x.mp4", 10); err != nil || applied {
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
	if applied, err := repo.MarkReady(ctx, row.ID, "owner-1", "/tmp/x.mp4", 10); err != nil || !applied {
		t.Fatalf("MarkReady(owner) = (%v, %v), want (true, nil)", applied, err)
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
