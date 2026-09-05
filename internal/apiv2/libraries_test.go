package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeLibraryAdmin is the seam double: it answers from fixed views, records
// the last command it received and fails wholesale when err is set.
type fakeLibraryAdmin struct {
	err       error
	libraries []handlers.LibraryView
	roots     []handlers.LibraryRootView
	unmatched []handlers.UnmatchedItemView
	// lastCreate, lastUpdate, lastReorder, lastOverride record commands.
	lastCreate   *handlers.LibraryCreateRequest
	lastUpdate   *handlers.LibraryUpdateRequest
	lastUpdateID int
	lastUserID   int
	lastReorder  []catalogpkg.FolderReorderEntry
	lastOverride *handlers.RootOverrideUpsertRequest
	lastDelete   *handlers.RootOverrideDeleteRequest
	lastRematch  string
	lastRootsQ   [4]int // libraryID, limit, offset, len(state)
	lastSearch   string
	lastLimit    int
	lastOffset   int
	// stage B commands.
	lastRefreshMode adminjob.LibraryRefreshMode
	lastPosterType  string
	lastPosterSize  int
	lastChain       map[string][]handlers.ProviderChainEntryInput
}

func libraryFixture(id int, name string) handlers.LibraryView {
	warnAt := fixedTime()
	code := "empty_root"
	return handlers.LibraryView{
		ID: id, Paths: []string{"/media/" + strings.ToLower(name)}, Type: "movies", Name: name, Enabled: true,
		MetadataLanguage: "en", ChapterThumbnailsSupported: true, TrailerKinds: []string{"trailer"}, SortOrder: id - 1,
		PosterURL: "https://s3.example.test/poster.jpg", LastScannedAt: ptr(fixedTime()),
		ScanWarningCode: &code, ScanWarningMessage: ptr("Root is empty"), ScanWarningAt: &warnAt,
	}
}

func notFoundLibrary() error {
	return &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Library not found"}
}

func (f *fakeLibraryAdmin) ListLibraries(context.Context) ([]handlers.LibraryView, error) {
	return f.libraries, f.err
}

func (f *fakeLibraryAdmin) CreateLibrary(_ context.Context, req handlers.LibraryCreateRequest) (handlers.LibraryView, error) {
	if f.err != nil {
		return handlers.LibraryView{}, f.err
	}
	f.lastCreate = &req
	if req.MetadataLanguage == "zz" {
		return handlers.LibraryView{}, &handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Invalid metadata_language", Field: "metadata_language"}
	}
	if req.Paths[0] == "/media/taken" {
		return handlers.LibraryView{}, &handlers.APIError{Status: http.StatusConflict, Code: "conflict", Message: "A library with this path already exists"}
	}
	v := libraryFixture(3, req.Name)
	v.Paths = req.Paths
	v.Type = req.Type
	return v, nil
}

func (f *fakeLibraryAdmin) UpdateLibrary(_ context.Context, id, userID int, req handlers.LibraryUpdateRequest) (handlers.LibraryView, error) {
	if f.err != nil {
		return handlers.LibraryView{}, f.err
	}
	f.lastUpdate, f.lastUpdateID, f.lastUserID = &req, id, userID
	for _, v := range f.libraries {
		if v.ID == id {
			if req.Name != nil {
				v.Name = *req.Name
			}
			return v, nil
		}
	}
	return handlers.LibraryView{}, notFoundLibrary()
}

func (f *fakeLibraryAdmin) DeleteLibrary(_ context.Context, id, userID int) (*models.AdminJob, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.lastUserID = userID
	for _, v := range f.libraries {
		if v.ID != id {
			continue
		}
		if v.Name == "Busy" {
			return nil, &handlers.APIError{Status: http.StatusConflict, Code: "conflict", Message: "A library deletion is already queued or running"}
		}
		return &models.AdminJob{ID: "job-1", JobType: "delete_library", Status: "queued", CreatedByUserID: userID,
			RequestPayload: json.RawMessage(`{"library_id":1}`), Message: "Queued library deletion", RequestedAt: fixedTime()}, nil
	}
	return nil, notFoundLibrary()
}

func (f *fakeLibraryAdmin) CheckLibraryMount(_ context.Context, id int) (handlers.LibraryMountCheckView, error) {
	if f.err != nil {
		return handlers.LibraryMountCheckView{}, f.err
	}
	for _, v := range f.libraries {
		if v.ID == id {
			return handlers.LibraryMountCheckView{Status: "healthy", LibraryID: id, LibraryName: v.Name, Healthy: true, CheckedAt: fixedTime(), Summary: "All 1 roots reachable",
				Roots: []handlers.LibraryMountCheckRootView{{Path: v.Paths[0], Reachable: true}}}, nil
		}
	}
	return handlers.LibraryMountCheckView{}, notFoundLibrary()
}

func (f *fakeLibraryAdmin) ConfirmEmptyRootCleanup(context.Context, int) error { return f.err }

func (f *fakeLibraryAdmin) ListMetadataMatchQueues(context.Context) ([]handlers.MetadataMatchQueueStatusView, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]handlers.MetadataMatchQueueStatusView, 0, len(f.libraries))
	for _, v := range f.libraries {
		out = append(out, handlers.MetadataMatchQueueStatusView{LibraryID: v.ID, MovieCount: 2, TotalCount: 2, PendingCount: 1, ParkedCount: 1})
	}
	return out, nil
}

func (f *fakeLibraryAdmin) LibraryProviderDefaults(_ context.Context, libraryType string) (map[string][]handlers.ChainLevelEntryView, error) {
	if f.err != nil {
		return nil, f.err
	}
	if libraryType != "movies" {
		return map[string][]handlers.ChainLevelEntryView{}, nil
	}
	return map[string][]handlers.ChainLevelEntryView{
		"movie": {{PluginInstallationID: 3, CapabilityID: "tmdb", ProviderSlug: "tmdb", Priority: 0, Enabled: true}},
	}, nil
}

func (f *fakeLibraryAdmin) ReorderLibraries(_ context.Context, entries []catalogpkg.FolderReorderEntry) error {
	f.lastReorder = entries
	return f.err
}

func (f *fakeLibraryAdmin) ListLibraryRoots(_ context.Context, libraryID int, state string, limit, offset int) ([]handlers.LibraryRootView, int, error) {
	if f.err != nil {
		return nil, 0, f.err
	}
	f.lastRootsQ = [4]int{libraryID, limit, offset, len(state)}
	if libraryID != 1 {
		return nil, 0, notFoundLibrary()
	}
	end := min(offset+limit, len(f.roots))
	if offset > len(f.roots) {
		return []handlers.LibraryRootView{}, len(f.roots), nil
	}
	return f.roots[offset:end], len(f.roots), nil
}

func (f *fakeLibraryAdmin) SetRootOverride(_ context.Context, userID int, req handlers.RootOverrideUpsertRequest) error {
	f.lastUserID, f.lastOverride = userID, &req
	if f.err != nil {
		return f.err
	}
	switch req.RootPath {
	case "/media/movies/Missing":
		return &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Root not found"}
	case "/media/movies/Mixed":
		return &handlers.APIError{Status: http.StatusConflict, Code: "ambiguous_root", Message: "Root contains files from multiple items"}
	}
	return nil
}

func (f *fakeLibraryAdmin) DeleteRootOverride(_ context.Context, req handlers.RootOverrideDeleteRequest) error {
	f.lastDelete = &req
	if f.err != nil {
		return f.err
	}
	if req.RootPath == "/media/movies/Missing" {
		return &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Root not found"}
	}
	return nil
}

func (f *fakeLibraryAdmin) ListSkippedRoots(context.Context) ([]handlers.SkippedRootView, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []handlers.SkippedRootView{{LibraryID: 1, LibraryName: "Movies", RootPath: "/media/movies/Extras", Reason: "no_media_files", SampleFilePath: "/media/movies/Extras/a.txt", FileCount: 3, FirstSeenAt: fixedTime(), LastSeenAt: fixedTime()}}, nil
}

func (f *fakeLibraryAdmin) ListStaleIDs(context.Context) ([]handlers.StaleMediaIDView, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []handlers.StaleMediaIDView{{ContentID: "movie:heat-1995", LibraryID: 1, LibraryName: "Movies", Title: "Heat", Year: 1995, ContentType: "movie", Provider: "tmdb", ProviderID: "949",
		FirstSeenAt: "2026-01-02T03:04:05Z", LastSeenAt: "2026-01-02T03:04:05Z", FirstSeen: fixedTime(), LastSeen: fixedTime()}}, nil
}

func (f *fakeLibraryAdmin) RematchStaleID(_ context.Context, contentID string) error {
	f.lastRematch = contentID
	return f.err
}

func (f *fakeLibraryAdmin) ListUnmatchedItems(_ context.Context, search string, limit, offset int) ([]handlers.UnmatchedItemView, int, error) {
	if f.err != nil {
		return nil, 0, f.err
	}
	f.lastSearch, f.lastLimit, f.lastOffset = search, limit, offset
	rows := f.unmatched
	if search != "" {
		rows = nil
		for _, r := range f.unmatched {
			if strings.Contains(strings.ToLower(r.Title), strings.ToLower(search)) {
				rows = append(rows, r)
			}
		}
	}
	if offset > len(rows) {
		return []handlers.UnmatchedItemView{}, len(rows), nil
	}
	return rows[offset:min(offset+limit, len(rows))], len(rows), nil
}

func libraryDeps(t *testing.T) (Dependencies, *fakeLibraryAdmin) {
	t.Helper()
	return withLibraryAdmin(pilotDeps(nil, nil))
}

// withLibraryAdmin wires the library-management fake; the fixtures use it too.
func withLibraryAdmin(deps Dependencies) (Dependencies, *fakeLibraryAdmin) {
	roots := make([]handlers.LibraryRootView, 0, 3)
	for i, title := range []string{"Alien", "Blade Runner", "Heat"} {
		roots = append(roots, handlers.LibraryRootView{LibraryID: 1, LibraryName: "Movies", RootPath: "/media/movies/" + title, State: "resolved", InferredType: "movie",
			TypeConfidence: "high", Title: title, Year: 1979 + i, ObservedFiles: 1, Evidence: json.RawMessage(`{"k":1}`), FirstSeenAt: fixedTime(), LastSeenAt: fixedTime()})
	}
	roots[2].ActiveOverride = &handlers.LibraryRootOverrideView{ForcedTitle: "Heat", ForcedYear: 1995, Note: "checked"}
	roots[2].ContentID = "movie:heat-1995"
	fake := &fakeLibraryAdmin{
		libraries: []handlers.LibraryView{libraryFixture(1, "Movies"), libraryFixture(2, "Busy")},
		roots:     roots,
		unmatched: []handlers.UnmatchedItemView{
			{ContentID: "movie:a", Title: "Alpha", Year: 2001, ContentType: "movie", LibraryID: 1, LibraryName: "Movies", Status: "unmatched"},
			{ContentID: "movie:b", Title: "Beta", Year: 2002, ContentType: "movie", Status: "pending"},
			{ContentID: "movie:c", Title: "Gamma", Year: 2003, ContentType: "movie", LibraryID: 1, LibraryName: "Movies", Status: "ambiguous"},
		},
	}
	deps.LibraryAdmin = fake
	return deps, fake
}

func decodeJSON(t *testing.T, rec interface{ String() string }, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(rec.String()), into); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, rec.String())
	}
}

func TestListLibraries(t *testing.T) {
	deps, _ := libraryDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/libraries", "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var body struct {
		Items []map[string]json.RawMessage `json:"items"`
		Page  *json.RawMessage             `json:"page"`
	}
	decodeJSON(t, rec.Body, &body)
	if len(body.Items) != 2 || body.Page != nil {
		t.Fatalf("body = %s", rec.Body.String())
	}
	for field, want := range map[string]string{
		"id": `"1"`, "name": `"Movies"`, "paths": `["/media/movies"]`, "trailer_kinds": `["trailer"]`, "sort_order": `0`,
		"chapter_thumbnails_supported": `true`, "poster_url": `"https://s3.example.test/poster.jpg"`,
		"last_scanned_at": `"2026-01-02T03:04:05.678Z"`, "scan_warning_code": `"empty_root"`, "scan_warning_at": `"2026-01-02T03:04:05.678Z"`,
	} {
		if string(body.Items[0][field]) != want {
			t.Errorf("%s = %s, want %s", field, body.Items[0][field], want)
		}
	}
	// Absent optional members are omitted, not null.
	deps.LibraryAdmin = &fakeLibraryAdmin{libraries: []handlers.LibraryView{{ID: 5, Name: "Bare", Type: "series"}}}
	rec = do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/libraries", "", bearer(adminToken))
	var bare struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	decodeJSON(t, rec.Body, &bare)
	body.Items = bare.Items
	for _, absent := range []string{"poster_url", "last_scanned_at", "scan_warning_code", "scan_warning_message", "scan_warning_at"} {
		if _, has := body.Items[0][absent]; has {
			t.Errorf("%s present on a bare library: %s", absent, rec.Body.String())
		}
	}
	if string(body.Items[0]["paths"]) != `[]` || string(body.Items[0]["trailer_kinds"]) != `[]` {
		t.Errorf("nil slices leaked: %s", rec.Body.String())
	}
}

func TestListLibrariesDenied(t *testing.T) {
	deps, fake := libraryDeps(t)
	h := newTestHandler(t, deps)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries", "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries", "", bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries", "", with(bearer(adminToken), "X-Profile-Id", "p-owner")), TypePermissionDenied)
	fake.err = &handlers.APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to list libraries"}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries", "", bearer(adminToken)), TypeInternalError)
	deps.LibraryAdmin = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/libraries", "", bearer(adminToken)), TypeDependencyUnavailable)
}

func TestCreateLibrary(t *testing.T) {
	deps, fake := libraryDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodPost, "/api/v2/libraries", `{"paths":["/media/tv"],"type":"series","name":"TV","trailer_kinds":[]}`, bearer(adminToken))
	if rec.Code != 201 || rec.Header().Get("Location") != "/api/v2/libraries/3" {
		t.Fatalf("%d %s %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	var body map[string]json.RawMessage
	decodeJSON(t, rec.Body, &body)
	if string(body["id"]) != `"3"` || string(body["type"]) != `"series"` || string(body["paths"]) != `["/media/tv"]` {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// An explicit empty trailer_kinds reaches the seam as an empty slice,
	// distinct from the omitted default.
	if fake.lastCreate.TrailerKinds == nil || len(fake.lastCreate.TrailerKinds) != 0 {
		t.Fatalf("trailer_kinds = %#v", fake.lastCreate.TrailerKinds)
	}
	do(t, h, http.MethodPost, "/api/v2/libraries", `{"paths":["/media/x"],"type":"movies","name":"X"}`, bearer(adminToken))
	if fake.lastCreate.TrailerKinds != nil {
		t.Fatalf("omitted trailer_kinds = %#v, want nil", fake.lastCreate.TrailerKinds)
	}

	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/libraries", `{"type":"movies","name":"X"}`, bearer(adminToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.paths" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	p = requireProblem(t, do(t, h, http.MethodPost, "/api/v2/libraries", `{"paths":["/m"],"type":"movies","name":"X","metadata_language":"zz"}`, bearer(adminToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.metadata_language" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/libraries", `{"paths":["/media/taken"],"type":"movies","name":"X"}`, bearer(adminToken)), TypeConflict)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/libraries", `{"paths":["/m"],"type":"movies","name":"X","extra":1}`, bearer(adminToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/libraries", `{"paths":["/m"],"type":"movies","name":"X"}`, bearer(memberToken)), TypePermissionDenied)
}

func TestUpdateLibrary(t *testing.T) {
	deps, fake := libraryDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodPatch, "/api/v2/libraries/1", `{"name":"Films","enabled":false,"paths":["/a","/b"]}`, bearer(adminToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var body map[string]json.RawMessage
	decodeJSON(t, rec.Body, &body)
	if string(body["name"]) != `"Films"` || fake.lastUpdateID != 1 || fake.lastUserID != 2 {
		t.Fatalf("body = %s update id %d user %d", rec.Body.String(), fake.lastUpdateID, fake.lastUserID)
	}
	if fake.lastUpdate.Type != nil || fake.lastUpdate.Enabled == nil || *fake.lastUpdate.Enabled || fake.lastUpdate.Paths == nil || len(*fake.lastUpdate.Paths) != 2 {
		t.Fatalf("command = %+v", fake.lastUpdate)
	}
	// PUT is gone: PATCH replaced it.
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/libraries/1", `{"name":"x"}`, bearer(adminToken)), TypeMethodNotAllowed)
	// No member is nullable.
	p := requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/libraries/1", `{"name":null}`, bearer(adminToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.name" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/libraries/1", `{"paths":[]}`, bearer(adminToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/libraries/9", `{"name":"x"}`, bearer(adminToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/libraries/abc", `{"name":"x"}`, bearer(adminToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/libraries/1", `{"name":"x"}`, bearer(memberToken)), TypePermissionDenied)
}

func TestDeleteLibrary(t *testing.T) {
	deps, fake := libraryDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodDelete, "/api/v2/libraries/1", "", bearer(adminToken))
	if rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var body map[string]json.RawMessage
	decodeJSON(t, rec.Body, &body)
	for field, want := range map[string]string{
		"id": `"job-1"`, "job_type": `"delete_library"`, "status": `"queued"`, "created_by_user_id": `"2"`,
		"request_payload": `{"library_id":1}`, "result_payload": `{}`, "requested_at": `"2026-01-02T03:04:05.678Z"`,
	} {
		if string(body[field]) != want {
			t.Errorf("%s = %s, want %s", field, body[field], want)
		}
	}
	if _, has := body["started_at"]; has || fake.lastUserID != 2 {
		t.Errorf("optional instant present or user %d: %s", fake.lastUserID, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/libraries/2", "", bearer(adminToken)), TypeConflict)
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/libraries/9", "", bearer(adminToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/libraries/1", "", bearer(memberToken)), TypePermissionDenied)
	fake.err = &handlers.APIError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "Library delete jobs are not configured"}
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/libraries/1", "", bearer(adminToken)), TypeDependencyUnavailable)
}

func TestCheckLibraryMount(t *testing.T) {
	deps, _ := libraryDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodPost, "/api/v2/libraries/1/check-mount", "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var body struct {
		LibraryID string `json:"library_id"`
		Healthy   bool   `json:"healthy"`
		CheckedAt string `json:"checked_at"`
		Roots     []map[string]json.RawMessage
	}
	decodeJSON(t, rec.Body, &body)
	if body.LibraryID != "1" || !body.Healthy || body.CheckedAt != "2026-01-02T03:04:05.678Z" || len(body.Roots) != 1 || string(body.Roots[0]["error_code"]) != "null" {
		t.Fatalf("body = %s", rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/libraries/9/check-mount", "", bearer(adminToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/libraries/1/check-mount", "", bearer(memberToken)), TypePermissionDenied)
}

func TestListMetadataMatchQueues(t *testing.T) {
	deps, _ := libraryDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/libraries/metadata-match-queue", "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var body struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	decodeJSON(t, rec.Body, &body)
	if len(body.Items) != 2 || string(body.Items[0]["library_id"]) != `"1"` || string(body.Items[0]["parked_count"]) != `1` {
		t.Fatalf("body = %s", rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries/metadata-match-queue", "", bearer(memberToken)), TypePermissionDenied)
}

func TestGetLibraryProviderDefaults(t *testing.T) {
	deps, _ := libraryDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/libraries/provider-defaults?library_type=movies", "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if want := `{"levels":[{"content_level":"movie","entries":[{"plugin_installation_id":"3","capability_id":"tmdb","provider_slug":"tmdb","priority":0,"enabled":true}]}]}`; strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/v2/libraries/provider-defaults?library_type=podcasts", "", bearer(adminToken))
	if strings.TrimSpace(rec.Body.String()) != `{"levels":[]}` {
		t.Fatalf("no-chain type = %s", rec.Body.String())
	}
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries/provider-defaults", "", bearer(adminToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.library_type" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries/provider-defaults?library_type=movies", "", bearer(memberToken)), TypePermissionDenied)
}

func TestReorderLibraries(t *testing.T) {
	deps, fake := libraryDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodPost, "/api/v2/libraries/reorder", `{"entries":[{"id":"2","position":0},{"id":"1","position":1}]}`, bearer(adminToken))
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	if len(fake.lastReorder) != 2 || fake.lastReorder[0].ID != 2 || fake.lastReorder[1].Position != 1 {
		t.Fatalf("entries = %+v", fake.lastReorder)
	}
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/libraries/reorder", `{"entries":[{"id":"x","position":0}]}`, bearer(adminToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.entries[0].id" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/libraries/reorder", `{"entries":[{"id":"1","position":-1}]}`, bearer(adminToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/libraries/reorder", `{"entries":[]}`, bearer(adminToken)), TypeMethodNotAllowed)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/libraries/reorder", `{"entries":[]}`, bearer(memberToken)), TypePermissionDenied)
}

type rootPage struct {
	Items []map[string]json.RawMessage `json:"items"`
	Page  struct {
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	} `json:"page"`
	Total int `json:"total"`
}

func TestListLibraryRoots(t *testing.T) {
	deps, fake := libraryDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/libraries/roots?library_id=1&limit=2", "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var first rootPage
	decodeJSON(t, rec.Body, &first)
	if len(first.Items) != 2 || !first.Page.HasMore || first.Page.NextCursor == "" || first.Total != 3 {
		t.Fatalf("first = %s", rec.Body.String())
	}
	if fake.lastRootsQ != [4]int{1, 3, 0, 0} {
		t.Fatalf("seam query = %v, want limit+1 probe from offset 0", fake.lastRootsQ)
	}
	if string(first.Items[0]["title"]) != `"Alien"` || string(first.Items[0]["evidence_json"]) != `{"k":1}` || string(first.Items[0]["library_id"]) != `"1"` {
		t.Fatalf("first item = %s", first.Items[0])
	}
	if _, has := first.Items[0]["active_override"]; has {
		t.Fatalf("override present on a plain root: %s", first.Items[0])
	}
	rec = do(t, h, http.MethodGet, "/api/v2/libraries/roots?library_id=1&limit=2&cursor="+first.Page.NextCursor, "", bearer(adminToken))
	var second rootPage
	decodeJSON(t, rec.Body, &second)
	if len(second.Items) != 1 || second.Page.HasMore || string(second.Items[0]["title"]) != `"Heat"` || string(second.Items[0]["content_id"]) != `"movie:heat-1995"` {
		t.Fatalf("second = %s", rec.Body.String())
	}
	if string(second.Items[0]["active_override"]) != `{"forced_title":"Heat","forced_year":1995,"note":"checked"}` {
		t.Fatalf("override = %s", second.Items[0]["active_override"])
	}
	// A cursor is bound to its filter.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries/roots?library_id=1&state=ambiguous&cursor="+first.Page.NextCursor, "", bearer(adminToken)), TypeInvalidCursor)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries/roots?library_id=1&offset=2", "", bearer(adminToken)), TypeValidationFailed)
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries/roots", "", bearer(adminToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.library_id" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries/roots?library_id=9", "", bearer(adminToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries/roots?library_id=1", "", bearer(memberToken)), TypePermissionDenied)
}

func TestRootOverride(t *testing.T) {
	deps, fake := libraryDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodPut, "/api/v2/libraries/roots/override", `{"library_id":"1","root_path":"/media/movies/Heat","forced_title":"Heat","forced_year":1995}`, bearer(adminToken))
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	if fake.lastOverride.LibraryID != 1 || fake.lastOverride.ForcedYear != 1995 || fake.lastUserID != 2 {
		t.Fatalf("command = %+v user %d", fake.lastOverride, fake.lastUserID)
	}
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/libraries/roots/override", `{"library_id":"1","root_path":"/media/movies/Missing"}`, bearer(adminToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/libraries/roots/override", `{"library_id":"1","root_path":"/media/movies/Mixed"}`, bearer(adminToken)), TypeConflict)
	p := requireProblem(t, do(t, h, http.MethodPut, "/api/v2/libraries/roots/override", `{"library_id":"nope","root_path":"/x"}`, bearer(adminToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.library_id" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/libraries/roots/override", `{"library_id":"1"}`, bearer(adminToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/libraries/roots/override", `{"library_id":"1","root_path":"/x"}`, bearer(memberToken)), TypePermissionDenied)

	// Delete takes the root in the query: a DELETE carries no body in v2.
	rec = do(t, h, http.MethodDelete, "/api/v2/libraries/roots/override?library_id=1&root_path=%2Fmedia%2Fmovies%2FHeat", "", bearer(adminToken))
	if rec.Code != 204 || fake.lastDelete == nil || fake.lastDelete.RootPath != "/media/movies/Heat" {
		t.Fatalf("%d %q %+v", rec.Code, rec.Body.String(), fake.lastDelete)
	}
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/libraries/roots/override?library_id=1&root_path=%2Fmedia%2Fmovies%2FMissing", "", bearer(adminToken)), TypeNotFound)
	p = requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/libraries/roots/override?library_id=1", "", bearer(adminToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.root_path" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/libraries/roots/override?library_id=1&root_path=%2Fx", "", bearer(memberToken)), TypePermissionDenied)
}

func TestListSkippedRootsAndStaleIDs(t *testing.T) {
	deps, fake := libraryDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/libraries/skipped-roots", "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var body struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	decodeJSON(t, rec.Body, &body)
	if len(body.Items) != 1 || string(body.Items[0]["library_id"]) != `"1"` || string(body.Items[0]["first_seen_at"]) != `"2026-01-02T03:04:05.678Z"` || string(body.Items[0]["file_count"]) != `3` {
		t.Fatalf("skipped = %s", rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/v2/libraries/stale-ids", "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	decodeJSON(t, rec.Body, &body)
	// v1 rendered second-precision strings; v2 carries the instants.
	if len(body.Items) != 1 || string(body.Items[0]["provider_id"]) != `"949"` || string(body.Items[0]["last_seen_at"]) != `"2026-01-02T03:04:05.678Z"` {
		t.Fatalf("stale = %s", rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries/skipped-roots", "", bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries/stale-ids", "", bearer(memberToken)), TypePermissionDenied)

	rec = do(t, h, http.MethodPost, "/api/v2/libraries/stale-ids/movie:heat-1995/rematch", "", bearer(adminToken))
	if rec.Code != 204 || fake.lastRematch != "movie:heat-1995" {
		t.Fatalf("%d %q rematch %q", rec.Code, rec.Body.String(), fake.lastRematch)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/libraries/stale-ids/movie:heat-1995/rematch", "", bearer(memberToken)), TypePermissionDenied)
	fake.err = &handlers.APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to clear IDs"}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/libraries/stale-ids/movie:heat-1995/rematch", "", bearer(adminToken)), TypeInternalError)
}

func TestListUnmatchedItems(t *testing.T) {
	deps, fake := libraryDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/libraries/unmatched-items?limit=2", "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var first rootPage
	decodeJSON(t, rec.Body, &first)
	if len(first.Items) != 2 || !first.Page.HasMore || first.Total != 3 || string(first.Items[1]["library_id"]) != `"0"` || string(first.Items[1]["library_name"]) != `""` {
		t.Fatalf("first = %s", rec.Body.String())
	}
	if fake.lastLimit != 3 || fake.lastOffset != 0 {
		t.Fatalf("seam limit %d offset %d", fake.lastLimit, fake.lastOffset)
	}
	rec = do(t, h, http.MethodGet, "/api/v2/libraries/unmatched-items?limit=2&cursor="+first.Page.NextCursor, "", bearer(adminToken))
	var second rootPage
	decodeJSON(t, rec.Body, &second)
	if len(second.Items) != 1 || second.Page.HasMore || string(second.Items[0]["title"]) != `"Gamma"` || fake.lastOffset != 2 {
		t.Fatalf("second = %s offset %d", rec.Body.String(), fake.lastOffset)
	}
	rec = do(t, h, http.MethodGet, "/api/v2/libraries/unmatched-items?q=gam", "", bearer(adminToken))
	decodeJSON(t, rec.Body, &second)
	if len(second.Items) != 1 || second.Total != 1 || fake.lastSearch != "gam" {
		t.Fatalf("search = %s (%q)", rec.Body.String(), fake.lastSearch)
	}
	// The cursor is bound to the search filter.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries/unmatched-items?limit=2&q=x&cursor="+first.Page.NextCursor, "", bearer(adminToken)), TypeInvalidCursor)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries/unmatched-items?offset=1", "", bearer(adminToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/libraries/unmatched-items", "", bearer(memberToken)), TypePermissionDenied)
}

// TestAdminJobOfInstants pins the optional-instant rendering the job
// resource shares with every operation that queues work.
func TestAdminJobOfInstants(t *testing.T) {
	started := fixedTime().Add(time.Minute)
	job := adminJobOf(&models.AdminJob{ID: "j", StartedAt: &started, RequestedAt: fixedTime()})
	if job.StartedAt == nil || job.StartedAt.String() != "2026-01-02T03:05:05.678Z" || job.CompletedAt != nil || string(job.ResultPayload) != `{}` {
		t.Fatalf("job = %+v", job)
	}
}
