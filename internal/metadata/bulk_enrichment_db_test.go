package metadata

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBulkEnrichmentTargetsPostgres(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	installationID := insertTestInstallation(t, pool, "plugin", true)
	bulkCap := fmt.Sprintf("test-bulk-%d", suffix)
	insertTestCapability(t, pool, installationID, bulkCap,
		`{"metadata":{"lookup_provider_ids":["imdb","tmdb"],"bulk_lookup_limit":100,"default_priority":{"movie":8,"series":8}}}`)
	lookupOnlyCap := fmt.Sprintf("test-lookup-only-%d", suffix)
	insertTestCapability(t, pool, installationID, lookupOnlyCap,
		`{"metadata":{"lookup_provider_ids":["imdb"],"default_priority":{"movie":9}}}`)

	chainRepo := NewChainRepository(pool)
	// A library with no chain of its own runs the default chain, which
	// includes both capabilities.
	defaultFolder := insertTestFolder(t, pool, "movies")
	// A library whose explicit chain leaves the bulk capability out.
	excludedFolder := insertTestFolder(t, pool, "movies")
	if err := chainRepo.SetChain(ctx, excludedFolder, []ChainEntry{
		{PluginInstallationID: installationID, CapabilityID: lookupOnlyCap, ContentLevel: "movie", Priority: 0, Enabled: true},
	}); err != nil {
		t.Fatalf("set chain: %v", err)
	}

	s := &MetadataService{
		chainRepo:      chainRepo,
		pluginResolver: stubMetadataResolver{},
		dbPool:         pool,
		chainCache:     make(map[string]chainCacheEntry),
		chainCacheTTL:  time.Minute,
	}
	targets, err := s.bulkEnrichmentTargets(ctx)
	if err != nil {
		t.Fatalf("bulkEnrichmentTargets() error = %v", err)
	}
	var target *bulkEnrichmentTarget
	for i := range targets {
		if targets[i].provider.Slug() == lookupOnlyCap {
			t.Fatalf("a capability without bulk_lookup_limit became a target")
		}
		if targets[i].provider.Slug() == bulkCap {
			target = &targets[i]
		}
	}
	if target == nil {
		t.Fatalf("bulk capability %s is not a target: %+v", bulkCap, targets)
	}
	if target.limit != 100 || !slices.Equal(target.lookupKeys, []string{"imdb", "tmdb"}) {
		t.Fatalf("target = limit %d keys %v, want 100 [imdb tmdb]", target.limit, target.lookupKeys)
	}
	if !slices.Contains(target.movieFolders, defaultFolder) || slices.Contains(target.movieFolders, excludedFolder) {
		t.Fatalf("movie folders = %v, want %d in and %d out", target.movieFolders, defaultFolder, excludedFolder)
	}
	if !slices.Contains(target.seriesFolders, defaultFolder) {
		t.Fatalf("series folders = %v, want the default-chain folder %d", target.seriesFolders, defaultFolder)
	}
}

func TestEnrichmentStateRepositoryPostgres(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	provider := fmt.Sprintf("test-bulk-%d", suffix)
	movieFolder := insertTestFolder(t, pool, "movies")
	seriesFolder := insertTestFolder(t, pool, "tv")
	otherFolder := insertTestFolder(t, pool, "movies")

	id := func(name string) string { return fmt.Sprintf("enrich-%s-%d", name, suffix) }
	var ids []string
	seed := func(name, itemType, status string, folder int, providerIDs map[string]string) string {
		t.Helper()
		contentID := id(name)
		ids = append(ids, contentID)
		execTest(t, pool, `INSERT INTO media_items (content_id, type, title, genres, status, default_metadata_language)
			VALUES ($1, $2, $1, '{}'::text[], $3, 'de')`, contentID, itemType, status)
		execTest(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, contentID, folder)
		for key, value := range providerIDs {
			execTest(t, pool, `INSERT INTO media_item_provider_ids (content_id, provider, provider_id, item_type) VALUES ($1, $2, $3, $4)`,
				contentID, key, fmt.Sprintf("%s-%d", value, suffix), itemType)
		}
		return contentID
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, ids)
	})

	// Candidates, in content ID order.
	movie := seed("a-movie", "movie", "matched", movieFolder, map[string]string{"imdb": "tt", "tvdb": "x"})
	series := seed("b-series", "series", "matched", seriesFolder, map[string]string{"tmdb": "1"})
	dueEmpty := seed("c-due-empty", "movie", "matched", movieFolder, map[string]string{"tmdb": "2"})
	// Not candidates.
	seed("d-no-lookup-id", "movie", "matched", movieFolder, map[string]string{"tvdb": "3"})
	seed("e-unmatched", "movie", "unmatched", movieFolder, map[string]string{"tmdb": "4"})
	seed("f-other-library", "movie", "matched", otherFolder, map[string]string{"tmdb": "5"})
	seed("g-series-in-movie-library", "series", "matched", movieFolder, map[string]string{"tmdb": "6"})
	found := seed("h-found", "movie", "matched", movieFolder, map[string]string{"tmdb": "7"})
	waiting := seed("i-waiting", "movie", "matched", movieFolder, map[string]string{"tmdb": "8"})

	repo := newEnrichmentStateRepository(pool)
	past, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	if err := repo.RecordOutcomes(ctx, provider, []enrichmentOutcome{
		{ContentID: dueEmpty, Outcome: enrichmentOutcomeEmpty, NextCheckAt: &past},
		{ContentID: waiting, Outcome: enrichmentOutcomeFailed, NextCheckAt: &future},
		// Deleted since selection: skipped, not an error.
		{ContentID: id("deleted"), Outcome: enrichmentOutcomeEmpty, NextCheckAt: &future},
	}); err != nil {
		t.Fatalf("RecordOutcomes() error = %v", err)
	}
	if err := repo.RecordFound(ctx, found, []string{provider}); err != nil {
		t.Fatalf("RecordFound() error = %v", err)
	}
	// Another provider's answers do not settle the item for this one.
	if err := repo.RecordFound(ctx, movie, []string{provider + "-other"}); err != nil {
		t.Fatalf("RecordFound(other) error = %v", err)
	}

	query := enrichmentCandidateQuery{
		Provider:      provider,
		LookupKeys:    []string{"imdb", "tmdb"},
		MovieFolders:  []int{movieFolder},
		SeriesFolders: []int{seriesFolder},
		Limit:         10,
	}
	got, err := repo.Candidates(ctx, query)
	if err != nil {
		t.Fatalf("Candidates() error = %v", err)
	}
	gotIDs := make([]string, 0, len(got))
	for _, candidate := range got {
		gotIDs = append(gotIDs, candidate.ContentID)
	}
	if want := []string{movie, series, dueEmpty}; !slices.Equal(gotIDs, want) {
		t.Fatalf("Candidates() = %v, want %v", gotIDs, want)
	}
	if first := got[0]; first.Type != "movie" || first.Language != "de" ||
		first.ProviderIDs["imdb"] != fmt.Sprintf("tt-%d", suffix) || first.ProviderIDs["tvdb"] != fmt.Sprintf("x-%d", suffix) {
		t.Fatalf("first candidate = %+v", first)
	}
	if count, err := repo.CountCandidates(ctx, query); err != nil || count != 3 {
		t.Fatalf("CountCandidates() = %d, %v; want 3", count, err)
	}

	// Paging continues after the last content ID.
	query.After, query.Limit = movie, 1
	if page, err := repo.Candidates(ctx, query); err != nil || len(page) != 1 || page[0].ContentID != series {
		t.Fatalf("second page = %+v, %v; want [%s]", page, err, series)
	}

	// Recording found settles an item for good.
	if err := repo.RecordOutcomes(ctx, provider, []enrichmentOutcome{{ContentID: dueEmpty, Outcome: enrichmentOutcomeFound}}); err != nil {
		t.Fatalf("RecordOutcomes(found) error = %v", err)
	}
	if count, err := repo.CountCandidates(ctx, enrichmentCandidateQuery{
		Provider: provider, LookupKeys: query.LookupKeys, MovieFolders: query.MovieFolders, SeriesFolders: query.SeriesFolders,
	}); err != nil || count != 2 {
		t.Fatalf("CountCandidates() after found = %d, %v; want 2", count, err)
	}
}

func execTest(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}
