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

func TestPlaylist_Create_ReturnsFullShape(t *testing.T) {
	hb := newPlaylistsHarness(t)
	body := []byte(`{"name":"queue","description":"d","isPublic":true}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists", nil, body, "1", "", hb.H.handleCreatePlaylist)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["name"] != "queue" {
		t.Errorf("name = %v, want queue", got["name"])
	}
	if got["isPublic"] != true {
		t.Errorf("isPublic = %v, want true", got["isPublic"])
	}
	items, _ := got["items"].([]any)
	if items == nil {
		t.Errorf("items missing on full-shape: %v", got)
	}
}

func TestPlaylist_Create_NameRequired_400(t *testing.T) {
	hb := newPlaylistsHarness(t)
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists", nil, []byte(`{}`), "1", "", hb.H.handleCreatePlaylist)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestPlaylist_Create_FiresPlaylistAddedEvent(t *testing.T) {
	hb := newPlaylistsHarness(t)
	_ = dispatchABSWithParams(http.MethodPost, "/api/playlists", nil, []byte(`{"name":"queue"}`), "7", "", hb.H.handleCreatePlaylist)
	evts := hb.Pub.snapshot()
	if len(evts) != 1 {
		t.Fatalf("events = %d, want 1", len(evts))
	}
	if evts[0].Event != "playlist_added" {
		t.Errorf("event = %q, want playlist_added", evts[0].Event)
	}
	if evts[0].UserID != "7" {
		t.Errorf("event userID = %q, want 7", evts[0].UserID)
	}
	payload, _ := evts[0].Payload.(map[string]any)
	if payload["name"] != "queue" {
		t.Errorf("payload name = %v, want queue", payload["name"])
	}
}
