package catalog

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// TestPersonalCollectionReadsRememberedDB covers the per-viewer memo of
// personal collection list reads: a count or a collage with no source is
// served again without scanning the members until the membership revision or
// the viewer's access changes. Set SILO_TEST_DATABASE_URL to a migrated
// database to run it.
func TestPersonalCollectionReadsRememberedDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	prefix, userID, profile, library, titles := seedPersonalCollectionReadMemo(t, pool)

	store, err := pgstore.NewPostgresProvider(pool).ForUser(ctx, userID)
	if err != nil {
		t.Fatalf("open user store: %v", err)
	}
	manual, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: profile, Name: "Manual", CollectionType: "manual"})
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	for i, id := range titles[:2] {
		if err := store.AddCollectionItem(ctx, manual.ID, id, i); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}
	viewer := AccessFilter{UserID: userID, ProfileID: profile}
	restricted := AccessFilter{UserID: userID, ProfileID: profile, AllowedLibraryIDs: []int{library}}

	t.Run("counts", func(t *testing.T) {
		counts := NewPersonalCollectionMemberCounts(pool)
		count := func(access AccessFilter) int {
			t.Helper()
			got, err := counts.Count(ctx, userID, []string{manual.ID}, access)
			if err != nil {
				t.Fatalf("count: %v", err)
			}
			return got[manual.ID]
		}
		if got := count(viewer); got != 2 {
			t.Fatalf("count = %d, want 2", got)
		}
		// A catalog change that leaves membership alone is served from the
		// memo until it expires.
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = $1`, titles[0])
		if got := count(viewer); got != 2 {
			t.Fatalf("remembered count = %d, want 2", got)
		}
		// Another access is counted afresh.
		if got := count(restricted); got != 1 {
			t.Fatalf("restricted count = %d, want 1", got)
		}
		// A membership change is counted at once.
		if err := store.RemoveCollectionItem(ctx, manual.ID, titles[1]); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if got := count(viewer); got != 0 {
			t.Fatalf("count after remove = %d, want 0", got)
		}
		batchEquivExec(t, pool, `INSERT INTO media_items (content_id, type, title, sort_title) VALUES ($1, 'movie', $1, $1)`, titles[0])
		batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, titles[0], library)
	})

	t.Run("collages", func(t *testing.T) {
		gen := &fakeCollageGenerator{}
		collages := NewPersonalCollectionCollages(pool, gen)
		def := []PersonalCollectionDefinition{{ID: manual.ID, CollectionType: "manual"}}
		if got := collages.Posters(ctx, userID, def, viewer); len(got) != 0 {
			t.Fatalf("posters = %+v, want none", got)
		}
		// A poster that arrives without a membership change is not looked
		// for again until the memo expires.
		poster := "test/" + prefix + "/poster.webp"
		batchEquivExec(t, pool, `UPDATE media_items SET poster_path = $2 WHERE content_id = $1`, titles[0], poster)
		ids := []string{manual.ID}
		revisions, err := personalCollectionRevisions(ctx, pool, userID, ids)
		if err != nil {
			t.Fatalf("revisions: %v", err)
		}
		if _, ok := collages.noSources.get(personalCollectionReadKey{userID, manual.ID, collageAccessKey(viewer)}, revisions[manual.ID], time.Now()); !ok {
			t.Fatal("the read did not remember that the collection has no collage")
		}
		if got := collages.Posters(ctx, userID, def, viewer); len(got) != 0 {
			t.Fatalf("posters = %+v, want none while remembered", got)
		}
		// A membership change looks again and builds the collage.
		if err := store.AddCollectionItem(ctx, manual.ID, titles[1], 1); err != nil {
			t.Fatalf("add: %v", err)
		}
		collages.Posters(ctx, userID, def, viewer)
		waitForStoredCollage(t, personalCollageStore{pool: pool, userID: userID}, CollectionCollageRef{CollectionID: manual.ID, Key: CollectionCollageKey([]string{poster})})
	})
}

// seedPersonalCollectionReadMemo seeds an account with one profile, a library
// and two titles in it without posters.
func seedPersonalCollectionReadMemo(t *testing.T, pool *pgxpool.Pool) (prefix string, userID int, profile string, library int, titles []string) {
	t.Helper()
	ctx := context.Background()
	prefix = fmt.Sprintf("collection-read-memo-%d", time.Now().UnixNano())
	library = seedCollagePosterLibrary(t, pool, prefix)
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM user_personal_collections WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_collection_revisions WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path LIKE $1`, "collection-images/%"+prefix+"%")
	})
	profile = prefix + "-profile"
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, 'owner')`, profile, userID); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	titles = make([]string, 2)
	for i := range titles {
		titles[i] = fmt.Sprintf("%s-%d", prefix, i)
		batchEquivExec(t, pool, `INSERT INTO media_items (content_id, type, title, sort_title) VALUES ($1, 'movie', $1, $1)`, titles[i])
		batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, titles[i], library)
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, titles[i])
		})
	}
	return prefix, userID, profile, library, titles
}
