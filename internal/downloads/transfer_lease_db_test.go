package downloads

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// An account-level transfer whose serving process died leaves its row
// 'downloading' with nothing renewing it. Once the lease lapses it must stop
// holding a concurrent slot, while a renewed transfer keeps its slot.
func TestDeadAccountTransferReleasesItsConcurrentSlotDB(t *testing.T) {
	f := seedManagedFixture(t)
	ctx := context.Background()
	id := fmt.Sprintf("lease-%d", time.Now().UnixNano())
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO downloads (id, user_id, media_file_id, content_id, format, status, updated_at)
		VALUES ($1, $2, $3, $4, 'original', 'downloading', now() - interval '10 minutes')`,
		id, f.userID, f.fileID, f.contentID); err != nil {
		t.Fatalf("seed account-level download: %v", err)
	}
	if n, err := f.repo.CountActiveByUser(ctx, f.userID); err != nil || n != 0 {
		t.Fatalf("unrenewed transfer: active = %d, %v; want 0", n, err)
	}

	if _, err := f.pool.Exec(ctx, `UPDATE downloads SET updated_at = now() WHERE id = $1`, id); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if n, err := f.repo.CountActiveByUser(ctx, f.userID); err != nil || n != 1 {
		t.Fatalf("renewed transfer: active = %d, %v; want 1", n, err)
	}
}
