package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeCatalog backs the catalog-items operations: it records the request
// the seam received and answers a fixed page.
type fakeCatalog struct {
	sourceOrder bool
	err         error
	lastReq     catalogpkg.CatalogRequest
	lastViewer  handlers.ItemViewer
	lastGroup   bool
	lastGroups  catalogpkg.AudiobookGroupsQuery
}

func (f *fakeCatalog) ContextAccessFilter(ctx context.Context, opts handlers.AccessFilterOptions) (catalogpkg.AccessFilter, error) {
	return catalogpkg.AccessFilter{UserID: claimsFrom(ctx).UserID, ProfileID: "p-owner", AllowedLibraryIDs: []int{1, 2}, PresentationLibraryID: opts.PresentationLibraryID, ScopeFilesToLibrary: opts.ScopeFilesToLibrary, SelectedFileID: opts.SelectedFileID}, nil
}

func fakeListingCard(id string) handlers.CollectionItemView {
	return handlers.CollectionItemView{ContentID: id, Type: "movie", Title: "Heat", Year: 1995, Genres: []string{"Crime"}, Status: "matched"}
}

func (f *fakeCatalog) Browse(_ context.Context, v handlers.ItemViewer, req catalogpkg.CatalogRequest, grouped bool) (handlers.CatalogBrowseView, error) {
	if f.err != nil {
		return handlers.CatalogBrowseView{}, f.err
	}
	f.lastReq, f.lastViewer, f.lastGroup = req, v, grouped
	view := handlers.CatalogBrowseView{Total: 3, TotalExact: true, Snapshot: "2026-01-02T03:04:05.678Z",
		EffectiveSort: &handlers.EffectiveSortView{Field: "title", Order: "asc"}}
	if f.sourceOrder {
		view.EffectiveSort = nil
		view.ResolvedSort = new(catalogpkg.QuerySort)
	}
	ids := []string{"movie:heat-1995", "movie:alien-1979", "movie:blade-runner-1982"}
	for i := req.Offset; i < len(ids) && len(view.Items) < req.Limit; i++ {
		view.Items = append(view.Items, fakeListingCard(ids[i]))
	}
	view.HasMore = req.Offset+len(view.Items) < len(ids)
	if req.SearchQuery != "" {
		view.SearchDiagnostics = &handlers.SearchDiagnosticsView{Provider: "postgres", Mode: "keyword"}
	}
	return view, nil
}

func (f *fakeCatalog) Filters(_ context.Context, _ handlers.ItemViewer, req catalogpkg.CatalogRequest, technical bool) (handlers.CatalogFiltersView, error) {
	if f.err != nil {
		return handlers.CatalogFiltersView{}, f.err
	}
	f.lastReq = req
	view := handlers.CatalogFiltersView{Genres: []string{"Crime"}, Authors: []string{"Frank Herbert"}}
	if technical {
		res := []string{"2160p"}
		view.Resolutions, view.AudioLanguages, view.SubtitleLanguages = &res, &[]string{}, &[]string{"en"}
	}
	return view, nil
}

func (f *fakeCatalog) SearchFacet(_ context.Context, _ handlers.ItemViewer, _ catalogpkg.CatalogRequest, facet, prefix string, limit int) (handlers.CatalogFacetSearchView, error) {
	if f.err != nil {
		return handlers.CatalogFacetSearchView{}, f.err
	}
	if facet == "author" && strings.HasPrefix("frank herbert", prefix) {
		return handlers.CatalogFacetSearchView{Matches: []string{"Frank Herbert"}, HasMore: limit == 1}, nil
	}
	return handlers.CatalogFacetSearchView{Matches: []string{}}, nil
}

func (f *fakeCatalog) AudiobookGroups(_ context.Context, v handlers.ItemViewer, q catalogpkg.AudiobookGroupsQuery) (handlers.AudiobookGroupsView, error) {
	if f.err != nil {
		return handlers.AudiobookGroupsView{}, f.err
	}
	f.lastGroups, f.lastViewer = q, v
	offset := 0
	if q.After != nil {
		offset = 1
	}
	view := handlers.AudiobookGroupsView{Total: 2, TotalExact: q.IncludeTotal, HasMore: offset == 0 && q.Limit == 1}
	names := []string{"Frank Herbert", "Ursula K. Le Guin"}
	for i := offset; i < len(names) && len(view.Groups) < q.Limit; i++ {
		view.Groups = append(view.Groups, handlers.AudiobookGroupView{Name: names[i], ItemCount: 3, TotalDurationSeconds: 7200, PosterURLs: nil})
	}
	if view.HasMore {
		view.Next = &catalogpkg.AudiobookGroupCursor{GroupKey: "frank herbert", Value: 3}
	}
	return view, nil
}

func notFoundItem() error {
	return &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Item not found"}
}

func (f *fakeCatalog) ItemDetail(_ context.Context, v handlers.ItemViewer, id string) (*catalogpkg.ItemDetail, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.lastViewer = v
	if id != "movie:heat-1995" {
		return nil, notFoundItem()
	}
	fileID := 120
	imdbVotes := int64(673852)
	return &catalogpkg.ItemDetail{ContentID: id, Type: "movie", Title: "Heat", Year: 1995, Genres: []string{"Crime"}, Tagline: "A Los Angeles crime saga",
		RatingSources: []catalogpkg.ItemRatingSourceInfo{
			{Source: models.RatingSourceIMDB, Score: 83, Votes: &imdbVotes},
			{Source: models.RatingSourceMDBList, Score: 86},
		},
		Cast:           []catalogpkg.CastCredit{{Name: "Al Pacino", Character: "Vincent Hanna", PersonID: "7"}},
		SeasonUserData: &catalogpkg.SeasonUserData{Played: true, WatchedCount: 1, LastFileID: &fileID},
		UserState:      &catalogpkg.ItemUserState{Played: true, IsFavorite: true},
		Versions:       []catalogpkg.FileVersion{{FileID: 120, FilePath: "/media/movies/Heat.mkv", Resolution: "2160p", AddedAt: fixedTime()}},
		Subtitles:      []catalogpkg.SubtitleInfo{}, OverlaySummary: &models.OverlaySummary{Resolution: "4K"},
		WorkFormats: []catalogpkg.WorkFormatSummary{{Type: "ebook", ContentID: "ebook:heat", LibraryID: 2}}}, nil
}

func (f *fakeCatalog) ItemVersions(_ context.Context, _ handlers.ItemViewer, id string) ([]catalogpkg.FileVersion, error) {
	if f.err != nil {
		return nil, f.err
	}
	if id != "movie:heat-1995" {
		return nil, notFoundItem()
	}
	return []catalogpkg.FileVersion{{FileID: 120, Resolution: "2160p", AddedAt: fixedTime()}}, nil
}

func (f *fakeCatalog) MangaFiles(_ context.Context, _ handlers.ItemViewer, id string) (*catalogpkg.MangaSeriesFiles, error) {
	if f.err != nil {
		return nil, f.err
	}
	if id != "series:berserk" {
		return nil, notFoundItem()
	}
	return &catalogpkg.MangaSeriesFiles{FolderPaths: []string{"/media/manga/Berserk"}, Files: []catalogpkg.MangaChapterFile{{ContentID: "manga:berserk-c001", Title: "Chapter 1", FileName: "c001.cbz", FileSize: 42}}}, nil
}

func fakeEpisode(n int) handlers.EpisodeView {
	return handlers.EpisodeView{ContentID: "episode:severance-s01e0" + string(rune('0'+n)), SeasonNumber: 1, EpisodeNumber: n, Title: "Good News About Hell", AirDate: "2022-02-18", Runtime: 57,
		Files: []handlers.EpisodeFileView{{FileID: 200 + n, Resolution: "1080p", FileSize: 1}}, UserData: &catalogpkg.SeasonUserData{Played: n == 1, WatchedCount: 1}}
}

func (f *fakeCatalog) ItemEpisodes(_ context.Context, _ handlers.ItemViewer, id string) ([]handlers.EpisodeView, error) {
	if f.err != nil {
		return nil, f.err
	}
	if id != "series:severance-S01" {
		return nil, notFoundItem()
	}
	return []handlers.EpisodeView{fakeEpisode(1), fakeEpisode(2)}, nil
}

func fakeSeason() handlers.SeasonView {
	return handlers.SeasonView{ContentID: "series:severance-S01", PlayContentID: "episode:severance-s01e02", SeasonNumber: 1, Title: "Season 1", AirDate: "2022-02-18", EpisodeCount: 9,
		UserData: &catalogpkg.SeasonUserData{WatchedCount: 1, UnplayedCount: 8}}
}

func (f *fakeCatalog) SeriesSeasons(_ context.Context, _ handlers.ItemViewer, id string, includeArtwork bool) ([]handlers.SeasonView, error) {
	if f.err != nil {
		return nil, f.err
	}
	if id != "series:severance" {
		return nil, notFoundItem()
	}
	season := fakeSeason()
	if includeArtwork {
		season.PosterURL = "https://images.example/season.webp"
		season.PosterThumbhash = "placeholder"
	}
	return []handlers.SeasonView{season}, nil
}

func (f *fakeCatalog) SeriesSeason(_ context.Context, _ handlers.ItemViewer, id string, num int) (handlers.SeasonView, error) {
	if f.err != nil {
		return handlers.SeasonView{}, f.err
	}
	if id != "series:severance" || num != 1 {
		return handlers.SeasonView{}, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Season not found"}
	}
	return fakeSeason(), nil
}

func (f *fakeCatalog) SeasonEpisodes(_ context.Context, _ handlers.ItemViewer, id string, num int) ([]handlers.EpisodeView, error) {
	if f.err != nil {
		return nil, f.err
	}
	if id != "series:severance" || num != 1 {
		return nil, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Season not found"}
	}
	return []handlers.EpisodeView{fakeEpisode(1), fakeEpisode(2)}, nil
}

func catalogDeps(t *testing.T) (Dependencies, *fakeCatalog) {
	t.Helper()
	deps := libraryViewDeps(t)
	deps.CursorSecret = []byte("catalog-cursor-key")
	fake := &fakeCatalog{}
	deps.CatalogAccess, deps.CatalogBrowse, deps.CatalogItems = fake, fake, fake
	return deps, fake
}

func TestListCatalogItems(t *testing.T) {
	deps, fake := catalogDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/catalog?limit=2&sort=-release_date&content_rating=R&content_rating=PG-13&year_min=1990&q=heat&library_id=1&image_size=small", "", viewerHeaders())
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var body struct {
		Items []map[string]json.RawMessage `json:"items"`
		Page  PageInfo                     `json:"page"`
		Total int                          `json:"total"`
		Diag  json.RawMessage              `json:"search_diagnostics"`
		Sort  json.RawMessage              `json:"effective_sort"`
	}
	decodeJSON(t, rec.Body, &body)
	if len(body.Items) != 2 || body.Total != 3 || !body.Page.HasMore || body.Page.NextCursor == "" || string(body.Sort) != `{"field":"title","order":"asc"}` || !strings.Contains(string(body.Diag), `"provider":"postgres"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if string(body.Items[0]["content_id"]) != `"movie:heat-1995"` || string(body.Items[0]["genres"]) != `["Crime"]` || string(body.Items[0]["keywords"]) != `[]` {
		t.Errorf("card = %v", body.Items[0])
	}
	req := fake.lastReq
	if req.Limit != 2 || req.Offset != 0 || req.SearchQuery != "heat" || req.Query.Sort.Field != "release_date" || req.Query.Sort.Order != "desc" || len(req.Query.LibraryIDs) != 1 || req.Query.LibraryIDs[0] != 1 {
		t.Fatalf("seam request = %+v", req)
	}
	ratings := 0
	for _, g := range req.Query.Groups {
		for _, r := range g.Rules {
			if r.Field == "content_rating" {
				ratings++
			}
		}
	}
	if ratings != 2 || fake.lastViewer.Access.ImageSize != "small" || fake.lastViewer.ProfileID != "p-owner" {
		t.Fatalf("ratings = %d viewer = %+v", ratings, fake.lastViewer)
	}
	// The next page resumes at the offset and pins the first page's snapshot.
	rec = do(t, h, http.MethodGet, "/api/v2/catalog?limit=2&sort=-release_date&content_rating=R&content_rating=PG-13&year_min=1990&q=heat&library_id=1&image_size=small&cursor="+body.Page.NextCursor, "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"has_more":false`) || fake.lastReq.Offset != 2 || fake.lastReq.SnapshotAt == nil {
		t.Fatalf("second page: %d %s offset=%d snapshot=%v", rec.Code, rec.Body.String(), fake.lastReq.Offset, fake.lastReq.SnapshotAt)
	}
	// The cursor is bound to the filters.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog?limit=2&cursor="+body.Page.NextCursor, "", viewerHeaders()), TypeInvalidCursor)
	// group=work reaches the seam as the grouped flag.
	rec = do(t, h, http.MethodGet, "/api/v2/catalog?group=work", "", viewerHeaders())
	if rec.Code != 200 || !fake.lastGroup {
		t.Fatalf("group=work: %d grouped=%v", rec.Code, fake.lastGroup)
	}
	// Validation: an unknown sort field, a source missing its required id, a
	// comma list where repeated keys are required, and a bad library id.
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog?sort=bogus", "", viewerHeaders()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.sort" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	p = requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog?source=section", "", viewerHeaders()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.section_id" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog?sort=title,year", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog?limit=2&limit=3", "", viewerHeaders()), TypeMalformedRequest)
	// Class denial: no profile.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog", "", nil), TypeAuthenticationRequired)
	// Seam failures keep the v1 decision.
	fake.err = &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Catalog source not found"}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog?source=user_collection&collection_id=x", "", viewerHeaders()), TypeNotFound)
	fake.err = &handlers.APIError{Status: http.StatusGatewayTimeout, Code: "search_timeout", Message: "Search took too long and was stopped"}
	rec = do(t, h, http.MethodGet, "/api/v2/catalog?q=slow", "", viewerHeaders())
	requireProblem(t, rec, TypeDependencyUnavailable)
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("timeout without Retry-After")
	}
	// Unwired service fails closed.
	deps.CatalogBrowse = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/catalog", "", viewerHeaders()), TypeDependencyUnavailable)
}

func TestCatalogStructuredWindowScope(t *testing.T) {
	deps, fake := catalogDeps(t)
	h := newTestHandler(t, deps)
	request := map[string]any{
		"source": "query", "library_id": "1", "limit": 2, "sort": "title", "query_limit": 3,
		"groups": []any{map[string]any{"match": "all", "rules": []any{map[string]any{"field": "year", "op": "gte", "value": 1990}}}},
	}
	send := func() *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		return do(t, h, http.MethodPost, "/api/v2/catalog/query", string(body), viewerHeaders())
	}
	rec := send()
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var first struct {
		Window string   `json:"window_cursor"`
		Page   PageInfo `json:"page"`
	}
	decodeJSON(t, rec.Body, &first)
	if first.Window == "" || first.Page.NextCursor == "" || !fake.lastReq.CursorPaging || fake.lastReq.Query.Limit == nil || *fake.lastReq.Query.Limit != 3 {
		t.Fatalf("window or query cap missing: %s %+v", rec.Body.String(), fake.lastReq)
	}
	request["cursor"], request["skip_total"], request["seek"] = first.Window, true, 2
	if rec = send(); rec.Code != 200 || fake.lastReq.Seek == nil || *fake.lastReq.Seek != 2 || !fake.lastReq.SkipTotal {
		t.Fatalf("window jump: %d %s %+v", rec.Code, rec.Body.String(), fake.lastReq)
	}
	request["seek"] = 0
	if rec = send(); rec.Code != 200 || fake.lastReq.Offset != 0 || fake.lastReq.After != nil {
		t.Fatalf("return to first window: %d %s %+v", rec.Code, rec.Body.String(), fake.lastReq)
	}
	request["limit"] = 1
	requireProblem(t, send(), TypeInvalidCursor)
	request["limit"], request["library_id"] = 2, "2"
	requireProblem(t, send(), TypeInvalidCursor)
	request["library_id"], request["query_limit"] = "1", 4
	requireProblem(t, send(), TypeInvalidCursor)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog?limit=2&cursor="+url.QueryEscape(first.Window), "", viewerHeaders()), TypeInvalidCursor)
	// JSON filters on GET receive the same field/operator validation as POST.
	bad := `[{"match":"all","rules":[{"field":"not_a_field","op":"is","value":true}]}]`
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog?groups="+url.QueryEscape(bad), "", viewerHeaders()), TypeValidationFailed)
	delete(request, "cursor")
	request["groups"] = json.RawMessage(bad)
	requireProblem(t, send(), TypeValidationFailed)
}

func TestCatalogUnsupportedStorageProblem(t *testing.T) {
	deps, fake := catalogDeps(t)
	h := newTestHandler(t, deps)
	for _, err := range []error{catalogpkg.ErrCatalogStorageUnsupported, &handlers.APIError{Status: http.StatusServiceUnavailable, Code: "catalog_storage_unsupported"}} {
		fake.err = err
		requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog", "", viewerHeaders()), TypeCapabilityUnsupported)
	}
}

func TestListAudiobookGroups(t *testing.T) {
	deps, fake := catalogDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/catalog/audiobook-groups?library_id=3&group_by=author&limit=1&sort=count&q=fr", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"Frank Herbert"`) || !strings.Contains(rec.Body.String(), `"poster_urls":[]`) || !strings.Contains(rec.Body.String(), `"has_more":true`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	q := fake.lastGroups
	if !q.CursorPaging || q.LibraryID != 3 || q.GroupBy != catalogpkg.AudiobookGroupByAuthor || q.Limit != 1 || q.Sort != "count" || q.SearchPrefix != "fr" || !q.IncludeTotal {
		t.Fatalf("query = %+v", q)
	}
	var body struct {
		Page PageInfo `json:"page"`
	}
	decodeJSON(t, rec.Body, &body)
	rec = do(t, h, http.MethodGet, "/api/v2/catalog/audiobook-groups?library_id=3&group_by=author&limit=1&sort=count&q=fr&cursor="+body.Page.NextCursor, "", viewerHeaders())
	if rec.Code != 200 || (fake.lastGroups.After == nil || fake.lastGroups.After.GroupKey != "frank herbert" || fake.lastGroups.After.Value != 3) || !strings.Contains(rec.Body.String(), `"Ursula K. Le Guin"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for _, changed := range []string{
		"library_id=4&group_by=author&limit=1&sort=count&q=fr",
		"library_id=3&group_by=narrator&limit=1&sort=count&q=fr",
		"library_id=3&group_by=author&limit=2&sort=count&q=fr",
		"library_id=3&group_by=author&limit=1&sort=name&q=fr",
		"library_id=3&group_by=author&limit=1&sort=count&q=xx",
	} {
		requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/audiobook-groups?"+changed+"&cursor="+body.Page.NextCursor, "", viewerHeaders()), TypeInvalidCursor)
	}
	rec = do(t, h, http.MethodGet, "/api/v2/catalog/audiobook-groups?library_id=3&group_by=author&limit=1&sort=count&q=fr&skip_total=true&cursor="+body.Page.NextCursor, "", viewerHeaders())
	if rec.Code != 200 || fake.lastGroups.IncludeTotal {
		t.Fatal(rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/v2/catalog/audiobook-groups?library_id=3&group_by=author&skip_total=true", "", viewerHeaders())
	if rec.Code != 200 || fake.lastGroups.IncludeTotal || !strings.Contains(rec.Body.String(), `"total_exact":false`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/audiobook-groups?group_by=author", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/audiobook-groups?library_id=3&group_by=publisher", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/audiobook-groups?library_id=x&group_by=author", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/audiobook-groups?library_id=3&group_by=author", "", bearer(memberToken)), TypeValidationFailed)
}

func TestCatalogFiltersAndFacetSearch(t *testing.T) {
	deps, fake := catalogDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/catalog/filters?library_id=1&type=audiobook", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"technical":{"resolutions":["2160p"],"audio_languages":[],"subtitle_languages":["en"]}`) || !strings.Contains(rec.Body.String(), `"studios":[]`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if fake.lastReq.Query.MediaScope != "audiobook" || len(fake.lastReq.Query.LibraryIDs) != 1 {
		t.Fatalf("seam request = %+v", fake.lastReq)
	}
	rec = do(t, h, http.MethodGet, "/api/v2/catalog/filters?skip_technical=true", "", viewerHeaders())
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"technical"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/filters?skip_technical=1", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/filters?source=person", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/filters", "", bearer(memberToken)), TypeValidationFailed)

	rec = do(t, h, http.MethodGet, "/api/v2/catalog/filters/search?facet=author&q=fra&limit=1", "", viewerHeaders())
	if rec.Code != 200 || rec.Body.String() != `{"matches":["Frank Herbert"],"has_more":true}`+"\n" {
		t.Fatal(rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/v2/catalog/filters/search?facet=narrator&q=zz", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"matches":[]`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/filters/search?q=fra", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/filters/search?facet=color", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/filters/search?facet=author&limit=500", "", viewerHeaders()), TypeValidationFailed)
	fake.err = &handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "unsupported facet"}
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/filters/search?facet=author", "", viewerHeaders()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.facet" {
		t.Fatalf("errors = %+v", p.Errors)
	}
}

func TestQueryCatalogItems(t *testing.T) {
	deps, fake := catalogDeps(t)
	h := newTestHandler(t, deps)
	body := `{"match":"all","groups":[{"match":"any","rules":[{"field":"genre","op":"contains","value":"Crime"}]}],"sort":"title","order":"asc","library_id":"1","limit":10}`
	rec := do(t, h, http.MethodPost, "/api/v2/catalog/query?image_size=large", body, viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"content_id":"movie:heat-1995"`) || !strings.Contains(rec.Body.String(), `"page":{"has_more":false}`) || !strings.Contains(rec.Body.String(), `"total":3`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	q := fake.lastReq
	if len(q.Query.LibraryIDs) != 1 || q.Query.LibraryIDs[0] != 1 || q.Limit != 10 || q.Query.Sort.Field != "title" || len(q.Query.Groups) != 1 || q.Query.Groups[0].Rules[0].Value != "Crime" || fake.lastViewer.Access.ImageSize != "large" {
		t.Fatalf("query = %+v viewer = %+v", q, fake.lastViewer)
	}
	rec = do(t, h, http.MethodPost, "/api/v2/catalog/query", `{}`, viewerHeaders())
	if rec.Code != 200 || fake.lastReq.Limit != 50 {
		t.Fatalf("defaults: %d limit=%d", rec.Code, fake.lastReq.Limit)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/query", `{"limit":500}`, viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/query", `{"library_id":"0"}`, viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/query", `{"unknown":1}`, viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/query", `{}`, bearer(memberToken)), TypeValidationFailed)
	fake.err = &handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Invalid filter: unknown field"}
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/query", `{}`, viewerHeaders()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.source" {
		t.Fatalf("errors = %+v", p.Errors)
	}
}

func TestGetCatalogItem(t *testing.T) {
	deps, fake := catalogDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/catalog/items/movie:heat-1995?library_id=2&file_id=120", "", viewerHeaders())
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var body map[string]json.RawMessage
	decodeJSON(t, rec.Body, &body)
	for k, want := range map[string]string{
		"content_id": `"movie:heat-1995"`, "tagline": `"A Los Angeles crime saga"`, "keywords": `[]`, "crew": `[]`, "subtitles": `[]`,
		"user_state": `{"played":true,"is_favorite":true,"in_watchlist":false}`, "overlay_summary": `{"resolution":"4K"}`,
		"work_formats": `[{"type":"ebook","content_id":"ebook:heat","library_id":"2"}]`,
	} {
		if string(body[k]) != want {
			t.Errorf("%s = %s, want %s", k, body[k], want)
		}
	}
	if !strings.Contains(string(body["user_data"]), `"last_file_id":"120"`) || !strings.Contains(string(body["versions"]), `"file_id":"120"`) || !strings.Contains(string(body["versions"]), `"added_at":"2026-01-02T03:04:05.678Z"`) {
		t.Errorf("user_data = %s versions = %s", body["user_data"], body["versions"])
	}
	if string(body["status"]) != `""` {
		t.Errorf("status = %s; the detail service does not load the match state", body["status"])
	}
	if fake.lastViewer.Access.PresentationLibraryID == nil || *fake.lastViewer.Access.PresentationLibraryID != 2 || fake.lastViewer.Access.SelectedFileID != 120 || fake.lastViewer.Access.ScopeFilesToLibrary {
		t.Fatalf("viewer = %+v", fake.lastViewer.Access)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/items/movie:nope", "", viewerHeaders()), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/items/movie:heat-1995?file_id=abc", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/items/movie:heat-1995", "", bearer(memberToken)), TypeValidationFailed)

	rec = do(t, h, http.MethodGet, "/api/v2/catalog/items/movie:heat-1995/versions", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"items":[{"file_id":"120"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/items/movie:nope/versions", "", viewerHeaders()), TypeNotFound)

	rec = do(t, h, http.MethodGet, "/api/v2/catalog/items/series:berserk/manga-files", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"folder_paths":["/media/manga/Berserk"]`) || !strings.Contains(rec.Body.String(), `"file_name":"c001.cbz"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/items/movie:heat-1995/manga-files", "", viewerHeaders()), TypeNotFound)

	rec = do(t, h, http.MethodGet, "/api/v2/catalog/items/series:severance-S01/episodes", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"air_date":"2022-02-18"`) || !strings.Contains(rec.Body.String(), `"files":[{"file_id":"201"`) || !strings.Contains(rec.Body.String(), `"user_data":{"watched_count":1,"unplayed_count":0,"in_progress_count":0,"played":true}`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/items/series:nope/episodes", "", viewerHeaders()), TypeNotFound)
}

func TestSeriesSeasons(t *testing.T) {
	deps, _ := catalogDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/catalog/series/series:severance/seasons", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"items":[{"content_id":"series:severance-S01","play_content_id":"episode:severance-s01e02"`) || !strings.Contains(rec.Body.String(), `"air_date":"2022-02-18"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/series/series:nope/seasons", "", viewerHeaders()), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/series/series:severance/seasons", "", bearer(memberToken)), TypeValidationFailed)

	rec = do(t, h, http.MethodGet, "/api/v2/catalog/series/series:severance/seasons/1", "", viewerHeaders())
	if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), `{"content_id":"series:severance-S01"`) || !strings.Contains(rec.Body.String(), `"episode_count":9`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/series/series:severance/seasons/2", "", viewerHeaders()), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/series/series:severance/seasons/one", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/series/series:severance/seasons/-1", "", viewerHeaders()), TypeValidationFailed)
}

func TestListCatalogItemsPreservesResolvedSourceOrderCursors(t *testing.T) {
	deps, fake := catalogDeps(t)
	fake.sourceOrder = true
	h := newTestHandler(t, deps)
	path := "/api/v2/catalog?source=library_collection&collection_id=collection&limit=1"
	first := do(t, h, http.MethodGet, path, "", viewerHeaders())
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	var body struct {
		Page         PageInfo `json:"page"`
		WindowCursor string   `json:"window_cursor"`
	}
	decodeJSON(t, first.Body, &body)
	for _, query := range []string{"&cursor=" + body.Page.NextCursor, "&cursor=" + body.WindowCursor + "&seek=1"} {
		response := do(t, h, http.MethodGet, path+query, "", viewerHeaders())
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		if fake.lastReq.ResolvedSort == nil || *fake.lastReq.ResolvedSort != (catalogpkg.QuerySort{}) {
			t.Fatalf("source-order sentinel lost: %+v", fake.lastReq.ResolvedSort)
		}
	}
}

func TestSeriesSeasonsArtwork(t *testing.T) {
	deps, _ := catalogDeps(t)
	h := newTestHandler(t, deps)
	capability := do(t, h, http.MethodGet, Prefix+"/images/capabilities", "", viewerHeaders())
	var discovery ImageCapabilities
	decodeJSON(t, capability.Body, &discovery)
	if capability.Code != http.StatusOK || discovery.SeasonListArtworkParam != "include_artwork" {
		t.Fatal(capability.Code, discovery)
	}
	var baseline Season
	for _, value := range []string{"", "true", "false"} {
		t.Run("include_artwork="+value, func(t *testing.T) {
			path := Prefix + "/catalog/series/series:severance/seasons"
			if value != "" {
				path += "?" + discovery.SeasonListArtworkParam + "=" + value
			}
			rec := do(t, h, http.MethodGet, path, "", viewerHeaders())
			if rec.Code != http.StatusOK {
				t.Fatal(rec.Code, rec.Body.String())
			}
			var body SeasonCollection
			decodeJSON(t, rec.Body, &body)
			if len(body.Items) != 1 {
				t.Fatal(body)
			}
			season := body.Items[0]
			if value == "false" {
				if strings.Contains(rec.Body.String(), `"poster_url"`) || strings.Contains(rec.Body.String(), `"poster_thumbhash"`) {
					t.Fatal("artwork fields present", rec.Body.String())
				}
			} else if season.PosterURL == "" || season.PosterThumbhash == "" {
				t.Fatal("artwork absent", season)
			}
			season.PosterURL = ""
			season.PosterThumbhash = ""
			if value == "" {
				baseline = season
			} else if !reflect.DeepEqual(baseline, season) {
				t.Fatal("non-artwork metadata changed", season)
			}
		})
	}
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/catalog/series/series:severance/seasons?include_artwork=invalid", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/catalog/series/series:severance/seasons/1?include_artwork=false", "", viewerHeaders()), TypeValidationFailed)
}

func TestCatalogStructuredSearchRelevance(t *testing.T) {
	for _, groups := range []string{`[]`, `[{"match":"all","rules":[{"field":"year","op":"gte","value":2000}]}]`} {
		t.Run(groups, func(t *testing.T) {
			deps, fake := catalogDeps(t)
			h := newTestHandler(t, deps)
			rec := do(t, h, http.MethodPost, "/api/v2/catalog/query", `{"source":"query","q":"heat","sort":"relevance","order":"desc","groups":`+groups+`}`, viewerHeaders())
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if fake.lastReq.Query.Sort.Field != "relevance" || fake.lastReq.SearchQuery != "heat" {
				t.Fatalf("lost search semantics: %+v", fake.lastReq)
			}
		})
	}
	deps, _ := catalogDeps(t)
	h := newTestHandler(t, deps)
	for _, body := range []string{
		`{"q":"heat","groups":[{"match":"all","rules":[{"field":"not-a-field","op":"eq","value":1}]}]}`,
		`{"source":"query","sort":"relevance","groups":[]}`,
		`{"source":"favorites","q":"heat","sort":"relevance","groups":[]}`,
	} {
		requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/query", body, viewerHeaders()), TypeValidationFailed)
	}
}

type fakeCatalogSettings map[string]string

func (f fakeCatalogSettings) Get(_ context.Context, key string) (string, error) { return f[key], nil }

// The library scope is opt-in through catalog.scope_versions_to_library and
// only reaches a read that named a library.
func TestGetCatalogItemScopesVersionsToLibraryWhenEnabled(t *testing.T) {
	deps, fake := catalogDeps(t)
	deps.CatalogSettings = fakeCatalogSettings{"catalog.scope_versions_to_library": "true"}
	h := newTestHandler(t, deps)

	if rec := do(t, h, http.MethodGet, "/api/v2/catalog/items/movie:heat-1995?library_id=2", "", viewerHeaders()); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if !fake.lastViewer.Access.ScopeFilesToLibrary || fake.lastViewer.Access.PresentationLibraryID == nil {
		t.Fatalf("viewer = %+v", fake.lastViewer.Access)
	}

	if rec := do(t, h, http.MethodGet, "/api/v2/catalog/items/movie:heat-1995", "", viewerHeaders()); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if fake.lastViewer.Access.ScopeFilesToLibrary || fake.lastViewer.Access.PresentationLibraryID != nil {
		t.Fatalf("viewer = %+v", fake.lastViewer.Access)
	}

	deps.CatalogSettings = nil
	h = newTestHandler(t, deps)
	if rec := do(t, h, http.MethodGet, "/api/v2/catalog/items/movie:heat-1995?library_id=2", "", viewerHeaders()); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if fake.lastViewer.Access.ScopeFilesToLibrary {
		t.Fatalf("viewer = %+v", fake.lastViewer.Access)
	}
}
