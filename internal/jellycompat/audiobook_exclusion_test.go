package jellycompat

import (
	"context"
	"net/url"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestCompatScopedTypes(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", compatVideoTypes},
		{"movie", "movie"},
		{"movie,series", "movie,series"},
		{"audiobook", compatNoMatchType},
		{"movie,audiobook", "movie"},
		{" Audiobook ", compatNoMatchType},
		{"podcast", compatNoMatchType},
		{"movie,podcast", "movie"},
		// Non-video types outside the allowlist are dropped, not passed through.
		{"musicalbum", compatNoMatchType},
		{"movie,musicalbum", "movie"},
	}
	for _, tc := range cases {
		if got := compatScopedTypes(tc.in); got != tc.want {
			t.Errorf("compatScopedTypes(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCompatScopedSearchTypes(t *testing.T) {
	if got := compatScopedSearchTypes(nil); len(got) != 3 {
		t.Fatalf("expected video type default, got %v", got)
	}
	got := compatScopedSearchTypes([]string{"movie", "audiobook"})
	if len(got) != 1 || got[0] != "movie" {
		t.Fatalf("expected [movie], got %v", got)
	}
	got = compatScopedSearchTypes([]string{"audiobook"})
	if len(got) != 1 || got[0] != compatNoMatchType {
		t.Fatalf("expected no-match sentinel, got %v", got)
	}
}

func TestIsCompatHiddenLibraryType(t *testing.T) {
	for _, val := range []string{"audiobooks", "audiobook", " Audiobooks ", "podcast", "podcasts"} {
		if !isCompatHiddenLibraryType(val) {
			t.Errorf("expected %q to be hidden from the compat surface", val)
		}
	}
	for _, val := range []string{"movies", "series", "mixed", ""} {
		if isCompatHiddenLibraryType(val) {
			t.Errorf("expected %q to be visible on the compat surface", val)
		}
	}
}

func TestCompatAccessFilterResolverStampsExclusions(t *testing.T) {
	resolved := compatAccessFilterResolver(nil)(context.Background(), 1, "p1")
	if len(resolved.ExcludedMediaTypes) == 0 {
		t.Fatalf("expected exclusions on nil base resolver, got %+v", resolved)
	}

	base := func(context.Context, int, string) catalog.AccessFilter {
		return catalog.AccessFilter{MaxContentRating: "PG-13"}
	}
	resolved = compatAccessFilterResolver(base)(context.Background(), 1, "p1")
	if resolved.MaxContentRating != "PG-13" {
		t.Fatalf("expected base filter fields preserved, got %+v", resolved)
	}
	for _, want := range []string{"audiobook", "podcast"} {
		found := false
		for _, got := range resolved.ExcludedMediaTypes {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected %q in ExcludedMediaTypes, got %v", want, resolved.ExcludedMediaTypes)
		}
	}

	// Pre-existing exclusions from the base resolver are preserved AND the
	// compat exclusions are merged in (not conditionally skipped).
	withExisting := func(context.Context, int, string) catalog.AccessFilter {
		return catalog.AccessFilter{ExcludedMediaTypes: []string{"livetv"}}
	}
	resolved = compatAccessFilterResolver(withExisting)(context.Background(), 1, "p1")
	for _, want := range []string{"livetv", "audiobook", "podcast"} {
		found := false
		for _, got := range resolved.ExcludedMediaTypes {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected %q in merged ExcludedMediaTypes, got %v", want, resolved.ExcludedMediaTypes)
		}
	}
}

// scopeFakeFolderRepo returns a fixed folder list.
type scopeFakeFolderRepo struct {
	folders []*models.MediaFolder
}

func (f *scopeFakeFolderRepo) GetEnabled(context.Context) ([]*models.MediaFolder, error) {
	return f.folders, nil
}

func (f *scopeFakeFolderRepo) ListByIDs(_ context.Context, ids []int) ([]*models.MediaFolder, error) {
	out := make([]*models.MediaFolder, 0, len(ids))
	for _, folder := range f.folders {
		for _, id := range ids {
			if folder.ID == id {
				out = append(out, folder)
			}
		}
	}
	return out, nil
}

func TestListUserLibrariesExcludesABSFolders(t *testing.T) {
	svc := &directContentService{
		folderRepo: &scopeFakeFolderRepo{folders: []*models.MediaFolder{
			{ID: 1, Name: "Movies", Type: "movies", Enabled: true},
			{ID: 2, Name: "Audiobooks", Type: "audiobooks", Enabled: true},
			{ID: 3, Name: "Shows", Type: "series", Enabled: true},
			{ID: 4, Name: "Legacy Books", Type: "audiobook", Enabled: true},
			{ID: 5, Name: "Podcasts", Type: "podcasts", Enabled: true},
		}},
	}

	libraries, err := svc.ListUserLibraries(context.Background(), &Session{StreamAppUserID: 1, ProfileID: "p1"})
	if err != nil {
		t.Fatalf("ListUserLibraries: %v", err)
	}
	if len(libraries) != 2 {
		t.Fatalf("expected 2 libraries, got %d: %+v", len(libraries), libraries)
	}
	for _, lib := range libraries {
		if lib.Name != "Movies" && lib.Name != "Shows" {
			t.Fatalf("ABS-surface library %q leaked into compat views", lib.Name)
		}
	}
}

// scopeFakeBrowseSource captures the filters passed to BrowsePage.
type scopeFakeBrowseSource struct {
	lastFilters catalog.BrowseFilters
}

func (f *scopeFakeBrowseSource) BrowsePage(_ context.Context, filters catalog.BrowseFilters, _ bool) (*catalog.BrowseResult, error) {
	f.lastFilters = filters
	return &catalog.BrowseResult{Items: []*models.MediaItem{}, Total: 0, HasMore: false}, nil
}

func (f *scopeFakeBrowseSource) ListGenres(_ context.Context, filters catalog.BrowseFilters) ([]string, error) {
	f.lastFilters = filters
	return []string{}, nil
}

func TestBrowseItemsDefaultsToVideoTypeScope(t *testing.T) {
	browse := &scopeFakeBrowseSource{}
	svc := &directContentService{browseRepo: browse}
	session := &Session{StreamAppUserID: 1, ProfileID: "p1"}

	if _, err := svc.BrowseItems(context.Background(), session, url.Values{}); err != nil {
		t.Fatalf("BrowseItems: %v", err)
	}
	if browse.lastFilters.Type != compatVideoTypes {
		t.Fatalf("expected default type scope %q, got %q", compatVideoTypes, browse.lastFilters.Type)
	}

	params := url.Values{}
	params.Set("type", "movie")
	if _, err := svc.BrowseItems(context.Background(), session, params); err != nil {
		t.Fatalf("BrowseItems: %v", err)
	}
	if browse.lastFilters.Type != "movie" {
		t.Fatalf("expected explicit type to pass through, got %q", browse.lastFilters.Type)
	}
}

func TestListItemFiltersDefaultsToVideoTypeScope(t *testing.T) {
	browse := &scopeFakeBrowseSource{}
	svc := &directContentService{browseRepo: browse}

	if _, err := svc.ListItemFilters(context.Background(), &Session{StreamAppUserID: 1, ProfileID: "p1"}, url.Values{}); err != nil {
		t.Fatalf("ListItemFilters: %v", err)
	}
	if browse.lastFilters.Type != compatVideoTypes {
		t.Fatalf("expected genre listing scoped to %q, got %q", compatVideoTypes, browse.lastFilters.Type)
	}
}
