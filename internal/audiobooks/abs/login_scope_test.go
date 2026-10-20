package abs

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// Only these two inert seams are used: no auth handler or token store is wired.
type envelopeScopeResolver struct {
	AccessResolver
	resolve func(context.Context, string, string) (catalog.AccessFilter, error)
}

func (s envelopeScopeResolver) ResolveABSAccess(ctx context.Context, userID, profileID string) (catalog.AccessFilter, error) {
	return s.resolve(ctx, userID, profileID)
}

type envelopeLibraryStore struct {
	MediaStore
	list func(context.Context, catalog.AccessFilter) ([]AudiobookLibrary, error)
}

func (s envelopeLibraryStore) ListAudiobookLibraries(ctx context.Context, filter catalog.AccessFilter) ([]AudiobookLibrary, error) {
	return s.list(ctx, filter)
}

func TestLoginEnvelopeScope_ExplicitPrincipalAndFilter(t *testing.T) {
	for _, tc := range []struct {
		name      string
		profileID string
		filter    catalog.AccessFilter
		libraries []AudiobookLibrary
	}{
		{name: "unrestricted", profileID: "reader", libraries: []AudiobookLibrary{{ID: 71, Name: "Books"}}},
		{name: "empty allowlist", profileID: "reader", filter: catalog.AccessFilter{AllowedLibraryIDs: []int{}}},
		{name: "subset", profileID: "reader", filter: catalog.AccessFilter{AllowedLibraryIDs: []int{71}}, libraries: []AudiobookLibrary{{ID: 71, Name: "Books"}}},
		{name: "disabled library", profileID: "reader", filter: catalog.AccessFilter{DisabledLibraryIDs: []int{73}}, libraries: []AudiobookLibrary{{ID: 71, Name: "Books"}}},
		{name: "resolved policy scope", profileID: "reader", filter: catalog.AccessFilter{AllowedLibraryIDs: []int{71, 73}, DisabledLibraryIDs: []int{73}}, libraries: []AudiobookLibrary{{ID: 71, Name: "Books"}}},
		{name: "account scope", filter: catalog.AccessFilter{AllowedLibraryIDs: []int{71}}, libraries: []AudiobookLibrary{{ID: 71, Name: "Books"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			principal := ctxAuth{UserID: "42", ProfileID: tc.profileID}
			// Even ambient context naming another principal cannot choose the scope.
			ctx := context.WithValue(t.Context(), ctxKey{}, ctxAuth{UserID: "99", ProfileID: "other"})
			resolveCalls, listCalls := 0, 0
			h := &Handler{deps: Dependencies{
				AccessResolver: envelopeScopeResolver{resolve: func(gotCtx context.Context, userID, profileID string) (catalog.AccessFilter, error) {
					resolveCalls++
					if gotCtx != ctx || userID != principal.UserID || profileID != principal.ProfileID {
						t.Fatal("resolver did not receive the explicit principal and context")
					}
					return tc.filter, nil
				}},
				MediaStore: envelopeLibraryStore{list: func(gotCtx context.Context, filter catalog.AccessFilter) ([]AudiobookLibrary, error) {
					listCalls++
					if gotCtx != ctx || !reflect.DeepEqual(filter, tc.filter) {
						t.Fatalf("library scope changed: got %#v, want %#v", filter, tc.filter)
					}
					return tc.libraries, nil
				}},
			}}
			env := h.loginEnvelope(ctx, time.Unix(100, 0), principal, "Reader", "", "", false)
			if resolveCalls != 1 || listCalls != 1 {
				t.Fatalf("resolve/list calls = %d/%d, want 1/1", resolveCalls, listCalls)
			}
			assertEnvelopeLibraryMetadata(t, env, tc.libraries)
			user := env["user"].(map[string]any)
			if user["id"] != principal.UserID || user["username"] != "Reader" {
				t.Fatalf("envelope principal = %v", user)
			}
		})
	}
}

func TestLoginEnvelopeScope_ErrorsOmitMetadata(t *testing.T) {
	for _, tc := range []struct {
		name       string
		resolveErr error
		listErr    error
		wantLists  int
	}{
		{name: "resolution failure", resolveErr: errors.New("scope unavailable")},
		{name: "partial listing failure", listErr: errors.New("listing unavailable"), wantLists: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listCalls := 0
			h := &Handler{deps: Dependencies{
				AccessResolver: envelopeScopeResolver{resolve: func(context.Context, string, string) (catalog.AccessFilter, error) {
					return catalog.AccessFilter{AllowedLibraryIDs: []int{71}}, tc.resolveErr
				}},
				MediaStore: envelopeLibraryStore{list: func(context.Context, catalog.AccessFilter) ([]AudiobookLibrary, error) {
					listCalls++
					return []AudiobookLibrary{{ID: 71, Name: "Partial"}}, tc.listErr
				}},
			}}
			env := h.loginEnvelope(t.Context(), time.Unix(100, 0), ctxAuth{UserID: "42", ProfileID: "reader"}, "Reader", "", "", false)
			if listCalls != tc.wantLists {
				t.Fatalf("library calls = %d, want %d", listCalls, tc.wantLists)
			}
			assertEnvelopeLibraryMetadata(t, env, nil)
		})
	}
}

func TestLoginEnvelopeScope_LegacyResolverFallback(t *testing.T) {
	listed := false
	h := &Handler{deps: Dependencies{MediaStore: envelopeLibraryStore{list: func(_ context.Context, filter catalog.AccessFilter) ([]AudiobookLibrary, error) {
		listed = true
		want := catalog.AccessFilter{UserID: 42, ProfileID: "reader"}
		if !reflect.DeepEqual(filter, want) {
			t.Fatalf("legacy scope = %#v, want %#v", filter, want)
		}
		return nil, nil
	}}}}
	env := h.loginEnvelope(t.Context(), time.Unix(100, 0), ctxAuth{UserID: "42", ProfileID: "reader"}, "", "", "", false)
	if !listed {
		t.Fatal("legacy resolver fallback did not list libraries")
	}
	assertEnvelopeLibraryMetadata(t, env, nil)
}

func assertEnvelopeLibraryMetadata(t *testing.T, env map[string]any, libraries []AudiobookLibrary) {
	t.Helper()
	got, ok := env["libraries"].([]map[string]any)
	if !ok || got == nil || len(got) != len(libraries) {
		t.Fatalf("libraries = %#v, want nonnil list of length %d", env["libraries"], len(libraries))
	}
	defaultID := VirtualLibraryID
	for i, library := range libraries {
		if !reflect.DeepEqual(got[i], audiobookLibraryMap(library)) {
			t.Fatalf("library %d = %#v", i, got[i])
		}
		if i == 0 {
			defaultID = audiobookLibraryID(library)
		}
	}
	user := env["user"].(map[string]any)
	if env["userDefaultLibraryId"] != defaultID || user["defaultLibraryId"] != defaultID {
		t.Fatalf("default metadata = %v/%v, want %v", env["userDefaultLibraryId"], user["defaultLibraryId"], defaultID)
	}
}
