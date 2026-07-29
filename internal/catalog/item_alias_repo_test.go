package catalog

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	catalogTestContentTypeSeries = "series"
	catalogTestProviderTMDB      = "tmdb"
	catalogTestProviderTVDB      = "tvdb"
)

func TestItemAliasRepositoryReplacesOnlySuccessfulProviderAndCascades(t *testing.T) {
	pool := newSemanticCoverageTestPool(t)
	ctx := context.Background()
	var aliasTable *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.media_item_aliases')::text`).Scan(&aliasTable); err != nil {
		t.Fatalf("check media_item_aliases table: %v", err)
	}
	if aliasTable == nil || *aliasTable == "" {
		t.Skip("test database has not applied media item aliases migration")
	}
	contentID := fmt.Sprintf("alias-repo-%d", time.Now().UnixNano())
	seedSemanticCoverageMediaItem(t, pool, contentID, catalogTestContentTypeSeries, "matched")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID) })
	repo := NewItemAliasRepository(pool)

	if err := repo.ReplaceProvider(ctx, contentID, catalogTestProviderTVDB, []models.MediaItemAlias{
		{Title: "10 Tokyo Warriors", Language: "en", Kind: itemAliasKindAlternate},
	}); err != nil {
		t.Fatalf("seed TVDB aliases: %v", err)
	}
	if err := repo.ReplaceProvider(ctx, contentID, catalogTestProviderTMDB, []models.MediaItemAlias{
		{Title: "Old TMDB Title", Language: "en", Kind: itemAliasKindLocalized},
	}); err != nil {
		t.Fatalf("seed TMDB aliases: %v", err)
	}
	if err := repo.ReplaceProvider(ctx, contentID, catalogTestProviderTMDB, []models.MediaItemAlias{
		{Title: "New TMDB Title", Language: "en", Kind: itemAliasKindLocalized},
		{Title: "New TMDB Title", Language: "en", Kind: itemAliasKindLocalized},
	}); err != nil {
		t.Fatalf("replace TMDB aliases: %v", err)
	}

	aliasesByID, err := repo.ListByContentIDs(ctx, []string{contentID})
	if err != nil {
		t.Fatalf("ListByContentIDs(): %v", err)
	}
	titles := make([]string, 0, len(aliasesByID[contentID]))
	for _, alias := range aliasesByID[contentID] {
		titles = append(titles, alias.Title)
	}
	if !slices.Contains(titles, "10 Tokyo Warriors") || !slices.Contains(titles, "New TMDB Title") || slices.Contains(titles, "Old TMDB Title") {
		t.Fatalf("titles = %v", titles)
	}
	if len(titles) != 2 {
		t.Fatalf("deduplicated titles = %v, want 2", titles)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID); err != nil {
		t.Fatalf("delete media item: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM media_item_aliases WHERE content_id = $1`, contentID).Scan(&count); err != nil {
		t.Fatalf("count cascaded aliases: %v", err)
	}
	if count != 0 {
		t.Fatalf("alias count after item delete = %d, want 0", count)
	}
}

func TestItemAliasRepositoryLanguageRefreshPreservesOtherLibraryLanguages(t *testing.T) {
	pool := newSemanticCoverageTestPool(t)
	ctx := context.Background()
	var aliasTable *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.media_item_aliases')::text`).Scan(&aliasTable); err != nil {
		t.Fatalf("check media_item_aliases table: %v", err)
	}
	if aliasTable == nil || *aliasTable == "" {
		t.Skip("test database has not applied media item aliases migration")
	}
	contentID := fmt.Sprintf("alias-language-%d", time.Now().UnixNano())
	seedSemanticCoverageMediaItem(t, pool, contentID, catalogTestContentTypeSeries, "matched")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID) })
	repo := NewItemAliasRepository(pool)

	if err := repo.RefreshProviderLanguage(ctx, contentID, catalogTestProviderTVDB, "ja", []models.MediaItemAlias{
		{Title: "日本語", Language: "ja", Kind: itemAliasKindLocalized},
	}, true); err != nil {
		t.Fatalf("seed Japanese aliases: %v", err)
	}
	if err := repo.RefreshProviderLanguage(ctx, contentID, catalogTestProviderTVDB, "en", []models.MediaItemAlias{
		{Title: "Old English", Language: "en", Kind: itemAliasKindLocalized},
		{Title: "Old Untagged", Kind: itemAliasKindAlternate},
	}, true); err != nil {
		t.Fatalf("seed English aliases: %v", err)
	}
	if err := repo.ReplaceProviderLanguage(ctx, contentID, catalogTestProviderTVDB, "en-US", []models.MediaItemAlias{
		{Title: "New English", Language: "en", Kind: itemAliasKindLocalized},
		{Title: "日本語原題", Language: "ja", Kind: itemAliasKindOriginal},
		{Title: "New Untagged", Kind: itemAliasKindAlternate},
	}); err != nil {
		t.Fatalf("refresh English aliases: %v", err)
	}

	aliasesByID, err := repo.ListByContentIDs(ctx, []string{contentID})
	if err != nil {
		t.Fatalf("ListByContentIDs(): %v", err)
	}
	titles := make([]string, 0, len(aliasesByID[contentID]))
	for _, alias := range aliasesByID[contentID] {
		titles = append(titles, alias.Title)
	}
	for _, want := range []string{"New English", "日本語", "日本語原題", "New Untagged"} {
		if !slices.Contains(titles, want) {
			t.Fatalf("titles = %v, want preserved/refreshed %q", titles, want)
		}
	}
	for _, stale := range []string{"Old English", "Old Untagged"} {
		if slices.Contains(titles, stale) {
			t.Fatalf("titles = %v, stale alias %q was not replaced", titles, stale)
		}
	}
}

func TestItemAliasRepositoryCompleteSnapshotReplacesCrossLanguageAliasesInItsScope(t *testing.T) {
	pool := newSemanticCoverageTestPool(t)
	ctx := context.Background()
	contentID := fmt.Sprintf("alias-cross-language-%d", time.Now().UnixNano())
	seedSemanticCoverageMediaItem(t, pool, contentID, catalogTestContentTypeSeries, "matched")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID) })
	repo := NewItemAliasRepository(pool)

	if err := repo.RefreshProviderLanguage(ctx, contentID, catalogTestProviderTVDB, "ja", []models.MediaItemAlias{
		{Title: "日本語ライブラリ", Language: "ja", Kind: itemAliasKindLocalized},
	}, true); err != nil {
		t.Fatalf("seed Japanese snapshot: %v", err)
	}
	if err := repo.RefreshProviderLanguage(ctx, contentID, catalogTestProviderTVDB, "en", []models.MediaItemAlias{
		{Title: "English Title", Language: "en", Kind: itemAliasKindLocalized},
		{Title: "Ancienne française", Language: "fr", Kind: itemAliasKindAlternate},
	}, true); err != nil {
		t.Fatalf("seed multilingual English snapshot: %v", err)
	}
	if err := repo.RefreshProviderLanguage(ctx, contentID, catalogTestProviderTVDB, "en", []models.MediaItemAlias{
		{Title: "Updated English Title", Language: "en", Kind: itemAliasKindLocalized},
	}, true); err != nil {
		t.Fatalf("replace English snapshot: %v", err)
	}

	aliasesByID, err := repo.ListByContentIDs(ctx, []string{contentID})
	if err != nil {
		t.Fatalf("ListByContentIDs(): %v", err)
	}
	titles := make([]string, 0, len(aliasesByID[contentID]))
	for _, alias := range aliasesByID[contentID] {
		titles = append(titles, alias.Title)
	}
	for _, want := range []string{"日本語ライブラリ", "Updated English Title"} {
		if !slices.Contains(titles, want) {
			t.Fatalf("titles = %v, want %q", titles, want)
		}
	}
	for _, stale := range []string{"English Title", "Ancienne française"} {
		if slices.Contains(titles, stale) {
			t.Fatalf("titles = %v, stale cross-language alias %q survived", titles, stale)
		}
	}
}

func TestItemAliasRepositoryListDeduplicatesAliasesAcrossSnapshotScopes(t *testing.T) {
	pool := newSemanticCoverageTestPool(t)
	ctx := context.Background()
	contentID := fmt.Sprintf("alias-snapshot-dedup-%d", time.Now().UnixNano())
	seedSemanticCoverageMediaItem(t, pool, contentID, catalogTestContentTypeSeries, "matched")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID) })
	repo := NewItemAliasRepository(pool)

	if err := repo.RefreshProviderLanguage(ctx, contentID, catalogTestProviderTVDB, "en", []models.MediaItemAlias{
		{Title: "The Office", Language: "en", Kind: itemAliasKindAlternate},
	}, true); err != nil {
		t.Fatalf("seed English snapshot: %v", err)
	}
	if err := repo.RefreshProviderLanguage(ctx, contentID, catalogTestProviderTVDB, "ja", []models.MediaItemAlias{
		{Title: "the office", Language: "en", Kind: itemAliasKindAlternate},
	}, true); err != nil {
		t.Fatalf("seed Japanese snapshot: %v", err)
	}

	aliasesByID, err := repo.ListByContentIDs(ctx, []string{contentID})
	if err != nil {
		t.Fatalf("ListByContentIDs(): %v", err)
	}
	aliases := aliasesByID[contentID]
	if len(aliases) != 1 {
		t.Fatalf("aliases = %#v, want one normalized alias", aliases)
	}
	if aliases[0].Title != "the office" {
		t.Fatalf("alias title = %q, want newest spelling", aliases[0].Title)
	}
}

func TestItemAliasRepositoryScopedRefreshAdoptsLegacyWithoutErasingProviderWideSnapshot(t *testing.T) {
	pool := newSemanticCoverageTestPool(t)
	ctx := context.Background()
	contentID := fmt.Sprintf("alias-scope-provenance-%d", time.Now().UnixNano())
	seedSemanticCoverageMediaItem(t, pool, contentID, "movie", "matched")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID) })
	repo := NewItemAliasRepository(pool)

	if err := repo.ReplaceProvider(ctx, contentID, catalogTestProviderTMDB, []models.MediaItemAlias{
		{Title: "Provider-wide title", Kind: itemAliasKindAlternate},
	}); err != nil {
		t.Fatalf("seed provider-wide snapshot: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_aliases (
			content_id, title, language, kind, provider, snapshot_language
		) VALUES
			($1, 'Legacy unscoped title', '', 'alternate', $2, NULL),
			($1, '従来の日本語タイトル', 'ja', 'localized', $2, NULL)
	`, contentID, catalogTestProviderTMDB); err != nil {
		t.Fatalf("seed legacy alias: %v", err)
	}
	if err := repo.RefreshProviderLanguage(ctx, contentID, catalogTestProviderTMDB, "en", []models.MediaItemAlias{
		{Title: "Current English title", Language: "en", Kind: itemAliasKindLocalized},
	}, true); err != nil {
		t.Fatalf("refresh English snapshot: %v", err)
	}

	aliasesByID, err := repo.ListByContentIDs(ctx, []string{contentID})
	if err != nil {
		t.Fatalf("ListByContentIDs(): %v", err)
	}
	titles := make([]string, 0, len(aliasesByID[contentID]))
	for _, alias := range aliasesByID[contentID] {
		titles = append(titles, alias.Title)
	}
	for _, want := range []string{"Provider-wide title", "Current English title", "従来の日本語タイトル"} {
		if !slices.Contains(titles, want) {
			t.Fatalf("titles = %v, want preserved/refreshed %q", titles, want)
		}
	}
	if slices.Contains(titles, "Legacy unscoped title") {
		t.Fatalf("titles = %v, legacy alias was not adopted by complete refresh", titles)
	}
}

func TestItemAliasRepositoryPartialRefreshMergesWithoutErasing(t *testing.T) {
	pool := newSemanticCoverageTestPool(t)
	ctx := context.Background()
	contentID := fmt.Sprintf("alias-partial-%d", time.Now().UnixNano())
	seedSemanticCoverageMediaItem(t, pool, contentID, "movie", "matched")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID) })
	repo := NewItemAliasRepository(pool)

	if err := repo.RefreshProviderLanguage(ctx, contentID, catalogTestProviderTMDB, "en", []models.MediaItemAlias{
		{Title: "Authoritative English", Language: "en", Kind: itemAliasKindLocalized},
		{Title: "Provider Alternate", Language: "en", Kind: itemAliasKindAlternate},
	}, true); err != nil {
		t.Fatalf("seed complete aliases: %v", err)
	}
	if err := repo.RefreshProviderLanguage(ctx, contentID, catalogTestProviderTMDB, "en", []models.MediaItemAlias{
		{Title: "Legacy Primary Title", Language: "en", Kind: itemAliasKindLocalized},
	}, false); err != nil {
		t.Fatalf("merge partial aliases: %v", err)
	}

	aliasesByID, err := repo.ListByContentIDs(ctx, []string{contentID})
	if err != nil {
		t.Fatalf("ListByContentIDs(): %v", err)
	}
	titles := make([]string, 0, len(aliasesByID[contentID]))
	for _, alias := range aliasesByID[contentID] {
		titles = append(titles, alias.Title)
	}
	for _, want := range []string{"Authoritative English", "Provider Alternate", "Legacy Primary Title"} {
		if !slices.Contains(titles, want) {
			t.Fatalf("titles = %v, partial refresh erased %q", titles, want)
		}
	}
}

func TestItemAliasRepositoryEmptySnapshotHonorsCompleteness(t *testing.T) {
	pool := newSemanticCoverageTestPool(t)
	ctx := context.Background()
	contentID := fmt.Sprintf("alias-empty-%d", time.Now().UnixNano())
	seedSemanticCoverageMediaItem(t, pool, contentID, "movie", "matched")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID) })
	repo := NewItemAliasRepository(pool)

	if err := repo.RefreshProviderLanguage(ctx, contentID, catalogTestProviderTMDB, "en", []models.MediaItemAlias{
		{Title: "Stale English", Language: "en", Kind: itemAliasKindLocalized},
	}, true); err != nil {
		t.Fatalf("seed TMDB aliases: %v", err)
	}
	if err := repo.RefreshProviderLanguage(ctx, contentID, catalogTestProviderTVDB, "en", []models.MediaItemAlias{
		{Title: "Keep TVDB", Language: "en", Kind: itemAliasKindAlternate},
	}, true); err != nil {
		t.Fatalf("seed TVDB aliases: %v", err)
	}

	if err := repo.RefreshProviderLanguage(ctx, contentID, catalogTestProviderTMDB, "en", nil, false); err != nil {
		t.Fatalf("merge empty partial snapshot: %v", err)
	}
	aliasesByID, err := repo.ListByContentIDs(ctx, []string{contentID})
	if err != nil {
		t.Fatalf("list aliases after partial snapshot: %v", err)
	}
	if !slices.ContainsFunc(aliasesByID[contentID], func(alias models.MediaItemAlias) bool {
		return alias.Title == "Stale English"
	}) {
		t.Fatalf("partial empty snapshot removed aliases: %+v", aliasesByID[contentID])
	}

	if err := repo.RefreshProviderLanguage(ctx, contentID, catalogTestProviderTMDB, "en", nil, true); err != nil {
		t.Fatalf("replace with empty complete snapshot: %v", err)
	}
	aliasesByID, err = repo.ListByContentIDs(ctx, []string{contentID})
	if err != nil {
		t.Fatalf("list aliases after complete snapshot: %v", err)
	}
	for _, alias := range aliasesByID[contentID] {
		if alias.Provider == catalogTestProviderTMDB {
			t.Fatalf("complete empty snapshot retained TMDB alias: %+v", alias)
		}
	}
	if !slices.ContainsFunc(aliasesByID[contentID], func(alias models.MediaItemAlias) bool {
		return alias.Title == "Keep TVDB" && alias.Provider == catalogTestProviderTVDB
	}) {
		t.Fatalf("complete TMDB snapshot erased another provider: %+v", aliasesByID[contentID])
	}
}

func TestItemAliasRepositoryBackfillResumesAndEnqueuesIndexEvents(t *testing.T) {
	pool := newSemanticCoverageTestPool(t)
	ctx := context.Background()
	var stateTable *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.media_item_alias_backfill_state')::text`).Scan(&stateTable); err != nil {
		t.Fatalf("check alias backfill state table: %v", err)
	}
	if stateTable == nil || *stateTable == "" {
		t.Skip("test database has not applied alias backfill state migration")
	}

	prefix := fmt.Sprintf("zz-alias-backfill-%d-", time.Now().UnixNano())
	firstID, secondID := prefix+"a", prefix+"b"
	for _, contentID := range []string{firstID, secondID} {
		seedSemanticCoverageMediaItem(t, pool, contentID, catalogTestContentTypeSeries, "matched")
		if _, err := pool.Exec(ctx, `
			UPDATE media_items SET original_title = '倒凶十将伝', original_language = 'jpn'
			WHERE content_id = $1
		`, contentID); err != nil {
			t.Fatalf("seed original title: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_item_localizations (content_id, language, title)
			VALUES ($1, 'en_US', '10 Tokyo Warriors')
		`, contentID); err != nil {
			t.Fatalf("seed localization: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM catalog_search_index_events WHERE content_id = ANY($1)`, []string{firstID, secondID})
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{firstID, secondID})
		_, _ = pool.Exec(ctx, `DELETE FROM media_item_alias_backfill_state WHERE task_key = 'media_item_aliases_v1'`)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_alias_backfill_state (task_key, last_content_id, completed)
		VALUES ('media_item_aliases_v1', $1, false)
		ON CONFLICT (task_key) DO UPDATE
		SET last_content_id = EXCLUDED.last_content_id, completed = false
	`, prefix); err != nil {
		t.Fatalf("seed backfill cursor: %v", err)
	}

	repo := NewItemAliasRepository(pool)
	repo.events.WithActiveProvider(SearchProviderMeilisearch)
	firstCursor, processed, err := repo.BackfillBatch(ctx, "", 1)
	if err != nil || processed != 1 || firstCursor != firstID {
		t.Fatalf("first BackfillBatch() = (%q, %d, %v), want (%q, 1, nil)", firstCursor, processed, err, firstID)
	}
	secondCursor, processed, err := repo.BackfillBatch(ctx, "", 1)
	if err != nil || processed != 1 || secondCursor != secondID {
		t.Fatalf("resumed BackfillBatch() = (%q, %d, %v), want (%q, 1, nil)", secondCursor, processed, err, secondID)
	}

	for _, contentID := range []string{firstID, secondID} {
		aliases, err := repo.ListByContentIDs(ctx, []string{contentID})
		if err != nil {
			t.Fatalf("list aliases for %s: %v", contentID, err)
		}
		languages := make([]string, 0, len(aliases[contentID]))
		for _, alias := range aliases[contentID] {
			languages = append(languages, alias.Language)
		}
		if !slices.Contains(languages, "ja") || !slices.Contains(languages, "en") {
			t.Fatalf("alias languages for %s = %v, want canonical ja and en", contentID, languages)
		}
		var eventCount int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM catalog_search_index_events
			WHERE content_id = $1 AND action = 'upsert'
		`, contentID).Scan(&eventCount); err != nil {
			t.Fatalf("count index events for %s: %v", contentID, err)
		}
		if eventCount == 0 {
			t.Fatalf("no search-index upsert event for %s", contentID)
		}
	}
}

func TestItemRepositorySearchesPersistedAliasesAcrossExactFTSAndFuzzyPaths(t *testing.T) {
	pool := newSemanticCoverageTestPool(t)
	ctx := context.Background()
	var aliasTable *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.media_item_aliases')::text`).Scan(&aliasTable); err != nil {
		t.Fatalf("check media_item_aliases table: %v", err)
	}
	if aliasTable == nil || *aliasTable == "" {
		t.Skip("test database has not applied media item aliases migration")
	}
	contentID := fmt.Sprintf("alias-search-%d", time.Now().UnixNano())
	seedSemanticCoverageMediaItem(t, pool, contentID, catalogTestContentTypeSeries, "matched")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID) })
	if _, err := pool.Exec(ctx, `
		UPDATE media_items SET title = '倒凶十将伝', original_title = '倒凶十将伝' WHERE content_id = $1
	`, contentID); err != nil {
		t.Fatalf("seed native title: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_aliases (content_id, title, language, kind, provider)
		VALUES ($1, '10 Tokyo Warriors', 'en', 'alternate', 'tvdb')
	`, contentID); err != nil {
		t.Fatalf("seed searchable alias: %v", err)
	}

	repo := NewItemRepository(pool)
	// The final query deliberately verifies fuzzy matching for a misspelling.
	for _, query := range []string{"10 Tokyo Warriors", "Tokyo Warriors", "10 Tokyo Warriros"} { //nolint:misspell
		items, _, err := repo.Search(ctx, query, []string{catalogTestContentTypeSeries}, 20, 0, AccessFilter{})
		if err != nil {
			t.Fatalf("Search(%q): %v", query, err)
		}
		found := false
		for _, item := range items {
			found = found || item.ContentID == contentID
		}
		if !found {
			t.Fatalf("Search(%q) did not return alias-backed item %s", query, contentID)
		}
	}
}
