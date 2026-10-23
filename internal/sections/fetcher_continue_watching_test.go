package sections

import (
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestCollapseContinueWatchingSeriesCandidates(t *testing.T) {
	t.Parallel()

	seriesID := "series-8"
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	episode := func(number int, source string, at time.Time) SectionItemMeta {
		return SectionItemMeta{SeriesID: &seriesID, SeasonNumber: intPtr(8), EpisodeNumber: intPtr(number), ItemSource: source, SortTimestamp: at}
	}

	tests := []struct {
		name  string
		items []string
		meta  map[string]SectionItemMeta
		want  []string
	}{
		{
			name:  "newest in-progress episode wins over next up, movies kept",
			items: []string{"movie-1", "ep-s8e6", "ep-s8e11", "ep-s8e12", "movie-2"},
			meta: map[string]SectionItemMeta{
				"ep-s8e6":  episode(6, "in_progress", base),
				"ep-s8e11": episode(11, "in_progress", base.Add(24*time.Hour)),
				"ep-s8e12": episode(12, "next_up", base.Add(48*time.Hour)),
			},
			want: []string{"movie-1", "ep-s8e11", "movie-2"},
		},
		{
			name:  "next up kept when the series has nothing in progress",
			items: []string{"ep-s8e12"},
			meta:  map[string]SectionItemMeta{"ep-s8e12": episode(12, "next_up", base)},
			want:  []string{"ep-s8e12"},
		},
		{
			name:  "equal progress timestamps prefer the later episode",
			items: []string{"ep-s8e3", "ep-s8e5"},
			meta: map[string]SectionItemMeta{
				"ep-s8e3": episode(3, "in_progress", base),
				"ep-s8e5": episode(5, "in_progress", base),
			},
			want: []string{"ep-s8e5"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := make([]*models.MediaItem, 0, len(tt.items))
			for _, id := range tt.items {
				items = append(items, &models.MediaItem{ContentID: id})
			}
			got := contentIDs(collapseContinueWatchingSeriesCandidates(items, tt.meta))
			if !slices.Equal(got, tt.want) {
				t.Fatalf("collapsed IDs = %v, want %v", got, tt.want)
			}
		})
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

func TestAppendContinuePageDedupesAcrossPages(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{})
	limit := 4

	pageOne := []*models.MediaItem{
		{ContentID: "book-a"},
		{ContentID: "book-b"},
	}
	ordered := appendContinuePage(nil, pageOne, nil, seen, limit, false)

	// A progress write between pages shifts rows across the LIMIT/OFFSET
	// boundary, so page two re-serves book-b.
	pageTwo := []*models.MediaItem{
		{ContentID: "book-b"},
		{ContentID: "book-c"},
	}
	ordered = appendContinuePage(ordered, pageTwo, nil, seen, limit, false)

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

// TestAppendContinuePageCollapsesBeforeTrimming pins that a series' second
// in-progress episode does not take a row slot: the page's next card fills it,
// rather than the slot going unfilled or to an older card from a later page.
func TestAppendContinuePageCollapsesBeforeTrimming(t *testing.T) {
	t.Parallel()

	seriesID := "series-1"
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	page := []*models.MediaItem{
		{ContentID: "ep-2", Type: "episode"},
		{ContentID: "ep-1", Type: "episode"},
		{ContentID: "movie-1", Type: "movie"},
		{ContentID: "movie-2", Type: "movie"},
	}
	meta := map[string]SectionItemMeta{
		"ep-2":    {SeriesID: &seriesID, SeasonNumber: intPtr(1), EpisodeNumber: intPtr(2), ItemSource: "in_progress", SortTimestamp: base},
		"ep-1":    {SeriesID: &seriesID, SeasonNumber: intPtr(1), EpisodeNumber: intPtr(1), ItemSource: "in_progress", SortTimestamp: base.Add(-time.Hour)},
		"movie-1": {ItemSource: "in_progress", SortTimestamp: base.Add(-2 * time.Hour)},
		"movie-2": {ItemSource: "in_progress", SortTimestamp: base.Add(-3 * time.Hour)},
	}

	ordered := appendContinuePage(nil, page, meta, make(map[string]struct{}), 2, true)
	if got := contentIDs(ordered); len(got) != 2 || got[0] != "ep-2" || got[1] != "movie-1" {
		t.Fatalf("ordered IDs = %v, want [ep-2 movie-1]", got)
	}
}

func TestContinueNextUpCutoff(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	items := []*models.MediaItem{{ContentID: "a"}, {ContentID: "b"}, {ContentID: "c"}}
	meta := map[string]SectionItemMeta{
		"a": {SortTimestamp: base},
		"b": {SortTimestamp: base.Add(-2 * time.Hour)},
		"c": {SortTimestamp: base.Add(-time.Hour)},
	}

	if got := continueNextUpCutoff(items, meta, 4); got != nil {
		t.Fatalf("cutoff for a row short of its limit = %v, want nil", got)
	}
	got := continueNextUpCutoff(items, meta, 3)
	if got == nil || !got.Equal(base.Add(-2*time.Hour)) {
		t.Fatalf("cutoff for a full row = %v, want the oldest card %v", got, base.Add(-2*time.Hour))
	}

	// A card without a timestamp sorts last, so a next-up card older than
	// every other card could still outrank it.
	meta["c"] = SectionItemMeta{}
	if got := continueNextUpCutoff(items, meta, 3); got != nil {
		t.Fatalf("cutoff with an untimed card = %v, want nil", got)
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
