package usercollections

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestLibraryTabCollectionsFollowSavedOrderDB: a library's Collections tab
// lists the viewer's own collections in its saved order, then other profiles'
// shared ones, each in its creator's order, as GET /api/v2/collections does.
// The viewer's profile id sorts after the sharer's, so its collections come
// first because they are its own, not because of the id.
func TestLibraryTabCollectionsFollowSavedOrderDB(t *testing.T) {
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
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	sfx := hex.EncodeToString(b)
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, permissions) VALUES ($1::text, $1::text || '@example.test', 'h', 'user', '{}') RETURNING id`, "ucord-"+sfx).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	viewer, sharer := "pz-"+sfx, "pa-"+sfx
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ($1, $3, 'Viewer', true), ($2, $3, 'Sharer', false)`, viewer, sharer, userID); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		creator, name string
		order         int
	}{
		{viewer, "Alpha", 1}, {viewer, "Bravo", 2}, {viewer, "Charlie", 0},
		{sharer, "Xray", 1}, {sharer, "Yankee", 0},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO user_personal_collections
			(id, user_id, profile_id, creator_profile_id, name, collection_type, is_shared, include_in_server_collections, sort_order, native)
			VALUES ($1, $2, $3, $3, $4, 'manual', TRUE, TRUE, $5, TRUE)`,
			"uc-"+c.name+"-"+sfx, userID, c.creator, c.name, c.order); err != nil {
			t.Fatal(err)
		}
	}

	got, err := ListServerVisibleByLibrary(ctx, pool, userID, viewer, 1)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(got))
	for _, c := range got {
		names = append(names, c.Name)
	}
	if want := []string{"Charlie", "Alpha", "Bravo", "Yankee", "Xray"}; !slices.Equal(names, want) {
		t.Fatalf("library tab order = %v, want %v", names, want)
	}
}
