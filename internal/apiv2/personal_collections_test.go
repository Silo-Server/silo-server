package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/mdblist"
	"github.com/Silo-Server/silo-server/internal/usercollections"
)

// fakePersonalCollections records the last command and answers fixtures.
type fakePersonalCollections struct {
	err        error
	list       handlers.PersonalCollectionListView
	lastCreate handlers.PersonalCollectionCreateCommand
	lastOrder  []string
	lastGroup  *string
	lastReq    handlers.CollectionGroupUpdateRequest
	lastID     string
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

func (f *fakePersonalCollections) Capabilities() handlers.CollectionCapabilitiesView {
	return handlers.CollectionCapabilitiesView{
		DisplayFilterFields:   []string{"type", "watched"},
		DisplayFilterPresets:  handlers.CollectionDisplayFilterPresetsView{Watched: []string{"all", "watched", "unwatched"}, Media: []string{"all", "movie", "series"}},
		CollectionDefaultSort: true, CollectionSortPreferences: true, EffectiveCollectionSort: true,
		SortPreferenceKinds: []string{"library", "user", "watchlist", "favorites"},
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
	v.CollectionType = cmd.Request.CollectionType
	v.AllowedProfileIDs = cmd.Request.AllowedProfileIDs
	return v, nil
}

func (f *fakePersonalCollections) ReorderPersonalCollections(_ context.Context, _ int, _ string, groupID *string, orderedIDs []string) error {
	f.lastGroup, f.lastOrder = groupID, orderedIDs
	if f.err != nil {
		return f.err
	}
	if len(orderedIDs) == 0 {
		return &handlers.APIError{Status: 400, Code: "bad_request", Message: "ordered_ids must include every visible collection in the group exactly once", Field: "ordered_ids"}
	}
	return nil
}

func (f *fakePersonalCollections) CreateCollectionGroup(_ context.Context, _ int, req handlers.CollectionGroupCreateRequest) (handlers.CollectionGroupView, error) {
	if f.err != nil {
		return handlers.CollectionGroupView{}, f.err
	}
	return handlers.CollectionGroupView{ID: "g1", Name: req.Name, Slug: "seasonal", DefaultSortMode: "manual"}, nil
}

func (f *fakePersonalCollections) UpdateCollectionGroup(_ context.Context, _ int, id string, req handlers.CollectionGroupUpdateRequest) (handlers.CollectionGroupView, error) {
	f.lastID, f.lastReq = id, req
	if f.err != nil {
		return handlers.CollectionGroupView{}, f.err
	}
	if id != "g1" {
		return handlers.CollectionGroupView{}, &handlers.APIError{Status: 400, Code: "bad_request", Message: "collection group not found"}
	}
	g := handlers.CollectionGroupView{ID: "g1", Name: "Seasonal", Slug: "seasonal", DefaultSortMode: "manual", SortOrder: 1}
	if req.Name != nil {
		g.Name = *req.Name
	}
	return g, nil
}

func (f *fakePersonalCollections) DeleteCollectionGroup(_ context.Context, _ int, id string) error {
	f.lastID = id
	return f.err
}

func (f *fakePersonalCollections) ReorderCollectionGroups(_ context.Context, _ int, orderedIDs []string) error {
	f.lastOrder = orderedIDs
	return f.err
}

// fakeCollectionImports answers one imported collection and one search.
type fakeCollectionImports struct {
	err        error
	configured bool
	lastMDB    handlers.UserImportMDBListRequest
	lastTMDB   handlers.UserImportTMDBRequest
	lastTrakt  handlers.UserImportTraktRequest
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

func (f *fakeCollectionImports) ImportTrakt(_ context.Context, _ int, _ string, req handlers.UserImportTraktRequest) (handlers.UserImportView, error) {
	f.lastTrakt = req
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
		AllowedProfileIDs: []string{}, QueryDefinition: json.RawMessage(`{}`), SortConfig: json.RawMessage(`{}`),
		ItemCount: 4, CreatedAt: stamp, UpdatedAt: stamp,
	}
}

func collectionDeps(t *testing.T) (Dependencies, *fakePersonalCollections, *fakeCollectionImports) {
	t.Helper()
	deps := pilotDeps(nil, nil)
	group := "g1"
	grouped := fixtureCollectionView()
	grouped.ID, grouped.GroupID, grouped.SortOrder = "c2", &group, 1
	pc := &fakePersonalCollections{list: handlers.PersonalCollectionListView{
		Collections: []handlers.PersonalCollectionView{fixtureCollectionView(), grouped},
		Groups:      []handlers.CollectionGroupView{{ID: "g1", Name: "Seasonal", Slug: "seasonal", DefaultSortMode: "manual"}},
	}}
	ci := &fakeCollectionImports{configured: true}
	deps.PersonalCollections = pc
	deps.CollectionImports = ci
	return deps, pc, ci
}

func TestListCollections(t *testing.T) {
	deps, _, _ := collectionDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/collections", "", viewerHeaders())
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	want := `{"items":[{"id":"c1","profile_id":"p-owner","creator_profile_id":"p-owner","name":"Rainy days","description":"","collection_type":"manual","is_shared":false,"allowed_profile_ids":[],"query_definition":{},"sort_config":{},"sort_order":0,"group_id":null,"source_url":"","sync_schedule":"","next_sync_at":null,"last_sync_at":null,"last_sync_status":"","last_sync_message":"","item_count":4,"include_in_server_collections":false,"poster_url":"","poster_thumbhash":"","created_at":"2026-01-02T03:04:05.678Z","updated_at":"2026-01-02T03:04:05.678Z"},` +
		`{"id":"c2","profile_id":"p-owner","creator_profile_id":"p-owner","name":"Rainy days","description":"","collection_type":"manual","is_shared":false,"allowed_profile_ids":[],"query_definition":{},"sort_config":{},"sort_order":1,"group_id":"g1","source_url":"","sync_schedule":"","next_sync_at":null,"last_sync_at":null,"last_sync_status":"","last_sync_message":"","item_count":4,"include_in_server_collections":false,"poster_url":"","poster_thumbhash":"","created_at":"2026-01-02T03:04:05.678Z","updated_at":"2026-01-02T03:04:05.678Z"}],` +
		`"groups":[{"id":"g1","name":"Seasonal","slug":"seasonal","default_sort_mode":"manual","sort_order":0}]}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// Another profile sees an empty, never-null envelope.
	rec = do(t, h, http.MethodGet, "/api/v2/collections", "", with(bearer(memberToken), "X-Profile-Id", "p-primary"))
	if rec.Code != 200 || rec.Body.String() != `{"items":[],"groups":[]}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// The class: no session, no profile header, a locked profile.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/collections", "", nil), TypeAuthenticationRequired)
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/collections", "", bearer(memberToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != locationProfileHeader {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/collections", "", with(bearer(memberToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
	// Unwired service and a service failure.
	deps.PersonalCollections = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/collections", "", viewerHeaders()), TypeDependencyUnavailable)
	deps.PersonalCollections = &fakePersonalCollections{err: errors.New("boom")}
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/collections", "", viewerHeaders()), TypeInternalError)
}

func TestGetCollectionCapabilities(t *testing.T) {
	deps, _, _ := collectionDeps(t)
	rec := do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/collections/capabilities", "", viewerHeaders())
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	want := `{"groups":false,"imports":false,"artwork":false,"item_reorder":false,"display_filter_fields":["type","watched"],"display_filter_presets":{"watched":["all","watched","unwatched"],"media":["all","movie","series"]},"collection_default_sort":true,"collection_sort_preferences":true,"effective_collection_sort":true,"sort_preference_kinds":["library","user","watchlist","favorites"]}` + "\n"
	if !capabilityBodyMatches(t, rec.Body.Bytes(), want) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestCreateCollection(t *testing.T) {
	deps, pc, _ := collectionDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodPost, "/api/v2/collections", `{"name":"Smart","collection_type":"smart","query_definition":{"filters":[]},"allowed_profile_ids":["p-primary"],"is_shared":true}`, viewerHeaders())
	if rec.Code != 201 || rec.Header().Get("Location") != "/api/v2/collections/c1" {
		t.Fatalf("%d %s %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"name":"Smart","description":"","collection_type":"smart"`) || !strings.Contains(rec.Body.String(), `"allowed_profile_ids":["p-primary"]`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	cmd := pc.lastCreate
	if cmd.UserID != 1 || cmd.ProfileID != "p-owner" || cmd.PosterFile != nil || !cmd.Request.IsShared || string(cmd.Request.QueryDefinition) != `{"filters":[]}` {
		t.Fatalf("command = %+v", cmd)
	}
	// Validation: the schema (missing name), the seam (empty name), an
	// unknown enum, and null on a non-nullable member.
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/collections", `{"collection_type":"manual"}`, viewerHeaders()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.name" || p.Errors[0].Code != codeRequired {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/collections", `{"name":"x","collection_type":"playlist"}`, viewerHeaders()), TypeValidationFailed)
	p = requireProblem(t, do(t, h, http.MethodPost, "/api/v2/collections", `{"name":"x","is_shared":null}`, viewerHeaders()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.is_shared" || p.Errors[0].Code != codeInvalidType {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/collections", `{"name":"x","extra":1}`, viewerHeaders()), TypeValidationFailed)
	// The class and demo mode.
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/collections", `{"name":"x"}`, bearer(memberToken)), TypeValidationFailed)
	demo := deps
	demo.DemoSettings = fakeSettings{demo: true}
	requireProblem(t, do(t, newTestHandler(t, demo), http.MethodPost, "/api/v2/collections", `{"name":"x"}`, viewerHeaders()), TypePermissionDenied)
}

func TestReorderCollections(t *testing.T) {
	deps, pc, _ := collectionDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodPut, "/api/v2/collections/order", `{"group_id":"g1","ordered_ids":["c2"]}`, with(viewerHeaders(), "If-Match", "*"))
	if rec.Code != 200 || pc.lastGroup == nil || *pc.lastGroup != "g1" || len(pc.lastOrder) != 1 || pc.lastOrder[0] != "c2" {
		t.Fatalf("%d %v %v", rec.Code, pc.lastGroup, pc.lastOrder)
	}
	// Null and omitted group both target the ungrouped section.
	for _, body := range []string{`{"group_id":null,"ordered_ids":["c1"]}`, `{"ordered_ids":["c1"]}`} {
		if rec := do(t, h, http.MethodPut, "/api/v2/collections/order", body, with(viewerHeaders(), "If-Match", "*")); rec.Code != 200 || pc.lastGroup != nil {
			t.Fatalf("%s: %d %v", body, rec.Code, pc.lastGroup)
		}
	}
	// The seam's mismatch is a validation problem at ordered_ids.
	p := requireProblem(t, do(t, h, http.MethodPut, "/api/v2/collections/order", `{"ordered_ids":[]}`, with(viewerHeaders(), "If-Match", "*")), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.ordered_ids" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/collections/order", `{"ordered_ids":["c1"]}`, with(viewerHeaders(), "If-Match", "*")), TypeMethodNotAllowed)
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/collections/order", `{"ordered_ids":["c1"]}`, nil), TypeAuthenticationRequired)
}

func TestCollectionGroups(t *testing.T) {
	deps, pc, _ := collectionDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodPost, "/api/v2/collections/groups", `{"name":"Seasonal","default_sort_mode":"manual"}`, with(viewerHeaders(), "If-Match", "*"))
	if rec.Code != 201 || rec.Header().Get("Location") != "/api/v2/collections/groups/g1" {
		t.Fatalf("%d %s %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if want := `{"id":"g1","name":"Seasonal","slug":"seasonal","default_sort_mode":"manual","sort_order":0}` + "\n"; rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/collections/groups", `{"name":"x","default_sort_mode":"random"}`, with(viewerHeaders(), "If-Match", "*")), TypeValidationFailed)

	rec = do(t, h, http.MethodPatch, "/api/v2/collections/groups/g1", `{"name":"Winter"}`, with(viewerHeaders(), "If-Match", "*"))
	if rec.Code != 200 || pc.lastID != "g1" || pc.lastReq.Name == nil || *pc.lastReq.Name != "Winter" || pc.lastReq.Slug != nil {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), pc.lastReq)
	}
	p := requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/collections/groups/g1", `{"slug":null}`, with(viewerHeaders(), "If-Match", "*")), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.slug" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// v1 answers an unknown group with 400; that is a validation problem.
	requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/collections/groups/g9", `{"name":"x"}`, with(viewerHeaders(), "If-Match", "*")), TypeNotFound)

	if rec := do(t, h, http.MethodPut, "/api/v2/collections/groups/order", `{"ordered_ids":["g1"]}`, with(viewerHeaders(), "If-Match", "*")); rec.Code != 200 || len(pc.lastOrder) != 1 || pc.lastOrder[0] != "g1" {
		t.Fatalf("%d %v", rec.Code, pc.lastOrder)
	}
	if rec := do(t, h, http.MethodDelete, "/api/v2/collections/groups/g1", "", with(viewerHeaders(), "If-Match", "*")); rec.Code != 204 || pc.lastID != "g1" {
		t.Fatalf("%d %s", rec.Code, pc.lastID)
	}
	demo := deps
	demo.DemoSettings = fakeSettings{demo: true}
	requireProblem(t, do(t, newTestHandler(t, demo), http.MethodDelete, "/api/v2/collections/groups/g1", "", with(viewerHeaders(), "If-Match", "*")), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/collections/groups/g1", "", bearer(memberToken)), TypeValidationFailed)
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
	rec = do(t, h, http.MethodPost, "/api/v2/collections/import/trakt", `{"title":"Trending","preset":"trending"}`, viewerHeaders())
	if rec.Code != 201 || ci.lastTrakt.Preset != "trending" || ci.lastTrakt.MediaType != "" {
		t.Fatalf("%d %+v", rec.Code, ci.lastTrakt)
	}
	// The seam's field error, the class, demo mode, and an unwired service.
	deps.CollectionImports = &fakeCollectionImports{err: &handlers.APIError{Status: 400, Code: "bad_request", Message: "url must be an MDBList list", Field: "url"}}
	p = requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, "/api/v2/collections/import/mdblist", `{"title":"Top","url":"u"}`, viewerHeaders()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.url" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/collections/import/trakt", `{"title":"x","preset":"trending"}`, bearer(memberToken)), TypeValidationFailed)
	demo := deps
	demo.DemoSettings = fakeSettings{demo: true}
	requireProblem(t, do(t, newTestHandler(t, demo), http.MethodPost, "/api/v2/collections/import/trakt", `{"title":"x","preset":"trending"}`, viewerHeaders()), TypePermissionDenied)
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
func (f *fakePersonalCollections) PersonalCollectionOrderEditor(_ context.Context, _ int, _ string, g *string) (handlers.PersonalCollectionOrderView, error) {
	return handlers.PersonalCollectionOrderView{GroupID: g, OrderedIDs: f.lastOrder, Revision: 1}, f.err
}
func (f *fakePersonalCollections) PersonalCollectionGroupsEditor(_ context.Context, _ int) ([]handlers.CollectionGroupView, int64, error) {
	return []handlers.CollectionGroupView{{ID: "g1", Name: "Seasonal", Slug: "seasonal", DefaultSortMode: "manual"}}, 1, f.err
}
func (f *fakePersonalCollections) PersonalCollectionGroupEditor(_ context.Context, _ int, id string) (handlers.PersonalCollectionGroupEditorView, error) {
	if id != "g1" {
		return handlers.PersonalCollectionGroupEditorView{}, &handlers.APIError{Status: 404, Code: "not_found", Message: "Collection group not found"}
	}
	name := "Seasonal"
	if f.lastReq.Name != nil {
		name = *f.lastReq.Name
	}
	return handlers.PersonalCollectionGroupEditorView{Group: handlers.CollectionGroupView{ID: id, Name: name, Slug: "seasonal", DefaultSortMode: "manual"}, Revision: 1}, f.err
}
func (f *fakePersonalCollections) PersonalCollectionItemsOrderEditor(_ context.Context, _ int, _ string, id string) (handlers.PersonalCollectionOrderView, error) {
	return handlers.PersonalCollectionOrderView{OrderedIDs: []string{}, Revision: 1}, f.err
}
