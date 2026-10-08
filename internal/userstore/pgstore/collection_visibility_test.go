package pgstore

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Native routes never read Audiobookshelf rows, which share the table without
// the native mark, and never another login's collections, shared or not.
func TestCollectionVisibilityExcludesAudiobookshelfAndOtherLoginsPostgres(t *testing.T) {
	pool, userID := newConstraintTestUser(t)
	_, otherUserID := newConstraintTestUser(t)
	ctx := t.Context()
	store := newStore(pool, userID)
	own, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "p1", Name: "own"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newStore(pool, otherUserID).CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "p1", Name: "other login", IsShared: true}); err != nil {
		t.Fatal(err)
	}
	// An Audiobookshelf playlist, public on its server: stored the way the
	// audiobook stores write it, without the native mark.
	if _, err := pool.Exec(ctx, `INSERT INTO user_personal_collections (id, user_id, profile_id, creator_profile_id, name, collection_type, is_shared)
		VALUES ('abs-playlist', $1, 'p1', 'p1', 'ABS playlist', 'playlist', TRUE)`, userID); err != nil {
		t.Fatal(err)
	}

	for _, profileID := range []string{"p1", "p2"} {
		listed, err := store.ListCollections(ctx, profileID)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range listed {
			if c.ID != own.ID {
				t.Fatalf("ListCollections(%s) returned %s (%s)", profileID, c.ID, c.Name)
			}
		}
	}
	if _, err := store.GetCollection(ctx, "abs-playlist"); err == nil {
		t.Fatal("GetCollection returned an Audiobookshelf row")
	}
}
