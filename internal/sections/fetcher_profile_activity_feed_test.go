package sections

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// TestProfileActivityFeedReadsOnlyTheCallersAccountDB checks that "What
// Others Just Watched" lists the caller's other profiles only, and that a
// profile pinned from another account lists nothing.
func TestProfileActivityFeedReadsOnlyTheCallersAccountDB(t *testing.T) {
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

	suffix := time.Now().UnixNano()
	seedUser := func(name string) int {
		t.Helper()
		username := fmt.Sprintf("activity-feed-%s-%d", name, suffix)
		var id int
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, '', 'user') RETURNING id`,
			username, username+"@example.test",
		).Scan(&id); err != nil {
			t.Fatalf("seed user %s: %v", name, err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id) })
		return id
	}
	owner := seedUser("owner")
	neighbor := seedUser("neighbor")

	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
		fmt.Sprintf("activity-feed-%d", suffix),
	).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	movie := func(name string) string { return fmt.Sprintf("activity-feed-%s-%d", name, suffix) }
	movies := []string{movie("owner-kid"), movie("neighbor-main"), movie("neighbor-kid")}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, movies)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	for _, id := range movies {
		if _, err := pool.Exec(ctx,
			`INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'movie', $1, 'matched', '{}'::text[])`, id,
		); err != nil {
			t.Fatalf("seed movie %s: %v", id, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, NOW())`, id, folderID,
		); err != nil {
			t.Fatalf("seed membership %s: %v", id, err)
		}
	}

	ownerMain := fmt.Sprintf("owner-main-%d", suffix)
	ownerKid := fmt.Sprintf("owner-kid-%d", suffix)
	neighborMain := fmt.Sprintf("neighbor-main-%d", suffix)
	neighborKid := fmt.Sprintf("neighbor-kid-%d", suffix)
	watch := func(userID int, profileID, contentID string) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`INSERT INTO user_watch_history (id, user_id, profile_id, media_item_id, watched_at, completed) VALUES ($1, $2, $3, $4, NOW(), true)`,
			fmt.Sprintf("%s-%s", profileID, contentID), userID, profileID, contentID,
		); err != nil {
			t.Fatalf("seed watch %s/%s: %v", profileID, contentID, err)
		}
	}
	watch(owner, ownerKid, movie("owner-kid"))
	watch(neighbor, neighborMain, movie("neighbor-main"))
	watch(neighbor, neighborKid, movie("neighbor-kid"))

	fetcher := NewFetcher(pool)
	tests := []struct {
		name      string
		userID    int
		profileID string
		pinned    string
		want      []string
	}{
		{name: "household, neighbor", userID: neighbor, profileID: neighborMain, want: []string{movie("neighbor-kid")}},
		{name: "household, owner", userID: owner, profileID: ownerMain, want: []string{movie("owner-kid")}},
		{name: "pinned to own profile", userID: neighbor, profileID: neighborMain, pinned: neighborKid, want: []string{movie("neighbor-kid")}},
		{name: "pinned to another account's profile", userID: neighbor, profileID: neighborMain, pinned: ownerKid},
		{name: "no account", profileID: neighborMain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := json.Marshal(map[string]string{"profile_id": tt.pinned})
			if err != nil {
				t.Fatal(err)
			}
			items, total, err := fetcher.fetchProfileActivityFeed(ctx, ResolvedSection{
				SectionType: SectionProfileActivityFeed,
				ItemLimit:   20,
				Config:      config,
			}, nil, []int{folderID}, tt.userID, tt.profileID, catalog.AccessFilter{})
			if err != nil {
				t.Fatalf("fetchProfileActivityFeed: %v", err)
			}
			got := make([]string, 0, len(items))
			for _, item := range items {
				got = append(got, item.ContentID)
			}
			if total != len(tt.want) || fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Fatalf("items = %v (total %d), want %v", got, total, tt.want)
			}
		})
	}
}
