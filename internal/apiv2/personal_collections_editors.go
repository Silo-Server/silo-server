package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type CollectionPreconditions struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type CollectionOrderReadInput struct {
	GroupID string `query:"group_id"`
}
type CollectionOrderOutput struct {
	ETag string `header:"ETag"`
	Body CollectionOrder
}
type CollectionGroupsOrderOutput struct {
	ETag string `header:"ETag"`
	Body CollectionGroupOrder
}
type CollectionItemsOrder struct {
	OrderedIDs []ID `json:"ordered_ids"`
	HasMore    bool `json:"has_more" doc:"True when this collection exceeds the 200-item editable order window"`
}
type CollectionItemsOrderOutput struct {
	ETag string `header:"ETag"`
	Body CollectionItemsOrder
}
type collectionEditors interface {
	PersonalCollectionEditor(context.Context, int, string, string) (handlers.PersonalCollectionEditorView, error)
	PersonalCollectionOrderEditor(context.Context, int, string, *string) (handlers.PersonalCollectionOrderView, error)
	PersonalCollectionGroupsEditor(context.Context, int) ([]handlers.CollectionGroupView, int64, error)
	PersonalCollectionGroupEditor(context.Context, int, string) (handlers.PersonalCollectionGroupEditorView, error)
	PersonalCollectionItemsOrderEditor(context.Context, int, string, string) (handlers.PersonalCollectionOrderView, error)
}

func (reg *Registry) collectionEditors() (collectionEditors, *Problem) {
	s, ok := reg.deps.PersonalCollections.(collectionEditors)
	if !ok {
		return nil, unavailable("collection editor")
	}
	return s, nil
}
func collectionEditorTag(ctx context.Context, kind, id string, revision int64) EntityTag {
	return RenderETag("collections:"+kind+":"+strconv.Itoa(claimsFrom(ctx).UserID)+":"+profileFrom(ctx)+":"+viewerScopeDigest(ctx), id, revision)
}
func collectionGuard(ctx context.Context, headers CollectionPreconditions, tag EntityTag, revision int64) (context.Context, *Problem) {
	if p := EvaluateGuardedPreconditions(headers.IfMatch, headers.IfNoneMatch, tag); p != nil {
		return ctx, p
	}
	if headers.IfMatch == "*" {
		revision = -1
	}
	return handlers.WithCollectionExpectedRevision(ctx, revision), nil
}
func registerCollectionEditors(reg *Registry) {
	op := func(path, id string) Operation {
		return Operation{Operation: humaOp(http.MethodGet, Prefix+path, id, "collections", "Read the canonical collection editor state and its strong validator."), Class: ClassProfileScoped, ServiceBacked: true}
	}
	Register(reg, op("/collections/order", "getCollectionOrder"), reg.getCollectionOrder)
	Register(reg, op("/collections/groups/order", "getCollectionGroupsOrder"), reg.getCollectionGroupsOrder)
	Register(reg, op("/collections/groups/{id}", "getCollectionGroup"), reg.getCollectionGroup)
	Register(reg, op("/collections/{id}/items/order", "getCollectionItemsOrder"), reg.getCollectionItemsOrder)
}
func (reg *Registry) getCollectionOrder(ctx context.Context, in *CollectionOrderReadInput) (*CollectionOrderOutput, error) {
	s, p := reg.collectionEditors()
	if p != nil {
		return nil, p
	}
	var group *string
	if in.GroupID != "" {
		group = &in.GroupID
	}
	v, e := s.PersonalCollectionOrderEditor(ctx, claimsFrom(ctx).UserID, profileFrom(ctx), group)
	if e != nil {
		return nil, collectionProblem(e)
	}
	ids := make([]ID, 0, len(v.OrderedIDs))
	for _, id := range v.OrderedIDs {
		ids = append(ids, ID(id))
	}
	var groupID *ID
	if group != nil {
		groupID = new(ID(*group))
	}
	return &CollectionOrderOutput{ETag: collectionEditorTag(ctx, "order", in.GroupID, v.Revision).String(), Body: CollectionOrder{GroupID: groupID, OrderedIDs: ids}}, nil
}
func (reg *Registry) getCollectionGroupsOrder(ctx context.Context, _ *struct{}) (*CollectionGroupsOrderOutput, error) {
	s, p := reg.collectionEditors()
	if p != nil {
		return nil, p
	}
	groups, rev, e := s.PersonalCollectionGroupsEditor(ctx, claimsFrom(ctx).UserID)
	if e != nil {
		return nil, collectionProblem(e)
	}
	ids := make([]ID, 0, len(groups))
	for _, g := range groups {
		ids = append(ids, ID(g.ID))
	}
	return &CollectionGroupsOrderOutput{ETag: collectionEditorTag(ctx, "groups-order", "", rev).String(), Body: CollectionGroupOrder{OrderedIDs: ids}}, nil
}
func (reg *Registry) getCollectionGroup(ctx context.Context, in *CollectionGroupIDInput) (*CollectionGroupOutput, error) {
	s, p := reg.collectionEditors()
	if p != nil {
		return nil, p
	}
	v, e := s.PersonalCollectionGroupEditor(ctx, claimsFrom(ctx).UserID, string(in.ID))
	if e != nil {
		return nil, collectionProblem(e)
	}
	return &CollectionGroupOutput{ETag: collectionEditorTag(ctx, "group", string(in.ID), v.Revision).String(), Body: collectionGroupOf(v.Group)}, nil
}
func (reg *Registry) getCollectionItemsOrder(ctx context.Context, in *PersonalCollectionIDInput) (*CollectionItemsOrderOutput, error) {
	s, p := reg.collectionEditors()
	if p != nil {
		return nil, p
	}
	v, e := s.PersonalCollectionItemsOrderEditor(ctx, claimsFrom(ctx).UserID, profileFrom(ctx), string(in.ID))
	if e != nil {
		return nil, collectionProblem(e)
	}
	ids := make([]ID, 0, len(v.OrderedIDs))
	for _, id := range v.OrderedIDs {
		ids = append(ids, ID(id))
	}
	return &CollectionItemsOrderOutput{ETag: collectionEditorTag(ctx, "items-order", string(in.ID), v.Revision).String(), Body: CollectionItemsOrder{OrderedIDs: ids, HasMore: v.HasMore}}, nil
}

func (reg *Registry) prepareCollectionGuard(ctx context.Context, kind, id string, headers CollectionPreconditions) (context.Context, EntityTag, *Problem) {
	s, p := reg.collectionEditors()
	if p != nil {
		return ctx, EntityTag{}, p
	}
	u := claimsFrom(ctx).UserID
	var rev int64
	var err error
	switch kind {
	case "collection":
		var v handlers.PersonalCollectionEditorView
		v, err = s.PersonalCollectionEditor(ctx, u, profileFrom(ctx), id)
		if err == nil && v.Collection.CreatorProfileID != profileFrom(ctx) {
			return ctx, EntityTag{}, NewProblem(TypePermissionDenied, "Only the creator can edit this collection.")
		}
		rev = v.Revision
	case "items-order":
		var v handlers.PersonalCollectionOrderView
		v, err = s.PersonalCollectionItemsOrderEditor(ctx, u, profileFrom(ctx), id)
		rev = v.Revision
	case "group":
		var v handlers.PersonalCollectionGroupEditorView
		v, err = s.PersonalCollectionGroupEditor(ctx, u, id)
		rev = v.Revision
	case "groups-order":
		_, rev, err = s.PersonalCollectionGroupsEditor(ctx, u)
	case "order":
		var group *string
		if id != "" {
			group = &id
		}
		var v handlers.PersonalCollectionOrderView
		v, err = s.PersonalCollectionOrderEditor(ctx, u, profileFrom(ctx), group)
		rev = v.Revision
	}
	if err != nil {
		return ctx, EntityTag{}, collectionProblem(err)
	}
	tag := collectionEditorTag(ctx, kind, id, rev)
	next, p := collectionGuard(ctx, headers, tag, rev)
	return next, tag, p
}
func (reg *Registry) guardedCollectionError(ctx context.Context, kind, id string, err error) error {
	if !errors.Is(err, userstore.ErrCollectionRevisionMismatch) {
		return collectionProblem(err)
	}
	_, tag, p := reg.prepareCollectionGuard(ctx, kind, id, CollectionPreconditions{IfMatch: "*"})
	if p != nil {
		return p
	}
	return StaleVersionProblem(tag)
}
