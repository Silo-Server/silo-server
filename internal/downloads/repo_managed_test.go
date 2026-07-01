package downloads

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// managedFixture is the seeded data a managed-entry authorization test needs:
// one user, two profiles on that same user_id, and a device per profile.
type managedFixture struct {
	pool      *pgxpool.Pool
	repo      *Repository
	userID    int
	profileA  string
	profileB  string
	deviceA   string
	deviceB   string
	contentID string
	fileID    int
}

func newDownloadsTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	// Skip when the managed downloads reshape migration has not been applied.
	var col *string
	err = pool.QueryRow(ctx, `SELECT column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'downloads' AND column_name = 'device_id'`).Scan(&col)
	if errors.Is(err, pgx.ErrNoRows) || col == nil {
		t.Skip("downloads managed reshape migration has not been applied")
	}
	if err != nil {
		t.Fatalf("check downloads reshape: %v", err)
	}
	return pool
}

func seedManagedFixture(t *testing.T) managedFixture {
	t.Helper()
	ctx := context.Background()
	pool := newDownloadsTestPool(t)
	suffix := time.Now().UnixNano()

	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name) VALUES ('movies', $1) RETURNING id`,
		fmt.Sprintf("Downloads Test %d", suffix),
	).Scan(&folderID); err != nil {
		t.Fatalf("seed media folder: %v", err)
	}

	contentID := fmt.Sprintf("dl-content-%d", suffix)
	var fileID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_files (content_id, media_folder_id, file_path, file_size)
		 VALUES ($1, $2, $3, 1024) RETURNING id`,
		contentID, folderID, fmt.Sprintf("/tmp/downloads-test-%d.mp4", suffix),
	).Scan(&fileID); err != nil {
		t.Fatalf("seed media file: %v", err)
	}

	var userID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, role, download_allowed) VALUES ($1, 'user', true) RETURNING id`,
		fmt.Sprintf("dluser-%d", suffix),
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	profileA := fmt.Sprintf("dlp-a-%d", suffix)
	profileB := fmt.Sprintf("dlp-b-%d", suffix)
	for _, p := range []struct{ id, name string }{{profileA, "A"}, {profileB, "B"}} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, $3)`,
			p.id, userID, p.name,
		); err != nil {
			t.Fatalf("seed profile %s: %v", p.id, err)
		}
	}

	repo := NewRepository(pool)
	deviceA := fmt.Sprintf("dev-a-%d", suffix)
	deviceB := fmt.Sprintf("dev-b-%d", suffix)
	if err := repo.EnsureDevice(ctx, userID, profileA, deviceA, "Phone A", "android"); err != nil {
		t.Fatalf("ensure device A: %v", err)
	}
	if err := repo.EnsureDevice(ctx, userID, profileB, deviceB, "Phone B", "android"); err != nil {
		t.Fatalf("ensure device B: %v", err)
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM downloads WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_devices WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_profiles WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE id = $1`, fileID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	return managedFixture{
		pool: pool, repo: repo, userID: userID,
		profileA: profileA, profileB: profileB, deviceA: deviceA, deviceB: deviceB,
		contentID: contentID, fileID: fileID,
	}
}

func (f managedFixture) createManagedEntry(t *testing.T) string {
	t.Helper()
	id := fmt.Sprintf("dl-%d", time.Now().UnixNano())
	now := time.Now()
	if err := f.repo.Create(context.Background(), &Download{
		ID: id, UserID: f.userID, ProfileID: f.profileA, DeviceID: f.deviceA,
		MediaFileID: f.fileID, ContentID: f.contentID, Kind: KindQueued,
		Status: StatusReady, Format: FormatOriginal, FileSize: 1024,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create managed entry: %v", err)
	}
	return id
}

// TestManagedEntryCrossProfileDeviceDenied is the Phase 1 / invariant-2
// acceptance test: a second profile on the SAME user_id (and a different
// device) is denied a managed row's /file (GetManagedByID), PATCH
// (UpdateManagedStatus), and DELETE (DeleteManaged).
func TestManagedEntryCrossProfileDeviceDenied(t *testing.T) {
	f := seedManagedFixture(t)
	ctx := context.Background()
	id := f.createManagedEntry(t)

	// Owner can read its own row.
	if _, err := f.repo.GetManagedByID(ctx, id, f.userID, f.profileA, f.deviceA); err != nil {
		t.Fatalf("owner GetManagedByID: %v", err)
	}

	// Cross-profile / cross-device reads are denied as not-found (no leak).
	denials := []struct {
		name            string
		profile, device string
	}{
		{"other profile + other device", f.profileB, f.deviceB},
		{"same profile + other device", f.profileA, f.deviceB},
		{"other profile + same device", f.profileB, f.deviceA},
	}
	for _, d := range denials {
		t.Run("file/"+d.name, func(t *testing.T) {
			if _, err := f.repo.GetManagedByID(ctx, id, f.userID, d.profile, d.device); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetManagedByID(%s) err = %v, want ErrNotFound", d.name, err)
			}
		})
	}

	// PATCH by the second profile/device is denied; the owner succeeds.
	if err := f.repo.UpdateManagedStatus(ctx, id, f.userID, f.profileB, f.deviceB, StatusCompleted, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-profile PATCH err = %v, want ErrNotFound", err)
	}
	now := time.Now()
	if err := f.repo.UpdateManagedStatus(ctx, id, f.userID, f.profileA, f.deviceA, StatusCompleted, &now); err != nil {
		t.Fatalf("owner PATCH: %v", err)
	}

	// DELETE by the second profile/device is denied; the owner succeeds.
	if err := f.repo.DeleteManaged(ctx, id, f.userID, f.profileB, f.deviceB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-profile DELETE err = %v, want ErrNotFound", err)
	}
	if err := f.repo.DeleteManaged(ctx, id, f.userID, f.profileA, f.deviceA); err != nil {
		t.Fatalf("owner DELETE: %v", err)
	}
	if _, err := f.repo.GetManagedByID(ctx, id, f.userID, f.profileA, f.deviceA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete GetManagedByID err = %v, want ErrNotFound", err)
	}
}

// TestManagedEntryUniqueAndRevoke covers the per-device unique constraint and
// the revoked-entry guard on status transitions.
func TestManagedEntryUniqueAndRevoke(t *testing.T) {
	f := seedManagedFixture(t)
	ctx := context.Background()
	id := f.createManagedEntry(t)

	// A second managed entry for the same (user, profile, device, content,
	// episode) violates the partial unique index.
	dupNow := time.Now()
	dupErr := f.repo.Create(ctx, &Download{
		ID: id + "-dup", UserID: f.userID, ProfileID: f.profileA, DeviceID: f.deviceA,
		MediaFileID: f.fileID, ContentID: f.contentID, Kind: KindQueued,
		Status: StatusReady, Format: FormatOriginal, FileSize: 1024,
		CreatedAt: dupNow, UpdatedAt: dupNow,
	})
	if dupErr == nil {
		t.Fatal("expected unique-violation creating a duplicate managed entry, got nil")
	}

	// GetManagedEntry resolves the row by its unique key.
	got, err := f.repo.GetManagedEntry(ctx, f.userID, f.profileA, f.deviceA, f.contentID, "")
	if err != nil {
		t.Fatalf("GetManagedEntry: %v", err)
	}
	if got.ID != id {
		t.Fatalf("GetManagedEntry id = %q, want %q", got.ID, id)
	}

	// A revoked entry cannot be transitioned back out of revoked.
	if _, err := f.pool.Exec(ctx, `UPDATE downloads SET status = 'revoked' WHERE id = $1`, id); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := f.repo.UpdateManagedStatus(ctx, id, f.userID, f.profileA, f.deviceA, StatusCompleted, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("PATCH of revoked entry err = %v, want ErrNotFound", err)
	}
}

// TestManagedStatusTransitionGate verifies UpdateManagedStatus only advances an
// artifact-ready entry into the client serve lifecycle: a 'preparing' (artifact
// still encoding) or 'failed' entry can never be marked downloading/completed.
func TestManagedStatusTransitionGate(t *testing.T) {
	f := seedManagedFixture(t)
	ctx := context.Background()
	id := f.createManagedEntry(t) // created StatusReady

	// ready -> completed is allowed.
	now := time.Now()
	if err := f.repo.UpdateManagedStatus(ctx, id, f.userID, f.profileA, f.deviceA, StatusCompleted, &now); err != nil {
		t.Fatalf("ready->completed: %v", err)
	}

	// A preparing entry (artifact not yet encoded) is not patchable.
	if _, err := f.pool.Exec(ctx, `UPDATE downloads SET status = 'preparing' WHERE id = $1`, id); err != nil {
		t.Fatalf("set preparing: %v", err)
	}
	if err := f.repo.UpdateManagedStatus(ctx, id, f.userID, f.profileA, f.deviceA, StatusCompleted, &now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("preparing->completed err = %v, want ErrNotFound", err)
	}

	// A failed entry is not patchable either.
	if _, err := f.pool.Exec(ctx, `UPDATE downloads SET status = 'failed' WHERE id = $1`, id); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	if err := f.repo.UpdateManagedStatus(ctx, id, f.userID, f.profileA, f.deviceA, StatusDownloading, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed->downloading err = %v, want ErrNotFound", err)
	}
}

// TestReconcileLinkedDownloads verifies recovery repairs downloads stranded in
// 'preparing' against a terminal artifact state: ready→ready (the crash window
// between MarkReady and MarkLinkedDownloadsReady) and failed→failed, and that
// re-running it is a no-op.
func TestReconcileLinkedDownloads(t *testing.T) {
	f := seedManagedFixture(t)
	ctx := context.Background()

	var present *string
	if err := f.pool.QueryRow(ctx, `SELECT to_regclass('public.download_artifacts')::text`).Scan(&present); err != nil {
		t.Fatalf("check download_artifacts: %v", err)
	}
	if present == nil {
		t.Skip("download_artifacts migration has not been applied")
	}
	arepo := NewArtifactRepository(f.pool)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM download_artifacts WHERE media_file_id = $1`, f.fileID)
	})

	suffix := time.Now().UnixNano()

	// A ready artifact whose linked (ephemeral) download is stuck 'preparing'.
	readyArt := newArtifact(t, f.fileID, fmt.Sprintf("hash-recon-ready-%d", suffix))
	if _, _, err := arepo.EnsureQueued(ctx, readyArt); err != nil {
		t.Fatalf("ensure ready artifact: %v", err)
	}
	if _, err := arepo.ClaimNext(ctx, "w", time.Minute); err != nil {
		t.Fatalf("claim ready artifact: %v", err)
	}
	if ok, err := arepo.MarkReady(ctx, readyArt.ID, "w", "/tmp/ready.mp4", 4242); err != nil || !ok {
		t.Fatalf("MarkReady = (%v, %v)", ok, err)
	}

	// A failed artifact whose linked download is stuck 'preparing'.
	failedArt := newArtifact(t, f.fileID, fmt.Sprintf("hash-recon-failed-%d", suffix))
	if _, _, err := arepo.EnsureQueued(ctx, failedArt); err != nil {
		t.Fatalf("ensure failed artifact: %v", err)
	}
	for i := 0; i < failedArt.MaxAttempts; i++ {
		if _, err := arepo.ClaimNext(ctx, "w", time.Minute); err != nil {
			t.Fatalf("claim failed artifact: %v", err)
		}
		if _, _, err := arepo.MarkFailedOrRetry(ctx, failedArt.ID, "w", "boom", time.Millisecond); err != nil {
			t.Fatalf("MarkFailedOrRetry: %v", err)
		}
		_, _ = f.pool.Exec(ctx, `UPDATE download_artifacts SET next_retry_at = now() - interval '1 second' WHERE id = $1`, failedArt.ID)
	}

	now := time.Now()
	mkPreparing := func(artID string) string {
		id := fmt.Sprintf("dl-%s-%d", artID, now.UnixNano())
		if err := f.repo.Create(ctx, &Download{
			ID: id, UserID: f.userID, MediaFileID: f.fileID, ContentID: f.contentID,
			Kind: KindQueued, Status: StatusPreparing, Format: FormatTranscode, ArtifactID: artID,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("create preparing download: %v", err)
		}
		return id
	}
	readyDL := mkPreparing(readyArt.ID)
	failedDL := mkPreparing(failedArt.ID)

	ready, failed, err := f.repo.ReconcileLinkedDownloads(ctx)
	if err != nil {
		t.Fatalf("ReconcileLinkedDownloads: %v", err)
	}
	if len(ready) != 1 || ready[0].ID != readyDL || ready[0].FileSize != 4242 {
		t.Fatalf("ready flipped = %+v, want one %s with size 4242", ready, readyDL)
	}
	if len(failed) != 1 || failed[0].ID != failedDL {
		t.Fatalf("failed flipped = %+v, want one %s", failed, failedDL)
	}

	gotReady, err := f.repo.GetByID(ctx, readyDL)
	if err != nil || gotReady.Status != StatusReady {
		t.Fatalf("ready download status = %v (%v), want ready", gotReady.Status, err)
	}
	gotFailed, err := f.repo.GetByID(ctx, failedDL)
	if err != nil || gotFailed.Status != StatusFailed {
		t.Fatalf("failed download status = %v (%v), want failed", gotFailed.Status, err)
	}

	// Idempotent: nothing left in 'preparing', so a second pass flips nothing.
	ready2, failed2, err := f.repo.ReconcileLinkedDownloads(ctx)
	if err != nil {
		t.Fatalf("second ReconcileLinkedDownloads: %v", err)
	}
	if len(ready2) != 0 || len(failed2) != 0 {
		t.Fatalf("second reconcile flipped ready=%d failed=%d, want 0/0", len(ready2), len(failed2))
	}
}

// TestManagedEntryWithoutPostgresProfileRow is the dual-backend regression
// test: with the sqlite userdb backend, profiles exist only in per-user SQLite
// stores and public.user_profiles stays empty, so device registration and
// managed creates must not depend on a Postgres profile row (the
// user_devices_profile_fkey drop). Simulated here by never seeding
// user_profiles for the user.
func TestManagedEntryWithoutPostgresProfileRow(t *testing.T) {
	ctx := context.Background()
	pool := newDownloadsTestPool(t)

	var fkExists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'user_devices_profile_fkey')`,
	).Scan(&fkExists); err != nil {
		t.Fatalf("check profile fkey: %v", err)
	}
	if fkExists {
		t.Skip("drop_user_devices_profile_fkey migration has not been applied")
	}

	suffix := time.Now().UnixNano()
	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name) VALUES ('movies', $1) RETURNING id`,
		fmt.Sprintf("Downloads SQLite Test %d", suffix),
	).Scan(&folderID); err != nil {
		t.Fatalf("seed media folder: %v", err)
	}
	contentID := fmt.Sprintf("dl-sqlite-content-%d", suffix)
	var fileID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_files (content_id, media_folder_id, file_path, file_size)
		 VALUES ($1, $2, $3, 1024) RETURNING id`,
		contentID, folderID, fmt.Sprintf("/tmp/downloads-sqlite-test-%d.mp4", suffix),
	).Scan(&fileID); err != nil {
		t.Fatalf("seed media file: %v", err)
	}
	var userID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, role, download_allowed) VALUES ($1, 'user', true) RETURNING id`,
		fmt.Sprintf("dlsqlite-%d", suffix),
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM downloads WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_devices WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE id = $1`, fileID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	repo := NewRepository(pool)
	profileID := fmt.Sprintf("sqlite-only-profile-%d", suffix)
	deviceID := fmt.Sprintf("sqlite-dev-%d", suffix)
	// No user_profiles row exists for profileID; this must still succeed.
	if err := repo.EnsureDevice(ctx, userID, profileID, deviceID, "Phone", "android"); err != nil {
		t.Fatalf("EnsureDevice without Postgres profile row: %v", err)
	}
	now := time.Now()
	if _, err := repo.CreateManagedEntry(ctx, &Download{
		ID: fmt.Sprintf("dl-sqlite-%d", suffix), UserID: userID, ProfileID: profileID,
		DeviceID: deviceID, MediaFileID: fileID, ContentID: contentID, Kind: KindQueued,
		Status: StatusReady, Format: FormatOriginal, FileSize: 1024,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateManagedEntry without Postgres profile row: %v", err)
	}
}

// TestPurgeProfileDevices verifies the app-level replacement for the dropped
// FK cascade: purging a profile removes its device rows and (via the
// downloads composite FK) its managed downloads, leaving other profiles'
// libraries untouched.
func TestPurgeProfileDevices(t *testing.T) {
	ctx := context.Background()
	f := seedManagedFixture(t)
	id := f.createManagedEntry(t)

	if err := f.repo.PurgeProfileDevices(ctx, f.userID, f.profileA); err != nil {
		t.Fatalf("PurgeProfileDevices: %v", err)
	}

	if _, err := f.repo.GetManagedByID(ctx, id, f.userID, f.profileA, f.deviceA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("managed entry survived profile purge: err=%v", err)
	}
	var deviceRows int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM user_devices WHERE user_id = $1 AND profile_id = $2`,
		f.userID, f.profileA,
	).Scan(&deviceRows); err != nil || deviceRows != 0 {
		t.Fatalf("profileA device rows = %d (%v), want 0", deviceRows, err)
	}
	var otherRows int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM user_devices WHERE user_id = $1 AND profile_id = $2`,
		f.userID, f.profileB,
	).Scan(&otherRows); err != nil || otherRows != 1 {
		t.Fatalf("profileB device rows = %d (%v), want 1", otherRows, err)
	}
}

// TestRegisterManagedItemsBatchAndCount pins the bulk-registration contract:
// registration is one batched fetch + one batched insert (not a per-episode
// loop), and the returned rows are ONLY the newly created ones, so the sync
// response's "registered" count reports 0 in the steady state.
func TestRegisterManagedItemsBatchAndCount(t *testing.T) {
	ctx := context.Background()
	f := seedManagedFixture(t)

	mkItems := func(n int) []managedItem {
		items := make([]managedItem, 0, n)
		for i := 0; i < n; i++ {
			items = append(items, managedItem{
				file:      &models.MediaFile{ID: f.fileID, FileSize: 1024},
				contentID: f.contentID,
				episodeID: fmt.Sprintf("reg-ep-%d", i),
			})
		}
		return items
	}

	first, err := registerManagedItems(ctx, f.repo, f.userID, f.profileA, f.deviceA, mkItems(3), "batch-reg")
	if err != nil {
		t.Fatalf("first register: %v", err)
	}
	if len(first) != 3 {
		t.Fatalf("first register = %d rows, want 3", len(first))
	}

	// Steady state: nothing new → zero rows returned.
	again, err := registerManagedItems(ctx, f.repo, f.userID, f.profileA, f.deviceA, mkItems(3), "batch-reg")
	if err != nil {
		t.Fatalf("second register: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("steady-state register = %d rows, want 0", len(again))
	}

	// A grown scope registers only the delta.
	grown, err := registerManagedItems(ctx, f.repo, f.userID, f.profileA, f.deviceA, mkItems(5), "batch-reg")
	if err != nil {
		t.Fatalf("grown register: %v", err)
	}
	if len(grown) != 2 {
		t.Fatalf("grown register = %d rows, want 2", len(grown))
	}
}
