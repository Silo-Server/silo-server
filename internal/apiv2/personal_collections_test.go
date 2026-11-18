package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
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

// The lifecycle operations find these methods by runtime assertion, so a
// removed method would only show up as a 503; keep the check at compile time.
var (
	_ personalCollectionLifecycle       = (*handlers.CollectionHandler)(nil)
	_ personalCollectionImportLifecycle = (*handlers.UserCollectionImportHandler)(nil)
)
