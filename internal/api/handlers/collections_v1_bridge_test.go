package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// v1BridgeStore is a login with three profiles, one shared and one private
// collection, and the PostgreSQL feature set after #1615 (no groups).
type v1BridgeStore struct {
	lifecycleStore
	collections []userstore.Collection
	reordered   []string
}

func (s *v1BridgeStore) ListCollections(_ context.Context, profileID string) ([]userstore.Collection, error) {
	var out []userstore.Collection
	for _, c := range s.collections {
		if c.VisibleTo(profileID) {
			out = append(out, c)
		}
	}
	return out, nil
}
func (s *v1BridgeStore) ListProfiles(context.Context) ([]userstore.Profile, error) {
	return []userstore.Profile{{ID: "p3"}, {ID: "owner"}, {ID: "p2"}}, nil
}
func (s *v1BridgeStore) CollectionFeatures() userstore.CollectionFeatures {
	return userstore.CollectionFeatures{Imports: true, Artwork: true, ItemReorder: true}
}
func (s *v1BridgeStore) ReorderCollections(_ context.Context, _ string, ids []string) error {
	s.reordered = ids
	return nil
}

type v1BridgeProvider struct {
	userstore.UserStoreProvider
	store *v1BridgeStore
}

func (p v1BridgeProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func v1BridgeRequest(method, target, body, profileID string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1})
	return req.WithContext(apimw.SetProfileID(ctx, profileID))
}

// The /api/v1 bridge keeps its response shape: allowed_profile_ids reports
// the effective audience (the creator alone, or every profile on the login),
// group_id is always null and groups is always empty.
func TestV1ListCollectionsReportsEffectiveAudience(t *testing.T) {
	store := &v1BridgeStore{collections: []userstore.Collection{
		{ID: "shared", CreatorProfileID: "owner", IsShared: true, CollectionType: "manual"},
		{ID: "private", CreatorProfileID: "owner", CollectionType: "manual"},
	}}
	h := NewCollectionHandler(v1BridgeProvider{store: store})

	for profileID, want := range map[string]map[string][]string{
		"owner": {"shared": {"owner", "p2", "p3"}, "private": {"owner"}},
		"p2":    {"shared": {"owner", "p2", "p3"}},
	} {
		rec := httptest.NewRecorder()
		h.HandleListCollections(rec, v1BridgeRequest(http.MethodGet, "/api/v1/collections", "", profileID))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", profileID, rec.Code, rec.Body)
		}
		var body struct {
			Collections []struct {
				ID                string   `json:"id"`
				AllowedProfileIDs []string `json:"allowed_profile_ids"`
				GroupID           *string  `json:"group_id"`
			} `json:"collections"`
			Groups []json.RawMessage `json:"groups"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Groups == nil || len(body.Groups) != 0 {
			t.Fatalf("%s: groups = %s, want []", profileID, rec.Body)
		}
		got := map[string][]string{}
		for _, c := range body.Collections {
			if c.GroupID != nil {
				t.Fatalf("%s: %s group_id = %q, want null", profileID, c.ID, *c.GroupID)
			}
			got[c.ID] = c.AllowedProfileIDs
		}
		if len(got) != len(want) {
			t.Fatalf("%s: listed %v, want %v", profileID, got, want)
		}
		for id, audience := range want {
			if !slices.Equal(got[id], audience) {
				t.Fatalf("%s: %s allowed_profile_ids = %v, want %v", profileID, id, got[id], audience)
			}
		}
	}
}

// v1 group writes answer as on an account without group support, and a
// group-scoped order is refused; the flat order still works.
func TestV1CollectionGroupsAreUnsupported(t *testing.T) {
	store := &v1BridgeStore{}
	h := NewCollectionHandler(v1BridgeProvider{store: store})
	for name, call := range map[string]func(*httptest.ResponseRecorder){
		"create": func(w *httptest.ResponseRecorder) {
			h.HandleCreateCollectionGroup(w, v1BridgeRequest(http.MethodPost, "/", `{"name":"G"}`, "owner"))
		},
		"order": func(w *httptest.ResponseRecorder) {
			h.HandleReorderCollectionGroups(w, v1BridgeRequest(http.MethodPut, "/", `{"ordered_ids":[]}`, "owner"))
		},
	} {
		rec := httptest.NewRecorder()
		call(rec)
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s: status %d, want 501: %s", name, rec.Code, rec.Body)
		}
	}

	rec := httptest.NewRecorder()
	h.HandleReorderCollections(rec, v1BridgeRequest(http.MethodPut, "/", `{"ordered_ids":["a"],"group_id":"g1"}`, "owner"))
	if rec.Code != http.StatusBadRequest || store.reordered != nil {
		t.Fatalf("group-scoped order: status %d, reordered %v", rec.Code, store.reordered)
	}
	rec = httptest.NewRecorder()
	h.HandleReorderCollections(rec, v1BridgeRequest(http.MethodPut, "/", `{"ordered_ids":["a"],"group_id":null}`, "owner"))
	if rec.Code != http.StatusNoContent || !slices.Equal(store.reordered, []string{"a"}) {
		t.Fatalf("flat order: status %d, reordered %v: %s", rec.Code, store.reordered, rec.Body)
	}
}
