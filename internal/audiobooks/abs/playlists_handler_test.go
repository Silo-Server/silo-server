package abs

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// memPlaylistStore is an in-memory PlaylistStore for handler tests.
type memPlaylistStore struct {
	mu    sync.Mutex
	rows  map[string]Playlist       // id -> row
	items map[string][]PlaylistItem // playlist_id -> items
}

func newMemPlaylistStore() *memPlaylistStore {
	return &memPlaylistStore{
		rows:  map[string]Playlist{},
		items: map[string][]PlaylistItem{},
	}
}

func (m *memPlaylistStore) ListUserPlaylists(_ context.Context, userID, profileID string) ([]Playlist, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Playlist, 0)
	for _, p := range m.rows {
		if p.UserID == userID && p.ProfileID == profileID {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *memPlaylistStore) GetPlaylist(_ context.Context, id string) (Playlist, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.rows[id]
	if !ok {
		return Playlist{}, ErrNotFound
	}
	return p, nil
}

func (m *memPlaylistStore) CreatePlaylist(_ context.Context, p Playlist) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[p.ID] = p
	return nil
}

func (m *memPlaylistStore) UpdatePlaylist(_ context.Context, p Playlist) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.rows[p.ID]
	if !ok {
		return ErrNotFound
	}
	existing.Name = p.Name
	existing.Description = p.Description
	existing.CoverItem = p.CoverItem
	existing.IsPublic = p.IsPublic
	existing.UpdatedAt = time.Now()
	m.rows[p.ID] = existing
	return nil
}

func (m *memPlaylistStore) DeletePlaylist(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, id)
	delete(m.items, id)
	return nil
}

func (m *memPlaylistStore) ListPlaylistItems(_ context.Context, playlistID string) ([]PlaylistItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := m.items[playlistID]
	out := make([]PlaylistItem, len(items))
	copy(out, items)
	sort.Slice(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out, nil
}

func (m *memPlaylistStore) AddPlaylistItem(_ context.Context, playlistID, libraryItemID, episodeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, it := range m.items[playlistID] {
		if it.LibraryItemID == libraryItemID && it.EpisodeID == episodeID {
			return nil // ON CONFLICT DO NOTHING
		}
	}
	maxPos := 0
	for _, it := range m.items[playlistID] {
		if it.Position > maxPos {
			maxPos = it.Position
		}
	}
	m.items[playlistID] = append(m.items[playlistID], PlaylistItem{
		PlaylistID:    playlistID,
		LibraryItemID: libraryItemID,
		EpisodeID:     episodeID,
		Position:      maxPos + 1,
		AddedAt:       time.Now(),
	})
	if p, ok := m.rows[playlistID]; ok {
		p.UpdatedAt = time.Now()
		m.rows[playlistID] = p
	}
	return nil
}

func (m *memPlaylistStore) RemovePlaylistItem(_ context.Context, playlistID, libraryItemID, episodeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := m.items[playlistID]
	out := items[:0]
	for _, it := range items {
		if it.LibraryItemID != libraryItemID || it.EpisodeID != episodeID {
			out = append(out, it)
		}
	}
	m.items[playlistID] = out
	if p, ok := m.rows[playlistID]; ok {
		p.UpdatedAt = time.Now()
		m.rows[playlistID] = p
	}
	return nil
}

type playlistsHarness struct {
	H    *Handler
	Play *memPlaylistStore
	Pub  *recordingPublisher
}

func newPlaylistsHarness(t *testing.T, knownItems ...string) *playlistsHarness {
	t.Helper()
	known := map[string]*models.MediaItem{}
	for _, id := range knownItems {
		known[id] = nil
	}
	pub := &recordingPublisher{}
	store := newMemPlaylistStore()
	h := New(Dependencies{
		MediaStore:    &stubMediaStore{known: known},
		PlaylistStore: store,
		Publisher:     pub,
	})
	return &playlistsHarness{H: h, Play: store, Pub: pub}
}

func TestPlaylist_NonOwner_404(t *testing.T) {
	item := `{"libraryItemId":"book-1"}`
	runNonOwner404(t, []nonOwnerCase[*playlistsHarness]{
		{name: "get private", seed: `{"name":"private"}`, method: http.MethodGet, handler: (*Handler).handleGetPlaylist},
		{name: "patch", method: http.MethodPatch, body: `{"name":"hijack"}`, handler: (*Handler).handleUpdatePlaylist,
			check: func(t *testing.T, hb *playlistsHarness, id string) {
				if p, _ := hb.Play.GetPlaylist(context.Background(), id); p.Name != "mine" {
					t.Errorf("non-owner mutation leaked: name = %q", p.Name)
				}
			}},
		{name: "delete", method: http.MethodDelete, handler: (*Handler).handleDeletePlaylist,
			check: func(t *testing.T, hb *playlistsHarness, id string) {
				if _, err := hb.Play.GetPlaylist(context.Background(), id); err != nil {
					t.Errorf("playlist wrongly deleted: %v", err)
				}
			}},
		{name: "add item", known: "book-1", method: http.MethodPost, suffix: "/item", body: item, handler: (*Handler).handleAddPlaylistItem},
		{name: "remove item", known: "book-1", method: http.MethodDelete, suffix: "/item/book-1",
			params: map[string]string{"libraryItemId": "book-1"}, handler: (*Handler).handleRemovePlaylistItem,
			before: func(t *testing.T, hb *playlistsHarness, id string) {
				_ = dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
					map[string]string{"id": id}, []byte(item), "1", "", hb.H.handleAddPlaylistItem)
			},
			check: func(t *testing.T, hb *playlistsHarness, id string) {
				if items, _ := hb.Play.ListPlaylistItems(context.Background(), id); len(items) != 1 {
					t.Errorf("items len = %d, want 1 (non-owner remove leaked)", len(items))
				}
			}},
		{name: "batch add", known: "book-1", method: http.MethodPost, suffix: "/batch/add",
			body: `{"items":[{"libraryItemId":"book-1"}]}`, handler: (*Handler).handleBatchAddPlaylistItems},
	}, newPlaylistsHarness, func(hb *playlistsHarness) *Handler { return hb.H }, "/api/playlists",
		func(h *Handler) http.HandlerFunc { return h.handleCreatePlaylist })
}

func TestPlaylist_List_ProfileIsolation(t *testing.T) {
	hb := newPlaylistsHarness(t)
	pA := "00000000-0000-0000-0000-0000000000aa"
	pB := "00000000-0000-0000-0000-0000000000bb"
	_ = createForUser(t, "/api/playlists", "1", pA, `{"name":"A"}`, hb.H.handleCreatePlaylist)
	rec := dispatchABSWithParams(http.MethodGet, "/api/playlists", nil, nil, "1", pB, hb.H.handleListPlaylists)
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	list, _ := env["playlists"].([]any)
	if len(list) != 0 {
		t.Errorf("profile B sees %d playlists, want 0", len(list))
	}
}
