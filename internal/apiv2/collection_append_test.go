package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

type appendPersonalService struct {
	*fakePersonalCollections
	personalCollectionLifecycle
	appends, positions int
}

func (s *appendPersonalService) AppendPersonalCollectionItem(_ context.Context, uid int, profile, collection, item string) error {
	if uid != 1 || profile != "p-owner" || collection != "c1" || item != "new" {
		panic("append lost caller or target")
	}
	s.appends++
	return nil
}
func (s *appendPersonalService) AddPersonalCollectionItem(_ context.Context, _ int, _, _, _ string, position int) error {
	s.positions = position
	return nil
}

type appendAdminService struct {
	*fakeAdminCollections
	appends, positions int
}

func (s *appendAdminService) AdminCollectionFeatures(context.Context) userstore.CollectionFeatures {
	return userstore.CollectionFeatures{ItemAppend: true}
}
func (s *appendAdminService) AppendAdminCollectionItem(_ context.Context, collection, item string) error {
	if collection != "c1" || item != "new" {
		panic("append lost target")
	}
	s.appends++
	return nil
}
func (s *appendAdminService) AddAdminCollectionItem(_ context.Context, _, _ string, position int) error {
	s.positions = position
	return nil
}

func TestCollectionAtomicAppendHTTP(t *testing.T) {
	deps, personal, _ := collectionDeps(t)
	personal.features.ItemAppend = true
	ps := &appendPersonalService{fakePersonalCollections: personal}
	as := &appendAdminService{fakeAdminCollections: newFakeAdminCollections()}
	deps.PersonalCollections, deps.AdminCollections = ps, as
	h := newTestHandler(t, deps)
	for _, tc := range []struct {
		path    string
		headers map[string]string
	}{
		{"/api/v2/collections", viewerHeaders()},
		{"/api/v2/admin/collections", bearer(adminToken)},
	} {
		capability := do(t, h, http.MethodGet, tc.path+"/capabilities", "", tc.headers)
		var caps struct {
			ItemAppend bool `json:"item_append"`
		}
		if err := json.Unmarshal(capability.Body.Bytes(), &caps); err != nil || capability.Code != 200 || !caps.ItemAppend {
			t.Fatalf("capability: %d %s (%v)", capability.Code, capability.Body, err)
		}
		for _, query := range []string{"?append=true", "?append=false", ""} {
			rec := do(t, h, http.MethodPut, tc.path+"/c1/items/new"+query, `{"position":37}`, tc.headers)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("%s%s: %d %s", tc.path, query, rec.Code, rec.Body)
			}
		}
	}
	if ps.appends != 1 || as.appends != 1 || ps.positions != 37 || as.positions != 37 {
		t.Fatalf("append/position dispatch: personal=%+v admin=%+v", ps, as)
	}
	rec := do(t, h, http.MethodPut, "/api/v2/admin/collections/c1/items/new?append=true", `{"position":0}`, viewerHeaders())
	if rec.Code != http.StatusForbidden || as.appends != 1 {
		t.Fatalf("non-admin append: %d %s", rec.Code, rec.Body)
	}
}

func TestCollectionAtomicAppendUnsupportedHTTP(t *testing.T) {
	deps, _, _ := collectionDeps(t)
	deps.AdminCollections = newFakeAdminCollections()
	h := newTestHandler(t, deps)
	for _, tc := range []struct {
		path    string
		headers map[string]string
	}{
		{"/api/v2/collections", viewerHeaders()},
		{"/api/v2/admin/collections", bearer(adminToken)},
	} {
		rec := do(t, h, http.MethodPut, tc.path+"/c1/items/new?append=true", `{"position":0}`, tc.headers)
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("unsupported append fell back to a position: %d %s", rec.Code, rec.Body)
		}
	}
}
