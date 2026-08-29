package catalog

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// sortPrefStore is a UserStore that only answers collection-sort-preference
// lookups; every other method panics so a test that strays off this path fails
// loudly rather than silently exercising unrelated behavior.
type sortPrefStore struct {
	userstore.UserStore
	pref *userstore.CollectionSortPreference
	err  error
	// calls records the lookups made, so tests can assert the store is not
	// consulted when there is no user scope to consult it with.
	calls        int
	lastKind     string
	lastTargetID string
}

func (s *sortPrefStore) GetCollectionSortPreference(_ context.Context, _, kind, collectionID string) (*userstore.CollectionSortPreference, error) {
	s.calls++
	s.lastKind = kind
	s.lastTargetID = collectionID
	if s.err != nil {
		return nil, s.err
	}
	return s.pref, nil
}

type sortPrefProvider struct {
	store *sortPrefStore
	err   error
}

func (p *sortPrefProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.store, nil
}

func (p *sortPrefProvider) Close() error { return nil }

func TestEffectiveCollectionSortPrecedence(t *testing.T) {
	const (
		libraryDefault = `{"field":"added_at","order":"desc"}`
		noDefault      = `{}`
	)
	scopedAccess := AccessFilter{UserID: 7, ProfileID: "profile-1"}

	cases := []struct {
		name       string
		access     AccessFilter
		sortConfig string
		pref       *userstore.CollectionSortPreference
		storeErr   error
		wantOK     bool
		wantField  string
		wantOrder  string
		wantCalls  int
	}{
		{
			name:       "no override falls back to the collection default",
			access:     scopedAccess,
			sortConfig: libraryDefault,
			wantOK:     true,
			wantField:  "added_at",
			wantOrder:  "desc",
			wantCalls:  1,
		},
		{
			// The reported behavior: admin defaults to Date Added, the viewer
			// switches to Title, and Title is what they get from then on.
			name:       "viewer override beats the collection default",
			access:     scopedAccess,
			sortConfig: libraryDefault,
			pref:       &userstore.CollectionSortPreference{SortField: "title", SortOrder: "asc"},
			wantOK:     true,
			wantField:  "title",
			wantOrder:  "asc",
			wantCalls:  1,
		},
		{
			// An empty stored field is a deliberate "put it back to the list's
			// own order" and must not fall through to the creator's default.
			name:       "override pinned to source order suppresses the default",
			access:     scopedAccess,
			sortConfig: libraryDefault,
			pref:       &userstore.CollectionSortPreference{SortField: "", SortOrder: ""},
			wantCalls:  1,
		},
		{
			name:       "no override and no default means source order",
			access:     scopedAccess,
			sortConfig: noDefault,
			wantCalls:  1,
		},
		{
			name:       "personalized viewer override is valid on a library collection",
			access:     scopedAccess,
			sortConfig: libraryDefault,
			pref:       &userstore.CollectionSortPreference{SortField: "progress", SortOrder: "desc"},
			wantOK:     true,
			wantField:  "progress",
			wantOrder:  "desc",
			wantCalls:  1,
		},
		{
			name:       "unusable override falls back to the default",
			access:     scopedAccess,
			sortConfig: libraryDefault,
			pref:       &userstore.CollectionSortPreference{SortField: "retired_sort", SortOrder: "desc"},
			wantOK:     true,
			wantField:  "added_at",
			wantOrder:  "desc",
			wantCalls:  1,
		},
		{
			name:       "store failure degrades to the default",
			access:     scopedAccess,
			sortConfig: libraryDefault,
			storeErr:   errors.New("boom"),
			wantOK:     true,
			wantField:  "added_at",
			wantOrder:  "desc",
			wantCalls:  1,
		},
		{
			// Unauthenticated / profile-less reads (jellycompat, public paths)
			// have no override to look up and must not query for one.
			name:       "no user scope skips the override lookup",
			access:     AccessFilter{},
			sortConfig: libraryDefault,
			pref:       &userstore.CollectionSortPreference{SortField: "title", SortOrder: "asc"},
			wantOK:     true,
			wantField:  "added_at",
			wantOrder:  "desc",
			wantCalls:  0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &sortPrefStore{pref: tc.pref, err: tc.storeErr}
			resolver := &CatalogResolver{storeProvider: &sortPrefProvider{store: store}}

			qs, ok := resolver.EffectiveCollectionSort(
				context.Background(),
				tc.access,
				userstore.CollectionKindLibrary,
				"collection-1",
				[]byte(tc.sortConfig),
			)

			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if store.calls != tc.wantCalls {
				t.Fatalf("override lookups = %d, want %d", store.calls, tc.wantCalls)
			}
			if !tc.wantOK {
				return
			}
			if qs.Field != tc.wantField || qs.Order != tc.wantOrder {
				t.Fatalf("got %q/%q, want %q/%q", qs.Field, qs.Order, tc.wantField, tc.wantOrder)
			}
		})
	}
}

func TestEffectiveCollectionSortAllowsPersonalizedOnUserCollections(t *testing.T) {
	store := &sortPrefStore{pref: &userstore.CollectionSortPreference{SortField: "progress", SortOrder: "desc"}}
	resolver := &CatalogResolver{storeProvider: &sortPrefProvider{store: store}}

	qs, ok := resolver.EffectiveCollectionSort(
		context.Background(),
		AccessFilter{UserID: 7, ProfileID: "profile-1"},
		userstore.CollectionKindUser,
		"collection-1",
		[]byte(`{}`),
	)
	if !ok {
		t.Fatal("personalized override rejected on a personal collection")
	}
	if qs.Field != "progress" || qs.Order != "desc" {
		t.Fatalf("got %q/%q, want progress/desc", qs.Field, qs.Order)
	}
}

func TestPersonalSourceSortPreferencePrecedence(t *testing.T) {
	access := AccessFilter{UserID: 7, ProfileID: "profile-1"}
	for _, source := range []CatalogSource{CatalogSourceWatchlist, CatalogSourceFavorites} {
		t.Run(string(source), func(t *testing.T) {
			t.Run("saved metadata sort", func(t *testing.T) {
				store := &sortPrefStore{pref: &userstore.CollectionSortPreference{SortField: "title", SortOrder: "asc"}}
				resolver := &CatalogResolver{storeProvider: &sortPrefProvider{store: store}}
				req := resolver.resolvePersonalSourceEffectiveSort(context.Background(), CatalogRequest{Source: source, UseSourceOrder: true}, access)
				if req.UseSourceOrder || req.Query.Sort != (QuerySort{Field: "title", Order: "asc"}) {
					t.Fatalf("resolved request = %+v, want title/asc query sort", req)
				}
				if store.lastKind != string(source) || store.lastTargetID != userstore.PersonalSortPreferenceCollectionID {
					t.Fatalf("preference lookup = %q/%q, want %q/%q", store.lastKind, store.lastTargetID, source, userstore.PersonalSortPreferenceCollectionID)
				}
			})

			t.Run("saved non-list metadata sort uses executor path", func(t *testing.T) {
				store := &sortPrefStore{pref: &userstore.CollectionSortPreference{SortField: "release_date", SortOrder: "desc"}}
				resolver := &CatalogResolver{storeProvider: &sortPrefProvider{store: store}}
				req := resolver.resolvePersonalSourceEffectiveSort(context.Background(), CatalogRequest{Source: source, UseSourceOrder: true}, access)
				if req.UseSourceOrder || req.Query.Sort != (QuerySort{Field: "release_date", Order: "desc"}) {
					t.Fatalf("resolved request = %+v, want release_date/desc executor path", req)
				}
			})

			t.Run("saved added_at sort keeps list path", func(t *testing.T) {
				store := &sortPrefStore{pref: &userstore.CollectionSortPreference{SortField: "added_at", SortOrder: "asc"}}
				resolver := &CatalogResolver{storeProvider: &sortPrefProvider{store: store}}
				req := resolver.resolvePersonalSourceEffectiveSort(context.Background(), CatalogRequest{Source: source, UseSourceOrder: true}, access)
				if !req.UseSourceOrder || req.Query.Sort != (QuerySort{Field: "added_at", Order: "asc"}) {
					t.Fatalf("resolved request = %+v, want added_at/asc source path", req)
				}
			})

			t.Run("explicit request wins without lookup", func(t *testing.T) {
				store := &sortPrefStore{pref: &userstore.CollectionSortPreference{SortField: "title", SortOrder: "asc"}}
				resolver := &CatalogResolver{storeProvider: &sortPrefProvider{store: store}}
				want := QuerySort{Field: "year", Order: "desc"}
				req := resolver.resolvePersonalSourceEffectiveSort(context.Background(), CatalogRequest{Source: source, Query: QueryDefinition{Sort: want}}, access)
				if req.Query.Sort != want || store.calls != 0 {
					t.Fatalf("resolved sort = %+v, lookups = %d; want explicit sort and zero lookups", req.Query.Sort, store.calls)
				}
			})

			t.Run("explicit added_at wins on source path without lookup", func(t *testing.T) {
				store := &sortPrefStore{pref: &userstore.CollectionSortPreference{SortField: "title", SortOrder: "asc"}}
				resolver := &CatalogResolver{storeProvider: &sortPrefProvider{store: store}}
				want := QuerySort{Field: "added_at", Order: "asc"}
				req := resolver.resolvePersonalSourceEffectiveSort(context.Background(), CatalogRequest{
					Source: source, Query: QueryDefinition{Sort: want}, UseSourceOrder: true,
				}, access)
				if req.Query.Sort != want || !req.UseSourceOrder || store.calls != 0 {
					t.Fatalf("resolved request = %+v, lookups = %d; want explicit added_at source path", req, store.calls)
				}
			})

			// Grouped browse pages one logical request several times. Once the
			// first page has resolved and advertised a sort, later pages must
			// reuse it rather than re-reading a preference that changed in
			// between.
			t.Run("frozen sort wins over a changed preference without a lookup", func(t *testing.T) {
				store := &sortPrefStore{pref: &userstore.CollectionSortPreference{SortField: "title", SortOrder: "asc"}}
				resolver := &CatalogResolver{storeProvider: &sortPrefProvider{store: store}}
				frozen := QuerySort{Field: "runtime", Order: "desc"}
				req := resolver.resolvePersonalSourceEffectiveSort(context.Background(), CatalogRequest{
					Source: source, UseSourceOrder: true, ResolvedSort: &frozen,
				}, access)
				if req.Query.Sort != frozen || req.UseSourceOrder || store.calls != 0 {
					t.Fatalf("resolved request = %+v, lookups = %d; want frozen runtime/desc and zero lookups", req, store.calls)
				}
			})

			t.Run("frozen source order stays on the list path", func(t *testing.T) {
				store := &sortPrefStore{pref: &userstore.CollectionSortPreference{SortField: "title", SortOrder: "asc"}}
				resolver := &CatalogResolver{storeProvider: &sortPrefProvider{store: store}}
				for _, frozen := range []QuerySort{{}, {Field: "added_at", Order: "desc"}} {
					req := resolver.resolvePersonalSourceEffectiveSort(context.Background(), CatalogRequest{
						Source: source, UseSourceOrder: true, ResolvedSort: &frozen,
					}, access)
					if !req.UseSourceOrder || req.Query.Sort != frozen || store.calls != 0 {
						t.Fatalf("frozen %+v resolved to %+v, lookups = %d; want the list path", frozen, req, store.calls)
					}
				}
			})

			for _, pref := range []*userstore.CollectionSortPreference{
				{SortField: "", SortOrder: ""},
				{SortField: "retired", SortOrder: "desc"},
				{SortField: "title", SortOrder: "sideways"},
			} {
				store := &sortPrefStore{pref: pref}
				resolver := &CatalogResolver{storeProvider: &sortPrefProvider{store: store}}
				req := resolver.resolvePersonalSourceEffectiveSort(context.Background(), CatalogRequest{Source: source, UseSourceOrder: true}, access)
				if !req.UseSourceOrder || req.Query.Sort.Field != "" {
					t.Fatalf("preference %+v resolved to %+v, want source order", pref, req)
				}
			}
		})
	}
}
