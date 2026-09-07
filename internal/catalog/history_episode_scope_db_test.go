package catalog

import (
	"context"
	"fmt"
	"os"
	"slices"
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
	ctx := t.Context()
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
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM user_watch_history WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_profiles WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		// episodes and episode_libraries cascade from the series row.
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	if _, err := pool.Exec(ctx,
		`INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'series', 'HES Series', 'matched', ARRAY['HES-Series'])`,
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
	base := time.Now().UTC().Add(-24 * time.Hour)
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
	// A matching movie predates more than 10,000 repeated episode watches.
	// Candidate loading must deduplicate the full history before filtering.
	olderMovieID := fmt.Sprintf("hes-older-%d", suffix)
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'movie', 'HES Earlier', 'matched', ARRAY['HES-Movie'])`, olderMovieID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, olderMovieID)
	})
	if err := store.AddHistory(ctx, userstore.WatchHistoryEntry{ProfileID: profileID, MediaItemID: olderMovieID, WatchedAt: base.Add(-time.Minute).Format(time.RFC3339), Completed: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_watch_history (id, user_id, profile_id, media_item_id, watched_at, completed, duration_seconds)
		SELECT $1 || '-' || n::text, $2, $3, $4, $5::timestamptz + n * interval '1 second', true, 0
		FROM generate_series(1,10001) n`, fmt.Sprintf("hes-bulk-%d", suffix), userID, profileID, ep2, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		allowedIDs []string
		req        CatalogRequest
		want       []string
		total      int
	}{
		{name: "oldest filtered beyond event cap", req: CatalogRequest{SearchQuery: "HES", Query: QueryDefinition{Sort: QuerySort{Field: "date_viewed", Order: "asc"}}}, want: []string{olderMovieID, seriesID}, total: 2},
		{name: "title sorting after history join", req: CatalogRequest{NamePrefix: "hes", Query: QueryDefinition{Sort: QuerySort{Field: "title", Order: "asc"}}}, want: []string{olderMovieID, seriesID}, total: 2},
		{name: "search normalized tokens", req: CatalogRequest{SearchQuery: "HES Earlier", Query: QueryDefinition{Sort: QuerySort{Field: "date_viewed", Order: "asc"}}}, want: []string{olderMovieID}, total: 1},
		{name: "name prefix before page cap", req: CatalogRequest{NamePrefix: "hes e", Query: QueryDefinition{Sort: QuerySort{Field: "date_viewed", Order: "desc"}, Limit: new(1)}}, want: []string{olderMovieID}, total: 1},
		{name: "newest filtered deduplicates series", req: CatalogRequest{SearchQuery: "HES", Query: QueryDefinition{Sort: QuerySort{Field: "date_viewed", Order: "desc"}}}, want: []string{seriesID, olderMovieID}, total: 2},
		{name: "episode content allowlist", allowedIDs: []string{ep1}, req: CatalogRequest{Query: QueryDefinition{MediaScope: "episode", Sort: QuerySort{Field: "date_viewed", Order: "asc"}}}, want: []string{ep1}, total: 1},
		{name: "oldest episodes beyond event cap", req: CatalogRequest{Query: QueryDefinition{MediaScope: "episode", Sort: QuerySort{Field: "date_viewed", Order: "asc"}}}, want: []string{ep1, ep2}, total: 2},
		{name: "query limit before pagination", req: CatalogRequest{Query: QueryDefinition{Sort: QuerySort{Field: "date_viewed", Order: "asc"}, Limit: new(1)}}, want: []string{olderMovieID}, total: 1},
		{name: "offset past query limit", req: CatalogRequest{Offset: 1, Query: QueryDefinition{Sort: QuerySort{Field: "date_viewed", Order: "asc"}, Limit: new(1)}}, total: 1},
		{name: "snapshot excludes later episode watches", req: CatalogRequest{SnapshotAt: new(base.Add(30 * time.Second)), Query: QueryDefinition{MediaScope: "episode", Sort: QuerySort{Field: "date_viewed", Order: "asc"}}}, want: []string{ep1}, total: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.req.Source = CatalogSourceHistory
			tc.req.Limit = 60
			caseAccess := access
			caseAccess.AllowedContentIDs = tc.allowedIDs
			result, err := resolver.Resolve(t.Context(), tc.req, caseAccess)
			if err != nil {
				t.Fatal(err)
			}
			if result.Total != tc.total || len(result.Items) != len(tc.want) {
				t.Fatalf("got total=%d items=%d, want total=%d items=%d", result.Total, len(result.Items), tc.total, len(tc.want))
			}
			for i, id := range tc.want {
				if result.Items[i].ContentID != id {
					t.Fatalf("item %d=%s, want %s", i, result.Items[i].ContentID, id)
				}
			}
			if result.HasMore {
				t.Fatal("complete or exhausted query-limited page must not advertise more results")
			}
		})
	}

	for _, prefix := range []string{"", "hes"} {
		t.Run("snapshot paging prefix="+prefix, func(t *testing.T) {
			req := CatalogRequest{Source: CatalogSourceHistory, Limit: 1, NamePrefix: prefix, Query: QueryDefinition{Sort: QuerySort{Field: "date_viewed", Order: "desc"}, Limit: new(2)}}
			first, err := resolver.Resolve(t.Context(), req, access)
			if err != nil {
				t.Fatal(err)
			}
			if first.SnapshotAt.IsZero() {
				t.Fatal("first history page must return a watch-event snapshot")
			}
			if len(first.Items) != 1 || first.Total != 2 || !first.HasMore {
				t.Fatalf("unexpected first page: %+v", first)
			}
			req.SnapshotAt = &first.SnapshotAt
			req.Limit = 60
			frozen, err := resolver.Resolve(t.Context(), req, access)
			if err != nil {
				t.Fatal(err)
			}
			if len(frozen.Items) != 2 {
				t.Fatalf("expected two frozen items, got %d", len(frozen.Items))
			}
			if err := store.AddHistory(t.Context(), userstore.WatchHistoryEntry{ProfileID: profileID, MediaItemID: frozen.Items[1].ContentID, WatchedAt: first.SnapshotAt.Add(time.Minute).Format(time.RFC3339), Completed: true}); err != nil {
				t.Fatal(err)
			}
			req.Offset = 1
			req.Limit = 1
			second, err := resolver.Resolve(t.Context(), req, access)
			if err != nil {
				t.Fatal(err)
			}
			if len(second.Items) != 1 || second.Items[0].ContentID != frozen.Items[1].ContentID || second.Total != 2 || second.HasMore || !second.SnapshotAt.Equal(first.SnapshotAt) {
				t.Fatalf("rewatch changed frozen page: %+v", second)
			}
			req.SkipTotal = true
			withoutTotal, err := resolver.Resolve(t.Context(), req, access)
			if err != nil {
				t.Fatal(err)
			}
			if len(withoutTotal.Items) != 1 || withoutTotal.HasMore || withoutTotal.TotalExact || withoutTotal.Total != 0 {
				t.Fatalf("query cap lost without total: %+v", withoutTotal)
			}
		})
	}

	for _, scope := range []string{"", "episode"} {
		t.Run("SQL facets scope="+scope, func(t *testing.T) {
			req := CatalogRequest{Source: CatalogSourceHistory, Query: QueryDefinition{MediaScope: scope}}
			filters, err := resolver.ListFiltersWithOptions(t.Context(), req, access, CatalogFilterOptions{})
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"HES-Movie", "HES-Series"}
			if scope == "episode" {
				want = []string{"HES-Series"}
			}
			if !slices.Equal(filters.Genres, want) {
				t.Fatalf("genres=%v, want %v", filters.Genres, want)
			}
			matches, err := resolver.SearchFacet(t.Context(), req, access, "genre", "HES-", 10)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(matches.Matches, want) {
				t.Fatalf("facet search=%v, want %v", matches.Matches, want)
			}
		})
	}

}
