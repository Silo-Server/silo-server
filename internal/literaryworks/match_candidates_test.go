package literaryworks

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestListMatchCandidatesBranches covers the UNION-branch candidate query:
// title, provider-id, and series matches must each surface candidates through
// their own indexable branch, ignored decisions must exclude, and same-format
// items must never match.
func TestListMatchCandidatesBranches(t *testing.T) {
	ctx := context.Background()
	pool := newLiteraryWorksTestPool(t)
	repo := NewRepository(pool)
	suffix := time.Now().UnixNano()
	folderID := seedLiteraryFolder(t, pool)

	id := func(kind string) string { return fmt.Sprintf("%s-test-%d", kind, suffix) }
	sourceID := id("audio-src")
	titleMatchID := id("ebook-title")
	seriesMatchID := id("ebook-series")
	providerMatchID := id("ebook-provider")
	ignoredID := id("ebook-ignored")
	sameFormatID := id("audio-same")
	unrelatedID := id("ebook-unrelated")

	allIDs := []string{sourceID, titleMatchID, seriesMatchID, providerMatchID, ignoredID, sameFormatID, unrelatedID}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM literary_work_match_decisions WHERE source_content_id = ANY($1) OR target_content_id = ANY($1)`, allIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, allIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	title := fmt.Sprintf("The Left Hand of Testing %d", suffix)
	seedLiteraryMediaItem(t, pool, sourceID, FormatAudiobook, title, folderID)
	seedLiteraryMediaItem(t, pool, titleMatchID, FormatEbook, title, folderID)
	seedLiteraryMediaItem(t, pool, seriesMatchID, FormatEbook, "Different Series Title", folderID)
	seedLiteraryMediaItem(t, pool, providerMatchID, FormatEbook, "Different Provider Title", folderID)
	seedLiteraryMediaItem(t, pool, ignoredID, FormatEbook, title, folderID)
	seedLiteraryMediaItem(t, pool, sameFormatID, FormatAudiobook, title, folderID)
	seedLiteraryMediaItem(t, pool, unrelatedID, FormatEbook, "Completely Unrelated", folderID)

	seriesName := fmt.Sprintf("Testing Cycle %d", suffix)
	if _, err := pool.Exec(ctx, `
		INSERT INTO ebook_series (content_id, series_name, series_index)
		VALUES ($1, $2, 2)
	`, seriesMatchID, seriesName); err != nil {
		t.Fatalf("seed ebook series: %v", err)
	}
	providerID := fmt.Sprintf("gr-%d", suffix)
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_provider_ids (content_id, provider, provider_id, item_type)
		VALUES ($1, 'goodreads', $2, 'ebook')
	`, providerMatchID, providerID); err != nil {
		t.Fatalf("seed provider id: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO literary_work_match_decisions (source_content_id, target_content_id, decision)
		VALUES ($1, $2, 'ignored')
	`, ignoredID, sourceID); err != nil {
		t.Fatalf("seed ignored decision: %v", err)
	}

	seriesIndex := 2.0
	source := MatchItem{
		ContentID:   sourceID,
		Type:        FormatAudiobook,
		Title:       title,
		SeriesName:  seriesName,
		SeriesIndex: &seriesIndex,
		ExternalIDs: map[string]string{"goodreads": providerID},
	}

	candidates, err := repo.ListMatchCandidates(ctx, source, 50)
	if err != nil {
		t.Fatalf("ListMatchCandidates: %v", err)
	}
	got := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		got[c.ContentID] = true
	}

	for _, want := range []struct {
		id     string
		branch string
	}{
		{titleMatchID, "title"},
		{seriesMatchID, "series"},
		{providerMatchID, "provider"},
	} {
		if !got[want.id] {
			t.Errorf("%s branch: candidate %s missing from %v", want.branch, want.id, candidates)
		}
	}
	for _, exclude := range []struct {
		id     string
		reason string
	}{
		{ignoredID, "ignored decision"},
		{sameFormatID, "same format as source"},
		{sourceID, "source itself"},
		{unrelatedID, "no matching predicate"},
	} {
		if got[exclude.id] {
			t.Errorf("%s must be excluded (%s)", exclude.id, exclude.reason)
		}
	}
}

// TestListMatchCandidatesNoPredicatesFallsBack preserves the pre-UNION
// behavior: a source without title/provider/series still lists every
// opposite-format item as a candidate.
func TestListMatchCandidatesNoPredicatesFallsBack(t *testing.T) {
	ctx := context.Background()
	pool := newLiteraryWorksTestPool(t)
	repo := NewRepository(pool)
	suffix := time.Now().UnixNano()
	folderID := seedLiteraryFolder(t, pool)

	sourceID := fmt.Sprintf("audio-nofilter-%d", suffix)
	ebookID := fmt.Sprintf("ebook-nofilter-%d", suffix)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{sourceID, ebookID})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	seedLiteraryMediaItem(t, pool, sourceID, FormatAudiobook, "Untitled Source", folderID)
	seedLiteraryMediaItem(t, pool, ebookID, FormatEbook, "Some Ebook", folderID)

	ids, err := repo.listMatchCandidateIDs(ctx, MatchItem{ContentID: sourceID, Type: FormatAudiobook}, 1000000)
	if err != nil {
		t.Fatalf("listMatchCandidateIDs: %v", err)
	}
	found := false
	for _, id := range ids {
		if id == ebookID {
			found = true
		}
		if id == sourceID {
			t.Fatal("source itself must not be a candidate")
		}
	}
	if !found {
		t.Fatalf("fallback branch: %s missing from %d candidates", ebookID, len(ids))
	}
}
