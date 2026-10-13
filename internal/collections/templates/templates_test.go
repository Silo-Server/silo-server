package templates

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBuiltinCatalog(t *testing.T) {
	cat := CatalogDefault()
	if len(cat.Categories) == 0 {
		t.Fatal("expected at least one category in the built-in catalog")
	}

	seenIDs := make(map[string]bool)
	for _, group := range cat.Categories {
		if group.Label == "" {
			t.Errorf("category %q has empty label", group.Category)
		}
		if len(group.Templates) == 0 {
			t.Errorf("category %q has no templates", group.Category)
		}
		for _, tmpl := range group.Templates {
			if seenIDs[tmpl.ID] {
				t.Errorf("duplicate template id %q", tmpl.ID)
			}
			if strings.TrimSpace(tmpl.PosterPath) == "" {
				t.Errorf("template %q has empty poster path", tmpl.ID)
			}
			if tmpl.PosterPath != "" && !strings.HasPrefix(tmpl.PosterPath, "/images/collection-templates/") {
				t.Errorf("template %q has invalid poster path %q", tmpl.ID, tmpl.PosterPath)
			}
			seenIDs[tmpl.ID] = true
		}
	}
	kids, ok := Get("tmdb_discover_kids_movies")
	if !ok || kids.TMDBDiscover == nil || kids.TMDBDiscover.CertificationLte != "PG" {
		t.Fatalf("Kids Movies must keep its PG certification ceiling: %+v", kids)
	}
}

func TestBuiltinTemplatePosterAssetsExist(t *testing.T) {
	assetRoot := filepath.Join("..", "..", "..", "web", "public", "images", "collection-templates")

	for _, tmpl := range List() {
		t.Run(tmpl.ID, func(t *testing.T) {
			if _, err := os.Stat(filepath.Join(assetRoot, tmpl.ID+".jpg")); err != nil {
				t.Fatalf("final poster asset missing: %v", err)
			}
		})
	}
}

func TestBuiltinTemplateSourcePlatesStayOutOfPublicAssets(t *testing.T) {
	webRoot := filepath.Join("..", "..", "..", "web")
	sourceRoot := filepath.Join(webRoot, "assets-source", "collection-templates", "raw")
	publicRoot := filepath.Join(webRoot, "public", "images", "collection-templates", "raw")

	if _, err := os.Stat(publicRoot); !os.IsNotExist(err) {
		t.Fatalf("raw poster plates must not be public assets: %v", err)
	}

	for _, tmpl := range List() {
		t.Run(tmpl.ID, func(t *testing.T) {
			if _, err := os.Stat(filepath.Join(sourceRoot, tmpl.ID+".png")); err != nil {
				t.Fatalf("raw poster plate missing: %v", err)
			}
		})
	}
}

// retiredTemplatePosterIDs lists removed templates whose final poster still
// ships. A collection created from a template stores the template's poster
// path and keeps it unless the poster was copied into artwork storage, so
// deleting one of these JPGs would blank that collection's poster. Remove an ID
// together with its JPG once no stored poster path can point at it. Retired
// templates keep no raw plate.
var retiredTemplatePosterIDs = map[string]bool{
	// Trakt templates, removed with Trakt-backed collection creation.
	"trakt_popular_movies":     true,
	"trakt_popular_shows":      true,
	"trakt_recommended_movies": true,
	"trakt_recommended_shows":  true,
	"trakt_trending_movies":    true,
	"trakt_trending_shows":     true,
}

// Files an OS or file manager may leave in an asset directory.
var ignoredAssetDirEntries = map[string]bool{"Thumbs.db": true, "desktop.ini": true}

func TestBuiltinTemplateAssetsHaveTemplates(t *testing.T) {
	webRoot := filepath.Join("..", "..", "..", "web")
	registered := make(map[string]bool)
	for _, tmpl := range List() {
		registered[tmpl.ID] = true
	}
	retired := slices.Sorted(maps.Keys(retiredTemplatePosterIDs))

	for _, id := range retired {
		if registered[id] {
			t.Errorf("retired template %q is registered again; drop it from retiredTemplatePosterIDs", id)
		}
	}

	dirs := []struct {
		path         string
		ext          string
		allowRetired bool
	}{
		{filepath.Join(webRoot, "public", "images", "collection-templates"), ".jpg", true},
		{filepath.Join(webRoot, "assets-source", "collection-templates", "raw"), ".png", false},
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir.path)
		if err != nil {
			t.Fatalf("read %s: %v", dir.path, err)
		}
		found := make(map[string]bool)
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".") || ignoredAssetDirEntries[name] {
				continue
			}
			id, ok := strings.CutSuffix(name, dir.ext)
			if ok {
				found[id] = true
			}
			switch {
			case entry.IsDir() || !ok:
				t.Errorf("%s: unexpected entry %q; only {template id}%s files belong here", dir.path, name, dir.ext)
			case registered[id]:
			case dir.allowRetired && retiredTemplatePosterIDs[id]:
			case dir.allowRetired:
				t.Errorf("%s has no registered template; delete it, or add %q to retiredTemplatePosterIDs if existing collections still use it", filepath.Join(dir.path, name), id)
			default:
				t.Errorf("%s has no registered template; delete it", filepath.Join(dir.path, name))
			}
		}
		if !dir.allowRetired {
			continue
		}
		for _, id := range retired {
			if !found[id] {
				t.Errorf("%s is missing; restore it, or drop %q from retiredTemplatePosterIDs once no stored poster path can point at it", filepath.Join(dir.path, id+dir.ext), id)
			}
		}
	}
}

func TestBuiltinTemplateDefaultLimits(t *testing.T) {
	// Finite canonical lists override the shared default: top-N truncation
	// would contradict what their titles promise. Catalog lists (Criterion,
	// A24) carry no limit at all so the collection holds every owned title.
	overrides := map[string]int{
		"mdblist_imdb_top_250_movies":       250,
		"mdblist_imdb_top_250_shows":        250,
		"mdblist_misc_criterion_collection": 0,
		"mdblist_misc_a24":                  0,
	}
	for _, tmpl := range List() {
		want := builtinDefaultLimit
		if override, ok := overrides[tmpl.ID]; ok {
			want = override
		}
		if tmpl.DefaultLimit != want {
			t.Errorf("template %q default_limit = %d, want %d", tmpl.ID, tmpl.DefaultLimit, want)
		}
	}
}

func TestCoreDefaultsBundleReferencesValidProfileFreeTemplates(t *testing.T) {
	bundle, ok := GetBundle("core_defaults")
	if !ok {
		t.Fatal("core_defaults bundle is not registered")
	}
	want := []string{
		"tmdb_trending_movies_week",
		"tmdb_popular_movies",
		"tmdb_top_rated_movies",
		"tmdb_now_playing",
		"tmdb_upcoming",
		"mdblist_imdb_top_250_movies",
		"mdblist_top_documentaries",
		"mdblist_top_horror",
		"mdblist_mindfuck_movies",
		"tmdb_trending_tv_week",
		"tmdb_popular_tv",
		"tmdb_top_rated_tv",
		"tmdb_airing_today",
		"tmdb_on_the_air",
		"mdblist_imdb_top_250_shows",
	}
	if len(bundle.TemplateIDs) != len(want) {
		t.Fatalf("template count = %d, want %d", len(bundle.TemplateIDs), len(want))
	}
	for i, id := range want {
		if bundle.TemplateIDs[i] != id {
			t.Fatalf("template_ids[%d] = %q, want %q", i, bundle.TemplateIDs[i], id)
		}
		tmpl, ok := Get(id)
		if !ok {
			t.Fatalf("template %q is not registered", id)
		}
		if tmpl.RequiresProfile {
			t.Fatalf("template %q requires profile and must not be in core defaults", id)
		}
	}
}

func TestBundleOnlyTemplatesAreReachableFromBundles(t *testing.T) {
	bundled := make(map[string]bool)
	for _, bundle := range ListBundles() {
		for _, id := range bundle.TemplateIDs {
			bundled[id] = true
		}
	}

	for _, tmpl := range List() {
		switch tmpl.Source {
		case SourceTMDBDiscover, SourceTMDBCollection:
			if !bundled[tmpl.ID] {
				t.Errorf("bundle-only template %q is not referenced by any bundle", tmpl.ID)
			}
		}
	}
}

func TestAllDefaultsBundleIncludesEveryOtherDefaultBundle(t *testing.T) {
	allDefaults, ok := GetBundle("all_defaults")
	if !ok {
		t.Fatal("all_defaults bundle is not registered")
	}
	bundles := ListBundles()
	if len(bundles) == 0 || bundles[0].ID != allDefaults.ID {
		t.Fatal("all_defaults should be the first displayed bundle")
	}

	seen := make(map[string]struct{}, len(allDefaults.TemplateIDs))
	for _, id := range allDefaults.TemplateIDs {
		if _, exists := seen[id]; exists {
			t.Fatalf("all_defaults contains duplicate template %q", id)
		}
		seen[id] = struct{}{}
	}

	for _, bundle := range bundles {
		if bundle.ID == allDefaults.ID {
			continue
		}
		for _, id := range bundle.TemplateIDs {
			if _, ok := seen[id]; !ok {
				t.Fatalf("all_defaults is missing %q from bundle %q", id, bundle.ID)
			}
		}
	}
}

func TestRegisterBundleRejectsProfileRequiredTemplates(t *testing.T) {
	r := NewRegistry()
	r.Register(Template{
		ID:              "recommended",
		Title:           "Recommended",
		Category:        CategoryEditorial,
		Source:          SourceTrakt,
		MediaKind:       MediaMovie,
		RequiresProfile: true,
		Trakt:           &TraktSpec{Preset: "recommended", MediaType: "movie"},
	})
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on profile-required template in bundle")
		}
	}()
	r.RegisterBundle(Bundle{ID: "bad", Title: "Bad", TemplateIDs: []string{"recommended"}})
}

func TestRegisterRejectsDuplicates(t *testing.T) {
	r := NewRegistry()
	tmpl := Template{
		ID:        "x",
		Title:     "X",
		Category:  CategoryTrending,
		Source:    SourceTMDB,
		MediaKind: MediaMovie,
		TMDB:      &TMDBSpec{Preset: "popular", MediaType: "movie"},
	}
	r.Register(tmpl)
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate ID")
		}
	}()
	r.Register(tmpl)
}

func TestRegisterValidatesTemplate(t *testing.T) {
	cases := []struct {
		name string
		tmpl Template
	}{
		{
			name: "missing id",
			tmpl: Template{Title: "x", Category: CategoryTrending, Source: SourceTMDB, MediaKind: MediaMovie, TMDB: &TMDBSpec{Preset: "popular", MediaType: "movie"}},
		},
		{
			name: "missing source spec",
			tmpl: Template{ID: "x", Title: "x", Category: CategoryTrending, Source: SourceTMDB, MediaKind: MediaMovie},
		},
		{
			name: "two source specs",
			tmpl: Template{
				ID: "x", Title: "x", Category: CategoryTrending, Source: SourceTMDB, MediaKind: MediaMovie,
				TMDB:  &TMDBSpec{Preset: "popular", MediaType: "movie"},
				Trakt: &TraktSpec{Preset: "popular", MediaType: "movie"},
			},
		},
		{
			name: "tmdb trending missing window",
			tmpl: Template{ID: "x", Title: "x", Category: CategoryTrending, Source: SourceTMDB, MediaKind: MediaMovie, TMDB: &TMDBSpec{Preset: "trending", MediaType: "movie"}},
		},
		{
			name: "tmdb now_playing wrong media type",
			tmpl: Template{ID: "x", Title: "x", Category: CategoryInTheaters, Source: SourceTMDB, MediaKind: MediaTV, TMDB: &TMDBSpec{Preset: "now_playing", MediaType: "tv"}},
		},
		{
			name: "trakt recommended without requires_profile",
			tmpl: Template{ID: "x", Title: "x", Category: CategoryEditorial, Source: SourceTrakt, MediaKind: MediaMovie, Trakt: &TraktSpec{Preset: "recommended", MediaType: "movie"}},
		},
		{
			name: "trakt recommended with bad media type",
			tmpl: Template{ID: "x", Title: "x", Category: CategoryEditorial, Source: SourceTrakt, MediaKind: MediaMovie, RequiresProfile: true, Trakt: &TraktSpec{Preset: "recommended", MediaType: "all"}},
		},
		{
			name: "mdblist non-http URL",
			tmpl: Template{ID: "x", Title: "x", Category: CategoryCustom, Source: SourceMDBList, MediaKind: MediaMixed, MDBList: &MDBListSpec{URL: "ftp://example.com/list.json"}},
		},
		{
			name: "mdblist off-host URL",
			tmpl: Template{ID: "x", Title: "x", Category: CategoryCustom, Source: SourceMDBList, MediaKind: MediaMixed, MDBList: &MDBListSpec{URL: "https://example.com/lists/x/y"}},
		},
		{
			name: "mdblist userinfo URL",
			tmpl: Template{ID: "x", Title: "x", Category: CategoryCustom, Source: SourceMDBList, MediaKind: MediaMixed, MDBList: &MDBListSpec{URL: "https://mdblist.com@127.0.0.1/lists/x/y"}},
		},
		{
			name: "mdblist disallowed port",
			tmpl: Template{ID: "x", Title: "x", Category: CategoryCustom, Source: SourceMDBList, MediaKind: MediaMixed, MDBList: &MDBListSpec{URL: "https://mdblist.com:8080/lists/x/y"}},
		},
		{
			name: "tmdb_collection missing spec",
			tmpl: Template{
				ID: "x", Title: "x", Category: CategoryEditorial,
				Source: SourceTMDBCollection, MediaKind: MediaMovie,
			},
		},
		{
			name: "tmdb_collection negative id",
			tmpl: Template{
				ID: "x", Title: "x", Category: CategoryEditorial,
				Source: SourceTMDBCollection, MediaKind: MediaMovie,
				TMDBCollection: &TMDBCollectionSpec{CollectionID: -1},
			},
		},
		{
			name: "tmdb_collection with mdblist spec also set",
			tmpl: Template{
				ID: "x", Title: "x", Category: CategoryEditorial,
				Source: SourceTMDBCollection, MediaKind: MediaMovie,
				TMDBCollection: &TMDBCollectionSpec{CollectionID: 86311},
				MDBList:        &MDBListSpec{URL: "https://mdblist.com/lists/x"},
			},
		},
		{
			name: "tmdb_collection with tmdb spec also set",
			tmpl: Template{
				ID: "x", Title: "x", Category: CategoryEditorial,
				Source: SourceTMDBCollection, MediaKind: MediaMovie,
				TMDBCollection: &TMDBCollectionSpec{CollectionID: 86311},
				TMDB:           &TMDBSpec{Preset: "popular", MediaType: "movie"},
			},
		},
		{
			name: "tmdb source with tmdb_collection also set",
			tmpl: Template{
				ID: "x", Title: "x", Category: CategoryPopular,
				Source: SourceTMDB, MediaKind: MediaMovie,
				TMDB:           &TMDBSpec{Preset: "popular", MediaType: "movie"},
				TMDBCollection: &TMDBCollectionSpec{CollectionID: 86311},
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := NewRegistry()
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic on invalid template")
				}
			}()
			r.Register(c.tmpl)
		})
	}
}

func TestRegisterAcceptsTMDBCollectionTemplates(t *testing.T) {
	// Happy-path register for a real TMDB-collection franchise template.
	r := NewRegistry()
	r.Register(Template{
		ID:        "tmdb_mcu",
		Title:     "Marvel Cinematic Universe",
		Category:  CategoryEditorial,
		Source:    SourceTMDBCollection,
		MediaKind: MediaMovie,
		TMDBCollection: &TMDBCollectionSpec{
			CollectionID: 86311,
		},
	})
	tmpl, ok := r.Get("tmdb_mcu")
	if !ok {
		t.Fatal("tmdb_mcu was not registered")
	}
	if tmpl.TMDBCollection == nil || tmpl.TMDBCollection.CollectionID != 86311 {
		t.Fatalf("registered spec was lost: %+v", tmpl.TMDBCollection)
	}
}

// TestRegisterAcceptsPlaceholderTMDBCollection pins the rule that a
// CollectionID of 0 is a permitted "fill me in later" sentinel — the generic
// "TMDB Franchise" catalog entry ships this way so admins can edit the
// resulting collection's source_config at apply-time.
func TestRegisterAcceptsPlaceholderTMDBCollection(t *testing.T) {
	r := NewRegistry()
	r.Register(Template{
		ID:        "tmdb_franchise_placeholder",
		Title:     "TMDB Franchise",
		Category:  CategoryCustom,
		Source:    SourceTMDBCollection,
		MediaKind: MediaMovie,
		TMDBCollection: &TMDBCollectionSpec{
			CollectionID: 0,
		},
	})
	tmpl, ok := r.Get("tmdb_franchise_placeholder")
	if !ok {
		t.Fatal("placeholder template was not registered")
	}
	if tmpl.TMDBCollection == nil || tmpl.TMDBCollection.CollectionID != 0 {
		t.Fatalf("placeholder spec was lost: %+v", tmpl.TMDBCollection)
	}
}

func validTMDBDiscoverSpec() *TMDBDiscoverSpec {
	return &TMDBDiscoverSpec{
		MediaType: "movie",
		SortBy:    "popularity.desc",
	}
}

func TestRegisterAcceptsTMDBDiscoverTemplate(t *testing.T) {
	r := NewRegistry()
	r.Register(Template{
		ID:           "discover_action",
		Title:        "Action",
		Category:     CategoryPopular,
		Source:       SourceTMDBDiscover,
		MediaKind:    MediaMovie,
		DefaultLimit: 50,
		TMDBDiscover: &TMDBDiscoverSpec{
			MediaType:        "movie",
			WithGenres:       []int{28},
			SortBy:           "popularity.desc",
			VoteCountGte:     300,
			VoteAverageGte:   6.5,
			ReleaseDateGte:   "2010-01-01",
			Certifications:   []string{"PG", "PG-13"},
			WithRuntimeGte:   60,
			WithRuntimeLte:   240,
			OriginalLanguage: "en",
		},
	})
	tmpl, ok := r.Get("discover_action")
	if !ok {
		t.Fatal("expected template to be registered")
	}
	if tmpl.Source != SourceTMDBDiscover || tmpl.TMDBDiscover == nil {
		t.Fatalf("unexpected template: %+v", tmpl)
	}
}

func TestRegisterRejectsInvalidTMDBDiscover(t *testing.T) {
	cases := []struct {
		name string
		tmpl Template
	}{
		{
			name: "missing tmdb_discover spec",
			tmpl: Template{ID: "x", Title: "x", Category: CategoryPopular, Source: SourceTMDBDiscover, MediaKind: MediaMovie},
		},
		{
			name: "missing media_type",
			tmpl: Template{
				ID: "x", Title: "x", Category: CategoryPopular, Source: SourceTMDBDiscover, MediaKind: MediaMovie,
				TMDBDiscover: &TMDBDiscoverSpec{SortBy: "popularity.desc"},
			},
		},
		{
			name: "invalid sort_by",
			tmpl: Template{
				ID: "x", Title: "x", Category: CategoryPopular, Source: SourceTMDBDiscover, MediaKind: MediaMovie,
				TMDBDiscover: &TMDBDiscoverSpec{MediaType: "movie", SortBy: "not_a_real_sort"},
			},
		},
		{
			name: "malformed release_date_gte",
			tmpl: Template{
				ID: "x", Title: "x", Category: CategoryPopular, Source: SourceTMDBDiscover, MediaKind: MediaMovie,
				TMDBDiscover: &TMDBDiscoverSpec{
					MediaType:      "movie",
					SortBy:         "popularity.desc",
					ReleaseDateGte: "2010/01/01",
				},
			},
		},
		{
			name: "runtime gte > lte",
			tmpl: Template{
				ID: "x", Title: "x", Category: CategoryPopular, Source: SourceTMDBDiscover, MediaKind: MediaMovie,
				TMDBDiscover: &TMDBDiscoverSpec{
					MediaType:      "movie",
					SortBy:         "popularity.desc",
					WithRuntimeGte: 240,
					WithRuntimeLte: 60,
				},
			},
		},
		{
			name: "single character original_language",
			tmpl: Template{
				ID: "x", Title: "x", Category: CategoryPopular, Source: SourceTMDBDiscover, MediaKind: MediaMovie,
				TMDBDiscover: &TMDBDiscoverSpec{
					MediaType:        "movie",
					SortBy:           "popularity.desc",
					OriginalLanguage: "e",
				},
			},
		},
		{
			name: "empty certification",
			tmpl: Template{
				ID: "x", Title: "x", Category: CategoryPopular, Source: SourceTMDBDiscover, MediaKind: MediaMovie,
				TMDBDiscover: &TMDBDiscoverSpec{
					MediaType:      "movie",
					SortBy:         "popularity.desc",
					Certifications: []string{"PG", "  "},
				},
			},
		},
		{
			name: "discover with tmdb spec",
			tmpl: Template{
				ID: "x", Title: "x", Category: CategoryPopular, Source: SourceTMDBDiscover, MediaKind: MediaMovie,
				TMDBDiscover: validTMDBDiscoverSpec(),
				TMDB:         &TMDBSpec{Preset: "popular", MediaType: "movie"},
			},
		},
		{
			name: "discover with trakt spec",
			tmpl: Template{
				ID: "x", Title: "x", Category: CategoryPopular, Source: SourceTMDBDiscover, MediaKind: MediaMovie,
				TMDBDiscover: validTMDBDiscoverSpec(),
				Trakt:        &TraktSpec{Preset: "popular", MediaType: "movie"},
			},
		},
		{
			name: "discover with mdblist spec",
			tmpl: Template{
				ID: "x", Title: "x", Category: CategoryPopular, Source: SourceTMDBDiscover, MediaKind: MediaMovie,
				TMDBDiscover: validTMDBDiscoverSpec(),
				MDBList:      &MDBListSpec{URL: "https://example.com/list"},
			},
		},
		{
			name: "tmdb source rejects tmdb_discover spec",
			tmpl: Template{
				ID: "x", Title: "x", Category: CategoryPopular, Source: SourceTMDB, MediaKind: MediaMovie,
				TMDB:         &TMDBSpec{Preset: "popular", MediaType: "movie"},
				TMDBDiscover: validTMDBDiscoverSpec(),
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := NewRegistry()
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic on invalid template")
				}
			}()
			r.Register(c.tmpl)
		})
	}
}

func TestCatalogPreservesOrder(t *testing.T) {
	cat := CatalogDefault()
	for i, group := range cat.Categories {
		if i == 0 && group.Category != CategoryTrending {
			t.Errorf("expected first category to be trending, got %q", group.Category)
		}
	}
}
