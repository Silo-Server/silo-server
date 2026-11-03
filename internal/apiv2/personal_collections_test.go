package apiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/collections/templates"
	"github.com/Silo-Server/silo-server/internal/mdblist"
	"github.com/Silo-Server/silo-server/internal/usercollections"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// fakePersonalCollections records the last command and answers fixtures.
type fakePersonalCollections struct {
	err        error
	list       handlers.PersonalCollectionListView
	lastCreate handlers.PersonalCollectionCreateCommand
	lastOrder  []string
	// holding answers PersonalCollectionsHoldingItem; holdingCalls records
	// each call's profile and item.
	holding      map[string]bool
	holdingErr   error
	holdingCalls []string
	features     userstore.CollectionFeatures
	// collages answers Capabilities' PosterCollages.
	collages bool
}

func (f *fakePersonalCollections) PersonalCollectionFeatures(context.Context, int) (userstore.CollectionFeatures, error) {
	return f.features, nil
}

func (f *fakePersonalCollections) ListPersonalCollections(_ context.Context, _ int, profileID string) (handlers.PersonalCollectionListView, error) {
	if f.err != nil {
		return handlers.PersonalCollectionListView{}, f.err
	}
	if profileID != "p-owner" {
		return handlers.PersonalCollectionListView{Collections: []handlers.PersonalCollectionView{}, Groups: []handlers.CollectionGroupView{}}, nil
	}
	return f.list, nil
}

func (f *fakePersonalCollections) PersonalCollectionsHoldingItem(_ context.Context, _ int, profileID, itemID string) (map[string]bool, error) {
	f.holdingCalls = append(f.holdingCalls, profileID+"/"+itemID)
	if f.holdingErr != nil {
		return nil, f.holdingErr
	}
	return f.holding, nil
}

func (f *fakePersonalCollections) Capabilities() handlers.CollectionCapabilitiesView {
	return handlers.CollectionCapabilitiesView{
		DisplayFilterFields:   []string{"type", "watched"},
		DisplayFilterPresets:  handlers.CollectionDisplayFilterPresetsView{Watched: []string{"all", "watched", "unwatched"}, Media: []string{"all", "movie", "series"}},
		CollectionDefaultSort: true, CollectionSortPreferences: true, EffectiveCollectionSort: true,
		SortPreferenceKinds: []string{"library", "user", "watchlist", "favorites"},
		PosterCollages:      f.collages,
	}
}

func (f *fakePersonalCollections) CreatePersonalCollection(_ context.Context, cmd handlers.PersonalCollectionCreateCommand) (handlers.PersonalCollectionView, error) {
	f.lastCreate = cmd
	if f.err != nil {
		return handlers.PersonalCollectionView{}, f.err
	}
	if cmd.Request.Name == "" {
		return handlers.PersonalCollectionView{}, &handlers.APIError{Status: 400, Code: "bad_request", Message: "Collection name is required", Field: "name"}
	}
	v := fixtureCollectionView()
	v.Name = cmd.Request.Name
	v.Description = cmd.Request.Description
	v.CollectionType = cmd.Request.CollectionType
	v.IsShared = cmd.Request.IsShared
	return v, nil
}

// ReorderPersonalCollections answers like the seam: ordered_ids must name
// each of the acting profile's own collections (here c1) exactly once.
func (f *fakePersonalCollections) ReorderPersonalCollections(_ context.Context, _ int, _ string, orderedIDs []string) error {
	f.lastOrder = orderedIDs
	if f.err != nil {
		return f.err
	}
	if !slices.Equal(orderedIDs, []string{"c1"}) {
		return &handlers.APIError{Status: 400, Code: "bad_request", Message: "ordered_ids must name each of your own collections exactly once", Field: "ordered_ids"}
	}
	return nil
}

// errGroupsUnsupported is how the seams answer every personal collection
// group operation since #1615.
var errGroupsUnsupported = &handlers.APIError{Status: http.StatusNotImplemented, Code: "unsupported", Message: "The acting account does not support collection groups"}

func (f *fakePersonalCollections) CreateCollectionGroup(context.Context, int, handlers.CollectionGroupCreateRequest) (handlers.CollectionGroupView, error) {
	return handlers.CollectionGroupView{}, errGroupsUnsupported
}

func (f *fakePersonalCollections) UpdateCollectionGroup(context.Context, int, string, handlers.CollectionGroupUpdateRequest) (handlers.CollectionGroupView, error) {
	return handlers.CollectionGroupView{}, errGroupsUnsupported
}

func (f *fakePersonalCollections) DeleteCollectionGroup(context.Context, int, string) error {
	return errGroupsUnsupported
}

func (f *fakePersonalCollections) ReorderCollectionGroups(context.Context, int, []string) error {
	return errGroupsUnsupported
}

// fakeCollectionImports answers one imported collection and one search.
type fakeCollectionImports struct {
	err        error
	configured bool
	lastMDB    handlers.UserImportMDBListRequest
	lastTMDB   handlers.UserImportTMDBRequest
	lastList   handlers.UserImportTMDBListRequest
	lastQuery  string
}

func (f *fakeCollectionImports) view() (handlers.UserImportView, error) {
	if f.err != nil {
		return handlers.UserImportView{}, f.err
	}
	c := fixtureCollectionView()
	c.CollectionType = "mdblist"
	c.SourceURL = "https://mdblist.com/lists/u/top"
	c.SourceConfig = json.RawMessage(`{"mode":"mdblist"}`)
	c.SyncSchedule = "daily"
	c.LastSyncStatus = "success"
	stamp := fixedTime().Format(time.RFC3339)
	c.NextSyncAt, c.LastSyncAt = stamp, stamp
	return handlers.UserImportView{Collection: c, Sync: &usercollections.SyncResult{Status: "success", ItemsMatched: 2, StartedAt: fixedTime(), CompletedAt: fixedTime()}}, nil
}

func (f *fakeCollectionImports) ImportMDBList(_ context.Context, _ int, _ string, req handlers.UserImportMDBListRequest) (handlers.UserImportView, error) {
	f.lastMDB = req
	return f.view()
}

func (f *fakeCollectionImports) ImportTMDB(_ context.Context, _ int, _ string, req handlers.UserImportTMDBRequest) (handlers.UserImportView, error) {
	f.lastTMDB = req
	return f.view()
}

func (f *fakeCollectionImports) ImportTMDBList(_ context.Context, _ int, _ string, req handlers.UserImportTMDBListRequest) (handlers.UserImportView, error) {
	f.lastList = req
	return f.view()
}

func (f *fakeCollectionImports) SearchMDBList(_ context.Context, q string) (handlers.MDBListDiscoveryView, error) {
	f.lastQuery = q
	if f.err != nil {
		return handlers.MDBListDiscoveryView{}, f.err
	}
	if !f.configured {
		return handlers.MDBListDiscoveryView{Configured: false, Lists: []mdblist.ListSummary{}}, nil
	}
	return handlers.MDBListDiscoveryView{Configured: true, Lists: []mdblist.ListSummary{{ID: 12, UserID: 7, UserName: "u", Name: "Top", Slug: "top", MediaType: "movie", Items: 100, Likes: 5, URL: "https://mdblist.com/lists/u/top"}}}, nil
}

func (f *fakeCollectionImports) TopMDBList(ctx context.Context) (handlers.MDBListDiscoveryView, error) {
	return f.SearchMDBList(ctx, "top")
}

func fixtureCollectionView() handlers.PersonalCollectionView {
	stamp := fixedTime().Format(time.RFC3339Nano)
	return handlers.PersonalCollectionView{
		ID: "c1", ProfileID: "p-owner", CreatorProfileID: "p-owner", Name: "Rainy days", CollectionType: "manual",
		QueryDefinition: json.RawMessage(`{}`), SortConfig: json.RawMessage(`{}`),
		ItemCount: 4, CreatedAt: stamp, UpdatedAt: stamp,
	}
}

func collectionDeps(t *testing.T) (Dependencies, *fakePersonalCollections, *fakeCollectionImports) {
	t.Helper()
	deps := pilotDeps(nil, nil)
	// The acting profile's own collection, then another profile's shared one.
	shared := fixtureCollectionView()
	shared.ID, shared.ProfileID, shared.CreatorProfileID, shared.Name, shared.IsShared = "c2", "p-primary", "p-primary", "Family night", true
	pc := &fakePersonalCollections{list: handlers.PersonalCollectionListView{
		Collections: []handlers.PersonalCollectionView{fixtureCollectionView(), shared},
		Groups:      []handlers.CollectionGroupView{},
	}}
	ci := &fakeCollectionImports{configured: true}
	deps.PersonalCollections = pc
	deps.CollectionImports = ci
	return deps, pc, ci
}

func TestGetCollectionCapabilities(t *testing.T) {
	deps, pc, _ := collectionDeps(t)
	deps.ScheduleZone = fixtureScheduleTimeZone
	pc.features.Description = true
	rec := do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/collections/capabilities", "", viewerHeaders())
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	want := `{"groups":false,"login_sharing":true,"imports":false,"import_sources":[],"artwork":false,"item_reorder":false,"display_filter_fields":["type","watched"],"display_filter_presets":{"watched":["all","watched","unwatched"],"media":["all","movie","series"]},"collection_default_sort":true,"collection_sort_preferences":true,"effective_collection_sort":true,"sort_preference_kinds":["library","user","watchlist","favorites"],"create_description":true,"mdblist_search":true,"schedule_time_zone":{"utc_offset":"-05:00","abbreviation":"CDT","name":"America/Chicago"},"sync_schedule_editable":false,"contains_item":true,"preview_posters":true,"poster_collages":false}` + "\n"
	if !capabilityBodyMatches(t, rec.Body.Bytes(), want) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// Collages need both the server's collage support and the account's artwork.
	pc.collages = true
	for _, artwork := range []bool{false, true} {
		pc.features.Artwork = artwork
		rec = do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/collections/capabilities", "", viewerHeaders())
		if want := fmt.Sprintf(`"poster_collages":%t`, artwork); !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("artwork %v: body = %s, want %s", artwork, rec.Body.String(), want)
		}
	}
	pc.collages, pc.features.Artwork = false, false
	// A store that does not persist descriptions does not advertise them.
	pc.features.Description = false
	rec = do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/collections/capabilities", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"create_description":false`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestReorderCollections(t *testing.T) {
	deps, pc, _ := collectionDeps(t)
	h := newTestHandler(t, deps)
	// Null and omitted group both mean the profile's one flat order.
	for _, body := range []string{`{"group_id":null,"ordered_ids":["c1"]}`, `{"ordered_ids":["c1"]}`} {
		if rec := do(t, h, http.MethodPut, "/api/v2/collections/order", body, with(viewerHeaders(), "If-Match", "*")); rec.Code != 200 || !slices.Equal(pc.lastOrder, []string{"c1"}) {
			t.Fatalf("%s: %d %v %s", body, rec.Code, pc.lastOrder, rec.Body.String())
		}
	}
	// Personal collection groups are gone: a group scope is refused before
	// the seam, in the body and in the order read.
	pc.lastOrder = nil
	p := requireProblem(t, do(t, h, http.MethodPut, "/api/v2/collections/order", `{"group_id":"g1","ordered_ids":["c1"]}`, with(viewerHeaders(), "If-Match", "*")), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.group_id" || pc.lastOrder != nil {
		t.Fatalf("errors = %+v, seam called with %v", p.Errors, pc.lastOrder)
	}
	p = requireProblem(t, do(t, h, http.MethodGet, "/api/v2/collections/order?group_id=g1", "", viewerHeaders()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.group_id" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// Another profile's collection, even a shared one the profile can see, is
	// not part of its order (D11): a validation problem at ordered_ids.
	for _, body := range []string{`{"ordered_ids":["c2","c1"]}`, `{"ordered_ids":[]}`} {
		p = requireProblem(t, do(t, h, http.MethodPut, "/api/v2/collections/order", body, with(viewerHeaders(), "If-Match", "*")), TypeValidationFailed)
		if len(p.Errors) != 1 || p.Errors[0].Location != "body.ordered_ids" {
			t.Fatalf("%s: errors = %+v", body, p.Errors)
		}
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/collections/order", `{"ordered_ids":["c1"]}`, with(viewerHeaders(), "If-Match", "*")), TypeMethodNotAllowed)
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/collections/order", `{"ordered_ids":["c1"]}`, nil), TypeAuthenticationRequired)
}

func TestImportCollections(t *testing.T) {
	deps, _, ci := collectionDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodPost, "/api/v2/collections/import/mdblist", `{"title":"Top","url":"https://mdblist.com/lists/u/top","limit":25,"library_ids":["1","2"],"sync_schedule":"daily"}`, viewerHeaders())
	if rec.Code != 201 || rec.Header().Get("Location") != "/api/v2/collections/c1" {
		t.Fatalf("%d %s %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"collection_type":"mdblist"`, `"source_config":{"mode":"mdblist"}`, `"sync_schedule":"daily"`, `"next_sync_at":"2026-01-02T03:04:05.000Z"`,
		`"sync":{"status":"success","message":"","items_matched":2,"items_unmatched":0,"started_at":"2026-01-02T03:04:05.678Z","completed_at":"2026-01-02T03:04:05.678Z"}`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body lacks %s: %s", want, body)
		}
	}
	if req := ci.lastMDB; req.URL != "https://mdblist.com/lists/u/top" || req.Limit == nil || *req.Limit != 25 || len(req.LibraryIDs) != 2 || req.LibraryIDs[1] != 2 || req.SyncSchedule != "daily" {
		t.Fatalf("request = %+v", req)
	}
	// A library id that is not one is refused before the seam.
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/collections/import/mdblist", `{"title":"Top","url":"u","library_ids":["x"]}`, viewerHeaders()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.library_ids[0]" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/collections/import/mdblist", `{"title":"Top"}`, viewerHeaders()), TypeValidationFailed)

	rec = do(t, h, http.MethodPost, "/api/v2/collections/import/tmdb", `{"title":"Trending","preset":"trending","media_type":"movie","time_window":"week"}`, viewerHeaders())
	if rec.Code != 201 || ci.lastTMDB.Preset != "trending" || ci.lastTMDB.MediaType != "movie" || ci.lastTMDB.TimeWindow != "week" {
		t.Fatalf("%d %+v", rec.Code, ci.lastTMDB)
	}
	rec = do(t, h, http.MethodPost, "/api/v2/collections/import/tmdb-list", `{"title":"My list","url":"https://www.themoviedb.org/list/310-my-movie-list","limit":40}`, viewerHeaders())
	if rec.Code != 201 || ci.lastList.URL != "https://www.themoviedb.org/list/310-my-movie-list" || ci.lastList.Title != "My list" || ci.lastList.Limit == nil || *ci.lastList.Limit != 40 {
		t.Fatalf("%d %+v", rec.Code, ci.lastList)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/collections/import/tmdb-list", `{"title":"My list"}`, viewerHeaders()), TypeValidationFailed)
	// The seam's field error, the class, demo mode, and an unwired service.
	deps.CollectionImports = &fakeCollectionImports{err: &handlers.APIError{Status: 400, Code: "bad_request", Message: "url must be an MDBList list", Field: "url"}}
	p = requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, "/api/v2/collections/import/mdblist", `{"title":"Top","url":"u"}`, viewerHeaders()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.url" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/collections/import/tmdb", `{"title":"x","preset":"trending"}`, bearer(memberToken)), TypeValidationFailed)
	demo := deps
	demo.DemoSettings = fakeSettings{demo: true}
	requireProblem(t, do(t, newTestHandler(t, demo), http.MethodPost, "/api/v2/collections/import/tmdb", `{"title":"x","preset":"trending"}`, viewerHeaders()), TypePermissionDenied)
	deps.CollectionImports = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, "/api/v2/collections/import/tmdb", `{"title":"x","preset":"trending"}`, viewerHeaders()), TypeDependencyUnavailable)
}

func TestMDBListDiscovery(t *testing.T) {
	deps, _, ci := collectionDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/collections/import/mdblist/search?q=top", "", viewerHeaders())
	if rec.Code != 200 || ci.lastQuery != "top" {
		t.Fatalf("%d %q %s", rec.Code, ci.lastQuery, rec.Body.String())
	}
	want := `{"items":[{"id":"12","user_id":"7","user_name":"u","name":"Top","slug":"top","description":"","media_type":"movie","items":100,"likes":5,"url":"https://mdblist.com/lists/u/top"}],"configured":true}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/api/v2/collections/import/mdblist/top", "", viewerHeaders()); rec.Code != 200 || rec.Body.String() != want {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// q is required at the schema.
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/collections/import/mdblist/search", "", viewerHeaders()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.q" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// Unconfigured: an empty, configured=false answer rather than an error.
	deps.CollectionImports = &fakeCollectionImports{}
	rec = do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/collections/import/mdblist/top", "", viewerHeaders())
	if rec.Code != 200 || rec.Body.String() != `{"items":[],"configured":false}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// v1's 502 upstream_error is a dependency problem with Retry-After.
	deps.CollectionImports = &fakeCollectionImports{err: &handlers.APIError{Status: http.StatusBadGateway, Code: "upstream_error", Message: "MDBList search failed"}}
	rec = do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/collections/import/mdblist/search?q=top", "", viewerHeaders())
	requireProblem(t, rec, TypeDependencyUnavailable)
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/collections/import/mdblist/top", "", nil), TypeAuthenticationRequired)
}

func TestCollectionProblemPreservesUnsupportedSource(t *testing.T) {
	p := collectionProblem(&handlers.APIError{
		Status:  http.StatusGone,
		Code:    "unsupported_source",
		Message: "new Trakt collections are not supported",
	})
	if p.Status != http.StatusGone || p.Type != TypeUnsupportedSource.URI() || p.Title != TypeUnsupportedSource.Title {
		t.Fatalf("problem = %#v", p)
	}
}

func (f *fakePersonalCollections) PersonalCollectionEditor(_ context.Context, _ int, _ string, id string) (handlers.PersonalCollectionEditorView, error) {
	return handlers.PersonalCollectionEditorView{Collection: fixtureCollectionView(), Revision: 1}, f.err
}
func (f *fakePersonalCollections) PersonalCollectionOrderEditor(context.Context, int, string) (handlers.PersonalCollectionOrderView, error) {
	return handlers.PersonalCollectionOrderView{OrderedIDs: f.lastOrder, Revision: 1}, f.err
}
func (f *fakePersonalCollections) PersonalCollectionGroupsEditor(context.Context, int) ([]handlers.CollectionGroupView, int64, error) {
	return nil, 0, errGroupsUnsupported
}
func (f *fakePersonalCollections) PersonalCollectionGroupEditor(context.Context, int, string) (handlers.PersonalCollectionGroupEditorView, error) {
	return handlers.PersonalCollectionGroupEditorView{}, errGroupsUnsupported
}
func (f *fakePersonalCollections) PersonalCollectionItemsOrderEditor(_ context.Context, _ int, _ string, id string) (handlers.PersonalCollectionOrderView, error) {
	return handlers.PersonalCollectionOrderView{OrderedIDs: []string{}, Revision: 1}, f.err
}

// The personal template gallery offered TMDB Discover and franchise templates
// that can't become personal collections, so Create did nothing (#1640).
func TestImportableCollectionTemplatesKeepsPersonalSources(t *testing.T) {
	full := templates.CatalogDefault()
	want := map[templates.Source]int{}
	hasExcludedSource := false
	for _, group := range full.Categories {
		for _, template := range group.Templates {
			if slices.Contains(importableCollectionSources[:], string(template.Source)) {
				want[template.Source]++
			} else {
				hasExcludedSource = true
			}
		}
	}
	if len(want) == 0 || !hasExcludedSource {
		t.Fatal("built-in catalog must contain both importable and excluded sources")
	}
	got := creatableCollectionTemplates(full)

	kept := map[templates.Source]int{}
	for _, group := range got.Categories {
		if len(group.Templates) == 0 {
			t.Errorf("category %q kept with no templates", group.Category)
		}
		for _, template := range group.Templates {
			kept[template.Source]++
		}
	}
	for source := range kept {
		if !slices.Contains(importableCollectionSources[:], string(source)) {
			t.Errorf("catalog keeps %d templates with source %q, which personal collections can't import", kept[source], source)
		}
	}
	for source, count := range want {
		if kept[source] != count {
			t.Errorf("kept %d templates with importable source %q, want %d", kept[source], source, count)
		}
	}
}
