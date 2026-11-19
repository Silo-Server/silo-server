package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

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
	lastCreate      *handlers.LibraryCreateRequest
	lastUpdate      *handlers.LibraryUpdateRequest
	lastUpdateID    int
	lastUserID      int
	lastReorder     []catalogpkg.FolderReorderEntry
	lastOverride    *handlers.RootOverrideUpsertRequest
	lastDelete      *handlers.RootOverrideDeleteRequest
	lastRematch     string
	lastRootsQ      [4]int // libraryID, limit, offset, len(state)
	lastRootsSearch string
	lastSearch      string
	lastLimit       int
	lastOffset      int
	lastIgnored     bool
	// stage B commands.
	lastRefreshMode adminjob.LibraryRefreshMode
	lastPosterType  string
	lastPosterSize  int
	lastChain       map[string][]handlers.ProviderChainEntryInput
	// providers overrides the chain LibraryProviders answers when set.
	providers map[string][]handlers.ChainLevelEntryView
}

func libraryFixture(id int, name string) handlers.LibraryView {
	warnAt := fixedTime()
	code := "empty_root"
	return handlers.LibraryView{
		ID: id, Paths: []string{"/media/" + strings.ToLower(name)}, Type: "movies", Name: name, Enabled: true,
		MetadataLanguage: "en", ChapterThumbnailsSupported: true, TrickplaySupported: true, TrailerKinds: []string{"trailer"}, RealtimeMonitoring: true, SortOrder: id - 1,
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
	if req.RealtimeMonitoring != nil {
		v.RealtimeMonitoring = *req.RealtimeMonitoring
	}
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
			if req.RealtimeMonitoring != nil {
				v.RealtimeMonitoring = *req.RealtimeMonitoring
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

func (f *fakeLibraryAdmin) ListLibraryRoots(_ context.Context, libraryID int, state, search string, limit, offset int) ([]handlers.LibraryRootView, int, error) {
	if f.err != nil {
		return nil, 0, f.err
	}
	f.lastRootsQ = [4]int{libraryID, limit, offset, len(state)}
	f.lastRootsSearch = search
	if libraryID != 1 {
		return nil, 0, notFoundLibrary()
	}
	roots := f.roots
	if search != "" {
		roots = nil
		for _, root := range f.roots {
			if strings.Contains(strings.ToLower(root.RootPath), strings.ToLower(search)) || strings.Contains(strings.ToLower(root.Title), strings.ToLower(search)) {
				roots = append(roots, root)
			}
		}
	}
	end := min(offset+limit, len(roots))
	if offset > len(roots) {
		return []handlers.LibraryRootView{}, len(roots), nil
	}
	return roots[offset:end], len(roots), nil
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

func (f *fakeLibraryAdmin) ListSkippedRoots(_ context.Context, search string, limit, offset int) ([]handlers.SkippedRootView, int, error) {
	if f.err != nil {
		return nil, 0, f.err
	}
	all := []handlers.SkippedRootView{{LibraryID: 1, LibraryName: "Movies", RootPath: "/media/movies/Extras", Reason: "no_media_files", SampleFilePath: "/media/movies/Extras/a.txt", FileCount: 3, FirstSeenAt: fixedTime(), LastSeenAt: fixedTime()}}
	f.lastSearch, f.lastLimit, f.lastOffset = search, limit, offset
	for i := range 2 {
		root := all[0]
		root.RootPath += strconv.Itoa(i)
		all = append(all, root)
	}
	return all[min(offset, len(all)):min(offset+limit, len(all))], len(all), nil
}

func (f *fakeLibraryAdmin) ListStaleIDs(_ context.Context, search string, limit, offset int) ([]handlers.StaleMediaIDView, int, error) {
	if f.err != nil {
		return nil, 0, f.err
	}
	f.lastLimit, f.lastOffset, f.lastSearch = limit, offset, search
	all := []handlers.StaleMediaIDView{
		{ContentID: "movie:heat-1995", LibraryID: 1, LibraryName: "Movies", Title: "Heat", Year: 1995, ContentType: "movie", Provider: "tmdb", ProviderID: "949",
			FirstSeenAt: "2026-01-02T03:04:05Z", LastSeenAt: "2026-01-02T03:04:05Z", FirstSeen: fixedTime(), LastSeen: fixedTime()},
		{ContentID: "movie:alien-1979", LibraryID: 1, LibraryName: "Movies", Title: "Alien", Year: 1979, ContentType: "movie", Provider: "imdb", ProviderID: "tt0078748",
			FirstSeenAt: "2026-01-02T03:04:05Z", LastSeenAt: "2026-01-02T03:04:05Z", FirstSeen: fixedTime(), LastSeen: fixedTime()},
		{ContentID: "series:lost-2004", LibraryID: 0, LibraryName: "", Title: "Lost", Year: 2004, ContentType: "series", Provider: "tvdb", ProviderID: "73739",
			FirstSeenAt: "2026-01-02T03:04:05Z", LastSeenAt: "2026-01-02T03:04:05Z", FirstSeen: fixedTime(), LastSeen: fixedTime()},
	}
	if search != "" {
		matches := all[:0]
		for _, row := range all {
			if strings.Contains(strings.ToLower(row.Title), strings.ToLower(search)) {
				matches = append(matches, row)
			}
		}
		all = matches
	}
	if offset > len(all) {
		return []handlers.StaleMediaIDView{}, len(all), nil
	}
	return all[offset:min(offset+limit, len(all))], len(all), nil
}

func (f *fakeLibraryAdmin) RematchStaleID(_ context.Context, contentID string) error {
	f.lastRematch = contentID
	return f.err
}

func (f *fakeLibraryAdmin) ListUnmatchedItems(_ context.Context, search string, ignored bool, limit, offset int) ([]handlers.UnmatchedItemView, int, error) {
	if f.err != nil {
		return nil, 0, f.err
	}
	f.lastSearch, f.lastIgnored, f.lastLimit, f.lastOffset = search, ignored, limit, offset
	rows := []handlers.UnmatchedItemView{}
	for _, r := range f.unmatched {
		if (r.IgnoredAt != nil) == ignored && strings.Contains(strings.ToLower(r.Title), strings.ToLower(search)) {
			rows = append(rows, r)
		}
	}
	if offset > len(rows) {
		return []handlers.UnmatchedItemView{}, len(rows), nil
	}
	return rows[offset:min(offset+limit, len(rows))], len(rows), nil
}

func (f *fakeLibraryAdmin) SetUnmatchedItemIgnored(_ context.Context, contentID string, ignored bool) error {
	if f.err != nil {
		return f.err
	}
	for i := range f.unmatched {
		if f.unmatched[i].ContentID != contentID {
			continue
		}
		switch {
		case !ignored:
			f.unmatched[i].IgnoredAt = nil
		case f.unmatched[i].IgnoredAt == nil:
			f.unmatched[i].IgnoredAt = ptr(fixedTime())
		}
		return nil
	}
	return &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Item not found"}
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
			{ContentID: "movie:d", Title: "Delta", Year: 2004, ContentType: "movie", LibraryID: 1, LibraryName: "Movies", Status: "unmatched", IgnoredAt: ptr(fixedTime())},
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

type rootPage struct {
	Items []map[string]json.RawMessage `json:"items"`
	Page  struct {
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	} `json:"page"`
	Total int `json:"total"`
}

// unmatchedItemIgnoreFixtureCases pin the ignored view of the unmatched
// list and the mark that puts an item there. The ignore and unignore cases
// leave the shared fake as they found it.
func unmatchedItemIgnoreFixtureCases() []fixtureCase {
	problem := "#/components/schemas/Problem"
	return []fixtureCase{
		{name: "list_ignored_unmatched_items_ok", operationID: opListUnmatchedItems,
			scenario: "The items an admin marked as intentionally unmatched, each with when it was ignored.",
			method:   http.MethodGet, path: "/api/v2/libraries/unmatched-items?ignored=true", headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/UnmatchedItemCollection"},
		{name: "ignore_unmatched_item_ok", operationID: opIgnoreUnmatchedItem,
			scenario: "An admin marks an item as intentionally unmatched; ignoring an ignored item again is a no-op. Success answers 204 with no body.",
			method:   http.MethodPut, path: "/api/v2/libraries/unmatched-items/movie:d/ignore", headers: bearer(adminToken),
			status: http.StatusNoContent, assertHeaders: []string{"Cache-Control"}},
		{name: "ignore_unmatched_item_not_found", operationID: opIgnoreUnmatchedItem,
			scenario: "Ignoring an item the catalog does not hold.",
			method:   http.MethodPut, path: "/api/v2/libraries/unmatched-items/movie:missing/ignore", headers: bearer(adminToken),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "unignore_unmatched_item_ok", operationID: opUnignoreUnmatchedItem,
			scenario: "Clearing the mark of an item that is not ignored leaves it as is. Success answers 204 with no body.",
			method:   http.MethodDelete, path: "/api/v2/libraries/unmatched-items/movie:b/ignore", headers: bearer(adminToken),
			status: http.StatusNoContent, assertHeaders: []string{"Cache-Control"}},
	}
}
