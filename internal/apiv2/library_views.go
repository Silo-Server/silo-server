package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/usercollections"
)

// The libraries domain, viewer side: the profile-scoped /library/{id} reads
// that render a library's home page (sections and their cards) and its
// Collections tab. Cards are the shared CatalogItem (catalog_types.go).

// LibraryViewInput names the library a viewer is browsing.
type LibraryViewInput struct {
	ID ID `path:"id" doc:"Library identifier" example:"1"`
}

// LibrarySectionsInput is the listLibrarySections query.
type LibrarySectionsInput struct {
	ID        ID     `path:"id" doc:"Library identifier" example:"1"`
	ImageSize string `query:"image_size" enum:"small,medium,large,original" doc:"Artwork variant to presign; absent picks each surface's default" example:"medium"`
}

// LibrarySectionItemsInput is the getLibrarySectionItems query.
type LibrarySectionItemsInput struct {
	ID        ID     `path:"id" doc:"Library identifier" example:"1"`
	SectionID string `path:"section_id" minLength:"1" doc:"Section identifier from the layout" example:"recently_added"`
	ImageSize string `query:"image_size" enum:"small,medium,large,original" doc:"Artwork variant to presign; absent picks each surface's default" example:"medium"`
}

// LibraryCollectionItemsInput names one collection of the library and the
// page of its items.
type LibraryCollectionItemsInput struct {
	ID           ID     `path:"id" doc:"Library identifier" example:"1"`
	CollectionID string `path:"collection_id" minLength:"1" doc:"Collection identifier" example:"01J9Z8C3W4R5T6Y7U8I9O0P1Q2"`
	LimitParam
	Cursor string `query:"cursor" doc:"Opaque cursor from page.next_cursor" example:"eyJvZmZzZXQiOjUwfQ"`
}

// SectionLayoutEntry is one section of a page layout, without items.
type SectionLayoutEntry struct {
	ID          string `json:"id" example:"recently_added"`
	SectionType string `json:"section_type" example:"recently_added"`
	Title       string `json:"title" example:"Recently Added"`
	Featured    bool   `json:"featured" example:"false"`
	ItemLimit   int    `json:"item_limit" example:"20"`
	IsCustom    bool   `json:"is_custom" doc:"An administrator-defined section rather than a built-in one" example:"false"`
	Customized  bool   `json:"customized" doc:"The profile has overridden the section's placement or visibility" example:"false"`
}

// SectionLayout is a page's sections in display order.
type SectionLayout struct {
	Sections []SectionLayoutEntry `json:"sections" doc:"Empty, never null"`
}

// SectionLayoutOutput is the getLibraryLayout response.
type SectionLayoutOutput struct {
	Body SectionLayout
}

// Section is one page section with its cards.
type Section struct {
	ID          string        `json:"id" example:"recently_added"`
	SectionType string        `json:"section_type" example:"recently_added"`
	Title       string        `json:"title" example:"Recently Added"`
	Featured    bool          `json:"featured" example:"false"`
	ItemLimit   int           `json:"item_limit" example:"20"`
	TotalCount  int           `json:"total_count" doc:"Items the section has in total, at least the number returned" example:"120"`
	IsCustom    bool          `json:"is_custom" example:"false"`
	Customized  bool          `json:"customized" example:"false"`
	Items       []CatalogItem `json:"items" doc:"Empty, never null"`
}

// SectionCollection is a page's sections with their cards.
type SectionCollection struct {
	Sections []Section `json:"sections" doc:"Empty, never null"`
}

// SectionCollectionOutput is the listLibrarySections response.
type SectionCollectionOutput struct {
	Body SectionCollection
}

// SectionOutput is the getLibrarySectionItems response.
type SectionOutput struct {
	Body Section
}

// CuratedCollection is one curated collection in full.
type CuratedCollection struct {
	ID                string          `json:"id" example:"01J9Z8C3W4R5T6Y7U8I9O0P1Q2"`
	LibraryID         ID              `json:"library_id" example:"1"`
	LibraryIDs        []ID            `json:"library_ids" doc:"Every library the collection spans; empty, never null"`
	Slug              string          `json:"slug" example:"oscar-winners"`
	Title             string          `json:"title" example:"Oscar Winners"`
	Description       string          `json:"description"`
	CollectionType    string          `json:"collection_type" doc:"manual, smart, or an import type" example:"manual"`
	Visibility        string          `json:"visibility" example:"visible"`
	SortOrder         int             `json:"sort_order" example:"0"`
	GroupID           *string         `json:"group_id" doc:"null when ungrouped"`
	Featured          bool            `json:"featured" example:"false"`
	PosterURL         string          `json:"poster_url" doc:"Presigned, short-lived; empty when none"`
	BackdropURL       string          `json:"backdrop_url" doc:"Presigned, short-lived; empty when none"`
	PosterThumbhash   string          `json:"poster_thumbhash,omitempty"`
	BackdropThumbhash string          `json:"backdrop_thumbhash,omitempty"`
	SourceURL         string          `json:"source_url" doc:"Where an imported collection came from; empty otherwise"`
	QueryDefinition   json.RawMessage `json:"query_definition" doc:"Smart collection query; null when not a smart collection"`
	SortConfig        json.RawMessage `json:"sort_config" doc:"Sort configuration document; null when default"`
	SourceConfig      json.RawMessage `json:"source_config" doc:"Import source configuration; null when not imported"`
	ManagementMode    string          `json:"management_mode" example:"manual"`
	ManagementSource  string          `json:"management_source"`
	ManagementKey     string          `json:"management_key"`
	LastSyncStatus    string          `json:"last_sync_status"`
	LastSyncMessage   string          `json:"last_sync_message"`
	LastSyncAt        *Instant        `json:"last_sync_at,omitempty"`
	SyncSchedule      string          `json:"sync_schedule,omitempty"`
	NextSyncAt        *Instant        `json:"next_sync_at,omitempty"`
	ItemCount         int             `json:"item_count" example:"12"`
	CreatedAt         Instant         `json:"created_at" example:"2026-01-02T03:04:05.678Z"`
	UpdatedAt         Instant         `json:"updated_at" example:"2026-01-02T03:04:05.678Z"`
}

// LibraryCollectionCard is a collection as the Collections tab shows it.
type LibraryCollectionCard struct {
	ID               string  `json:"id" example:"01J9Z8C3W4R5T6Y7U8I9O0P1Q2"`
	Title            string  `json:"title" example:"Oscar Winners"`
	PosterURL        string  `json:"poster_url" doc:"Presigned, short-lived; empty when none"`
	PosterThumbhash  string  `json:"poster_thumbhash,omitempty"`
	ItemCount        int     `json:"item_count" example:"12"`
	Featured         bool    `json:"featured,omitempty"`
	CreatorProfileID *string `json:"creator_profile_id,omitempty" doc:"Present on a personal collection: the profile that made it"`
}

// LibraryCollectionGroup is one group of the Collections tab.
type LibraryCollectionGroup struct {
	ID          string                  `json:"id" example:"01J9Z8C3W4R5T6Y7U8I9O0P1Q3"`
	Name        string                  `json:"name" example:"Franchises"`
	Kind        string                  `json:"kind" doc:"admin or user_collections" example:"admin"`
	SortMode    string                  `json:"sort_mode" example:"name_asc"`
	SortOrder   int                     `json:"sort_order" example:"0"`
	Collections []LibraryCollectionCard `json:"collections"`
}

// LibraryCollectionUngrouped is the tab's collections outside any group.
type LibraryCollectionUngrouped struct {
	SortOrder   int                     `json:"sort_order" example:"9999"`
	Collections []LibraryCollectionCard `json:"collections"`
}

// LibraryCollectionTab is a library's Collections tab.
type LibraryCollectionTab struct {
	LibraryID   ID                          `json:"library_id" example:"1"`
	Collections []CuratedCollection         `json:"collections" doc:"Every visible curated collection in full; empty, never null"`
	Groups      []LibraryCollectionGroup    `json:"groups" doc:"Non-empty groups in display order; empty when groups are not configured"`
	Ungrouped   *LibraryCollectionUngrouped `json:"ungrouped,omitempty" doc:"Absent when every collection is grouped"`
}

// LibraryCollectionTabOutput is the getLibraryCollections response.
type LibraryCollectionTabOutput struct {
	Body LibraryCollectionTab
}

// UserCollection is a personal collection the viewer opted into a library tab.
type UserCollection struct {
	ID               string  `json:"id" example:"01J9Z8C3W4R5T6Y7U8I9O0P1Q4"`
	CreatorProfileID string  `json:"creator_profile_id" example:"p-owner"`
	Name             string  `json:"name" example:"Rainy days"`
	Description      string  `json:"description,omitempty"`
	CollectionType   string  `json:"collection_type" example:"manual"`
	ItemCount        int     `json:"item_count" example:"4"`
	PosterURL        string  `json:"poster_url,omitempty" doc:"Presigned, short-lived"`
	PosterThumbhash  string  `json:"poster_thumbhash,omitempty"`
	CreatedAt        Instant `json:"created_at" example:"2026-01-02T03:04:05.678Z"`
	UpdatedAt        Instant `json:"updated_at" example:"2026-01-02T03:04:05.678Z"`
}

// UserCollectionCollection is the listLibraryUserCollections envelope.
type UserCollectionCollection struct {
	Collection[UserCollection]
}

// UserCollectionCollectionOutput is the listLibraryUserCollections response.
type UserCollectionCollectionOutput struct {
	Body UserCollectionCollection
}

// CatalogItemCollection is a page of cards.
type CatalogItemCollection struct {
	Collection[CatalogItem]
}

// opGetLibraryCollectionItems is the operation id; the cursor scope is
// bound to it.
const opGetLibraryCollectionItems = "getLibraryCollectionItems"

// CatalogItemCollectionOutput is the getLibraryCollectionItems response.
type CatalogItemCollectionOutput struct {
	Body CatalogItemCollection
}

func registerLibraryViews(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	viewer := func(op huma.Operation) Operation {
		return Operation{Operation: op, Class: ClassProfileScoped, ServiceBacked: true}
	}
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/library/{id}/layout", "getLibraryLayout", "libraries",
		"The library's section layout for the acting profile, without items.")), reg.getLibraryLayout)
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/library/{id}/sections", "listLibrarySections", "libraries",
		"The library's sections with their cards, as the acting profile sees them.")), reg.listLibrarySections)
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/library/{id}/sections/{section_id}/items", "getLibrarySectionItems", "libraries",
		"One section of the library with its cards.")), reg.getLibrarySectionItems)
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/library/{id}/collections", "getLibraryCollections", "libraries",
		"The library's Collections tab: curated collections, their groups, and the viewer's opted-in personal collections.")), reg.getLibraryCollections)
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/library/{id}/collections/{collection_id}/items", opGetLibraryCollectionItems, "libraries",
		"Page the cards of one visible collection of the library, in its curated or query order.")), func(ctx context.Context, in *LibraryCollectionItemsInput) (*CatalogItemCollectionOutput, error) {
		return reg.getLibraryCollectionItems(ctx, cursors, in)
	})
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/library/{id}/user-collections", "listLibraryUserCollections", "libraries",
		"The viewer's own personal collections opted into this library's tab.")), reg.listLibraryUserCollections)
}

// viewerIdentity is the profile-scoped caller; the gate guarantees both.
func viewerIdentity(ctx context.Context) (int, string, *Problem) {
	claims := claimsFrom(ctx)
	profileID := profileFrom(ctx)
	if claims == nil || profileID == "" {
		return 0, "", NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	return claims.UserID, profileID, nil
}

// sectionViewer is what the section seams need beyond the context. The v2
// listener does not read a device header, so the access filter carries no
// device id.
func sectionViewer(ctx context.Context, imageSize string) handlers.SectionViewer {
	size, err := imagesize.Parse(imageSize)
	if err != nil {
		size = imagesize.Unset
	}
	return handlers.SectionViewer{Access: handlers.ViewerAccessFilter(ctx, ""), ImageSize: size}
}

func (reg *Registry) librarySections() (LibrarySectionService, *Problem) {
	if reg.deps.LibrarySections == nil {
		return nil, unavailable("library sections")
	}
	return reg.deps.LibrarySections, nil
}

func (reg *Registry) libraryCollections() (LibraryCollectionService, *Problem) {
	if reg.deps.LibraryCollections == nil {
		return nil, unavailable("library collections")
	}
	return reg.deps.LibraryCollections, nil
}

func (reg *Registry) getLibraryLayout(ctx context.Context, in *LibraryViewInput) (*SectionLayoutOutput, error) {
	svc, p := reg.librarySections()
	if p != nil {
		return nil, p
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	if _, _, p := viewerIdentity(ctx); p != nil {
		return nil, p
	}
	view, err := svc.LibraryLayout(ctx, id)
	if err != nil {
		return nil, serviceProblem(err)
	}
	out := SectionLayout{Sections: make([]SectionLayoutEntry, 0, len(view.Sections))}
	for _, s := range view.Sections {
		out.Sections = append(out.Sections, SectionLayoutEntry{ID: s.ID, SectionType: s.SectionType, Title: s.Title, Featured: s.Featured, ItemLimit: s.ItemLimit, IsCustom: s.IsCustom, Customized: s.Customized})
	}
	return &SectionLayoutOutput{Body: out}, nil
}

func sectionOf(v handlers.SectionView) Section {
	items := make([]CatalogItem, 0, len(v.Items))
	for _, item := range v.Items {
		items = append(items, catalogItemOfSection(item))
	}
	return Section{ID: v.ID, SectionType: v.SectionType, Title: v.Title, Featured: v.Featured, ItemLimit: v.ItemLimit, TotalCount: v.TotalCount, IsCustom: v.IsCustom, Customized: v.Customized, Items: items}
}

func (reg *Registry) listLibrarySections(ctx context.Context, in *LibrarySectionsInput) (*SectionCollectionOutput, error) {
	svc, p := reg.librarySections()
	if p != nil {
		return nil, p
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	if _, _, p := viewerIdentity(ctx); p != nil {
		return nil, p
	}
	view, err := svc.LibrarySections(ctx, id, sectionViewer(ctx, in.ImageSize))
	if err != nil {
		return nil, serviceProblem(err)
	}
	out := SectionCollection{Sections: make([]Section, 0, len(view.Sections))}
	for _, s := range view.Sections {
		out.Sections = append(out.Sections, sectionOf(s))
	}
	return &SectionCollectionOutput{Body: out}, nil
}

func (reg *Registry) getLibrarySectionItems(ctx context.Context, in *LibrarySectionItemsInput) (*SectionOutput, error) {
	svc, p := reg.librarySections()
	if p != nil {
		return nil, p
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	if _, _, p := viewerIdentity(ctx); p != nil {
		return nil, p
	}
	view, err := svc.LibrarySectionItems(ctx, id, in.SectionID, sectionViewer(ctx, in.ImageSize))
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &SectionOutput{Body: sectionOf(view)}, nil
}

func instantOfStamp(s string) *Instant {
	if s == "" {
		return nil
	}
	return instantOfRFC3339(&s)
}

func curatedCollectionOf(v handlers.LibraryCollectionView) CuratedCollection {
	ids := make([]ID, 0, len(v.LibraryIDs))
	for _, id := range v.LibraryIDs {
		ids = append(ids, IDFromInt(int64(id)))
	}
	created, updated := Instant{}, Instant{}
	if t := instantOfStamp(v.CreatedAt); t != nil {
		created = *t
	}
	if t := instantOfStamp(v.UpdatedAt); t != nil {
		updated = *t
	}
	return CuratedCollection{
		ID: v.ID, LibraryID: IDFromInt(int64(v.LibraryID)), LibraryIDs: ids, Slug: v.Slug, Title: v.Title, Description: v.Description,
		CollectionType: v.CollectionType, Visibility: v.Visibility, SortOrder: v.SortOrder, GroupID: v.GroupID, Featured: v.Featured,
		PosterURL: v.PosterURL, BackdropURL: v.BackdropURL, PosterThumbhash: v.PosterThumbhash, BackdropThumbhash: v.BackdropThumbhash,
		SourceURL: v.SourceURL, QueryDefinition: jsonValue(v.QueryDefinition), SortConfig: jsonValue(v.SortConfig), SourceConfig: jsonValue(v.SourceConfig),
		ManagementMode: v.ManagementMode, ManagementSource: v.ManagementSource, ManagementKey: v.ManagementKey,
		LastSyncStatus: v.LastSyncStatus, LastSyncMessage: v.LastSyncMessage, LastSyncAt: instantOfStamp(v.LastSyncAt), SyncSchedule: v.SyncSchedule, NextSyncAt: instantOfStamp(v.NextSyncAt),
		ItemCount: v.ItemCount, CreatedAt: created, UpdatedAt: updated,
	}
}

// jsonValue is JSON null where the store holds no document, so the member
// is always a JSON value.
func jsonValue(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`null`)
	}
	return raw
}

func collectionCardsOf(cards []handlers.LibraryCollectionTabEntryView) []LibraryCollectionCard {
	out := make([]LibraryCollectionCard, 0, len(cards))
	for _, c := range cards {
		out = append(out, LibraryCollectionCard{ID: c.ID, Title: c.Title, PosterURL: c.PosterURL, PosterThumbhash: c.PosterThumbhash, ItemCount: c.ItemCount, Featured: c.Featured, CreatorProfileID: c.CreatorProfileID})
	}
	return out
}

func (reg *Registry) getLibraryCollections(ctx context.Context, in *LibraryViewInput) (*LibraryCollectionTabOutput, error) {
	svc, p := reg.libraryCollections()
	if p != nil {
		return nil, p
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	view, err := svc.LibraryCollectionsTab(ctx, id, userID, profileID)
	if err != nil {
		return nil, serviceProblem(err)
	}
	out := LibraryCollectionTab{
		LibraryID:   IDFromInt(int64(view.LibraryID)),
		Collections: make([]CuratedCollection, 0, len(view.Collections)),
		Groups:      make([]LibraryCollectionGroup, 0, len(view.Groups)),
	}
	for _, c := range view.Collections {
		out.Collections = append(out.Collections, curatedCollectionOf(c))
	}
	for _, g := range view.Groups {
		out.Groups = append(out.Groups, LibraryCollectionGroup{ID: g.ID, Name: g.Name, Kind: string(g.Kind), SortMode: string(g.SortMode), SortOrder: g.SortOrder, Collections: collectionCardsOf(g.Collections)})
	}
	if view.Ungrouped != nil {
		out.Ungrouped = &LibraryCollectionUngrouped{SortOrder: view.Ungrouped.SortOrder, Collections: collectionCardsOf(view.Ungrouped.Collections)}
	}
	return &LibraryCollectionTabOutput{Body: out}, nil
}

// getLibraryCollectionItems pages the collection's order by offset behind
// an opaque cursor: a curated collection has no natural key beyond its
// stored position, and a smart collection's order is whatever its query
// sorts by. The seam cuts the window before loading items and reports
// whether positions follow it, so a page the access filter thins out still
// points at the next window.
func (reg *Registry) getLibraryCollectionItems(ctx context.Context, cursors *Cursors, in *LibraryCollectionItemsInput) (*CatalogItemCollectionOutput, error) {
	svc, p := reg.libraryCollections()
	if p != nil {
		return nil, p
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	scope := CursorScope{
		OperationID: opGetLibraryCollectionItems,
		// The items are access-filtered, so the cursor dies with the viewer
		// policy it was minted under.
		Security:   strconv.Itoa(userID) + "/" + profileID + "/" + viewerScopeDigest(ctx),
		Filter:     "library_id=" + strconv.Itoa(id) + "&collection_id=" + in.CollectionID,
		Sort:       "collection",
		Tiebreaker: "position",
	}
	offset, p := decodeOffset(cursors, scope, in.Cursor)
	if p != nil {
		return nil, p
	}
	views, hasMore, err := svc.LibraryCollectionItems(ctx, id, in.CollectionID, handlers.ViewerAccessFilter(ctx, ""), handlers.CollectionItemPage{Limit: in.Limit, Offset: offset})
	if err != nil {
		return nil, collectionItemsProblem(err)
	}
	next := ""
	if hasMore {
		next, err = cursors.Encode(scope, offsetPosition{Offset: offset + in.Limit})
		if err != nil {
			return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
		}
	}
	items := make([]CatalogItem, 0, len(views))
	for _, v := range views {
		items = append(items, catalogItemOfListing(v))
	}
	return &CatalogItemCollectionOutput{Body: CatalogItemCollection{Collection: Paginated(items, next)}}, nil
}

// collectionItemsProblem maps the v1 decision: a smart query the executor
// refuses is a 422 on the stored definition, the rest follow the status.
func collectionItemsProblem(err error) *Problem {
	apiErr, ok := err.(*handlers.APIError) //nolint:errorlint // the seams return the value directly
	if ok && apiErr.Status == http.StatusBadRequest {
		return NewProblem(TypeValidationFailed, "The collection's query definition could not be run; see errors.").
			WithErrors(ProblemError{Location: "path.collection_id", Code: codeInvalid, Detail: apiErr.Message})
	}
	return serviceProblem(err)
}

func userCollectionOf(c usercollections.ServerVisibleCollection) UserCollection {
	created, updated := Instant{}, Instant{}
	if t := instantOfStamp(c.CreatedAt); t != nil {
		created = *t
	}
	if t := instantOfStamp(c.UpdatedAt); t != nil {
		updated = *t
	}
	return UserCollection{ID: c.ID, CreatorProfileID: c.CreatorProfileID, Name: c.Name, Description: c.Description, CollectionType: c.CollectionType,
		ItemCount: c.ItemCount, PosterURL: c.PosterURL, PosterThumbhash: c.PosterThumbhash, CreatedAt: created, UpdatedAt: updated}
}

func (reg *Registry) listLibraryUserCollections(ctx context.Context, in *LibraryViewInput) (*UserCollectionCollectionOutput, error) {
	svc, p := reg.libraryCollections()
	if p != nil {
		return nil, p
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	views, err := svc.LibraryUserCollections(ctx, id, userID, profileID)
	if err != nil {
		return nil, serviceProblem(err)
	}
	items := make([]UserCollection, 0, len(views))
	for _, v := range views {
		items = append(items, userCollectionOf(v))
	}
	return &UserCollectionCollectionOutput{Body: UserCollectionCollection{Collection: NewCollection(items)}}, nil
}
