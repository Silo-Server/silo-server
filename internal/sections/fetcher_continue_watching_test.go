package sections

import (
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestCollapseContinueWatchingSeriesCandidatesPrefersNewestInProgressEpisode(t *testing.T) {
	t.Parallel()

	seriesID := "series-8"
	importedAt := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	localAt := time.Date(2025, 1, 2, 12, 0, 0, 0, time.UTC)
	nextUpAt := time.Date(2025, 1, 3, 12, 0, 0, 0, time.UTC)

	items := []*models.MediaItem{
		{ContentID: "movie-1", Type: "movie", Title: "Movie One"},
		{ContentID: "ep-s8e6", Type: "episode", Title: "Imported partial"},
		{ContentID: "ep-s8e11", Type: "episode", Title: "Local partial"},
		{ContentID: "ep-s8e12", Type: "episode", Title: "Next up"},
		{ContentID: "movie-2", Type: "movie", Title: "Movie Two"},
	}
	meta := map[string]SectionItemMeta{
		"ep-s8e6": {
			SeriesID:      &seriesID,
			SeasonNumber:  intPtr(8),
			EpisodeNumber: intPtr(6),
			ItemSource:    "in_progress",
			SortTimestamp: importedAt,
		},
		"ep-s8e11": {
			SeriesID:      &seriesID,
			SeasonNumber:  intPtr(8),
			EpisodeNumber: intPtr(11),
			ItemSource:    "in_progress",
			SortTimestamp: localAt,
		},
		"ep-s8e12": {
			SeriesID:      &seriesID,
			SeasonNumber:  intPtr(8),
			EpisodeNumber: intPtr(12),
			ItemSource:    "next_up",
			SortTimestamp: nextUpAt,
		},
	}

	collapsed := collapseContinueWatchingSeriesCandidates(items, meta)

	gotIDs := contentIDs(collapsed)
	wantIDs := []string{"movie-1", "ep-s8e11", "movie-2"}
	if len(gotIDs) != len(wantIDs) {
		t.Fatalf("collapsed IDs = %v, want %v", gotIDs, wantIDs)
	}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("collapsed IDs = %v, want %v", gotIDs, wantIDs)
		}
	}
}

func TestCollapseContinueWatchingSeriesCandidatesKeepsNextUpWhenNoInProgress(t *testing.T) {
	t.Parallel()

	seriesID := "series-8"
	items := []*models.MediaItem{
		{ContentID: "ep-s8e12", Type: "episode", Title: "Next up"},
	}
	meta := map[string]SectionItemMeta{
		"ep-s8e12": {
			SeriesID:      &seriesID,
			SeasonNumber:  intPtr(8),
			EpisodeNumber: intPtr(12),
			ItemSource:    "next_up",
			SortTimestamp: time.Date(2025, 1, 3, 12, 0, 0, 0, time.UTC),
		},
	}

	collapsed := collapseContinueWatchingSeriesCandidates(items, meta)

	if len(collapsed) != 1 || collapsed[0].ContentID != "ep-s8e12" {
		t.Fatalf("collapsed = %v, want only ep-s8e12", contentIDs(collapsed))
	}
}

func TestMatchesContinueWatchingFilterIncludesAudiobooks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		filterType string
		itemType   string
		want       bool
	}{
		{name: "movie keeps movie", filterType: "movie", itemType: "movie", want: true},
		{name: "movie keeps episode through watching type", filterType: "movie", itemType: "episode", want: true},
		{name: "series keeps episode", filterType: "series", itemType: "episode", want: true},
		{name: "audiobook keeps audiobook", filterType: "audiobook", itemType: "audiobook", want: true},
		{name: "audiobook rejects movie", filterType: "audiobook", itemType: "movie", want: false},
		{name: "ebook keeps ebook", filterType: "ebook", itemType: "ebook", want: true},
		{name: "unknown passes through audiobook", filterType: "unknown", itemType: "audiobook", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesContinueWatchingFilter(tt.filterType, tt.itemType); got != tt.want {
				t.Fatalf("matchesContinueWatchingFilter(%q, %q) = %v, want %v", tt.filterType, tt.itemType, got, tt.want)
			}
		})
	}
}

func TestParseContinueType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config string
		want   ContinueType
	}{
		{name: "missing defaults watching", config: `{}`, want: ContinueTypeWatching},
		{name: "explicit watching", config: `{"continue_type":"watching"}`, want: ContinueTypeWatching},
		{name: "explicit listening", config: `{"continue_type":"listening"}`, want: ContinueTypeListening},
		{name: "legacy audiobook filter", config: `{"filter_type":"audiobook"}`, want: ContinueTypeListening},
		{name: "legacy audiobook media scope", config: `{"media_scope":"audiobook"}`, want: ContinueTypeListening},
		{name: "future reading", config: `{"continue_type":"reading"}`, want: ContinueTypeReading},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseContinueType([]byte(tt.config))
			if err != nil {
				t.Fatalf("ParseContinueType(%s): %v", tt.config, err)
			}
			if got != tt.want {
				t.Fatalf("ParseContinueType(%s) = %q, want %q", tt.config, got, tt.want)
			}
		})
	}
}

func TestParseContinueTypeRejectsUnknownExplicitType(t *testing.T) {
	t.Parallel()

	if _, err := ParseContinueType([]byte(`{"continue_type":"scrolling"}`)); err == nil {
		t.Fatal("ParseContinueType accepted unknown continue_type")
	}
}

func TestEbookContinueWatchingProgressQueryGatesHiddenHistory(t *testing.T) {
	t.Parallel()

	query := strings.Join(strings.Fields(ebookContinueWatchingProgressQuery), " ")

	// Continue Reading must honor user_history_hidden_items with the same
	// semantics as the video path (pgstore ListProgress "in_progress"): a row
	// is hidden while updated_at <= hidden_before and counts again once new
	// reading activity lands after hidden_before.
	for _, term := range []string{
		"FROM ebook_reader_progress",
		"NOT EXISTS",
		"FROM user_history_hidden_items hhi",
		"hhi.media_item_id = ebook_reader_progress.content_id",
		"ebook_reader_progress.updated_at <= hhi.hidden_before",
	} {
		if !strings.Contains(query, term) {
			t.Fatalf("ebook continue watching query missing %q:\n%s", term, query)
		}
	}
}

func TestAppendUniqueContinueItemsDedupesAcrossPages(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{})
	limit := 4

	pageOne := []*models.MediaItem{
		{ContentID: "book-a"},
		{ContentID: "book-b"},
	}
	ordered := appendUniqueContinueItems(nil, pageOne, seen, limit)

	// A progress write between pages shifts rows across the LIMIT/OFFSET
	// boundary, so page two re-serves book-b.
	pageTwo := []*models.MediaItem{
		{ContentID: "book-b"},
		{ContentID: "book-c"},
	}
	ordered = appendUniqueContinueItems(ordered, pageTwo, seen, limit)

	got := contentIDs(ordered)
	want := []string{"book-a", "book-b", "book-c"}
	if len(got) != len(want) {
		t.Fatalf("ordered IDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ordered IDs = %v, want %v", got, want)
		}
	}
}

func TestAppendUniqueContinueItemsRespectsLimit(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{})
	page := []*models.MediaItem{
		{ContentID: "a"},
		{ContentID: "b"},
		{ContentID: "c"},
	}

	ordered := appendUniqueContinueItems(nil, page, seen, 2)
	if got := contentIDs(ordered); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("ordered IDs = %v, want [a b]", got)
	}
}

func contentIDs(items []*models.MediaItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ContentID)
	}
	return ids
}

func intPtr(v int) *int {
	return &v
}
