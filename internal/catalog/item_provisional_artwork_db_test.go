package catalog

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// A provisional item's sidecar artwork is written only while the row is still
// provisional with the sources and images lock the writer read: a match or an
// admin's artwork pick or lock that committed in between is not overwritten.
func TestUpdateProvisionalArtworkIsCompareAndSetDB(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	repo := NewItemRepository(pool)
	contentID := fmt.Sprintf("provisional-art-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID) })
	if err := repo.Upsert(ctx, &models.MediaItem{ContentID: contentID, Type: "movie", Title: "Birthday", Status: "unmatched"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	read := func() *models.MediaItem {
		t.Helper()
		item, err := repo.GetByID(ctx, contentID)
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	posterUpdate := func(source string) *MetadataUpdate {
		empty := ""
		return &MetadataUpdate{PosterPath: &empty, PosterSourcePath: &source, PosterThumbhash: &empty}
	}

	snapshot := read()
	if ok, err := repo.UpdateProvisionalArtwork(ctx, snapshot, posterUpdate("file:///m/a-poster.jpg?v=1-1")); err != nil || !ok {
		t.Fatalf("write on an unchanged unmatched row = %v, %v; want written", ok, err)
	}
	if got := read(); got.PosterSourcePath != "file:///m/a-poster.jpg?v=1-1" || got.Status != "unmatched" || got.Title != "Birthday" {
		t.Fatalf("stored item = source %q status %q title %q", got.PosterSourcePath, got.Status, got.Title)
	}

	// The poster source changed since the read (the write above stands in for
	// an admin's pick): nothing is written.
	if ok, err := repo.UpdateProvisionalArtwork(ctx, snapshot, posterUpdate("file:///m/b-poster.jpg?v=1-1")); err != nil || ok {
		t.Fatalf("write over a changed poster source = %v, %v; want no write", ok, err)
	}
	if got := read().PosterSourcePath; got != "file:///m/a-poster.jpg?v=1-1" {
		t.Fatalf("poster source = %q, want the newer value kept", got)
	}

	// An admin locked the artwork since the read, sources unchanged: nothing
	// is written.
	snapshot = read()
	if _, err := pool.Exec(ctx, `UPDATE media_items SET locked_fields = $2::int[] WHERE content_id = $1`,
		contentID, []int{fieldImagesLocked}); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.UpdateProvisionalArtwork(ctx, snapshot, posterUpdate("file:///m/b-poster.jpg?v=1-1")); err != nil || ok {
		t.Fatalf("write after the images lock was set = %v, %v; want no write", ok, err)
	}
	if got := read().PosterSourcePath; got != "file:///m/a-poster.jpg?v=1-1" {
		t.Fatalf("locked poster source = %q, want it untouched", got)
	}

	// The item was matched since the read: nothing is written.
	snapshot = read()
	if _, err := pool.Exec(ctx, `UPDATE media_items SET status = 'matched' WHERE content_id = $1`, contentID); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.UpdateProvisionalArtwork(ctx, snapshot, posterUpdate("file:///m/b-poster.jpg?v=1-1")); err != nil || ok {
		t.Fatalf("write on a matched row = %v, %v; want no write", ok, err)
	}
	if got := read().PosterSourcePath; got != "file:///m/a-poster.jpg?v=1-1" {
		t.Fatalf("matched poster source = %q, want it untouched", got)
	}
}
