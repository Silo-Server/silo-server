package handlers

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/blobstore/blobstoretest"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// Deleting an account removes the artwork of every personal collection its
// profiles had; the foreign-key cascade that removes the rows cannot. Another account's collection keeps its artwork.
func TestAdminAccountDeleteRemovesCollectionArtworkDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	insert := func(label, role string) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, enabled)
			VALUES ($1, $2, 'x', $3, true) RETURNING id`, label+"-"+suffix, label+"-"+suffix+"@example.invalid", role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
		return id
	}
	provider := pgstore.NewPostgresProvider(pool)
	artwork := blobstoretest.New()
	// seed gives each of the account's profiles a collection with artwork.
	seed := func(userID int, profiles ...string) []string {
		t.Helper()
		store, err := provider.ForUser(ctx, userID)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for i, profileID := range profiles {
			if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: profileID, IsPrimary: i == 0}); err != nil {
				t.Fatal(err)
			}
			c, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: profileID, Name: profileID, CollectionType: userstore.CollectionTypeManual})
			if err != nil {
				t.Fatal(err)
			}
			if err := artwork.Put(ctx, userCollectionImagePrefix+"/"+c.ID+"/poster/w300.a.webp", []byte("img")); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, c.ID)
		}
		return ids
	}
	artworkLeft := func(collectionID string) int {
		t.Helper()
		objects, _, err := artwork.List(ctx, userCollectionImagePrefix+"/"+collectionID+"/", "", 10)
		if err != nil {
			t.Fatal(err)
		}
		return len(objects)
	}

	adminID := insert("artwork-admin", models.RoleAdmin)
	keptID := insert("artwork-kept", models.RoleUser)
	kept := seed(keptID, "kept-"+suffix)
	h := NewAdminHandler(auth.NewUserRepository(pool), pool, provider)
	h.ArtworkStore = artwork

	goneID := insert("artwork-gone", models.RoleUser)
	gone := seed(goneID, "main-"+suffix, "kid-"+suffix)
	if err := h.DeleteAdminAccount(claimsCtx(adminID), goneID, -1, 0); err != nil {
		t.Fatal(err)
	}
	for _, id := range gone {
		if n := artworkLeft(id); n != 0 {
			t.Errorf("deleted account's collection %s keeps %d artwork objects", id, n)
		}
	}
	if n := artworkLeft(kept[0]); n != 1 {
		t.Errorf("other account's collection has %d artwork objects, want 1", n)
	}
}
