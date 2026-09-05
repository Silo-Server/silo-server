package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/usercollections"
)

// --- stage B admin fakes on fakeLibraryAdmin ---

func (f *fakeLibraryAdmin) knownLibrary(id int) error {
	if f.err != nil {
		return f.err
	}
	for _, v := range f.libraries {
		if v.ID == id {
			return nil
		}
	}
	return notFoundLibrary()
}

func fakeQueueStatus(id int) handlers.MetadataMatchQueueStatusView {
	return handlers.MetadataMatchQueueStatusView{LibraryID: id, MovieCount: 2, SeriesCount: 1, RawFileCount: 1, TotalCount: 4, PendingCount: 3, ParkedCount: 1}
}

func (f *fakeLibraryAdmin) GetMetadataMatchQueue(_ context.Context, id, limit, offset int) (handlers.MetadataMatchQueueDetailView, error) {
	if err := f.knownLibrary(id); err != nil {
		return handlers.MetadataMatchQueueDetailView{}, err
	}
	f.lastLimit, f.lastOffset = limit, offset
	parked := fixedTime()
	view := handlers.MetadataMatchQueueDetailView{
		Limit: limit, Offset: offset,
		Movies: []handlers.MovieMatchQueueEntryView{{MediaFileID: 120, MediaFolderID: id, FilePath: "/media/movies/Heat (1995)/Heat.mkv", FirstQueuedAt: fixedTime(), AvailableAt: fixedTime(),
			AttemptCount: 1, State: "parked", FailureKind: "no_match", FailureDetail: json.RawMessage(`{"candidates":0}`), MatcherRevision: 3, ParkedAt: &parked, UpdatedAt: fixedTime()}},
		Series:     []handlers.SeriesMatchQueueEntryView{{MediaFolderID: id, ObservedRootPath: "/media/tv/Severance", FirstQueuedAt: fixedTime(), AvailableAt: fixedTime(), State: "pending", UpdatedAt: fixedTime()}},
		RawFiles:   []handlers.RawMatchBacklogEntryView{{MediaFileID: 121, MediaFolderID: id, FilePath: "/media/movies/unknown.mkv", BaseTitle: "unknown", CreatedAt: fixedTime(), UpdatedAt: fixedTime()}},
		MovieTotal: 2, SeriesTotal: 1, RawFileTotal: 1,
	}
	view.LibraryID, view.MovieCount, view.SeriesCount, view.RawFileCount, view.TotalCount, view.PendingCount, view.ParkedCount = id, 2, 1, 1, 4, 3, 1
	if offset > 0 {
		view.Movies, view.Series, view.RawFiles = nil, nil, nil
	}
	return view, nil
}

func (f *fakeLibraryAdmin) RetryMetadataMatchQueue(_ context.Context, id int) (handlers.MetadataMatchQueueActionView, error) {
	if err := f.knownLibrary(id); err != nil {
		return handlers.MetadataMatchQueueActionView{}, err
	}
	return handlers.MetadataMatchQueueActionView{Status: "queued", LibraryID: id, RawFileRetried: 1, Queue: fakeQueueStatus(id)}, nil
}

func (f *fakeLibraryAdmin) CancelMetadataMatchQueue(_ context.Context, id int) (handlers.MetadataMatchQueueActionView, error) {
	if err := f.knownLibrary(id); err != nil {
		return handlers.MetadataMatchQueueActionView{}, err
	}
	return handlers.MetadataMatchQueueActionView{Status: "cancelled", LibraryID: id, MovieCancelled: 2, SeriesCancelled: 1, RawFileCancelled: 1, TotalCancelled: 4, Queue: handlers.MetadataMatchQueueStatusView{LibraryID: id}}, nil //nolint:misspell // v1 wire value
}

func (f *fakeLibraryAdmin) RefreshLibraryMetadata(_ context.Context, id, userID int, mode adminjob.LibraryRefreshMode) (*models.AdminJob, error) {
	if err := f.knownLibrary(id); err != nil {
		return nil, err
	}
	f.lastUserID = userID
	f.lastRefreshMode = mode
	for _, v := range f.libraries {
		if v.ID == id && v.Name == "Busy" {
			return nil, &handlers.APIError{Status: http.StatusConflict, Code: "conflict", Message: "A metadata refresh is already queued or running for this library"}
		}
	}
	return &models.AdminJob{ID: "job-2", JobType: "library_refresh", Status: "queued", CreatedByUserID: userID,
		RequestPayload: json.RawMessage(`{"library_id":1,"mode":"` + string(mode) + `"}`), Message: "Queued " + string(mode) + " library metadata refresh", RequestedAt: fixedTime()}, nil
}

func (f *fakeLibraryAdmin) UploadLibraryPoster(_ context.Context, id int, contentType string, data []byte) (handlers.LibraryView, error) {
	if err := f.knownLibrary(id); err != nil {
		return handlers.LibraryView{}, err
	}
	f.lastPosterType, f.lastPosterSize = contentType, len(data)
	view := libraryFixture(id, "Movies")
	view.PosterURL = "https://s3.example.test/library-posters/1.png"
	return view, nil
}

func (f *fakeLibraryAdmin) DeleteLibraryPoster(_ context.Context, id int) error {
	return f.knownLibrary(id)
}

func (f *fakeLibraryAdmin) LibraryProviders(_ context.Context, id int) (map[string][]handlers.ChainLevelEntryView, error) {
	if err := f.knownLibrary(id); err != nil {
		return nil, err
	}
	return map[string][]handlers.ChainLevelEntryView{
		"movie": {{PluginInstallationID: 3, CapabilityID: "tmdb", ProviderSlug: "tmdb", Priority: 0, Enabled: true}},
	}, nil
}

func (f *fakeLibraryAdmin) SetLibraryProviders(_ context.Context, id int, levels map[string][]handlers.ProviderChainEntryInput) error {
	if err := f.knownLibrary(id); err != nil {
		return err
	}
	f.lastChain = levels
	return nil
}

// --- viewer fakes ---

type fakeLibraryViews struct {
	err error
}

func fakeCard() handlers.SectionItemView {
	imdb := 8.3
	stamp := "2026-01-02T03:04:05Z"
	pos, dur := 1200.5, 10200.0
	return handlers.SectionItemView{ContentID: "movie:heat-1995", Type: "movie", Title: "Heat", Year: 1995, Runtime: 170, Genres: []string{"Crime"}, Keywords: nil,
		ContentRating: "R", Status: "matched", RatingIMDB: &imdb, PosterURL: "https://s3.example.test/poster.jpg", PositionSeconds: &pos, DurationSeconds: &dur, ProgressUpdatedAt: &stamp,
		OverlaySummary: &models.OverlaySummary{Resolution: "4K", HDR: "Dolby Vision"}, UserState: &handlers.ItemUserStateView{InWatchlist: true}, ItemSource: "in_progress"}
}

func (f *fakeLibraryViews) LibraryLayout(_ context.Context, libraryID int) (handlers.SectionLayoutView, error) {
	if f.err != nil {
		return handlers.SectionLayoutView{}, f.err
	}
	return handlers.SectionLayoutView{Sections: []handlers.SectionLayoutEntryView{{ID: "continue_watching", SectionType: "continue_watching", Title: "Continue Watching", ItemLimit: 20}, {ID: "recently_added", SectionType: "recently_added", Title: "Recently Added", ItemLimit: 20, Customized: true}}}, nil
}

func (f *fakeLibraryViews) LibrarySections(_ context.Context, libraryID int, viewer handlers.SectionViewer) (handlers.SectionsView, error) {
	if f.err != nil {
		return handlers.SectionsView{}, f.err
	}
	return handlers.SectionsView{Sections: []handlers.SectionView{{ID: "continue_watching", SectionType: "continue_watching", Title: "Continue Watching", ItemLimit: 20, TotalCount: 1, Items: []handlers.SectionItemView{fakeCard()}}}}, nil
}

func (f *fakeLibraryViews) LibrarySectionItems(_ context.Context, libraryID int, sectionID string, viewer handlers.SectionViewer) (handlers.SectionView, error) {
	if f.err != nil {
		return handlers.SectionView{}, f.err
	}
	if sectionID != "continue_watching" {
		return handlers.SectionView{}, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Section not found"}
	}
	return handlers.SectionView{ID: sectionID, SectionType: "continue_watching", Title: "Continue Watching", ItemLimit: 20, TotalCount: 1, Items: []handlers.SectionItemView{fakeCard()}}, nil
}

func (f *fakeLibraryViews) LibraryCollectionsTab(_ context.Context, libraryID, userID int, profileID string) (handlers.LibraryCollectionTabView, error) {
	if f.err != nil {
		return handlers.LibraryCollectionTabView{}, f.err
	}
	creator := profileID
	return handlers.LibraryCollectionTabView{
		LibraryID: libraryID,
		Collections: []handlers.LibraryCollectionView{{ID: "c1", LibraryID: libraryID, LibraryIDs: []int{libraryID}, Slug: "oscar-winners", Title: "Oscar Winners", CollectionType: "manual", Visibility: "visible",
			QueryDefinition: json.RawMessage(`{"filters":[]}`), ManagementMode: "manual", ItemCount: 12, CreatedAt: "2026-01-02T03:04:05Z", UpdatedAt: "2026-01-02T03:04:05Z"}},
		Groups:    []handlers.LibraryCollectionTabGroupView{{ID: "g1", Name: "Mine", Kind: models.GroupKindUserCollections, SortMode: models.GroupSortNameAsc, Collections: []handlers.LibraryCollectionTabEntryView{{ID: "u1", Title: "Rainy days", ItemCount: 4, CreatorProfileID: &creator}}}},
		Ungrouped: &handlers.LibraryCollectionTabUngroupedView{SortOrder: 9999, Collections: []handlers.LibraryCollectionTabEntryView{{ID: "c1", Title: "Oscar Winners", ItemCount: 12, Featured: true}}},
	}, nil
}

func (f *fakeLibraryViews) LibraryUserCollections(_ context.Context, libraryID, userID int, profileID string) ([]usercollections.ServerVisibleCollection, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []usercollections.ServerVisibleCollection{{ID: "u1", CreatorProfileID: profileID, Name: "Rainy days", CollectionType: "manual", ItemCount: 4, CreatedAt: "2026-01-02T03:04:05Z", UpdatedAt: "2026-01-02T03:04:05Z"}}, nil
}

func (f *fakeLibraryViews) LibraryCollectionItems(_ context.Context, libraryID int, collectionID string, access catalogpkg.AccessFilter) ([]handlers.CollectionItemView, error) {
	if f.err != nil {
		return nil, f.err
	}
	switch collectionID {
	case "c1":
		added := fixedTime()
		return []handlers.CollectionItemView{{ContentID: "movie:heat-1995", Type: "movie", Title: "Heat", Year: 1995, Genres: []string{"Crime"}, Status: "matched", AddedAt: &added,
			WorkFormats: []catalogpkg.WorkFormatSummary{{Type: "ebook", ContentID: "ebook:heat", LibraryID: 2}}}}, nil
	case "broken":
		return nil, &handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "validating smart collection query definition: unknown filter"}
	}
	return nil, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Collection not found"}
}

func libraryViewDeps(t *testing.T) Dependencies {
	t.Helper()
	deps, _ := libraryDeps(t)
	deps.LibrarySections = &fakeLibraryViews{}
	deps.LibraryCollections = &fakeLibraryViews{}
	return deps
}

func viewerHeaders() map[string]string { return with(bearer(memberToken), "X-Profile-Id", "p-owner") }

// --- stage B admin tests ---

func TestConfirmEmptyRootCleanup(t *testing.T) {
	deps, fake := libraryDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodPost, "/api/v2/libraries/1/confirm-empty-root-cleanup", "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	fake.err = notFoundLibrary()
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/libraries/9/confirm-empty-root-cleanup", "", bearer(adminToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/libraries/1/confirm-empty-root-cleanup", "", bearer(memberToken)), TypePermissionDenied)
}

func TestGetMetadataMatchQueue(t *testing.T) {
	deps, fake := libraryDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/libraries/1/metadata-match-queue?limit=1", "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var body struct {
		LibraryID  string                       `json:"library_id"`
		TotalCount int                          `json:"total_count"`
		Movies     []map[string]json.RawMessage `json:"movies"`
		Series     []map[string]json.RawMessage `json:"series"`
		RawFiles   []map[string]json.RawMessage `json:"raw_files"`
		Page       PageInfo                     `json:"page"`
	}
	decodeJSON(t, rec.Body, &body)
	if body.LibraryID != "1" || body.TotalCount != 4 || len(body.Movies) != 1 || len(body.Series) != 1 || len(body.RawFiles) != 1 || !body.Page.HasMore || body.Page.NextCursor == "" {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if string(body.Movies[0]["media_file_id"]) != `"120"` || string(body.Movies[0]["parked_at"]) != `"2026-01-02T03:04:05.678Z"` || string(body.Movies[0]["failure_detail"]) != `{"candidates":0}` {
		t.Fatalf("movie = %v", body.Movies[0])
	}
	if fake.lastLimit != 1 || fake.lastOffset != 0 {
		t.Fatalf("seam got limit=%d offset=%d", fake.lastLimit, fake.lastOffset)
	}
	rec = do(t, h, http.MethodGet, "/api/v2/libraries/1/metadata-match-queue?limit=1&cursor="+body.Page.NextCursor, "", bearer(adminToken))
	if rec.Code != 200 || fake.lastOffset != 1 || !strings.Contains(rec.Body.String(), `"has_more":false`) {
		t.Fatalf("page 2: %d %s offset=%d", rec.Code, rec.Body.String(), fake.lastOffset)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries/1/metadata-match-queue?offset=5", "", bearer(adminToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries/1/metadata-match-queue?limit=51", "", bearer(adminToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries/9/metadata-match-queue", "", bearer(adminToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries/1/metadata-match-queue", "", bearer(memberToken)), TypePermissionDenied)
	fake.err = &handlers.APIError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "Metadata matcher backlog is not configured"}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries/1/metadata-match-queue", "", bearer(adminToken)), TypeDependencyUnavailable)
}

func TestMetadataMatchQueueActions(t *testing.T) {
	deps, _ := libraryDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodPost, "/api/v2/libraries/1/metadata-match-queue/retry", "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"queued"`) || !strings.Contains(rec.Body.String(), `"raw_file_retried":1`) || strings.Contains(rec.Body.String(), "total_cancelled") { //nolint:misspell // v1 wire value
		t.Fatal(rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v2/libraries/1/metadata-match-queue/cancel", "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"cancelled"`) || !strings.Contains(rec.Body.String(), `"total_cancelled":4`) || !strings.Contains(rec.Body.String(), `"queue":{"library_id":"1"`) { //nolint:misspell // v1 wire value
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/libraries/9/metadata-match-queue/cancel", "", bearer(adminToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/libraries/1/metadata-match-queue/retry", "", bearer(memberToken)), TypePermissionDenied)
}

func TestRefreshLibraryMetadata(t *testing.T) {
	deps, fake := libraryDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodPost, "/api/v2/libraries/1/refresh-metadata", "", bearer(adminToken))
	if rec.Code != 202 || fake.lastRefreshMode != adminjob.LibraryRefreshModeQuick || fake.lastUserID != 2 || !strings.Contains(rec.Body.String(), `"job_type":"library_refresh"`) {
		t.Fatal(rec.Code, rec.Body.String(), fake.lastRefreshMode)
	}
	rec = do(t, h, http.MethodPost, "/api/v2/libraries/1/refresh-metadata", `{"mode":"full"}`, bearer(adminToken))
	if rec.Code != 202 || fake.lastRefreshMode != adminjob.LibraryRefreshModeFull {
		t.Fatal(rec.Code, rec.Body.String())
	}
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/libraries/1/refresh-metadata", `{"mode":"deep"}`, bearer(adminToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.mode" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/libraries/2/refresh-metadata", "", bearer(adminToken)), TypeConflict)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/libraries/9/refresh-metadata", "", bearer(adminToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/libraries/1/refresh-metadata", "", bearer(memberToken)), TypePermissionDenied)
}

func TestLibraryProviders(t *testing.T) {
	deps, fake := libraryDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/libraries/1/providers", "", bearer(adminToken))
	if rec.Code != 200 || rec.Body.String() != `{"levels":[{"content_level":"movie","entries":[{"plugin_installation_id":"3","capability_id":"tmdb","provider_slug":"tmdb","priority":0,"enabled":true}]}]}`+"\n" {
		t.Fatal(rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPut, "/api/v2/libraries/1/providers", `{"levels":[{"content_level":"movie","entries":[{"plugin_installation_id":"3","capability_id":"tmdb","priority":1,"enabled":true}]},{"content_level":"episode","entries":[]}]}`, bearer(adminToken))
	if rec.Code != 204 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if len(fake.lastChain) != 2 || fake.lastChain["movie"][0].PluginInstallationID != 3 || fake.lastChain["movie"][0].Priority != 1 || len(fake.lastChain["episode"]) != 0 {
		t.Fatalf("chain = %+v", fake.lastChain)
	}
	p := requireProblem(t, do(t, h, http.MethodPut, "/api/v2/libraries/1/providers", `{"levels":[{"content_level":"movie","entries":[]},{"content_level":"movie","entries":[]}]}`, bearer(adminToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.levels[1].content_level" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	p = requireProblem(t, do(t, h, http.MethodPut, "/api/v2/libraries/1/providers", `{"levels":[{"content_level":"movie","entries":[{"plugin_installation_id":"x","capability_id":"tmdb","priority":0,"enabled":true}]}]}`, bearer(adminToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.levels[0].entries[0].plugin_installation_id" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/libraries/1/providers", `{"levels":[{"content_level":"movie","entries":[]}],"extra":1}`, bearer(adminToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries/9/providers", "", bearer(adminToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries/1/providers", "", bearer(memberToken)), TypePermissionDenied)
}

func posterForm(t *testing.T, field, contentType string, size int) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	hdr := make(map[string][]string)
	hdr["Content-Disposition"] = []string{`form-data; name="` + field + `"; filename="poster.png"`}
	hdr["Content-Type"] = []string{contentType}
	part, err := w.CreatePart(hdr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(bytes.Repeat([]byte{0x89}, size)); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	return buf.String(), w.FormDataContentType()
}

func TestUploadLibraryPoster(t *testing.T) {
	deps, fake := libraryDeps(t)
	h := newTestHandler(t, deps)
	body, ct := posterForm(t, "poster", "image/png", 64)
	rec := do(t, h, http.MethodPut, "/api/v2/libraries/1/poster", body, with(bearer(adminToken), "Content-Type", ct))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"poster_url":"https://s3.example.test/library-posters/1.png"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if fake.lastPosterType != "image/png" || fake.lastPosterSize != 64 {
		t.Fatalf("seam got %q %d", fake.lastPosterType, fake.lastPosterSize)
	}
	// A JSON body on a multipart operation is the unsupported media type.
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/libraries/1/poster", `{}`, bearer(adminToken)), TypeUnsupportedMediaType)
	// The wrong field name and an unsupported image type are validation failures.
	body, ct = posterForm(t, "image", "image/png", 8)
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/libraries/1/poster", body, with(bearer(adminToken), "Content-Type", ct)), TypeValidationFailed)
	body, ct = posterForm(t, "poster", "image/gif", 8)
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/libraries/1/poster", body, with(bearer(adminToken), "Content-Type", ct)), TypeValidationFailed)
	// Over the limit: refused by the declared length before the form is read.
	body, ct = posterForm(t, "poster", "image/png", maxPosterBytes+posterFormOverhead+1)
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/libraries/1/poster", body, with(bearer(adminToken), "Content-Type", ct)), TypePayloadTooLarge)
	body, ct = posterForm(t, "poster", "image/png", 8)
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/libraries/9/poster", body, with(bearer(adminToken), "Content-Type", ct)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/libraries/1/poster", body, with(bearer(memberToken), "Content-Type", ct)), TypePermissionDenied)

	rec = do(t, h, http.MethodDelete, "/api/v2/libraries/1/poster", "", bearer(adminToken))
	if rec.Code != 204 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/libraries/9/poster", "", bearer(adminToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/libraries/1/poster", "", bearer(memberToken)), TypePermissionDenied)
}

// --- viewer tests ---

func TestLibraryLayoutAndSections(t *testing.T) {
	h := newTestHandler(t, libraryViewDeps(t))
	rec := do(t, h, http.MethodGet, "/api/v2/library/1/layout", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"recently_added"`) || !strings.Contains(rec.Body.String(), `"customized":true`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/v2/library/1/sections?image_size=medium", "", viewerHeaders())
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var body struct {
		Sections []struct {
			ID    string                       `json:"id"`
			Items []map[string]json.RawMessage `json:"items"`
		} `json:"sections"`
	}
	decodeJSON(t, rec.Body, &body)
	if len(body.Sections) != 1 || len(body.Sections[0].Items) != 1 {
		t.Fatalf("body = %s", rec.Body.String())
	}
	card := body.Sections[0].Items[0]
	for k, want := range map[string]string{"content_id": `"movie:heat-1995"`, "keywords": `[]`, "progress_updated_at": `"2026-01-02T03:04:05.000Z"`, "overlay_summary": `{"resolution":"4K","hdr":"Dolby Vision"}`, "user_state": `{"played":false,"is_favorite":false,"in_watchlist":true}`, "item_source": `"in_progress"`} {
		if string(card[k]) != want {
			t.Errorf("%s = %s, want %s", k, card[k], want)
		}
	}
	if _, ok := card["added_at"]; ok {
		t.Error("a section card carries no added_at")
	}
	rec = do(t, h, http.MethodGet, "/api/v2/library/1/sections/continue_watching/items", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"total_count":1`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/library/1/sections/nope/items", "", viewerHeaders()), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/library/1/sections?image_size=huge", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/library/x/sections", "", viewerHeaders()), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/library/1/sections", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/library/1/layout", "", nil), TypeAuthenticationRequired)
	deps := libraryViewDeps(t)
	deps.LibrarySections = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/library/1/layout", "", viewerHeaders()), TypeDependencyUnavailable)
}

func TestLibraryCollections(t *testing.T) {
	h := newTestHandler(t, libraryViewDeps(t))
	rec := do(t, h, http.MethodGet, "/api/v2/library/1/collections", "", viewerHeaders())
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var body struct {
		LibraryID   string                       `json:"library_id"`
		Collections []map[string]json.RawMessage `json:"collections"`
		Groups      []map[string]json.RawMessage `json:"groups"`
		Ungrouped   map[string]json.RawMessage   `json:"ungrouped"`
	}
	decodeJSON(t, rec.Body, &body)
	if body.LibraryID != "1" || len(body.Collections) != 1 || len(body.Groups) != 1 || body.Ungrouped == nil {
		t.Fatalf("body = %s", rec.Body.String())
	}
	c := body.Collections[0]
	for k, want := range map[string]string{"library_ids": `["1"]`, "query_definition": `{"filters":[]}`, "sort_config": `null`, "group_id": `null`, "created_at": `"2026-01-02T03:04:05.000Z"`} {
		if string(c[k]) != want {
			t.Errorf("%s = %s, want %s", k, c[k], want)
		}
	}
	if string(body.Groups[0]["kind"]) != `"user_collections"` || !strings.Contains(string(body.Groups[0]["collections"]), `"creator_profile_id":"p-owner"`) {
		t.Errorf("group = %v", body.Groups[0])
	}
	rec = do(t, h, http.MethodGet, "/api/v2/library/1/collections/c1/items", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"added_at":"2026-01-02T03:04:05.678Z"`) || !strings.Contains(rec.Body.String(), `"work_formats":[{"type":"ebook","content_id":"ebook:heat","library_id":"2"}]`) || strings.Contains(rec.Body.String(), `"page"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/library/1/collections/broken/items", "", viewerHeaders()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "path.collection_id" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/library/1/collections/nope/items", "", viewerHeaders()), TypeNotFound)
	rec = do(t, h, http.MethodGet, "/api/v2/library/1/user-collections", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"creator_profile_id":"p-owner"`) || !strings.Contains(rec.Body.String(), `"updated_at":"2026-01-02T03:04:05.000Z"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/library/1/user-collections", "", bearer(memberToken)), TypeValidationFailed)
	deps := libraryViewDeps(t)
	deps.LibraryCollections = &fakeLibraryViews{err: &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Library not found"}}
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/library/1/collections", "", viewerHeaders()), TypeNotFound)
}
