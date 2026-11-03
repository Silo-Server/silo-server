package metadata

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestIdentityProviderIDsContradict(t *testing.T) {
	for _, tc := range []struct {
		name     string
		itemType string
		a, b     map[string]string
		want     bool
	}{
		{"different TMDB, shared TVDB (#2263)", "movie",
			map[string]string{"tmdb": "125249", "imdb": "tt0035665", "tvdb": "66484"},
			map[string]string{"tmdb": "43506", "imdb": "tt0035664", "tvdb": "66484"}, true},
		{"different IMDb, shared TVDB, one TMDB", "movie",
			map[string]string{"imdb": "tt0035665", "tvdb": "66484"},
			map[string]string{"tmdb": "43506", "imdb": "tt0035664", "tvdb": "66484"}, true},
		{"same IMDb, different TVDB", "movie",
			map[string]string{"imdb": "tt1", "tvdb": "7"},
			map[string]string{"imdb": "tt1", "tvdb": "8"}, false},
		{"same TMDB, remapped TVDB", "movie",
			map[string]string{"tmdb": "949", "tvdb": "2002"},
			map[string]string{"tmdb": "949", "tvdb": "1001"}, false},
		{"only TVDB shared and different", "movie",
			map[string]string{"tvdb": "1", "tmdb": "5"},
			map[string]string{"tvdb": "2", "imdb": "tt1"}, true},
		{"only IMDb shared and equal", "movie",
			map[string]string{"tmdb": "1", "imdb": "tt1"},
			map[string]string{"tvdb": "9", "imdb": "tt1"}, false},
		{"TMDB in its slug URL form", "movie",
			map[string]string{"tmdb": "1931", "imdb": "tt1"},
			map[string]string{"tmdb": "1931-disney-s-adventures-of-the-gummi-bears", "imdb": "tt2"}, false},
		{"different TMDB, one in slug form", "movie",
			map[string]string{"tmdb": "19310"},
			map[string]string{"tmdb": "1931-disney-s-adventures-of-the-gummi-bears"}, true},
		{"nothing shared", "movie", map[string]string{"tmdb": "1"}, map[string]string{"tvdb": "2"}, false},
		{"series: same TVDB, remapped TMDB", "series",
			map[string]string{"tvdb": "368611", "tmdb": "92749", "imdb": "tt10234724"},
			map[string]string{"tvdb": "368611", "tmdb": "92750", "imdb": "tt10234724"}, false},
		{"series: different TVDB, shared TMDB", "series",
			map[string]string{"tvdb": "1", "tmdb": "5"},
			map[string]string{"tvdb": "2", "tmdb": "5"}, true},
		{"series: no TVDB on one side, different TMDB", "series",
			map[string]string{"tmdb": "5", "imdb": "tt1"},
			map[string]string{"tvdb": "2", "tmdb": "6", "imdb": "tt1"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := identityProviderIDsContradict(tc.itemType, tc.a, tc.b); got != tc.want {
				t.Fatalf("identityProviderIDsContradict = %v, want %v", got, tc.want)
			}
		})
	}
}

// A series anchors on TVDB, so a copy matched with a corrected TMDB mapping
// still finds the series it shares a TVDB ID with, even one first anchored on
// TMDB. A movie with the same IDs is a different title.
func TestFindExistingByProviderIDsDecidesSeriesOnTVDB(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	providerRepo := newFakeProviderIDRepo()
	for _, item := range []*models.MediaItem{
		{ContentID: "series-tmdb-92749", Type: "series", Title: "Moon Knight", Status: "matched"},
		{ContentID: "movie-tmdb-92749", Type: "movie", Title: "Moon Knight", Status: "matched"},
	} {
		item.Studios, item.Networks, item.Countries, item.Genres = []string{}, []string{}, []string{}, []string{}
		if err := h.itemRepo.Upsert(ctx, item); err != nil {
			t.Fatalf("upsert %s: %v", item.ContentID, err)
		}
		providerRepo.set(item.ContentID,
			&models.MediaItemProviderID{ContentID: item.ContentID, ItemType: item.Type, Provider: "tmdb", ProviderID: "92750"},
			&models.MediaItemProviderID{ContentID: item.ContentID, ItemType: item.Type, Provider: "tvdb", ProviderID: "368611"})
	}
	h.service.providerIDRepo = providerRepo
	ids := map[string]string{"tvdb": "368611", "tmdb": "92749"}

	series, err := h.service.findExistingByProviderIDs(ctx, ids, "series", "")
	if err != nil {
		t.Fatalf("findExistingByProviderIDs series: %v", err)
	}
	if series == nil || series.ContentID != "series-tmdb-92749" {
		t.Fatalf("series owner = %#v, want series-tmdb-92749", series)
	}
	movie, err := h.service.findExistingByProviderIDs(ctx, ids, "movie", "")
	if err != nil {
		t.Fatalf("findExistingByProviderIDs movie: %v", err)
	}
	if movie != nil {
		t.Fatalf("movie owner = %s, want none: its TMDB ID differs", movie.ContentID)
	}
}

// The scanner's tag lookup can hit a different series first (TMDB before
// TVDB) and refuse it. The matched series at the tags' own content ID is then
// linked, not reset to a provisional skeleton.
func TestCreateOrFindSkeleton_RefusedTagHitKeepsMatchedAnchor(t *testing.T) {
	h := newTestHarness()
	ctx := t.Context()
	for _, item := range []*models.MediaItem{
		{ContentID: "series-tvdb-1", Type: "series", Title: "Neighbor Show", Status: "matched", TmdbID: "5", TvdbID: "1"},
		{ContentID: "series-tvdb-2", Type: "series", Title: "Example Show", Status: "matched", TmdbID: "6"},
	} {
		item.Studios, item.Networks, item.Countries, item.Genres = []string{}, []string{}, []string{}, []string{}
		if err := h.itemRepo.Upsert(ctx, item); err != nil {
			t.Fatalf("upsert %s: %v", item.ContentID, err)
		}
	}
	root := "/tv/Example Show [tmdbid-5][tvdbid-2]"
	file := &models.MediaFile{
		ID: 1, MediaFolderID: 10, FilePath: root + "/Season 01/Example Show - S01E01.mkv",
		BaseTitle: "Example Show", BaseType: "series", ObservedRootPath: root, CanonicalRootPath: root,
	}

	skeleton, err := h.service.createOrFindSkeleton(ctx, file, 10, "/tv")
	if err != nil {
		t.Fatal(err)
	}
	if skeleton.ContentID != "series-tvdb-2" || skeleton.IsNew {
		t.Fatalf("skeleton = %s (new %v), want the existing series-tvdb-2", skeleton.ContentID, skeleton.IsNew)
	}
	item, err := h.itemRepo.GetByID(ctx, "series-tvdb-2")
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "matched" || item.Title != "Example Show" {
		t.Fatalf("series-tvdb-2 is %q %q, want the matched Example Show", item.Status, item.Title)
	}
}
