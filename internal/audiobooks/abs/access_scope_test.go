package abs

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// scopedLibraryStore lists only the audiobook libraries the access filter
// allows, as ABSMediaStore does.
type scopedLibraryStore struct {
	stubMediaStore
	libs []AudiobookLibrary
}

func (s *scopedLibraryStore) ListAudiobookLibraries(_ context.Context, access catalog.AccessFilter) ([]AudiobookLibrary, error) {
	if access.AllowedLibraryIDs == nil {
		return s.libs, nil
	}
	var out []AudiobookLibrary
	for _, lib := range s.libs {
		if slices.Contains(access.AllowedLibraryIDs, int(lib.ID)) {
			out = append(out, lib)
		}
	}
	return out, nil
}

func twoLibraryStore(known ...string) *scopedLibraryStore {
	items := map[string]*models.MediaItem{}
	for _, id := range known {
		items[id] = nil
	}
	return &scopedLibraryStore{
		stubMediaStore: stubMediaStore{known: items},
		libs: []AudiobookLibrary{
			{ID: 1, Name: "Adults", Type: "audiobooks"},
			{ID: 2, Name: "Kids", Type: "audiobooks"},
		},
	}
}

// A profile limited to the server's second audiobook library sees that
// library's ID on every collection entry, hydrated or stub, not the server's
// first library.
func TestCollectionBooks_CarryTheProfileLibrary(t *testing.T) {
	ctx := context.Background()
	store := newMemCollectionStore()
	_ = store.CreateCollection(ctx, Collection{ID: "c1", UserID: "1", ProfileID: "kids", Name: "Bedtime"})
	_ = store.AddCollectionItem(ctx, "c1", "book-known")
	_ = store.AddCollectionItem(ctx, "c1", "book-missing")
	h := New(Dependencies{
		MediaStore:      twoLibraryStore("book-known"),
		CollectionStore: store,
		AccessResolver:  newFakeOwnerResolver().set("1", "kids", catalog.AccessFilter{AllowedLibraryIDs: []int{2}}),
	})

	rec := dispatchABSWithParams(http.MethodGet, "/api/collections/c1", map[string]string{"id": "c1"}, nil, "1", "kids", h.handleGetCollection)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Books []map[string]any `json:"books"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Books) != 2 {
		t.Fatalf("books = %d, want 2; body=%s", len(got.Books), rec.Body.String())
	}
	for _, b := range got.Books {
		if b["libraryId"] != "2" {
			t.Errorf("book %v libraryId = %v, want the profile's library 2", b["id"], b["libraryId"])
		}
	}
}

// When the caller's scope can't be resolved, collection and playlist requests
// fail closed: the request is refused before it lists catalog data or writes.
func TestScopeResolutionFailure_FailsClosed(t *testing.T) {
	ctx := context.Background()
	newHandler := func() (*Handler, *memPlaylistStore) {
		colls := newMemCollectionStore()
		_ = colls.CreateCollection(ctx, Collection{ID: "c1", UserID: "1", ProfileID: "p1", Name: "Mine"})
		_ = colls.AddCollectionItem(ctx, "c1", "book-known")
		plays := newMemPlaylistStore()
		_ = plays.CreatePlaylist(ctx, Playlist{ID: "pl1", UserID: "1", ProfileID: "p1", Name: "Mine"})
		_ = plays.AddPlaylistItem(ctx, "pl1", "book-known", "")
		return New(Dependencies{
			MediaStore:      twoLibraryStore("book-known"),
			CollectionStore: colls,
			PlaylistStore:   plays,
			AccessResolver:  failingOwnerResolver{},
		}), plays
	}

	cases := []struct {
		name    string
		method  string
		path    string
		params  map[string]string
		body    string
		handler func(*Handler) http.HandlerFunc
	}{
		{name: "get collection", method: http.MethodGet, path: "/api/collections/c1",
			params: map[string]string{"id": "c1"}, handler: func(h *Handler) http.HandlerFunc { return h.handleGetCollection }},
		{name: "library collections", method: http.MethodGet, path: "/api/libraries/2/collections",
			params: map[string]string{"libraryId": "2"}, handler: func(h *Handler) http.HandlerFunc { return h.handleListLibraryCollections }},
		{name: "get playlist", method: http.MethodGet, path: "/api/playlists/pl1",
			params: map[string]string{"id": "pl1"}, handler: func(h *Handler) http.HandlerFunc { return h.handleGetPlaylist }},
		{name: "create playlist", method: http.MethodPost, path: "/api/playlists",
			body: `{"name":"New","items":[{"libraryItemId":"book-known"}]}`, handler: func(h *Handler) http.HandlerFunc { return h.handleCreatePlaylist }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, plays := newHandler()
			var body []byte
			if tc.body != "" {
				body = []byte(tc.body)
			}
			rec := dispatchABSWithParams(tc.method, tc.path, tc.params, body, "1", "p1", tc.handler(h))
			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "book-known") {
				t.Errorf("response lists a book despite the failed scope: %s", rec.Body.String())
			}
			if tc.method == http.MethodPost {
				if n := len(plays.rows); n != 1 {
					t.Errorf("playlists = %d after the refused create, want only the 1 seeded", n)
				}
			}
		})
	}
}
