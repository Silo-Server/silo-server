package apiv2

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/collections/templates"
	"github.com/Silo-Server/silo-server/internal/usercollections"
)

type PersonalCollectionPosterForm struct {
	Poster    huma.FormFile `form:"poster" contentType:"image/jpeg,image/png,image/webp" required:"false"`
	SourceURL string        `form:"source_url" required:"false"`
}

type PersonalCollectionPosterInput struct {
	ID      ID `path:"id"`
	RawBody huma.MultipartFormFiles[PersonalCollectionPosterForm]
}

type PersonalCollectionIDInput struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	ID          ID     `path:"id"`
}
type PersonalCollectionUpdate struct {
	Name                       *string         `json:"name,omitempty" nullable:"false" minLength:"1"`
	Description                *string         `json:"description,omitempty" nullable:"false"`
	IsShared                   *bool           `json:"is_shared,omitempty" nullable:"false"`
	AllowedProfileIDs          *[]ID           `json:"allowed_profile_ids,omitempty" nullable:"false"`
	QueryDefinition            json.RawMessage `json:"query_definition,omitempty"`
	SortConfig                 json.RawMessage `json:"sort_config,omitempty"`
	SourceURL                  *string         `json:"source_url,omitempty" nullable:"false"`
	MaxItems                   *int            `json:"max_items,omitempty" nullable:"false" minimum:"0"`
	LibraryIDs                 *[]ID           `json:"library_ids,omitempty" nullable:"false"`
	DisplayQueryDefinition     json.RawMessage `json:"display_query_definition,omitempty"`
	IncludeInServerCollections *bool           `json:"include_in_server_collections,omitempty" nullable:"false"`
	PosterSourceURL            *string         `json:"poster_source_url,omitempty" nullable:"false"`
	GroupID                    *ID             `json:"group_id,omitempty" nullable:"true" doc:"Null removes the group; omitted leaves it unchanged"`
}
type PersonalCollectionUpdateInput struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	ID          ID     `path:"id"`
	Body        PersonalCollectionUpdate
	RawBody     []byte
}
type PersonalCollectionItemInput struct {
	ID     ID `path:"id"`
	ItemID ID `path:"item_id"`
}
type PersonalCollectionAddItemInput struct {
	ID     ID `path:"id"`
	ItemID ID `path:"item_id"`
	Body   struct {
		Position int `json:"position" minimum:"0"`
	}
}
type PersonalCollectionItemsOrderInput struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	ID          ID     `path:"id"`
	Body        struct {
		OrderedIDs []ID `json:"ordered_ids" maxItems:"10000"`
	}
}
type PersonalCollectionPreviewInput struct {
	Body struct {
		QueryDefinition json.RawMessage `json:"query_definition"`
		Limit           int             `json:"limit" minimum:"1" maximum:"100" default:"20"`
	}
}
type PersonalCollectionPreviewItem struct {
	ContentID ID     `json:"content_id"`
	Title     string `json:"title"`
	Type      string `json:"type"`
}
type PersonalCollectionPreviewOutput struct {
	Body struct {
		Collection[PersonalCollectionPreviewItem]
		Total int `json:"total"`
	}
}
type PersonalCollectionImageInput struct {
	ID   ID     `path:"id"`
	Type string `query:"type" enum:"poster" default:"poster"`
}
type CollectionSortPreference struct {
	CollectionKind string `json:"collection_kind" enum:"library,user,watchlist,favorites"`
	CollectionID   ID     `json:"collection_id,omitempty"`
	Field          string `json:"field"`
	Order          string `json:"order" enum:",asc,desc"`
}
type CollectionSortPreferenceInput struct{ Body CollectionSortPreference }
type CollectionSortPreferenceDeleteInput struct {
	CollectionKind string `query:"collection_kind" required:"true" enum:"library,user,watchlist,favorites"`
	CollectionID   ID     `query:"collection_id"`
}
type CollectionSortPreferenceOutput struct{ Body CollectionSortPreference }
type PersonalCollectionTemplatesOutput struct{ Body templates.Catalog }
type PersonalCollectionSyncOutput struct{ Body CollectionSyncResult }
type ServerCollectionLibrary struct {
	LibraryID   ID                      `json:"library_id"`
	LibraryName string                  `json:"library_name"`
	TotalCount  int                     `json:"total_count"`
	Collections []LibraryCollectionCard `json:"collections"`
}
type ServerCollectionsOutput struct {
	Body struct {
		Libraries []ServerCollectionLibrary `json:"libraries"`
	}
}

type personalCollectionLifecycle interface {
	GetPersonalCollection(context.Context, int, string, string) (handlers.PersonalCollectionView, error)
	UpdatePersonalCollection(context.Context, handlers.PersonalCollectionUpdateCommand) (handlers.PersonalCollectionView, error)
	DeletePersonalCollection(context.Context, int, string, string) error
	AddPersonalCollectionItem(context.Context, int, string, string, string, int) error
	RemovePersonalCollectionItem(context.Context, int, string, string, string) error
	ReorderPersonalCollectionItems(context.Context, int, string, string, []string) error
	PreviewPersonalCollection(context.Context, handlers.PersonalCollectionPreviewRequest, catalogsvc.AccessFilter) (handlers.PersonalCollectionPreviewView, error)
	DeletePersonalCollectionImage(context.Context, int, string, string, string) error
	SetPersonalCollectionSortPreference(context.Context, int, string, catalogsvc.AccessFilter, handlers.CollectionSortPreferenceRequest) (handlers.CollectionSortPreferenceResponse, error)
	ClearPersonalCollectionSortPreference(context.Context, int, string, string, string) error
}
type personalCollectionImportLifecycle interface {
	CollectionTemplates() templates.Catalog
	SyncPersonalCollection(context.Context, int, string, string) (*usercollections.SyncResult, error)
}

func (reg *Registry) collectionLifecycle() (personalCollectionLifecycle, *Problem) {
	s, ok := reg.deps.PersonalCollections.(personalCollectionLifecycle)
	if !ok {
		return nil, unavailable("collection lifecycle")
	}
	return s, nil
}
func registerPersonalCollectionLifecycle(reg *Registry) {
	op := func(method, path, id, summary string) Operation {
		o := Operation{Operation: humaOp(method, Prefix+path, id, "collections", summary), Class: ClassProfileScoped, ServiceBacked: true}
		if method != http.MethodGet {
			o.DemoRestricted = true
			o.RetrySafety = RetrySafetyNonRetryable
			o.Guarded = id == "updateCollection" || id == "deleteCollection" || id == "reorderCollectionItems"
		}
		return o
	}
	upload := op(http.MethodPut, "/collections/{id}/poster", "uploadCollectionPoster", "Upload the creator's collection poster.")
	upload.MaxBodyBytes = maxPosterBytes + posterFormOverhead
	Register(reg, upload, reg.uploadPersonalCollectionPoster)
	Register(reg, op(http.MethodGet, "/collections/{id}", "getCollection", "Read a collection visible to the acting profile."), reg.getPersonalCollection)
	Register(reg, op(http.MethodPatch, "/collections/{id}", "updateCollection", "Update the creator's collection; omitted fields are unchanged."), reg.updatePersonalCollection)
	noContent := func(method, path, id, summary string) Operation {
		o := op(method, path, id, summary)
		o.DefaultStatus = http.StatusNoContent
		return o
	}
	Register(reg, noContent(http.MethodDelete, "/collections/{id}", "deleteCollection", "Delete the creator's collection."), reg.deletePersonalCollection)
	Register(reg, noContent(http.MethodPut, "/collections/{id}/items/{item_id}", "addCollectionItem", "Add an item to the creator's manual collection."), reg.addPersonalCollectionItem)
	Register(reg, noContent(http.MethodDelete, "/collections/{id}/items/{item_id}", "removeCollectionItem", "Remove an item from the creator's manual collection."), reg.removePersonalCollectionItem)
	Register(reg, op(http.MethodPut, "/collections/{id}/items/order", "reorderCollectionItems", "Replace the manual collection item order."), reg.reorderPersonalCollectionItems)
	preview := op(http.MethodPost, "/collections/preview", "previewCollection", "Preview a smart query within the acting profile's access.")
	preview.RetrySafety = RetrySafetyNaturalIdempotent
	Register(reg, preview, reg.previewPersonalCollection)
	Register(reg, noContent(http.MethodDelete, "/collections/{id}/image", "deleteCollectionImage", "Remove the creator's collection poster."), reg.deletePersonalCollectionImage)
	Register(reg, op(http.MethodPut, "/collections/sort-preference", "setCollectionSortPreference", "Save the acting profile's collection sort preference."), reg.setPersonalCollectionSortPreference)
	Register(reg, noContent(http.MethodDelete, "/collections/sort-preference", "clearCollectionSortPreference", "Clear the acting profile's collection sort preference."), reg.clearPersonalCollectionSortPreference)
	Register(reg, op(http.MethodGet, "/collections/templates", "listCollectionTemplates", "List supported collection import templates."), reg.listPersonalCollectionTemplates)
	Register(reg, op(http.MethodPost, "/collections/{id}/sync", "syncCollection", "Synchronize the creator's imported collection. The operation is not retryable and does not provide cluster-wide coalescing."), reg.syncPersonalCollection)
	Register(reg, op(http.MethodGet, "/collections/server", "listServerCollections", "List visible server collections grouped by library."), reg.listServerCollections)
}
func (reg *Registry) getPersonalCollection(ctx context.Context, in *PersonalCollectionIDInput) (*PersonalCollectionOutput, error) {
	svc, p := reg.collectionEditors()
	if p != nil {
		return nil, p
	}
	v, err := svc.PersonalCollectionEditor(ctx, claimsFrom(ctx).UserID, profileFrom(ctx), string(in.ID))
	if err != nil {
		return nil, collectionProblem(err)
	}
	return &PersonalCollectionOutput{ETag: collectionEditorTag(ctx, "collection", string(in.ID), v.Revision).String(), Body: personalCollectionOf(v.Collection)}, nil
}
func (reg *Registry) updatePersonalCollection(ctx context.Context, in *PersonalCollectionUpdateInput) (*PersonalCollectionOutput, error) {
	s, p := reg.collectionLifecycle()
	if p != nil {
		return nil, p
	}
	u, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	if p := rejectNonNullableNulls(in.RawBody, map[string]bool{"group_id": true}); p != nil {
		return nil, p
	}
	guarded, _, p := reg.prepareCollectionGuard(ctx, "collection", string(in.ID), CollectionPreconditions{IfMatch: in.IfMatch, IfNoneMatch: in.IfNoneMatch})
	if p != nil {
		return nil, p
	}
	if in.Body.PosterSourceURL != nil {
		return nil, NewProblem(TypeValidationFailed, "Update artwork using the separate poster operation.")
	}
	b := in.Body
	r := handlers.PersonalCollectionUpdateRequest{Name: b.Name, Description: b.Description, IsShared: b.IsShared, QueryDefinition: b.QueryDefinition, SortConfig: b.SortConfig, SourceURL: b.SourceURL, MaxItems: b.MaxItems, DisplayQueryDefinition: b.DisplayQueryDefinition, IncludeInServerCollections: b.IncludeInServerCollections, PosterSourceURL: b.PosterSourceURL}
	if b.AllowedProfileIDs != nil {
		r.AllowedProfileIDs = new(stringsOfIDs(*b.AllowedProfileIDs))
	}
	if b.LibraryIDs != nil {
		ids, p := intsOfIDs(*b.LibraryIDs, "library_ids")
		if p != nil {
			return nil, p
		}
		r.LibraryIDs = &ids
	}
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(in.RawBody, &fields); e != nil {
		return nil, collectionProblem(e)
	}
	if raw, ok := fields["group_id"]; ok {
		if e := json.Unmarshal(raw, &r.GroupID); e != nil {
			return nil, collectionProblem(e)
		}
	}
	_, e := s.UpdatePersonalCollection(guarded, handlers.PersonalCollectionUpdateCommand{UserID: u, ProfileID: profileFrom(ctx), CollectionID: string(in.ID), Request: r})
	if e != nil {
		return nil, reg.guardedCollectionError(ctx, "collection", string(in.ID), e)
	}
	return reg.getPersonalCollection(ctx, &PersonalCollectionIDInput{ID: in.ID})
}
func (reg *Registry) deletePersonalCollection(ctx context.Context, in *PersonalCollectionIDInput) (*struct{}, error) {
	svc, p := reg.collectionLifecycle()
	if p != nil {
		return nil, p
	}
	guarded, _, p := reg.prepareCollectionGuard(ctx, "collection", string(in.ID), CollectionPreconditions{IfMatch: in.IfMatch, IfNoneMatch: in.IfNoneMatch})
	if p != nil {
		return nil, p
	}
	if err := svc.DeletePersonalCollection(guarded, claimsFrom(ctx).UserID, profileFrom(ctx), string(in.ID)); err != nil {
		return nil, reg.guardedCollectionError(ctx, "collection", string(in.ID), err)
	}
	return nil, nil
}
func (reg *Registry) addPersonalCollectionItem(ctx context.Context, in *PersonalCollectionAddItemInput) (*struct{}, error) {
	s, p := reg.collectionLifecycle()
	if p != nil {
		return nil, p
	}
	u, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	if e := s.AddPersonalCollectionItem(ctx, u, profileFrom(ctx), string(in.ID), string(in.ItemID), in.Body.Position); e != nil {
		return nil, collectionProblem(e)
	}
	return nil, nil
}
func (reg *Registry) removePersonalCollectionItem(ctx context.Context, in *PersonalCollectionItemInput) (*struct{}, error) {
	s, p := reg.collectionLifecycle()
	if p != nil {
		return nil, p
	}
	u, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	if e := s.RemovePersonalCollectionItem(ctx, u, profileFrom(ctx), string(in.ID), string(in.ItemID)); e != nil {
		return nil, collectionProblem(e)
	}
	return nil, nil
}
func (reg *Registry) reorderPersonalCollectionItems(ctx context.Context, in *PersonalCollectionItemsOrderInput) (*CollectionItemsOrderOutput, error) {
	svc, p := reg.collectionLifecycle()
	if p != nil {
		return nil, p
	}
	guarded, _, p := reg.prepareCollectionGuard(ctx, "items-order", string(in.ID), CollectionPreconditions{IfMatch: in.IfMatch, IfNoneMatch: in.IfNoneMatch})
	if p != nil {
		return nil, p
	}
	if err := svc.ReorderPersonalCollectionItems(guarded, claimsFrom(ctx).UserID, profileFrom(ctx), string(in.ID), stringsOfIDs(in.Body.OrderedIDs)); err != nil {
		return nil, reg.guardedCollectionError(ctx, "items-order", string(in.ID), err)
	}
	return reg.getCollectionItemsOrder(ctx, &PersonalCollectionIDInput{ID: in.ID})
}
func (reg *Registry) previewPersonalCollection(ctx context.Context, in *PersonalCollectionPreviewInput) (*PersonalCollectionPreviewOutput, error) {
	s, p := reg.collectionLifecycle()
	if p != nil {
		return nil, p
	}
	v, e := s.PreviewPersonalCollection(ctx, handlers.PersonalCollectionPreviewRequest{QueryDefinition: in.Body.QueryDefinition, Limit: in.Body.Limit}, handlers.AccessFilterFromContext(ctx, ""))
	if e != nil {
		return nil, collectionProblem(e)
	}
	items := make([]PersonalCollectionPreviewItem, 0, len(v.Items))
	for _, i := range v.Items {
		items = append(items, PersonalCollectionPreviewItem{ContentID: ID(i.ContentID), Title: i.Title, Type: i.Type})
	}
	out := &PersonalCollectionPreviewOutput{}
	out.Body.Collection = NewCollection(items)
	out.Body.Total = v.Total
	return out, nil
}
func (reg *Registry) deletePersonalCollectionImage(ctx context.Context, in *PersonalCollectionImageInput) (*struct{}, error) {
	s, p := reg.collectionLifecycle()
	if p != nil {
		return nil, p
	}
	u, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	if e := s.DeletePersonalCollectionImage(ctx, u, profileFrom(ctx), string(in.ID), in.Type); e != nil {
		return nil, collectionProblem(e)
	}
	return nil, nil
}
func (reg *Registry) setPersonalCollectionSortPreference(ctx context.Context, in *CollectionSortPreferenceInput) (*CollectionSortPreferenceOutput, error) {
	s, p := reg.collectionLifecycle()
	if p != nil {
		return nil, p
	}
	u, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	b := in.Body
	v, e := s.SetPersonalCollectionSortPreference(ctx, u, profileFrom(ctx), handlers.AccessFilterFromContext(ctx, ""), handlers.CollectionSortPreferenceRequest{CollectionKind: b.CollectionKind, CollectionID: string(b.CollectionID), Field: b.Field, Order: b.Order})
	if e != nil {
		return nil, collectionProblem(e)
	}
	return &CollectionSortPreferenceOutput{Body: CollectionSortPreference{CollectionKind: v.CollectionKind, CollectionID: ID(v.CollectionID), Field: v.Field, Order: v.Order}}, nil
}
func (reg *Registry) clearPersonalCollectionSortPreference(ctx context.Context, in *CollectionSortPreferenceDeleteInput) (*struct{}, error) {
	s, p := reg.collectionLifecycle()
	if p != nil {
		return nil, p
	}
	u, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	if e := s.ClearPersonalCollectionSortPreference(ctx, u, profileFrom(ctx), in.CollectionKind, string(in.CollectionID)); e != nil {
		return nil, collectionProblem(e)
	}
	return nil, nil
}
func (reg *Registry) listPersonalCollectionTemplates(_ context.Context, _ *struct{}) (*PersonalCollectionTemplatesOutput, error) {
	s, ok := reg.deps.CollectionImports.(personalCollectionImportLifecycle)
	if !ok {
		return nil, unavailable("collection templates")
	}
	return &PersonalCollectionTemplatesOutput{Body: s.CollectionTemplates()}, nil
}
func (reg *Registry) syncPersonalCollection(ctx context.Context, in *PersonalCollectionIDInput) (*PersonalCollectionSyncOutput, error) {
	s, ok := reg.deps.CollectionImports.(personalCollectionImportLifecycle)
	if !ok {
		return nil, unavailable("collection sync")
	}
	u, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	v, e := s.SyncPersonalCollection(ctx, u, profileFrom(ctx), string(in.ID))
	if e != nil {
		return nil, collectionProblem(e)
	}
	if v == nil {
		return nil, NewProblem(TypeInternalError, "The sync returned no result.")
	}
	return &PersonalCollectionSyncOutput{Body: *collectionSyncResultOf(v)}, nil
}
func (reg *Registry) listServerCollections(ctx context.Context, _ *struct{}) (*ServerCollectionsOutput, error) {
	s, ok := reg.deps.LibraryCollections.(interface {
		ListServerCollections(context.Context, catalogsvc.AccessFilter) (handlers.ServerCollectionsView, error)
	})
	if !ok {
		return nil, unavailable("server collections")
	}
	v, e := s.ListServerCollections(ctx, handlers.AccessFilterFromContext(ctx, ""))
	if e != nil {
		return nil, collectionProblem(e)
	}
	out := &ServerCollectionsOutput{}
	out.Body.Libraries = make([]ServerCollectionLibrary, 0, len(v.Libraries))
	for _, l := range v.Libraries {
		cards := make([]LibraryCollectionCard, 0, len(l.Collections))
		for _, c := range l.Collections {
			cards = append(cards, LibraryCollectionCard{ID: c.ID, Title: c.Title, PosterURL: c.PosterURL, PosterThumbhash: c.PosterThumbhash, ItemCount: c.ItemCount, Featured: c.Featured})
		}
		out.Body.Libraries = append(out.Body.Libraries, ServerCollectionLibrary{LibraryID: IDFromInt(int64(l.LibraryID)), LibraryName: l.LibraryName, TotalCount: l.TotalCount, Collections: cards})
	}
	return out, nil
}

func (reg *Registry) uploadPersonalCollectionPoster(ctx context.Context, in *PersonalCollectionPosterInput) (*PersonalCollectionOutput, error) {
	s, ok := reg.deps.PersonalCollections.(interface {
		UploadPersonalCollectionPoster(context.Context, int, string, string, []byte) (handlers.PersonalCollectionView, error)
	})
	if !ok {
		return nil, unavailable("collection artwork")
	}
	u, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	form := in.RawBody.Data()
	if form != nil && form.SourceURL != "" {
		if form.Poster.IsSet {
			return nil, NewProblem(TypeValidationFailed, "Supply either poster or source_url, not both.")
		}
		source, ok := reg.deps.PersonalCollections.(interface {
			SetPersonalCollectionPosterSource(context.Context, int, string, string, string) (handlers.PersonalCollectionView, error)
		})
		if !ok {
			return nil, unavailable("collection artwork")
		}
		v, err := source.SetPersonalCollectionPosterSource(ctx, u, profileFrom(ctx), string(in.ID), form.SourceURL)
		if err != nil {
			return nil, collectionProblem(err)
		}
		return &PersonalCollectionOutput{Body: personalCollectionOf(v)}, nil
	}
	if form == nil || !form.Poster.IsSet {
		return nil, NewProblem(TypeValidationFailed, "A poster file is required.")
	}
	if form.Poster.Size > maxPosterBytes {
		return nil, NewProblem(TypePayloadTooLarge, "The poster exceeds the 10 MiB limit.")
	}
	data, e := io.ReadAll(io.LimitReader(form.Poster, maxPosterBytes+1))
	if e != nil {
		return nil, NewProblem(TypeInternalError, "Failed to read the poster.")
	}
	if len(data) > maxPosterBytes {
		return nil, NewProblem(TypePayloadTooLarge, "The poster exceeds the 10 MiB limit.")
	}
	v, e := s.UploadPersonalCollectionPoster(ctx, u, profileFrom(ctx), string(in.ID), data)
	if e != nil {
		return nil, collectionProblem(e)
	}
	return &PersonalCollectionOutput{Body: personalCollectionOf(v)}, nil
}
