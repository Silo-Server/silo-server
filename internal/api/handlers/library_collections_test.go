package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/collections/templates"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestNormalizeTraktPresetRequest(t *testing.T) {
	preset, mediaType, profileID, err := normalizeTraktPresetRequest("recommended", "tv", "prof-1")
	if err != nil {
		t.Fatalf("normalizeTraktPresetRequest: %v", err)
	}
	if preset != "recommended" || mediaType != "tv" || profileID != "prof-1" {
		t.Fatalf("got %q/%q/%q", preset, mediaType, profileID)
	}

	if _, _, _, err := normalizeTraktPresetRequest("recommended", "movie", ""); err == nil {
		t.Fatal("expected profile_id error")
	}
	if _, _, _, err := normalizeTraktPresetRequest("popular", "all", ""); err == nil {
		t.Fatal("expected media_type error")
	}
}

func TestBuildTMDBCollectionSourceConfig(t *testing.T) {
	limit := 12
	raw, err := buildTMDBCollectionSourceConfig(86311, &limit, false)
	if err != nil {
		t.Fatalf("buildTMDBCollectionSourceConfig: %v", err)
	}

	// Wire-format assertion: mode is set, collection_id round-trips, limit
	// round-trips. Empty fields (provider, preset, etc.) must not appear.
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["mode"] != collectionSourceModeTMDBCollection {
		t.Errorf("mode = %v, want %q", got["mode"], collectionSourceModeTMDBCollection)
	}
	if cid, ok := got["collection_id"].(float64); !ok || int(cid) != 86311 {
		t.Errorf("collection_id = %v, want 86311", got["collection_id"])
	}
	if lim, ok := got["limit"].(float64); !ok || int(lim) != 12 {
		t.Errorf("limit = %v, want 12", got["limit"])
	}
	for _, unwanted := range []string{"preset", "media_type", "time_window", "provider", "url", "profile_id"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("unexpected field %q in payload: %#v", unwanted, got)
		}
	}

	// Decode into the catalog source config struct via JSON so we pin the
	// round-trip stability of every field name and the omitempty behavior
	// of collection_id. This protects against a silent rename on the
	// catalog side breaking the bundle apply path.
	type sourceConfigShape struct {
		Mode         string `json:"mode"`
		CollectionID int    `json:"collection_id,omitempty"`
		Limit        *int   `json:"limit,omitempty"`
	}
	var decoded sourceConfigShape
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode source config shape: %v", err)
	}
	if decoded.Mode != collectionSourceModeTMDBCollection {
		t.Fatalf("decoded mode = %q", decoded.Mode)
	}
	if decoded.CollectionID != 86311 {
		t.Fatalf("decoded collection_id = %d, want 86311", decoded.CollectionID)
	}
	if decoded.Limit == nil || *decoded.Limit != 12 {
		t.Fatalf("decoded limit = %v, want 12", decoded.Limit)
	}
}

func TestBuildTMDBCollectionSourceConfig_PlaceholderOmitsCollectionID(t *testing.T) {
	// CollectionID == 0 is a documented placeholder; with omitempty the JSON
	// must drop the field entirely so older sync code that didn't know about
	// the field still works.
	raw, err := buildTMDBCollectionSourceConfig(0, nil, false)
	if err != nil {
		t.Fatalf("buildTMDBCollectionSourceConfig: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["mode"] != collectionSourceModeTMDBCollection {
		t.Fatalf("mode = %v, want %q", got["mode"], collectionSourceModeTMDBCollection)
	}
	if _, ok := got["collection_id"]; ok {
		t.Errorf("collection_id should be omitted when zero, got %v", got["collection_id"])
	}
	if _, ok := got["limit"]; ok {
		t.Errorf("limit should be omitted when nil, got %v", got["limit"])
	}
}

func TestBuildTMDBCollectionSourceURL(t *testing.T) {
	if url := buildTMDBCollectionSourceURL(86311); url != "tmdb://collection/86311" {
		t.Fatalf("source url = %q", url)
	}
	if url := buildTMDBCollectionSourceURL(0); url != "tmdb://collection/0" {
		t.Fatalf("placeholder source url = %q", url)
	}
}

func TestBuildTMDBDiscoverSourceConfigRoundTrips(t *testing.T) {
	limit := 50
	spec := importTMDBDiscoverSpecBody{
		WithGenres:       []int{28, 12},
		WithoutGenres:    []int{99},
		SortBy:           "popularity.desc",
		VoteCountGte:     300,
		VoteAverageGte:   6.5,
		ReleaseDateGte:   "2010-01-01",
		ReleaseDateLte:   "2025-12-31",
		Certifications:   []string{"PG", "PG-13"},
		WithRuntimeGte:   60,
		WithRuntimeLte:   240,
		OriginalLanguage: "en",
	}
	raw, err := buildTMDBDiscoverSourceConfig("movie", spec, &limit, false)
	if err != nil {
		t.Fatalf("buildTMDBDiscoverSourceConfig: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["mode"] != "tmdb_discover" {
		t.Errorf("mode = %v, want tmdb_discover", got["mode"])
	}
	if got["media_type"] != "movie" {
		t.Errorf("media_type = %v, want movie", got["media_type"])
	}
	if got["limit"].(float64) != 50 {
		t.Errorf("limit = %v, want 50", got["limit"])
	}
	discover, ok := got["discover"].(map[string]any)
	if !ok {
		t.Fatalf("discover missing or wrong shape: %#v", got["discover"])
	}
	if discover["sort_by"] != "popularity.desc" {
		t.Errorf("discover.sort_by = %v, want popularity.desc", discover["sort_by"])
	}
	if discover["vote_count_gte"].(float64) != 300 {
		t.Errorf("discover.vote_count_gte = %v, want 300", discover["vote_count_gte"])
	}
	if discover["vote_average_gte"].(float64) != 6.5 {
		t.Errorf("discover.vote_average_gte = %v, want 6.5", discover["vote_average_gte"])
	}
	if discover["release_date_gte"] != "2010-01-01" {
		t.Errorf("discover.release_date_gte = %v, want 2010-01-01", discover["release_date_gte"])
	}
	if discover["original_language"] != "en" {
		t.Errorf("discover.original_language = %v, want en", discover["original_language"])
	}
	genres, ok := discover["with_genres"].([]any)
	if !ok || len(genres) != 2 {
		t.Fatalf("discover.with_genres = %#v", discover["with_genres"])
	}
	if genres[0].(float64) != 28 || genres[1].(float64) != 12 {
		t.Errorf("discover.with_genres values = %v %v", genres[0], genres[1])
	}
	certs, ok := discover["certifications"].([]any)
	if !ok || len(certs) != 2 {
		t.Fatalf("discover.certifications = %#v", discover["certifications"])
	}
	if certs[0].(string) != "PG" || certs[1].(string) != "PG-13" {
		t.Errorf("discover.certifications values = %v %v", certs[0], certs[1])
	}

	// Round-trip into the same runtime shape that catalog.SyncCollectionWithOptions
	// uses so the on-disk JSON layout stays compatible with the sync decoder.
	type runtimeDiscover struct {
		WithGenres       []int    `json:"with_genres,omitempty"`
		WithoutGenres    []int    `json:"without_genres,omitempty"`
		SortBy           string   `json:"sort_by"`
		VoteCountGte     int      `json:"vote_count_gte,omitempty"`
		VoteAverageGte   float64  `json:"vote_average_gte,omitempty"`
		ReleaseDateGte   string   `json:"release_date_gte,omitempty"`
		ReleaseDateLte   string   `json:"release_date_lte,omitempty"`
		Certifications   []string `json:"certifications,omitempty"`
		CertificationLte string   `json:"certification_lte,omitempty"`
		WithRuntimeGte   int      `json:"with_runtime_gte,omitempty"`
		WithRuntimeLte   int      `json:"with_runtime_lte,omitempty"`
		OriginalLanguage string   `json:"original_language,omitempty"`
	}
	type runtimeConfig struct {
		Mode      string           `json:"mode"`
		MediaType string           `json:"media_type"`
		Limit     *int             `json:"limit,omitempty"`
		Discover  *runtimeDiscover `json:"discover,omitempty"`
	}
	var cfg runtimeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("decode into runtime config: %v", err)
	}
	if cfg.Mode != "tmdb_discover" {
		t.Errorf("runtime mode = %q, want tmdb_discover", cfg.Mode)
	}
	if cfg.MediaType != "movie" {
		t.Errorf("runtime media_type = %q, want movie", cfg.MediaType)
	}
	if cfg.Limit == nil || *cfg.Limit != 50 {
		t.Errorf("runtime limit = %v, want 50", cfg.Limit)
	}
	if cfg.Discover == nil {
		t.Fatal("runtime discover missing")
	}
	if cfg.Discover.SortBy != "popularity.desc" {
		t.Errorf("runtime sort_by = %q", cfg.Discover.SortBy)
	}
	if cfg.Discover.VoteCountGte != 300 || cfg.Discover.VoteAverageGte != 6.5 {
		t.Errorf("runtime vote thresholds = %d / %v", cfg.Discover.VoteCountGte, cfg.Discover.VoteAverageGte)
	}
	if cfg.Discover.OriginalLanguage != "en" {
		t.Errorf("runtime original_language = %q", cfg.Discover.OriginalLanguage)
	}
	if len(cfg.Discover.WithGenres) != 2 || cfg.Discover.WithGenres[0] != 28 || cfg.Discover.WithGenres[1] != 12 {
		t.Errorf("runtime with_genres = %v", cfg.Discover.WithGenres)
	}
}

func TestBuildTMDBDiscoverSourceConfigMinimal(t *testing.T) {
	raw, err := buildTMDBDiscoverSourceConfig("tv", importTMDBDiscoverSpecBody{
		SortBy: "vote_average.desc",
	}, nil, false)
	if err != nil {
		t.Fatalf("buildTMDBDiscoverSourceConfig: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["mode"] != "tmdb_discover" || got["media_type"] != "tv" {
		t.Errorf("mode/media_type = %v / %v", got["mode"], got["media_type"])
	}
	if _, ok := got["limit"]; ok {
		t.Errorf("limit should be omitted when nil, got = %#v", got["limit"])
	}
	discover, ok := got["discover"].(map[string]any)
	if !ok {
		t.Fatalf("discover missing: %#v", got["discover"])
	}
	if discover["sort_by"] != "vote_average.desc" {
		t.Errorf("discover.sort_by = %v", discover["sort_by"])
	}
	if _, ok := discover["with_genres"]; ok {
		t.Errorf("with_genres should be omitted when empty")
	}
}

func TestValidateTMDBDiscoverRequest(t *testing.T) {
	good := importTMDBDiscoverRequest{
		LibraryID: 1,
		Title:     "Action",
		MediaType: "movie",
		Spec:      importTMDBDiscoverSpecBody{SortBy: "popularity.desc"},
	}
	if err := validateTMDBDiscoverRequest(good); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}

	bad := good
	bad.MediaType = "all"
	if err := validateTMDBDiscoverRequest(bad); err == nil {
		t.Fatal("expected error for media_type=all")
	}

	bad = good
	bad.Spec.SortBy = ""
	if err := validateTMDBDiscoverRequest(bad); err == nil {
		t.Fatal("expected error for missing sort_by")
	}

	bad = good
	limit := 0
	bad.Limit = &limit
	if err := validateTMDBDiscoverRequest(bad); err == nil {
		t.Fatal("expected error for limit=0")
	}
}

func TestBuildTraktSourceConfig(t *testing.T) {
	limit := 25
	raw, err := buildTraktSourceConfig("recommended", "movie", "prof-1", &limit, false)
	if err != nil {
		t.Fatalf("buildTraktSourceConfig: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["mode"] != "trakt_preset" || got["provider"] != "trakt" ||
		got["preset"] != "recommended" || got["media_type"] != "movie" ||
		got["profile_id"] != "prof-1" || got["limit"].(float64) != 25 {
		t.Fatalf("source config = %#v", got)
	}
	if url := buildTraktSourceURL("recommended", "movie", "prof-1"); url != "trakt://recommended/movie/prof-1" {
		t.Fatalf("source url = %q", url)
	}
}

func TestNormalizeCollectionManagementFields(t *testing.T) {
	mode, source, key, err := normalizeCollectionManagementFields("section", "recipe_gallery", "trakt:trending:movie:library:1")
	if err != nil {
		t.Fatalf("normalize section management: %v", err)
	}
	if mode != "section" || source != "recipe_gallery" || key != "trakt:trending:movie:library:1" {
		t.Fatalf("got %q/%q/%q", mode, source, key)
	}

	mode, source, key, err = normalizeCollectionManagementFields("", "ignored", "ignored")
	if err != nil {
		t.Fatalf("normalize manual management: %v", err)
	}
	if mode != "manual" || source != "" || key != "" {
		t.Fatalf("manual fields should be cleared, got %q/%q/%q", mode, source, key)
	}

	if _, _, _, err := normalizeCollectionManagementFields("section", "", ""); err == nil {
		t.Fatal("expected section-managed collection without key to fail")
	}

	mode, source, key, err = normalizeCollectionManagementFields("template_bundle", "core_defaults", "core_defaults:x:library:1")
	if err != nil {
		t.Fatalf("normalize template bundle management: %v", err)
	}
	if mode != "template_bundle" || source != "core_defaults" || key != "core_defaults:x:library:1" {
		t.Fatalf("got %q/%q/%q", mode, source, key)
	}
}

func TestTemplateBundleEligibilityByLibraryType(t *testing.T) {
	movieTemplate := templates.Template{ID: "movie", MediaKind: templates.MediaMovie}
	tvTemplate := templates.Template{ID: "tv", MediaKind: templates.MediaTV}

	movies := &models.MediaFolder{ID: 1, Name: "Movies", Type: "movies"}
	series := &models.MediaFolder{ID: 2, Name: "TV", Type: "series"}
	mixed := &models.MediaFolder{ID: 3, Name: "Mixed", Type: "mixed"}

	if !templateEligibleForLibrary(movieTemplate, movies) {
		t.Fatal("movie template should be eligible for movie libraries")
	}
	if templateEligibleForLibrary(tvTemplate, movies) {
		t.Fatal("tv template should not be eligible for movie libraries")
	}
	if templateEligibleForLibrary(movieTemplate, series) {
		t.Fatal("movie template should not be eligible for series libraries")
	}
	if !templateEligibleForLibrary(tvTemplate, series) {
		t.Fatal("tv template should be eligible for series libraries")
	}
	if !templateEligibleForLibrary(movieTemplate, mixed) || !templateEligibleForLibrary(tvTemplate, mixed) {
		t.Fatal("mixed libraries should accept movie and tv templates")
	}
}

func TestTemplateBundleManagementKeyIsStable(t *testing.T) {
	got := templateBundleManagementKey("core_defaults", "tmdb_popular_movies", 42)
	want := "core_defaults:tmdb_popular_movies:library:42"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTemplateBundleFeaturedDryRun(t *testing.T) {
	bundle, ok := templates.GetBundle("core_defaults")
	if !ok {
		t.Fatal("core_defaults bundle missing")
	}
	tmpl, ok := templates.Get("tmdb_trending_movies_week")
	if !ok {
		t.Fatal("tmdb_trending_movies_week template missing")
	}
	library := &models.MediaFolder{ID: 1, Name: "Movies", Type: "movies"}
	refs := map[templateBundleCollectionRefKey]templateBundleCollectionRef{
		{LibraryID: library.ID, TemplateID: tmpl.ID}: {
			Template: tmpl,
			Library:  library,
		},
	}
	handler := &LibraryCollectionHandler{}

	featured, failed := handler.applyTemplateBundleFeaturedSections(
		context.Background(),
		bundle,
		&templateBundleFeaturedRequest{
			Home:      &templateBundleFeaturedHome{LibraryID: library.ID, TemplateID: tmpl.ID},
			Libraries: map[int]string{library.ID: tmpl.ID},
		},
		map[int]*models.MediaFolder{library.ID: library},
		refs,
		true,
	)
	if len(failed) != 0 {
		t.Fatalf("failed = %+v", failed)
	}
	if len(featured) != 2 {
		t.Fatalf("featured count = %d, want 2", len(featured))
	}
	if featured[0].Surface != "home" || featured[0].Reason != "would_create" {
		t.Fatalf("home featured = %+v", featured[0])
	}
	if featured[1].Surface != "library" || featured[1].LibraryID != library.ID {
		t.Fatalf("library featured = %+v", featured[1])
	}
}

func TestTemplateBundleFeaturedSectionConfig(t *testing.T) {
	tmpl := templates.Template{ID: "tmdb_trending_movies_week"}
	library := &models.MediaFolder{ID: 42}
	raw, err := templateBundleFeaturedSectionConfig("core_defaults", "library", templateBundleCollectionRef{
		CollectionID: "lc_123",
		Template:     tmpl,
		Library:      library,
	})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["library_collection_id"] != "lc_123" ||
		got["generated_source"] != "template_bundle_featured" ||
		got["template_bundle"] != "core_defaults" ||
		got["template_id"] != "tmdb_trending_movies_week" ||
		got["surface"] != "library" ||
		got["library_id"].(float64) != 42 {
		t.Fatalf("unexpected config: %#v", got)
	}
}

func TestAllCollectionLibrariesSelected(t *testing.T) {
	selected := map[int]struct{}{1: {}, 2: {}}
	if !allCollectionLibrariesSelected(&models.LibraryCollection{LibraryIDs: []int{1, 2}}, selected) {
		t.Fatal("collection scoped only to selected libraries should be deletable")
	}
	if allCollectionLibrariesSelected(&models.LibraryCollection{LibraryIDs: []int{1, 3}}, selected) {
		t.Fatal("collection shared with unselected libraries should not be deletable")
	}
}

func TestLibraryCollectionGroupResponseUsesAPIFieldNames(t *testing.T) {
	body, err := json.Marshal(listGroupsResponse{
		Groups: toLibraryCollectionGroupResponses([]models.LibraryCollectionGroup{{
			ID:              "lcg_movies",
			LibraryID:       1,
			Name:            "Movies",
			Slug:            "movies",
			Kind:            models.GroupKindRegular,
			DefaultSortMode: models.GroupSortManual,
			SortOrder:       2,
		}}),
		UngroupedSortOrder: 9999,
	})
	if err != nil {
		t.Fatalf("marshal group response: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal group response: %v", err)
	}
	groups := got["groups"].([]any)
	group := groups[0].(map[string]any)
	if group["id"] != "lcg_movies" ||
		group["name"] != "Movies" ||
		group["default_sort_mode"] != "manual" ||
		group["sort_order"].(float64) != 2 {
		t.Fatalf("unexpected group JSON: %#v", group)
	}
	if _, ok := group["ID"]; ok {
		t.Fatalf("group response leaked Go field names: %#v", group)
	}
}
