package apiv2

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
)

type fakeAdminCollectionGroups struct {
	group          handlers.AdminCollectionGroupView
	revision       int64
	reads, writes  int
	order, members []string
	strict         bool
	mismatch       bool
	deleted        bool
}

func newFakeAdminCollectionGroups() *fakeAdminCollectionGroups {
	return &fakeAdminCollectionGroups{
		group:    handlers.AdminCollectionGroupView{ID: "g1", LibraryID: 1, Name: "Original", Slug: "original", Kind: "regular", DefaultSortMode: "manual"},
		revision: 1, order: []string{"g1", "ungrouped"}, members: []string{"c1"},
	}
}
func (f *fakeAdminCollectionGroups) ListAdminCollectionGroups(context.Context, int) (handlers.AdminCollectionGroupsView, error) {
	f.reads++
	return handlers.AdminCollectionGroupsView{Groups: []handlers.AdminCollectionGroupView{f.group}, UngroupedSortOrder: 1}, nil
}
func (f *fakeAdminCollectionGroups) CreateAdminCollectionGroup(_ context.Context, lib int, in handlers.AdminCollectionGroupCreate) (handlers.AdminCollectionGroupView, error) {
	f.writes++
	f.group.LibraryID, f.group.Name = lib, in.Name
	return f.group, nil
}
func (f *fakeAdminCollectionGroups) GetAdminCollectionGroup(_ context.Context, id string) (handlers.AdminCollectionGroupView, int64, error) {
	f.reads++
	if f.deleted || id != f.group.ID {
		return handlers.AdminCollectionGroupView{}, 0, catalogsvc.ErrLibraryCollectionGroupNotFound
	}
	return f.group, f.revision, nil
}
func (f *fakeAdminCollectionGroups) mutate() error {
	f.writes++
	f.revision++
	if f.mismatch {
		f.mismatch = false
		return catalogsvc.ErrLibraryCollectionRevisionMismatch
	}
	return nil
}
func (f *fakeAdminCollectionGroups) UpdateAdminCollectionGroup(_ context.Context, _ string, in handlers.AdminCollectionGroupUpdate) (handlers.AdminCollectionGroupView, error) {
	if err := f.mutate(); err != nil {
		return handlers.AdminCollectionGroupView{}, err
	}
	if in.Name != nil {
		f.group.Name = *in.Name
	}
	return f.group, nil
}
func (f *fakeAdminCollectionGroups) DeleteAdminCollectionGroup(context.Context, string) error {
	if err := f.mutate(); err != nil {
		return err
	}
	f.deleted = true
	return nil
}
func (f *fakeAdminCollectionGroups) AdminCollectionGroupOrder(context.Context, int) (handlers.AdminCollectionOrderView, error) {
	f.reads++
	return handlers.AdminCollectionOrderView{LibraryID: 1, OrderedIDs: slices.Clone(f.order), Revision: f.revision}, nil
}
func (f *fakeAdminCollectionGroups) ReorderAdminCollectionGroups(_ context.Context, _ int, ids []string) error {
	if err := f.mutate(); err != nil {
		return err
	}
	f.order = slices.Clone(ids)
	return nil
}
func (f *fakeAdminCollectionGroups) AdminGroupCollectionOrder(_ context.Context, id string, lib int) (handlers.AdminCollectionOrderView, error) {
	f.reads++
	var group *string
	if id != "ungrouped" {
		group, lib = &id, f.group.LibraryID
	}
	return handlers.AdminCollectionOrderView{LibraryID: lib, GroupID: group, OrderedIDs: slices.Clone(f.members), Revision: f.revision}, nil
}
func (f *fakeAdminCollectionGroups) MoveAdminGroupCollections(_ context.Context, _ string, _ int, ids []string, strict bool) error {
	if err := f.mutate(); err != nil {
		return err
	}
	f.members, f.strict = slices.Clone(ids), strict
	return nil
}

func adminCollectionGroupsTestHandler(t *testing.T, f *fakeAdminCollectionGroups) http.Handler {
	t.Helper()
	deps, _ := libraryDeps(t)
	deps.AdminCollectionGroups = f
	return newTestHandler(t, deps)
}

func TestAdminCollectionGroupValidationAndAuthorizationPrecedeService(t *testing.T) {
	f := newFakeAdminCollectionGroups()
	h := adminCollectionGroupsTestHandler(t, f)
	path := Prefix + "/admin/libraries/1/collection-groups/order"
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, "PUT", path, `{"ordered_ids":["g1","g1"]}`, with(bearer(adminToken), "If-Match", "*")), TypeValidationFailed)
	requireProblem(t, do(t, h, "POST", Prefix+"/admin/libraries/1/collection-groups", `{"name":"Group","slug":null}`, bearer(adminToken)), TypeValidationFailed)
	if f.reads != 0 || f.writes != 0 {
		t.Fatal("invalid or unauthorized request reached service")
	}
}
