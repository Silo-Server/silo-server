package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/mdblist"
	"github.com/Silo-Server/silo-server/internal/usercollections"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Section personal-collections: a profile's own collections and the ones
// other profiles on its login share (v1 /collections, CollectionHandler and
// UserCollectionImportHandler).

// PersonalCollection is a collection the acting profile owns, or another
// profile's collection shared with every profile on the login. Only its
// creator changes it.
type PersonalCollection struct {
	ID                         ID              `json:"id" example:"01J9Z8C3W4R5T6Y7U8I9O0P1Q4"`
	ProfileID                  ID              `json:"profile_id" doc:"The profile the row is scoped to" example:"p-owner"`
	CreatorProfileID           ID              `json:"creator_profile_id" example:"p-owner"`
	Name                       string          `json:"name" example:"Rainy days"`
	Description                string          `json:"description" example:""`
	CollectionType             string          `json:"collection_type" doc:"manual, smart, or an import source (mdblist, tmdb, trakt)" example:"manual"`
	IsShared                   bool            `json:"is_shared" doc:"Every profile on the login sees the collection read-only, including profiles added later; otherwise only its creator does" example:"false"`
	QueryDefinition            json.RawMessage `json:"query_definition" doc:"Smart-collection query document; {} for a manual collection"`
	SortConfig                 json.RawMessage `json:"sort_config" doc:"Default sort document; {} when unset"`
	SortOrder                  int             `json:"sort_order" doc:"Position in its creator's own order of collections" example:"0"`
	GroupID                    *ID             `json:"group_id" nullable:"true" doc:"Always null: personal collection groups are no longer supported" example:"g1"`
	SourceURL                  string          `json:"source_url" doc:"Where an imported collection is synced from; empty otherwise" example:""`
	SourceConfig               json.RawMessage `json:"source_config,omitempty" doc:"Import source document; absent for a manual or smart collection"`
	SyncSchedule               string          `json:"sync_schedule" doc:"Empty when the collection is not synced" example:""`
	SyncCadence                string          `json:"sync_cadence" enum:",daily,weekly,monthly,custom" doc:"The cadence sync_schedule names; empty when the collection is not synced, custom for a schedule no cadence name produces" example:""`
	NextSyncAt                 *Instant        `json:"next_sync_at" nullable:"true" example:"2026-01-02T03:04:05.678Z"`
	LastSyncAt                 *Instant        `json:"last_sync_at" nullable:"true" example:"2026-01-02T03:04:05.678Z"`
	LastSyncStatus             string          `json:"last_sync_status" doc:"Empty until the first sync" example:""`
	LastSyncMessage            string          `json:"last_sync_message" example:""`
	DisplayQueryDefinition     json.RawMessage `json:"display_query_definition,omitempty" doc:"Display filter fragment; absent when none"`
	ItemCount                  int             `json:"item_count" example:"4"`
	IncludeInServerCollections bool            `json:"include_in_server_collections" example:"false"`
	PosterURL                  string          `json:"poster_url" doc:"Presigned, short-lived; empty when there is no poster" example:""`
	PosterThumbhash            string          `json:"poster_thumbhash" example:""`
	PosterIsCollage            bool            `json:"poster_is_collage" doc:"poster_url is a collage of the collection's first titles the acting profile can see, composed by the server because the collection has no uploaded or imported poster. False for an uploaded or imported poster and whenever poster_url is empty: before the collage is built, when no title the profile can see has a poster, and on getCollection and updateCollection, which carry no poster_url for any poster because their body sits behind a strong ETag. See getCollectionCapabilities poster_collages" example:"false"`
	CreatedAt                  Instant         `json:"created_at" example:"2026-01-02T03:04:05.678Z"`
	UpdatedAt                  Instant         `json:"updated_at" example:"2026-01-02T03:04:05.678Z"`
	Contains                   *bool           `json:"contains,omitempty" doc:"Whether the collection holds the listCollections contains_item title. Present only when contains_item is sent, and then only on the acting profile's own manual collections; false for a title the profile cannot access" example:"true"`
}

// CollectionGroup is an account-wide grouping of personal collections.
type CollectionGroup struct {
	ID              ID     `json:"id" example:"g1"`
	Name            string `json:"name" example:"Seasonal"`
	Slug            string `json:"slug" example:"seasonal"`
	DefaultSortMode string `json:"default_sort_mode" doc:"How the group orders its collections: manual, name_asc, name_desc, recent or most_items" example:"manual"`
	SortOrder       int    `json:"sort_order" example:"0"`
}

// PersonalCollectionCollection is the listCollections envelope: the
// profile's own collections, then the shared collections of other profiles on
// the login. Groups is always empty.
type PersonalCollectionCollection struct {
	Collection[PersonalCollection]
	Groups []CollectionGroup `json:"groups" doc:"Always empty: personal collection groups are no longer supported"`
}

// PersonalCollectionListInput is the listCollections request.
type PersonalCollectionListInput struct {
	ContainsItem string `query:"contains_item" doc:"A title's content id. Each of the acting profile's own manual collections then carries contains. Accepted when getCollectionCapabilities reports contains_item" example:"01J9Z8C3W4R5T6Y7U8I9O0P1Q5"`
}

// PersonalCollectionCollectionOutput is the listCollections response.
type PersonalCollectionCollectionOutput struct {
	Body PersonalCollectionCollection
}

// PersonalCollectionOutput is a single-collection response.
type PersonalCollectionOutput struct {
	ETag string `header:"ETag"`
	Body PersonalCollection
}

// PersonalCollectionCreatedOutput is the createCollection response.
type PersonalCollectionCreatedOutput struct {
	Location string `header:"Location" doc:"The created collection's resource path"`
	Body     PersonalCollection
}

// PersonalCollectionCreate is the createCollection body.
type PersonalCollectionCreate struct {
	Name                       string          `json:"name" minLength:"1" example:"Rainy days"`
	Description                *string         `json:"description,omitempty" nullable:"false" doc:"Empty when omitted. Send only when getCollectionCapabilities reports create_description; otherwise the request fails" example:"For wet afternoons"`
	CollectionType             *string         `json:"collection_type,omitempty" nullable:"false" enum:"manual,smart" doc:"Defaults to manual" example:"manual"`
	IsShared                   *bool           `json:"is_shared,omitempty" nullable:"false" doc:"Show the collection to every profile on the login; defaults to false" example:"false"`
	QueryDefinition            json.RawMessage `json:"query_definition,omitempty" doc:"Smart-collection query document; required to be valid when collection_type is smart"`
	SortConfig                 json.RawMessage `json:"sort_config,omitempty" doc:"Default sort document"`
	DisplayQueryDefinition     json.RawMessage `json:"display_query_definition,omitempty" doc:"Display filter fragment"`
	IncludeInServerCollections *bool           `json:"include_in_server_collections,omitempty" nullable:"false" example:"false"`
	PosterSourceURL            *string         `json:"poster_source_url,omitempty" nullable:"false" doc:"An image URL the server fetches and stores as the poster" example:""`
}

// PersonalCollectionCreateInput is the createCollection request.
type PersonalCollectionCreateInput struct {
	Body    PersonalCollectionCreate
	RawBody []byte
}

// CollectionOrder is the reorderCollections body.
type CollectionOrder struct {
	GroupID    *ID  `json:"group_id,omitempty" nullable:"true" doc:"Omit or send null: personal collection groups are no longer supported, and any other value is a validation failure" example:"g1"`
	OrderedIDs []ID `json:"ordered_ids" doc:"Each of the acting profile's own collections, exactly once, in the new order; another profile's collection is a validation failure" example:"[\"01J9Z8C3W4R5T6Y7U8I9O0P1Q4\"]"`
}

// CollectionOrderInput is the reorderCollections request.
type CollectionOrderInput struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        CollectionOrder
}

// CollectionCapabilities is the additive feature support collection clients
// detect before using a member.
type CollectionCapabilities struct {
	Capability
	Groups                    bool                           `json:"groups" doc:"Always false: personal collection groups are no longer supported"`
	LoginSharing              bool                           `json:"login_sharing" doc:"is_shared shows a collection to every profile on the login, listCollections includes other profiles' shared collections, and only a collection's creator changes or orders it"`
	Imports                   bool                           `json:"imports" doc:"The acting account supports imported collections"`
	ImportSources             []string                       `json:"import_sources" enum:"mdblist,tmdb,tmdb_list" doc:"Import sources the acting account can create a collection from; empty when imports is false" example:"[\"mdblist\",\"tmdb\",\"tmdb_list\"]"`
	Artwork                   bool                           `json:"artwork" doc:"The acting account supports collection artwork"`
	ItemReorder               bool                           `json:"item_reorder" doc:"The acting account supports reordering collection items"`
	DisplayFilterFields       []string                       `json:"display_filter_fields" doc:"Catalog query fields a display filter may use" example:"[\"type\",\"watched\"]"`
	DisplayFilterPresets      CollectionDisplayFilterPresets `json:"display_filter_presets"`
	CollectionDefaultSort     bool                           `json:"collection_default_sort" example:"true"`
	CollectionSortPreferences bool                           `json:"collection_sort_preferences" example:"true"`
	EffectiveCollectionSort   bool                           `json:"effective_collection_sort" example:"true"`
	SortPreferenceKinds       []string                       `json:"sort_preference_kinds" doc:"collection_kind values the sort-preference operations accept" example:"[\"library\",\"user\",\"watchlist\",\"favorites\"]"`
	CreateDescription         bool                           `json:"create_description" doc:"createCollection stores a description for the acting account" example:"true"`
	MDBListSearch             bool                           `json:"mdblist_search" doc:"searchMDBListLists and listTopMDBListLists return lists; false when the server has no MDBList API key" example:"true"`
	ScheduleTimeZone          CollectionScheduleTimeZone     `json:"schedule_time_zone"`
	SyncScheduleEditable      bool                           `json:"sync_schedule_editable" doc:"updateCollection accepts sync_schedule on a synced list; false when imports is false" example:"true"`
	ContainsItem              bool                           `json:"contains_item" doc:"listCollections accepts contains_item and marks the acting profile's own manual collections with contains" example:"true"`
	PreviewPosters            bool                           `json:"preview_posters" doc:"previewCollection items carry poster_url when the title has a poster" example:"true"`
	PosterCollages            bool                           `json:"poster_collages" doc:"A collection with no uploaded or imported poster shows a collage of its first titles the acting profile can see in poster_url, marked by poster_is_collage, on listCollections, getLibraryCollections and listLibraryUserCollections, once the server has built it. False when the server has no artwork storage or the acting account's store keeps no artwork" example:"true"`
}

// importableCollectionSources are the import sources a new collection can be
// created from. Trakt is absent: existing Trakt collections still sync, but
// new ones are refused.
var importableCollectionSources = [...]string{importSourceMDBList, importSourceTMDB, importSourceTMDBList}

// Import source names reported in import_sources.
const (
	importSourceMDBList  = "mdblist"
	importSourceTMDB     = "tmdb"
	importSourceTMDBList = "tmdb_list"
)

// collectionTypeManual is the collection_type of a collection whose titles
// are added by hand.
const collectionTypeManual = "manual"

// collectionImportSources reports importableCollectionSources when imports
// are supported and an empty list otherwise.
func collectionImportSources(imports bool) []string {
	if !imports {
		return []string{}
	}
	return importableCollectionSources[:]
}

// CollectionDisplayFilterPresets are the preset values of the display filter.
type CollectionDisplayFilterPresets struct {
	Watched []string `json:"watched" example:"[\"all\",\"watched\",\"unwatched\"]"`
	Media   []string `json:"media" example:"[\"all\",\"movie\",\"series\"]"`
}

// CollectionCapabilitiesOutput is the getCollectionCapabilities response.
type CollectionCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         CollectionCapabilities
}

// CollectionGroupCreate is the createCollectionGroup body.
type CollectionGroupCreate struct {
	Name            string  `json:"name" minLength:"1" doc:"Trimmed" example:"Seasonal"`
	Slug            *string `json:"slug,omitempty" nullable:"false" doc:"Trimmed; derived from the name when omitted" example:"seasonal"`
	DefaultSortMode *string `json:"default_sort_mode,omitempty" nullable:"false" enum:"manual,name_asc,name_desc,recent,most_items" example:"manual"`
}

// CollectionGroupCreateInput is the createCollectionGroup request.
type CollectionGroupCreateInput struct {
	Body CollectionGroupCreate
}

// CollectionGroupUpdate is the updateCollectionGroup body; omitted members
// are unchanged.
type CollectionGroupUpdate struct {
	Name            *string `json:"name,omitempty" nullable:"false" minLength:"1" doc:"Trimmed; must not be empty" example:"Seasonal"`
	Slug            *string `json:"slug,omitempty" nullable:"false" doc:"Trimmed" example:"seasonal"`
	DefaultSortMode *string `json:"default_sort_mode,omitempty" nullable:"false" enum:"manual,name_asc,name_desc,recent,most_items" example:"manual"`
}

// CollectionGroupUpdateInput is the updateCollectionGroup request.
type CollectionGroupUpdateInput struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	ID          ID     `path:"id" doc:"The group" example:"g1"`
	Body        CollectionGroupUpdate
	RawBody     []byte
}

// CollectionGroupIDInput is the request of an operation addressing one group.
type CollectionGroupIDInput struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	ID          ID     `path:"id" doc:"The group" example:"g1"`
}

// CollectionGroupOutput is a single-group response.
type CollectionGroupOutput struct {
	ETag string `header:"ETag"`
	Body CollectionGroup
}

// CollectionGroupCreatedOutput is the createCollectionGroup response.
type CollectionGroupCreatedOutput struct {
	Location string `header:"Location" doc:"The created group's resource path"`
	Body     CollectionGroup
}

// CollectionGroupOrder is the reorderCollectionGroups body.
type CollectionGroupOrder struct {
	OrderedIDs []ID `json:"ordered_ids" doc:"Every group on the account, exactly once, in the new order" example:"[\"g1\",\"g2\"]"`
}

// CollectionGroupOrderInput is the reorderCollectionGroups request.
type CollectionGroupOrderInput struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        CollectionGroupOrder
}

// CollectionImportBase is the part every import shares.
type CollectionImportBase struct {
	Title                  string          `json:"title" minLength:"1" doc:"The new collection's name; trimmed" example:"Trending movies"`
	Description            *string         `json:"description,omitempty" nullable:"false" example:""`
	Limit                  *int            `json:"limit,omitempty" nullable:"false" minimum:"1" doc:"Cap on synced items; the server's own maximum applies when omitted" example:"50"`
	SyncSchedule           *string         `json:"sync_schedule,omitempty" nullable:"false" doc:"Sync cadence name; the server default when omitted" example:"daily"`
	IsShared               *bool           `json:"is_shared,omitempty" nullable:"false" doc:"Show the collection to every profile on the login; defaults to false" example:"false"`
	PosterURL              *string         `json:"poster_url,omitempty" nullable:"false" doc:"A bundled template poster path or an image URL" example:""`
	LibraryIDs             *[]ID           `json:"library_ids,omitempty" nullable:"false" doc:"Libraries the sync matches against, limited to the libraries the collection's owner can access; every library the owner can access when omitted" example:"[\"1\"]"`
	DisplayQueryDefinition json.RawMessage `json:"display_query_definition,omitempty" doc:"Display filter fragment"`
	SortConfig             json.RawMessage `json:"sort_config,omitempty" doc:"Default sort document"`
}

// MDBListCollectionImport is the importMDBListCollection body.
type MDBListCollectionImport struct {
	CollectionImportBase
	URL string `json:"url" minLength:"1" doc:"An MDBList list page (https://mdblist.com/lists/...)" example:"https://mdblist.com/lists/linaspurinis/top-watched-movies-of-the-week"`
}

// MDBListCollectionImportInput is the importMDBListCollection request.
type MDBListCollectionImportInput struct {
	Body MDBListCollectionImport
}

// TMDBCollectionImport is the importTMDBCollection body.
type TMDBCollectionImport struct {
	CollectionImportBase
	Preset     string  `json:"preset" minLength:"1" doc:"A TMDB preset name" example:"trending"`
	MediaType  *string `json:"media_type,omitempty" nullable:"false" doc:"movie or tv; the preset's default when omitted" example:"movie"`
	TimeWindow *string `json:"time_window,omitempty" nullable:"false" doc:"day or week for a trending preset" example:"week"`
}

// TMDBCollectionImportInput is the importTMDBCollection request.
type TMDBCollectionImportInput struct {
	Body TMDBCollectionImport
}

// TMDBListCollectionImport is the importTMDBListCollection body.
type TMDBListCollectionImport struct {
	CollectionImportBase
	URL string `json:"url" minLength:"1" doc:"A public TMDB list page (https://www.themoviedb.org/list/...) or its numeric ID" example:"https://www.themoviedb.org/list/310-my-movie-list"`
}

// TMDBListCollectionImportInput is the importTMDBListCollection request.
type TMDBListCollectionImportInput struct {
	Body TMDBListCollectionImport
}

// TraktCollectionImport is the importTraktCollection body.
type TraktCollectionImport struct {
	CollectionImportBase
	Preset    string  `json:"preset" minLength:"1" doc:"A Trakt preset name" example:"trending"`
	MediaType *string `json:"media_type,omitempty" nullable:"false" doc:"movie or show; the preset's default when omitted" example:"movie"`
}

// TraktCollectionImportInput is the importTraktCollection request.
type TraktCollectionImportInput struct {
	Body TraktCollectionImport
}

// CollectionSyncResult is one sync run's outcome.
type CollectionSyncResult struct {
	Status         string  `json:"status" doc:"success, partial or failed" example:"success"`
	Message        string  `json:"message" example:""`
	ItemsMatched   int     `json:"items_matched" example:"42"`
	ItemsUnmatched int     `json:"items_unmatched" example:"3"`
	StartedAt      Instant `json:"started_at" example:"2026-01-02T03:04:05.678Z"`
	CompletedAt    Instant `json:"completed_at" example:"2026-01-02T03:04:06.678Z"`
}

// CollectionImportResult is an import's answer: the collection and, when
// the first sync ran, its outcome. A failed first sync still creates the
// collection (last_sync_status is failed) so the caller can retry the sync.
type CollectionImportResult struct {
	Collection PersonalCollection    `json:"collection"`
	Sync       *CollectionSyncResult `json:"sync,omitempty" doc:"Absent when the first sync could not run"`
}

// CollectionImportOutput is an import response.
type CollectionImportOutput struct {
	Location string `header:"Location" doc:"The created collection's resource path"`
	Body     CollectionImportResult
}

// MDBListList is one MDBList list as the discovery API describes it.
type MDBListList struct {
	ID          ID     `json:"id" example:"12345"`
	UserID      ID     `json:"user_id" example:"678"`
	UserName    string `json:"user_name" example:"linaspurinis"`
	Name        string `json:"name" example:"Top watched movies of the week"`
	Slug        string `json:"slug" example:"top-watched-movies-of-the-week"`
	Description string `json:"description" example:""`
	MediaType   string `json:"media_type" doc:"movie or show as MDBList reports it" example:"movie"`
	Items       int    `json:"items" example:"100"`
	Likes       int    `json:"likes" example:"250"`
	URL         string `json:"url" doc:"The list page; empty when MDBList did not report one" example:"https://mdblist.com/lists/linaspurinis/top-watched-movies-of-the-week"`
}

// MDBListListCollection is the MDBList discovery envelope.
type MDBListListCollection struct {
	Collection[MDBListList]
	Configured bool `json:"configured" doc:"false when this server has no MDBList API key; items is then empty and clients hide discovery" example:"true"`
}

// MDBListListCollectionOutput is an MDBList discovery response.
type MDBListListCollectionOutput struct {
	Body MDBListListCollection
}

// MDBListSearchInput is the searchMDBListLists request.
type MDBListSearchInput struct {
	Q string `query:"q" required:"true" minLength:"1" doc:"Free-text list search" example:"top watched"`
}

// PersonalCollectionService is the slice of *handlers.CollectionHandler the
// personal collection operations use. Every method returns the view its v1
// handler writes and an *handlers.APIError on failure.
type PersonalCollectionService interface {
	ListPersonalCollections(ctx context.Context, userID int, profileID string) (handlers.PersonalCollectionListView, error)
	PersonalCollectionsHoldingItem(ctx context.Context, userID int, profileID, itemID string) (map[string]bool, error)
	Capabilities() handlers.CollectionCapabilitiesView
	CreatePersonalCollection(ctx context.Context, cmd handlers.PersonalCollectionCreateCommand) (handlers.PersonalCollectionView, error)
	ReorderPersonalCollections(ctx context.Context, userID int, profileID string, orderedIDs []string) error
	CreateCollectionGroup(ctx context.Context, userID int, req handlers.CollectionGroupCreateRequest) (handlers.CollectionGroupView, error)
	UpdateCollectionGroup(ctx context.Context, userID int, id string, req handlers.CollectionGroupUpdateRequest) (handlers.CollectionGroupView, error)
	DeleteCollectionGroup(ctx context.Context, userID int, id string) error
	ReorderCollectionGroups(ctx context.Context, userID int, orderedIDs []string) error
}

// CollectionImportService is the slice of
// *handlers.UserCollectionImportHandler the import and discovery operations
// use.
type CollectionImportService interface {
	ImportMDBList(ctx context.Context, userID int, profileID string, req handlers.UserImportMDBListRequest) (handlers.UserImportView, error)
	ImportTMDB(ctx context.Context, userID int, profileID string, req handlers.UserImportTMDBRequest) (handlers.UserImportView, error)
	ImportTMDBList(ctx context.Context, userID int, profileID string, req handlers.UserImportTMDBListRequest) (handlers.UserImportView, error)
	ImportTrakt(ctx context.Context, userID int, profileID string, req handlers.UserImportTraktRequest) (handlers.UserImportView, error)
	SearchMDBList(ctx context.Context, query string) (handlers.MDBListDiscoveryView, error)
	TopMDBList(ctx context.Context) (handlers.MDBListDiscoveryView, error)
	MDBListConfigured() bool
}

// groupsRemovedSummary describes each personal collection group operation,
// kept (answering 501) until native clients tolerate its absence (#1615).
const groupsRemovedSummary = "Not supported: personal collection groups were removed, and this operation answers capability_unsupported."

const (
	opReorderCollections      = "reorderCollections"
	opReorderCollectionGroups = "reorderCollectionGroups"
	opUpdateCollectionGroup   = "updateCollectionGroup"
	opDeleteCollectionGroup   = "deleteCollectionGroup"
)

func registerPersonalCollections(reg *Registry) {
	registerPersonalCollectionLifecycle(reg)
	registerCollectionPaging(reg)
	registerCollectionEditors(reg)
	// Every operation is profile scoped with the header required, as the v1
	// /collections group (RequireProfile), and demo restricted where v1's
	// demo guard covers the mutation.
	read := func(op huma.Operation) Operation {
		return Operation{Operation: op, Class: ClassProfileScoped, ServiceBacked: true}
	}
	write := func(op huma.Operation) Operation {
		return Operation{Operation: op, Class: ClassProfileScoped, DemoRestricted: isMutatingMethod(op.Method), ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable, Guarded: op.OperationID == opReorderCollections || op.OperationID == opReorderCollectionGroups || op.OperationID == opUpdateCollectionGroup || op.OperationID == opDeleteCollectionGroup}
	}

	Register(reg, read(humaOp(http.MethodGet, Prefix+"/collections", "listCollections", "collections",
		"List the acting profile's own collections in its order, then the collections other profiles on the login share, grouped by creator and each in its creator's order.")), reg.listCollections)

	create := humaOp(http.MethodPost, Prefix+"/collections", "createCollection", "collections",
		"Create a manual or smart collection for the acting profile. Not idempotent: a retry after a lost response creates a second collection.")
	create.DefaultStatus = http.StatusCreated
	createOp := write(create)
	// A description or poster the acting account's store cannot keep.
	createOp.Errors = append(createOp.Errors, http.StatusNotImplemented)
	Register(reg, createOp, reg.createCollection)

	Register(reg, read(humaOp(http.MethodGet, Prefix+"/collections/capabilities", "getCollectionCapabilities", "collections",
		"The collection features this server supports.")), reg.getCollectionCapabilities)

	order := humaOp(http.MethodPut, Prefix+"/collections/order", opReorderCollections, "collections",
		"Replace the order of the acting profile's own collections. Retries are not safe after an intervening mutation.")
	order.DefaultStatus = http.StatusOK
	Register(reg, write(order), reg.reorderCollections)

	groupOp := func(op huma.Operation) Operation {
		o := write(op)
		o.Errors = append(o.Errors, http.StatusNotImplemented)
		return o
	}
	createGroup := humaOp(http.MethodPost, Prefix+"/collections/groups", "createCollectionGroup", "collections",
		groupsRemovedSummary)
	createGroup.DefaultStatus = http.StatusCreated
	Register(reg, groupOp(createGroup), reg.createCollectionGroup)

	groupOrder := humaOp(http.MethodPut, Prefix+"/collections/groups/order", opReorderCollectionGroups, "collections",
		groupsRemovedSummary)
	groupOrder.DefaultStatus = http.StatusOK
	Register(reg, groupOp(groupOrder), reg.reorderCollectionGroups)

	Register(reg, groupOp(humaOp(http.MethodPatch, Prefix+"/collections/groups/{id}", opUpdateCollectionGroup, "collections",
		groupsRemovedSummary)), reg.updateCollectionGroup)

	deleteGroup := humaOp(http.MethodDelete, Prefix+"/collections/groups/{id}", opDeleteCollectionGroup, "collections",
		groupsRemovedSummary)
	deleteGroup.DefaultStatus = http.StatusNoContent
	Register(reg, groupOp(deleteGroup), reg.deleteCollectionGroup)

	importOp := func(path, id, summary string) huma.Operation {
		op := humaOp(http.MethodPost, Prefix+path, id, "collections",
			summary+" Creates the collection and runs its first sync. Not idempotent: a retry after a lost response creates a second collection.")
		op.DefaultStatus = http.StatusCreated
		return op
	}
	Register(reg, write(importOp("/collections/import/mdblist", "importMDBListCollection",
		"Import an MDBList list as a synced collection.")), reg.importMDBListCollection)
	Register(reg, write(importOp("/collections/import/tmdb", "importTMDBCollection",
		"Import a TMDB preset as a synced collection.")), reg.importTMDBCollection)
	Register(reg, write(importOp("/collections/import/tmdb-list", "importTMDBListCollection",
		"Import a public TMDB list as a synced collection.")), reg.importTMDBListCollection)
	Register(reg, write(importOp("/collections/import/trakt", "importTraktCollection",
		"Import a Trakt preset as a synced collection.")), reg.importTraktCollection)

	Register(reg, read(humaOp(http.MethodGet, Prefix+"/collections/import/mdblist/search", "searchMDBListLists", "collections",
		"Search MDBList for lists to import.")), reg.searchMDBListLists)
	Register(reg, read(humaOp(http.MethodGet, Prefix+"/collections/import/mdblist/top", "listTopMDBListLists", "collections",
		"MDBList's most-liked lists.")), reg.listTopMDBListLists)
}

func (reg *Registry) personalCollections() (PersonalCollectionService, *Problem) {
	if reg.deps.PersonalCollections == nil {
		return nil, unavailable("personal collections")
	}
	return reg.deps.PersonalCollections, nil
}

func (reg *Registry) collectionImports() (CollectionImportService, *Problem) {
	if reg.deps.CollectionImports == nil {
		return nil, unavailable("collection import")
	}
	return reg.deps.CollectionImports, nil
}

// collectionProblem renders a seam failure. Field errors and v1 400s are
// validation problems; v1's 502 upstream_error (MDBList unreachable) is a
// dependency problem rather than an internal error.
func collectionProblem(err error) *Problem {
	apiErr, ok := errors.AsType[*handlers.APIError](err)
	if !ok {
		return serviceProblem(err)
	}
	switch {
	case apiErr.Field != "":
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationBody + "." + apiErr.Field, Code: codeInvalid, Detail: apiErr.Message})
	case apiErr.Status == http.StatusBadRequest:
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationBody, Code: codeInvalid, Detail: apiErr.Message})
	case apiErr.Status == http.StatusGone && apiErr.Code == codeUnsupportedSource:
		return NewProblem(TypeUnsupportedSource, apiErr.Message)
	case apiErr.Status == http.StatusNotImplemented:
		return NewProblem(TypeCapabilityUnsupported, apiErr.Message)
	case apiErr.Status == http.StatusServiceUnavailable:
		return NewProblem(TypeDependencyUnavailable, apiErr.Message)
	case apiErr.Status == http.StatusBadGateway:
		return NewProblem(TypeDependencyUnavailable, apiErr.Message).WithRetryAfter(30)
	}
	return serviceProblem(err)
}

func personalCollectionOf(v handlers.PersonalCollectionView) PersonalCollection {
	out := PersonalCollection{
		ID: ID(v.ID), ProfileID: ID(v.ProfileID), CreatorProfileID: ID(v.CreatorProfileID),
		Name: v.Name, Description: v.Description, CollectionType: v.CollectionType, IsShared: v.IsShared,
		QueryDefinition: jsonDocument(v.QueryDefinition), SortConfig: jsonDocument(v.SortConfig),
		SortOrder: v.SortOrder, SourceURL: v.SourceURL, SyncSchedule: v.SyncSchedule, SyncCadence: usercollections.CadenceOf(v.SyncSchedule),
		NextSyncAt: instantOfStamp(v.NextSyncAt), LastSyncAt: instantOfStamp(v.LastSyncAt),
		LastSyncStatus: v.LastSyncStatus, LastSyncMessage: v.LastSyncMessage,
		ItemCount: v.ItemCount, IncludeInServerCollections: v.IncludeInServerCollections,
		PosterURL: v.PosterURL, PosterThumbhash: v.PosterThumbhash, PosterIsCollage: v.PosterIsCollage && v.PosterURL != "",
	}
	if v.GroupID != nil {
		out.GroupID = new(ID(*v.GroupID))
	}
	if len(v.SourceConfig) > 0 {
		out.SourceConfig = v.SourceConfig
	}
	if len(v.DisplayQueryDefinition) > 0 {
		out.DisplayQueryDefinition = v.DisplayQueryDefinition
	}
	if t := instantOfStamp(v.CreatedAt); t != nil {
		out.CreatedAt = *t
	}
	if t := instantOfStamp(v.UpdatedAt); t != nil {
		out.UpdatedAt = *t
	}
	return out
}

func collectionGroupOf(v handlers.CollectionGroupView) CollectionGroup {
	return CollectionGroup{ID: ID(v.ID), Name: v.Name, Slug: v.Slug, DefaultSortMode: v.DefaultSortMode, SortOrder: v.SortOrder}
}

func stringsOfIDs(ids []ID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}

// intsOfIDs lowers opaque ids to the integer keys the store uses; an id
// that is not one is a validation problem at member[i].
func intsOfIDs(ids []ID, member string) ([]int, *Problem) {
	out := make([]int, 0, len(ids))
	for i, id := range ids {
		n, err := intOfID(id)
		if err != nil || n <= 0 {
			return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: locationBody + "." + member + "[" + strconv.Itoa(i) + "]", Code: codeInvalid, Detail: detailNotLibraryID})
		}
		out = append(out, n)
	}
	return out, nil
}

func (reg *Registry) listCollections(ctx context.Context, in *PersonalCollectionListInput) (*PersonalCollectionCollectionOutput, error) {
	svc, p := reg.personalCollections()
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	profileID := profileFrom(ctx)
	// Marked as /api/v2, the read shows collections' collages (#1618).
	view, err := svc.ListPersonalCollections(handlers.WithNativeAPIV2(ctx), userID, profileID)
	if err != nil {
		return nil, collectionProblem(err)
	}
	marking := in.ContainsItem != ""
	var holding map[string]bool
	if marking {
		if holding, err = svc.PersonalCollectionsHoldingItem(ctx, userID, profileID, in.ContainsItem); err != nil {
			return nil, collectionProblem(err)
		}
	}
	items := make([]PersonalCollection, 0, len(view.Collections))
	for _, c := range view.Collections {
		item := personalCollectionOf(c)
		// Only the profile's own manual collections take a title from Add
		// to collection; every other collection leaves contains out.
		if marking && c.CreatorProfileID == profileID && c.CollectionType == collectionTypeManual {
			item.Contains = new(holding[c.ID])
		}
		items = append(items, item)
	}
	groups := make([]CollectionGroup, 0, len(view.Groups))
	for _, g := range view.Groups {
		groups = append(groups, collectionGroupOf(g))
	}
	return &PersonalCollectionCollectionOutput{Body: PersonalCollectionCollection{Collection: NewCollection(items), Groups: groups}}, nil
}

func (reg *Registry) getCollectionCapabilities(ctx context.Context, _ *CapabilityInput) (*CollectionCapabilitiesOutput, error) {
	svc := reg.deps.PersonalCollections
	if svc == nil {
		return &CollectionCapabilitiesOutput{Body: CollectionCapabilities{Capability: Capability{State: StateNotConfigured}, DisplayFilterFields: []string{}, DisplayFilterPresets: CollectionDisplayFilterPresets{Watched: []string{}, Media: []string{}}, SortPreferenceKinds: []string{}, MDBListSearch: reg.mdblistSearch(), ScheduleTimeZone: reg.scheduleTimeZone()}}, nil
	}
	v := svc.Capabilities()
	features := userstore.CollectionFeatures{}
	if provider, ok := svc.(interface {
		PersonalCollectionFeatures(context.Context, int) (userstore.CollectionFeatures, error)
	}); ok {
		u, p := actingUserID(ctx)
		if p != nil {
			return nil, p
		}
		var err error
		features, err = provider.PersonalCollectionFeatures(ctx, u)
		if err != nil {
			return nil, collectionProblem(err)
		}
	}
	return &CollectionCapabilitiesOutput{Body: CollectionCapabilities{
		Groups: features.Groups, LoginSharing: true, Imports: features.Imports, Artwork: features.Artwork, ItemReorder: features.ItemReorder,
		ImportSources:       collectionImportSources(features.Imports),
		DisplayFilterFields: NonNil(v.DisplayFilterFields),
		DisplayFilterPresets: CollectionDisplayFilterPresets{
			Watched: NonNil(v.DisplayFilterPresets.Watched), Media: NonNil(v.DisplayFilterPresets.Media),
		},
		CollectionDefaultSort:     v.CollectionDefaultSort,
		CollectionSortPreferences: v.CollectionSortPreferences,
		EffectiveCollectionSort:   v.EffectiveCollectionSort,
		SortPreferenceKinds:       NonNil(v.SortPreferenceKinds),
		CreateDescription:         features.Description,
		MDBListSearch:             reg.mdblistSearch(),
		ScheduleTimeZone:          reg.scheduleTimeZone(),
		SyncScheduleEditable:      features.Imports,
		ContainsItem:              true,
		PreviewPosters:            true,
		PosterCollages:            v.PosterCollages && features.Artwork,
	}}, nil
}

// createCollection is the JSON form of v1 POST /collections. The v1
// multipart variant (a poster file beside the document) is not carried:
// poster_source_url covers a remote image, and poster upload is a separate
// image operation.
func (reg *Registry) createCollection(ctx context.Context, in *PersonalCollectionCreateInput) (*PersonalCollectionCreatedOutput, error) {
	svc, p := reg.personalCollections()
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	b := in.Body
	req := handlers.PersonalCollectionCreateRequest{
		Name:                   b.Name,
		QueryDefinition:        b.QueryDefinition,
		SortConfig:             b.SortConfig,
		DisplayQueryDefinition: b.DisplayQueryDefinition,
	}
	if b.Description != nil {
		req.Description = *b.Description
	}
	if b.CollectionType != nil {
		req.CollectionType = *b.CollectionType
	}
	if b.IsShared != nil {
		req.IsShared = *b.IsShared
	}
	if b.IncludeInServerCollections != nil {
		req.IncludeInServerCollections = *b.IncludeInServerCollections
	}
	if b.PosterSourceURL != nil {
		req.PosterSourceURL = *b.PosterSourceURL
	}
	view, err := svc.CreatePersonalCollection(ctx, handlers.PersonalCollectionCreateCommand{
		UserID: userID, ProfileID: profileFrom(ctx), Request: req,
	})
	if err != nil {
		return nil, collectionProblem(err)
	}
	c := personalCollectionOf(view)
	return &PersonalCollectionCreatedOutput{Location: Prefix + "/collections/" + string(c.ID), Body: c}, nil
}

func (reg *Registry) reorderCollections(ctx context.Context, in *CollectionOrderInput) (*CollectionOrderOutput, error) {
	svc, p := reg.personalCollections()
	if p != nil {
		return nil, p
	}
	if in.Body.GroupID != nil {
		return nil, collectionGroupScopeProblem(locationBody + ".group_id")
	}
	guarded, _, p := reg.prepareCollectionGuard(ctx, "order", "", CollectionPreconditions{IfMatch: in.IfMatch, IfNoneMatch: in.IfNoneMatch})
	if p != nil {
		return nil, p
	}
	if err := svc.ReorderPersonalCollections(guarded, claimsFrom(ctx).UserID, profileFrom(ctx), stringsOfIDs(in.Body.OrderedIDs)); err != nil {
		return nil, reg.guardedCollectionError(ctx, "order", "", err)
	}
	return reg.getCollectionOrder(ctx, &CollectionOrderReadInput{})
}

// collectionGroupScopeProblem refuses a group-scoped collection order: each
// profile has one flat order of its own collections.
func collectionGroupScopeProblem(location string) *Problem {
	return validationProblem(location, codeInvalid, "Personal collection groups are not supported; omit group_id.")
}

func (reg *Registry) createCollectionGroup(ctx context.Context, in *CollectionGroupCreateInput) (*CollectionGroupCreatedOutput, error) {
	svc, p := reg.personalCollections()
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	req := handlers.CollectionGroupCreateRequest{Name: in.Body.Name}
	if in.Body.Slug != nil {
		req.Slug = *in.Body.Slug
	}
	if in.Body.DefaultSortMode != nil {
		req.DefaultSortMode = *in.Body.DefaultSortMode
	}
	view, err := svc.CreateCollectionGroup(ctx, userID, req)
	if err != nil {
		return nil, collectionProblem(err)
	}
	g := collectionGroupOf(view)
	return &CollectionGroupCreatedOutput{Location: Prefix + "/collections/groups/" + string(g.ID), Body: g}, nil
}

func (reg *Registry) updateCollectionGroup(ctx context.Context, in *CollectionGroupUpdateInput) (*CollectionGroupOutput, error) {
	svc, p := reg.personalCollections()
	if p != nil {
		return nil, p
	}
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	guarded, _, p := reg.prepareCollectionGuard(ctx, "group", string(in.ID), CollectionPreconditions{IfMatch: in.IfMatch, IfNoneMatch: in.IfNoneMatch})
	if p != nil {
		return nil, p
	}
	_, err := svc.UpdateCollectionGroup(guarded, claimsFrom(ctx).UserID, string(in.ID), handlers.CollectionGroupUpdateRequest{Name: in.Body.Name, Slug: in.Body.Slug, DefaultSortMode: in.Body.DefaultSortMode})
	if err != nil {
		return nil, reg.guardedCollectionError(ctx, "group", string(in.ID), err)
	}
	return reg.getCollectionGroup(ctx, &CollectionGroupIDInput{ID: in.ID})
}

func (reg *Registry) deleteCollectionGroup(ctx context.Context, in *CollectionGroupIDInput) (*struct{}, error) {
	svc, p := reg.personalCollections()
	if p != nil {
		return nil, p
	}
	guarded, _, p := reg.prepareCollectionGuard(ctx, "group", string(in.ID), CollectionPreconditions{IfMatch: in.IfMatch, IfNoneMatch: in.IfNoneMatch})
	if p != nil {
		return nil, p
	}
	if err := svc.DeleteCollectionGroup(guarded, claimsFrom(ctx).UserID, string(in.ID)); err != nil {
		return nil, reg.guardedCollectionError(ctx, "group", string(in.ID), err)
	}
	return nil, nil
}

// importSharedFields lowers the shared import members onto the v1 request.
func importSharedFields(b CollectionImportBase) (handlers.UserImportSharedFields, *Problem) {
	out := handlers.UserImportSharedFields{
		Title: b.Title, Limit: b.Limit,
		DisplayQueryDefinition: b.DisplayQueryDefinition, SortConfig: b.SortConfig,
	}
	if b.Description != nil {
		out.Description = *b.Description
	}
	if b.SyncSchedule != nil {
		out.SyncSchedule = *b.SyncSchedule
	}
	if b.IsShared != nil {
		out.IsShared = *b.IsShared
	}
	if b.PosterURL != nil {
		out.PosterURL = *b.PosterURL
	}
	if b.LibraryIDs != nil {
		ids, p := intsOfIDs(*b.LibraryIDs, "library_ids")
		if p != nil {
			return out, p
		}
		out.LibraryIDs = ids
	}
	return out, nil
}

func collectionSyncResultOf(r *usercollections.SyncResult) *CollectionSyncResult {
	if r == nil {
		return nil
	}
	return &CollectionSyncResult{
		Status: r.Status, Message: r.Message, ItemsMatched: r.ItemsMatched, ItemsUnmatched: r.ItemsUnmatched,
		StartedAt: NewInstant(r.StartedAt), CompletedAt: NewInstant(r.CompletedAt),
	}
}

func importOutput(view handlers.UserImportView) *CollectionImportOutput {
	c := personalCollectionOf(view.Collection)
	return &CollectionImportOutput{
		Location: Prefix + "/collections/" + string(c.ID),
		Body:     CollectionImportResult{Collection: c, Sync: collectionSyncResultOf(view.Sync)},
	}
}

func (reg *Registry) importMDBListCollection(ctx context.Context, in *MDBListCollectionImportInput) (*CollectionImportOutput, error) {
	svc, p := reg.collectionImports()
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	shared, p := importSharedFields(in.Body.CollectionImportBase)
	if p != nil {
		return nil, p
	}
	view, err := svc.ImportMDBList(ctx, userID, profileFrom(ctx), handlers.UserImportMDBListRequest{UserImportSharedFields: shared, URL: in.Body.URL})
	if err != nil {
		return nil, collectionProblem(err)
	}
	return importOutput(view), nil
}

func (reg *Registry) importTMDBCollection(ctx context.Context, in *TMDBCollectionImportInput) (*CollectionImportOutput, error) {
	svc, p := reg.collectionImports()
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	shared, p := importSharedFields(in.Body.CollectionImportBase)
	if p != nil {
		return nil, p
	}
	req := handlers.UserImportTMDBRequest{UserImportSharedFields: shared, Preset: in.Body.Preset}
	if in.Body.MediaType != nil {
		req.MediaType = *in.Body.MediaType
	}
	if in.Body.TimeWindow != nil {
		req.TimeWindow = *in.Body.TimeWindow
	}
	view, err := svc.ImportTMDB(ctx, userID, profileFrom(ctx), req)
	if err != nil {
		return nil, collectionProblem(err)
	}
	return importOutput(view), nil
}

func (reg *Registry) importTMDBListCollection(ctx context.Context, in *TMDBListCollectionImportInput) (*CollectionImportOutput, error) {
	svc, p := reg.collectionImports()
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	shared, p := importSharedFields(in.Body.CollectionImportBase)
	if p != nil {
		return nil, p
	}
	view, err := svc.ImportTMDBList(ctx, userID, profileFrom(ctx), handlers.UserImportTMDBListRequest{UserImportSharedFields: shared, URL: in.Body.URL})
	if err != nil {
		return nil, collectionProblem(err)
	}
	return importOutput(view), nil
}

func (reg *Registry) importTraktCollection(ctx context.Context, in *TraktCollectionImportInput) (*CollectionImportOutput, error) {
	svc, p := reg.collectionImports()
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	shared, p := importSharedFields(in.Body.CollectionImportBase)
	if p != nil {
		return nil, p
	}
	req := handlers.UserImportTraktRequest{UserImportSharedFields: shared, Preset: in.Body.Preset}
	if in.Body.MediaType != nil {
		req.MediaType = *in.Body.MediaType
	}
	view, err := svc.ImportTrakt(ctx, userID, profileFrom(ctx), req)
	if err != nil {
		return nil, collectionProblem(err)
	}
	return importOutput(view), nil
}

func mdblistDiscoveryOf(v handlers.MDBListDiscoveryView) *MDBListListCollectionOutput {
	items := make([]MDBListList, 0, len(v.Lists))
	for _, l := range v.Lists {
		items = append(items, mdblistListOf(l))
	}
	return &MDBListListCollectionOutput{Body: MDBListListCollection{Collection: NewCollection(items), Configured: v.Configured}}
}

func mdblistListOf(l mdblist.ListSummary) MDBListList {
	return MDBListList{
		ID: IDFromInt(l.ID), UserID: IDFromInt(l.UserID), UserName: l.UserName, Name: l.Name, Slug: l.Slug,
		Description: l.Description, MediaType: l.MediaType, Items: l.Items, Likes: l.Likes, URL: l.URL,
	}
}

func (reg *Registry) searchMDBListLists(ctx context.Context, in *MDBListSearchInput) (*MDBListListCollectionOutput, error) {
	svc, p := reg.collectionImports()
	if p != nil {
		return nil, p
	}
	view, err := svc.SearchMDBList(ctx, in.Q)
	if err != nil {
		return nil, collectionProblem(err)
	}
	return mdblistDiscoveryOf(view), nil
}

func (reg *Registry) listTopMDBListLists(ctx context.Context, _ *struct{}) (*MDBListListCollectionOutput, error) {
	svc, p := reg.collectionImports()
	if p != nil {
		return nil, p
	}
	view, err := svc.TopMDBList(ctx)
	if err != nil {
		return nil, collectionProblem(err)
	}
	return mdblistDiscoveryOf(view), nil
}

func (reg *Registry) reorderCollectionGroups(ctx context.Context, in *CollectionGroupOrderInput) (*CollectionGroupsOrderOutput, error) {
	svc, p := reg.personalCollections()
	if p != nil {
		return nil, p
	}
	guarded, _, p := reg.prepareCollectionGuard(ctx, "groups-order", "", CollectionPreconditions{IfMatch: in.IfMatch, IfNoneMatch: in.IfNoneMatch})
	if p != nil {
		return nil, p
	}
	if err := svc.ReorderCollectionGroups(guarded, claimsFrom(ctx).UserID, stringsOfIDs(in.Body.OrderedIDs)); err != nil {
		return nil, reg.guardedCollectionError(ctx, "groups-order", "", err)
	}
	return reg.getCollectionGroupsOrder(ctx, &struct{}{})
}

func (c CollectionCapabilities) capabilityState() string { return StateAvailable }
