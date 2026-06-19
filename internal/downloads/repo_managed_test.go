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

	// Skip when the device/format reshape migration has not been applied.
	var col *string
	err = pool.QueryRow(ctx, `SELECT column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'downloads' AND column_name = 'device_id'`).Scan(&col)
	if errors.Is(err, pgx.ErrNoRows) || col == nil {
		t.Skip("downloads device/format reshape migration has not been applied")
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
