package handlers

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type bulkProgressStore struct {
	userstore.UserStore
	profiles []string
	items    []string
}

func (s *bulkProgressStore) UpdateProgress(_ context.Context, profile, item string, _, _ float64, _ userstore.ProgressThresholds) error {
	s.profiles = append(s.profiles, profile)
	s.items = append(s.items, item)
	return nil
}

type bulkProgressProvider struct {
	userstore.UserStoreProvider
	store *bulkProgressStore
	user  int
}

func (p *bulkProgressProvider) ForUser(_ context.Context, user int) (userstore.UserStore, error) {
	p.user = user
	return p.store, nil
}

func TestSyncProgressNativeVisibilityAndSelectedStore(t *testing.T) {
	store := &bulkProgressStore{}
	provider := &bulkProgressProvider{store: store}
	lookup := &fakeProgressLookup{accessible: map[string]bool{"visible": true}}
	h := NewProgressHandler(provider)
	h.LibraryLookup = lookup
	ctx := access.SetScope(t.Context(), access.Scope{AllowedLibraryIDs: []int{4}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}})
	out, err := h.SyncProgress(ctx, 17, "child", []ProgressSyncUpdate{{MediaItemID: "hidden", CheckAccess: true}, {MediaItemID: "visible", CheckAccess: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].FailureStatus != 404 || out[1].Status != "ok" || provider.user != 17 || len(store.items) != 1 || store.items[0] != "visible" || store.profiles[0] != "child" {
		t.Fatalf("result=%+v user=%d items=%v profiles=%v", out, provider.user, store.items, store.profiles)
	}
	if len(lookup.gotAllowed) != 1 || lookup.gotAllowed[0] != 4 || lookup.gotRating != "PG" {
		t.Fatalf("scope not forwarded: %+v", lookup)
	}
	// The bridge retains its preexisting write semantics and wire fields.
	out, err = h.SyncProgress(ctx, 17, "child", []ProgressSyncUpdate{{MediaItemID: "legacy"}})
	if err != nil || out[0].Status != "ok" || len(store.items) != 2 {
		t.Fatalf("bridge=%+v %v", out, err)
	}
}

func TestSyncProgressNativeMissingScopeHasNoWrites(t *testing.T) {
	store := &bulkProgressStore{}
	h := NewProgressHandler(&bulkProgressProvider{store: store})
	h.LibraryLookup = &fakeProgressLookup{accessible: map[string]bool{"item": true}}
	if _, err := h.SyncProgress(t.Context(), 17, "child", []ProgressSyncUpdate{{MediaItemID: "item", CheckAccess: true}}); err == nil {
		t.Fatal("missing viewer scope accepted")
	}
	if len(store.items) != 0 {
		t.Fatalf("unscoped writes=%v", store.items)
	}
}
