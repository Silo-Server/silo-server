package templates

import (
	"os"
	"path/filepath"
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
}

func TestBuiltinTemplatePosterAssetsExist(t *testing.T) {
	assetRoot := filepath.Join("..", "..", "..", "web", "public", "images", "collection-templates")
	rawRoot := filepath.Join(assetRoot, "raw")

	for _, tmpl := range List() {
		t.Run(tmpl.ID, func(t *testing.T) {
			if _, err := os.Stat(filepath.Join(assetRoot, tmpl.ID+".jpg")); err != nil {
				t.Fatalf("final poster asset missing: %v", err)
			}
			if _, err := os.Stat(filepath.Join(rawRoot, tmpl.ID+".png")); err != nil {
				t.Fatalf("raw poster plate missing: %v", err)
			}
		})
	}
}

func TestBuiltinTemplatesValidate(t *testing.T) {
	for _, tmpl := range List() {
		t.Run(tmpl.ID, func(t *testing.T) {
			if err := validate(tmpl); err != nil {
				t.Fatalf("template %s failed validation: %v", tmpl.ID, err)
			}
		})
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

func TestGetReturnsRegisteredTemplate(t *testing.T) {
	tmpl, ok := Get("tmdb_trending_all_today")
	if !ok {
		t.Fatal("expected built-in template to be findable by ID")
	}
	if tmpl.Source != SourceTMDB {
		t.Errorf("source mismatch: got %q", tmpl.Source)
	}
	if tmpl.TMDB == nil || tmpl.TMDB.Preset != "trending" {
		t.Errorf("tmdb spec missing or wrong preset")
	}
}

func TestCategoryLabelKnowsAllBuiltinCategories(t *testing.T) {
	cat := CatalogDefault()
	for _, group := range cat.Categories {
		if strings.TrimSpace(CategoryLabel(group.Category)) == "" {
			t.Errorf("missing label for category %q", group.Category)
		}
	}
}

// TestPhase2DiscoverTemplatesUseExpectedBands pins the Phase-2 TMDB Discover
// genre matrix to its documented sort-order bands and TMDB query parameters.
// Drift here would either reorder the gallery in surprising ways (band
// regressions) or change what a "Popular Action" / "Top Rated Action" template
// actually returns from TMDB (filter regressions) — both silent failures from
// the operator's perspective, so make them loud.
func TestPhase2DiscoverTemplatesUseExpectedBands(t *testing.T) {
	const (
		popularPrefix  = "tmdb_discover_popular_"
		topRatedPrefix = "tmdb_discover_top_rated_"
		kidsPrefix     = "tmdb_discover_kids_"
	)
	var popularCount, topRatedCount, kidsCount int
	for _, tmpl := range List() {
		switch {
		case strings.HasPrefix(tmpl.ID, popularPrefix):
			popularCount++
			if tmpl.Source != SourceTMDBDiscover {
				t.Errorf("%s: Source = %q, want %q", tmpl.ID, tmpl.Source, SourceTMDBDiscover)
			}
			if tmpl.TMDBDiscover == nil {
				t.Fatalf("%s: TMDBDiscover spec is nil", tmpl.ID)
			}
			if tmpl.DefaultSortOrder < 5000 || tmpl.DefaultSortOrder > 5999 {
				t.Errorf("%s: DefaultSortOrder %d outside Popular band [5000, 5999]", tmpl.ID, tmpl.DefaultSortOrder)
			}
			if tmpl.TMDBDiscover.SortBy != "popularity.desc" {
				t.Errorf("%s: SortBy = %q, want popularity.desc", tmpl.ID, tmpl.TMDBDiscover.SortBy)
			}
			if tmpl.TMDBDiscover.VoteCountGte != 300 {
				t.Errorf("%s: VoteCountGte = %d, want 300", tmpl.ID, tmpl.TMDBDiscover.VoteCountGte)
			}
		case strings.HasPrefix(tmpl.ID, topRatedPrefix):
			topRatedCount++
			if tmpl.Source != SourceTMDBDiscover {
				t.Errorf("%s: Source = %q, want %q", tmpl.ID, tmpl.Source, SourceTMDBDiscover)
			}
			if tmpl.TMDBDiscover == nil {
				t.Fatalf("%s: TMDBDiscover spec is nil", tmpl.ID)
			}
			if tmpl.DefaultSortOrder < 6000 || tmpl.DefaultSortOrder > 6999 {
				t.Errorf("%s: DefaultSortOrder %d outside Top Rated band [6000, 6999]", tmpl.ID, tmpl.DefaultSortOrder)
			}
			if tmpl.TMDBDiscover.SortBy != "vote_average.desc" {
				t.Errorf("%s: SortBy = %q, want vote_average.desc", tmpl.ID, tmpl.TMDBDiscover.SortBy)
			}
			if tmpl.TMDBDiscover.VoteCountGte != 1000 {
				t.Errorf("%s: VoteCountGte = %d, want 1000", tmpl.ID, tmpl.TMDBDiscover.VoteCountGte)
			}
		case strings.HasPrefix(tmpl.ID, kidsPrefix):
			kidsCount++
			if tmpl.Source != SourceTMDBDiscover {
				t.Errorf("%s: Source = %q, want %q", tmpl.ID, tmpl.Source, SourceTMDBDiscover)
			}
			if tmpl.TMDBDiscover == nil {
				t.Fatalf("%s: TMDBDiscover spec is nil", tmpl.ID)
			}
			if tmpl.DefaultSortOrder < 9000 || tmpl.DefaultSortOrder > 9999 {
				t.Errorf("%s: DefaultSortOrder %d outside Misc/Kids band [9000, 9999]", tmpl.ID, tmpl.DefaultSortOrder)
			}
			if tmpl.TMDBDiscover.CertificationLte != "PG" {
				t.Errorf("%s: CertificationLte = %q, want PG", tmpl.ID, tmpl.TMDBDiscover.CertificationLte)
			}
		}
	}
	if popularCount != 18 {
		t.Errorf("Popular by Genre count = %d, want 18", popularCount)
	}
	if topRatedCount != 18 {
		t.Errorf("Top Rated by Genre count = %d, want 18", topRatedCount)
	}
	if kidsCount != 1 {
		t.Errorf("Kids count = %d, want 1", kidsCount)
	}
}

// TestPhase3FranchiseTemplatesUseExpectedBands pins the Phase-3 TMDB Collection
// franchise templates to their documented sort-order band, source, and spec
// shape. The placeholder template is allowed to ship with CollectionID == 0 (a
// permitted sentinel — see validateTMDBCollection); every other franchise must
// resolve to a real TMDB collection ID. Drift here would either silently
// reorder the gallery or, worse, swap a curated franchise for the placeholder.
func TestPhase3FranchiseTemplatesUseExpectedBands(t *testing.T) {
	const (
		franchisePrefix = "tmdb_franchise_"
		placeholderID   = "tmdb_franchise_placeholder"
	)

	// Keep in lockstep with the entries added to builtin.go. If a franchise
	// is added or skipped, update this number — the count assertion is the
	// canary that catches accidental deletions.
	const wantFranchiseTemplateCount = 11

	var (
		matched          int
		sawPlaceholder   bool
		curatedFranchise int
	)
	for _, tmpl := range List() {
		if !strings.HasPrefix(tmpl.ID, franchisePrefix) {
			continue
		}
		matched++

		if tmpl.Source != SourceTMDBCollection {
			t.Errorf("%s: Source = %q, want %q", tmpl.ID, tmpl.Source, SourceTMDBCollection)
		}
		if tmpl.MediaKind != MediaMovie {
			t.Errorf("%s: MediaKind = %q, want %q", tmpl.ID, tmpl.MediaKind, MediaMovie)
		}
		if tmpl.DefaultSortOrder < 7000 || tmpl.DefaultSortOrder > 7999 {
			t.Errorf("%s: DefaultSortOrder %d outside Franchises band [7000, 7999]", tmpl.ID, tmpl.DefaultSortOrder)
		}
		if tmpl.TMDBCollection == nil {
			t.Fatalf("%s: TMDBCollection spec is nil", tmpl.ID)
		}

		if tmpl.ID == placeholderID {
			sawPlaceholder = true
			if tmpl.TMDBCollection.CollectionID != 0 {
				t.Errorf("%s: placeholder CollectionID = %d, want 0", tmpl.ID, tmpl.TMDBCollection.CollectionID)
			}
		} else {
			curatedFranchise++
			if tmpl.TMDBCollection.CollectionID <= 0 {
				t.Errorf("%s: CollectionID = %d, want > 0", tmpl.ID, tmpl.TMDBCollection.CollectionID)
			}
		}
	}

	if !sawPlaceholder {
		t.Error("placeholder template tmdb_franchise_placeholder is missing")
	}
	if matched != wantFranchiseTemplateCount {
		t.Errorf("franchise template count = %d, want %d (curated=%d + placeholder=1)",
			matched, wantFranchiseTemplateCount, curatedFranchise)
	}
}

// TestPhase1MDBListTemplatesHaveSortOrderInExpectedBands verifies that the
// Phase-1 MDBList-backed templates land in the sort-order bands documented in
// docs/superpowers/plans/2026-05-22-collection-templates-expansion.md. The
// banding scheme drives apply-time ordering for the resulting collections, so
// regressing a band (e.g. assigning a streaming template into the 9000s) would
// silently reorder a user's library — make that loud.
func TestPhase1MDBListTemplatesHaveSortOrderInExpectedBands(t *testing.T) {
	type band struct {
		name string
		lo   int
		hi   int
	}
	rules := []struct {
		prefix string
		band   band
	}{
		{prefix: "mdblist_charts_", band: band{name: "Charts", lo: 1000, hi: 1999}},
		{prefix: "mdblist_best_of_", band: band{name: "Best of Year", lo: 2000, hi: 2999}},
		{prefix: "mdblist_awards_", band: band{name: "Awards", lo: 3000, hi: 3999}},
		{prefix: "mdblist_streaming_", band: band{name: "Streaming Originals", lo: 4000, hi: 4999}},
		{prefix: "mdblist_seasonal_", band: band{name: "Seasonal/Holiday", lo: 8000, hi: 8999}},
		{prefix: "mdblist_misc_", band: band{name: "Editorial/Misc", lo: 9000, hi: 9999}},
	}

	matched := 0
	for _, tmpl := range List() {
		for _, rule := range rules {
			if !strings.HasPrefix(tmpl.ID, rule.prefix) {
				continue
			}
			matched++
			if tmpl.DefaultSortOrder < rule.band.lo || tmpl.DefaultSortOrder > rule.band.hi {
				t.Errorf("template %q: DefaultSortOrder %d outside %s band [%d, %d]",
					tmpl.ID, tmpl.DefaultSortOrder, rule.band.name, rule.band.lo, rule.band.hi)
			}
			break
		}
	}
	if matched == 0 {
		t.Fatal("no Phase-1 MDBList templates found by prefix — did the catalog change?")
	}
}
