package recommendations

import (
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// A built Similar Users row can be empty when too few accounts share the
// profile's taste. Reading it must not queue a rebuild, which could not fill
// it; only a row that was never built asks for one.
func TestSimilarUsersReadRefreshesOnlyAMissingRow(t *testing.T) {
	for _, tc := range []struct {
		name          string
		cached        map[string][]ScoredItem
		wantRefreshes int
	}{
		{name: "built empty", cached: map[string][]ScoredItem{RecTypeSimilarUsersLiked + "|": {}}, wantRefreshes: 0},
		{name: "never built", cached: map[string][]ScoredItem{}, wantRefreshes: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refresher := &countingReadRefresher{}
			r := &Reader{repo: &fakeReaderRepo{personal: tc.cached}, refresh: refresher, signals: NewSignalReader(&fakeSignalRepo{}, nil)}
			items, err := r.GetSimilarUsersLiked(t.Context(), 7, "p1", 20, catalog.AccessFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if items == nil || len(items) != 0 {
				t.Fatalf("items = %#v, want an empty list", items)
			}
			if refresher.calls != tc.wantRefreshes {
				t.Fatalf("refreshes = %d, want %d", refresher.calls, tc.wantRefreshes)
			}
		})
	}
}

// GetForYouPage is the main row followed by the other rows, each filtered for
// the viewer and trimmed to the limit.
func TestGetForYouPageServesTheMainRowFirst(t *testing.T) {
	repo := &fakeReaderRepo{
		meta:     &TasteProfileMeta{SignalCounts: map[string]int{"watch_high": ColdStartFullPersonalized}},
		clusters: []TasteCluster{{ClusterIdx: 0, Label: "Drama"}},
		personal: map[string][]ScoredItem{
			RecTypeForYouMain + "|":                {{MediaItemID: "main-1"}, {MediaItemID: "main-hidden"}, {MediaItemID: "main-2"}, {MediaItemID: "main-3"}},
			RecTypeForYouClusterPrefix + "0" + "|": {{MediaItemID: "drama-hidden"}},
		},
		global: map[string][]ScoredItem{RecTypePopular: {{MediaItemID: "popular"}}},
		hidden: map[string]struct{}{"main-hidden": {}, "drama-hidden": {}},
	}
	r := &Reader{repo: repo, signals: NewSignalReader(&fakeSignalRepo{}, nil)}

	rows, err := r.GetForYouPage(t.Context(), 7, "p1", 2, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rowLabels(rows), []string{"For You", "Popular on This Server"}; !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v: the hidden-only Drama row drops out", got, want)
	}
	var main []string
	for _, item := range rows[0].Items {
		main = append(main, item.MediaItemID)
	}
	if want := []string{"main-1", "main-2"}; !slices.Equal(main, want) {
		t.Fatalf("main row = %v, want %v", main, want)
	}
}

// Rows cached before personal rows were kept to recommendableMediaTypes still
// hold books until they are rebuilt. A read serves none of them, and a row
// holding only books drops out.
func TestReadsServeOnlyRecommendableMediaTypes(t *testing.T) {
	repo := &fakeReaderRepo{
		meta:     &TasteProfileMeta{SignalCounts: map[string]int{"watch_high": ColdStartFullPersonalized}},
		clusters: []TasteCluster{{ClusterIdx: 0, Label: "Drama"}},
		personal: map[string][]ScoredItem{
			RecTypeForYouMain + "|":                {{MediaItemID: "audiobook"}, {MediaItemID: "movie"}, {MediaItemID: "ebook"}, {MediaItemID: "series"}},
			RecTypeForYouClusterPrefix + "0" + "|": {{MediaItemID: "audiobook-2"}},
			RecTypeSimilarUsersLiked + "|":         {{MediaItemID: "ebook"}, {MediaItemID: "movie-2"}},
		},
		types: map[string]string{
			"audiobook": "audiobook", "audiobook-2": "audiobook", "ebook": "ebook",
			"movie": "movie", "movie-2": "movie", "series": "series",
		},
	}
	r := &Reader{repo: repo, signals: NewSignalReader(&fakeSignalRepo{}, nil)}

	rows, err := r.GetForYouPage(t.Context(), 7, "p1", 20, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rowLabels(rows), []string{"For You"}; !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v: the audiobook-only Drama row drops out", got, want)
	}
	if got, want := scoredIDs(rows[0].Items), []string{"movie", "series"}; !slices.Equal(got, want) {
		t.Fatalf("main row = %v, want %v", got, want)
	}

	similar, err := r.GetSimilarUsersLiked(t.Context(), 7, "p1", 20, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := scoredIDs(similar), []string{"movie-2"}; !slices.Equal(got, want) {
		t.Fatalf("Similar Users = %v, want %v", got, want)
	}
}
