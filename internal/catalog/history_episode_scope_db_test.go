package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// TestResolveHistoryEpisodeScope verifies the two history granularities end to
// end: the default (unscoped) history view collapses episode watch events into
// one series display item, while the episode media scope resolves the same
// history rows to individual episode items in most-recent-first watch order.
func TestResolveHistoryEpisodeScope(t *testing.T) {
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
	seriesID := fmt.Sprintf("hes-series-%d", suffix)
	ep1 := fmt.Sprintf("hes-ep1-%d", suffix)
	ep2 := fmt.Sprintf("hes-ep2-%d", suffix)

	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ('series', 'HES Test', true) RETURNING id`,
	).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	var userID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		fmt.Sprintf("hes-user-%d", suffix),
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	profileID := fmt.Sprintf("00000000-0000-4000-8000-%012d", suffix%1_000_000_000_000)
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, 'HES Profile')`,
		profileID, userID,
	); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM user_watch_history WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_profiles WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		// episodes and episode_libraries cascade from the series row.
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	if _, err := pool.Exec(ctx,
		`INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'series', 'HES Series', 'matched', '{}'::text[])`,
		seriesID,
	); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	for i, epID := range []string{ep1, ep2} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
			VALUES ($1, $2, 1, $3, 'HES Ep')`,
			epID, seriesID, i+1,
		); err != nil {
			t.Fatalf("seed episode %s: %v", epID, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at)
			VALUES ($1, $2, NOW())`,
			epID, folderID,
		); err != nil {
			t.Fatalf("seed episode library %s: %v", epID, err)
		}
	}

	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatalf("store for user: %v", err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	// ep1 watched first, then ep2: most-recent-first order is ep2, ep1.
	for i, epID := range []string{ep1, ep2} {
		if err := store.AddHistory(ctx, userstore.WatchHistoryEntry{
			ProfileID:   profileID,
			MediaItemID: epID,
			WatchedAt:   base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			Completed:   true,
		}); err != nil {
			t.Fatalf("seed history %s: %v", epID, err)
		}
	}

	resolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool)).
		WithUserStoreProvider(provider).
		WithEpisodeRepository(NewEpisodeRepository(pool))
	access := AccessFilter{UserID: userID, ProfileID: profileID}

	episodeResult, err := resolver.Resolve(ctx, CatalogRequest{
		Source:         CatalogSourceHistory,
		Limit:          20,
		UseSourceOrder: true,
		Query:          QueryDefinition{MediaScope: "episode"},
	}, access)
	if err != nil {
		t.Fatalf("resolve episode-scoped history: %v", err)
	}
	if len(episodeResult.Items) != 2 {
		t.Fatalf("episode-scoped history returned %d items, want 2", len(episodeResult.Items))
	}
	if episodeResult.Items[0].ContentID != ep2 || episodeResult.Items[1].ContentID != ep1 {
		t.Fatalf("episode-scoped history order = [%s, %s], want [%s, %s]",
			episodeResult.Items[0].ContentID, episodeResult.Items[1].ContentID, ep2, ep1)
	}
	for _, item := range episodeResult.Items {
		if item.Type != "episode" {
			t.Fatalf("episode-scoped history item %s has type %q, want episode", item.ContentID, item.Type)
		}
	}

	seriesResult, err := resolver.Resolve(ctx, CatalogRequest{
		Source:         CatalogSourceHistory,
		Limit:          20,
		UseSourceOrder: true,
	}, access)
	if err != nil {
		t.Fatalf("resolve unscoped history: %v", err)
	}
	if len(seriesResult.Items) != 1 || seriesResult.Items[0].ContentID != seriesID {
		t.Fatalf("unscoped history = %+v, want single series item %s", seriesResult.Items, seriesID)
	}
}
