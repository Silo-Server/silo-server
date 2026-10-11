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

// TestProfileActivityFeedPinnedProfileStaysOnCallersAccountDB pins the scope of
// What Others Just Watched. Household mode lists every other profile on the
// server, like Trending and Most Watched. A pinned profile_id is saved section
// config the caller chooses, so it resolves only to a profile on the caller's
// account: pinning another account's profile lists nothing.
func TestProfileActivityFeedPinnedProfileStaysOnCallersAccountDB(t *testing.T) {
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
	id := func(name string) string { return fmt.Sprintf("activity-feed-%s-%d", name, suffix) }
	seedUser := func(name string) int {
		var userID int
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, '', 'user') RETURNING id`,
			id(name), id(name)+"@example.test",
		).Scan(&userID); err != nil {
			t.Fatalf("seed user %s: %v", name, err)
		}
		return userID
	}
	owner, neighbor := seedUser("owner"), seedUser("neighbor")
	ownerMain, ownerKid, neighborMain := id("owner-main"), id("owner-kid"), id("neighbor-main")
	ownerKidMovie, neighborMovie := id("owner-kid-movie"), id("neighbor-movie")
	movies := []string{ownerKidMovie, neighborMovie}

	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, id("folder"),
	).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1)`, []int{owner, neighbor})
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, movies)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	if _, err := pool.Exec(ctx,
		`INSERT INTO media_items (content_id, type, title, status, genres) SELECT unnest($1::text[]), 'movie', 'Movie', 'matched', '{}'::text[]`, movies,
	); err != nil {
		t.Fatalf("seed items: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO media_item_libraries (content_id, media_folder_id) SELECT unnest($1::text[]), $2`, movies, folderID,
	); err != nil {
		t.Fatalf("seed memberships: %v", err)
	}

	now := time.Now()
	plays := []struct {
		userID         int
		profile, movie string
		at             time.Time
	}{
		{owner, ownerKid, ownerKidMovie, now.Add(-2 * time.Hour)},
		{neighbor, neighborMain, neighborMovie, now.Add(-time.Hour)},
	}
	for i, p := range plays {
		if _, err := pool.Exec(ctx,
			`INSERT INTO user_watch_history (id, user_id, profile_id, media_item_id, watched_at, completed) VALUES ($1, $2, $3, $4, $5, true)`,
			fmt.Sprintf("%s-%d", id("history"), i), p.userID, p.profile, p.movie, p.at,
		); err != nil {
			t.Fatalf("seed history %d: %v", i, err)
		}
	}

	fetcher := NewFetcher(pool)
	tests := []struct {
		name      string
		userID    int
		profileID string
		pinned    string
		want      []string
	}{
		// Household mode covers the whole server, latest first.
		{name: "household mode lists other accounts", userID: owner, profileID: ownerMain, want: []string{neighborMovie, ownerKidMovie}},
		{name: "pinned to a profile on the caller's account", userID: owner, profileID: ownerMain, pinned: ownerKid, want: []string{ownerKidMovie}},
		{name: "pinned to another account's profile", userID: owner, profileID: ownerMain, pinned: neighborMain, want: []string{}},
		{name: "pinned without an account", pinned: neighborMain, want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := json.Marshal(map[string]string{"profile_id": tt.pinned})
			if err != nil {
				t.Fatalf("marshal config: %v", err)
			}
			s := ResolvedSection{SectionType: SectionProfileActivityFeed, ItemLimit: 10, Config: config}
			items, total, err := fetcher.fetchSection(ctx, s, &folderID, nil, tt.userID, tt.profileID, catalog.AccessFilter{})
			if err != nil {
				t.Fatalf("fetch: %v", err)
			}
			got := make([]string, len(items))
			for i, item := range items {
				got[i] = item.ContentID
			}
			if total != len(tt.want) || fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Fatalf("items = %v (total %d), want %v", got, total, tt.want)
			}
		})
	}
}
