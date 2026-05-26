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

func createPlaylistForUser(t *testing.T, hb *playlistsHarness, userID, profileID, body string) string {
	t.Helper()
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists", nil, []byte(body), userID, profileID, hb.H.handleCreatePlaylist)
	if rec.Code != http.StatusOK {
		t.Fatalf("seed POST status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	id, _ := got["id"].(string)
	if id == "" {
		t.Fatalf("seed POST returned no id; body=%s", rec.Body.String())
	}
	return id
}

func TestPlaylist_List_WrappedEnvelope(t *testing.T) {
	hb := newPlaylistsHarness(t)
	_ = createPlaylistForUser(t, hb, "1", "", `{"name":"a"}`)
	_ = createPlaylistForUser(t, hb, "1", "", `{"name":"b"}`)

	rec := dispatchABSWithParams(http.MethodGet, "/api/playlists", nil, nil, "1", "", hb.H.handleListPlaylists)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	list, ok := env["playlists"].([]any)
	if !ok {
		t.Fatalf("response missing 'playlists' key; body=%s", rec.Body.String())
	}
	if len(list) != 2 {
		t.Errorf("list len = %d, want 2", len(list))
	}
	for _, p := range list {
		entry := p.(map[string]any)
		if _, has := entry["items"]; has {
			t.Errorf("list entry has items key (should be detail-only): %v", entry)
		}
	}
}

func TestPlaylist_List_ProfileIsolation(t *testing.T) {
	hb := newPlaylistsHarness(t)
	pA := "00000000-0000-0000-0000-0000000000aa"
	pB := "00000000-0000-0000-0000-0000000000bb"
	_ = createPlaylistForUser(t, hb, "1", pA, `{"name":"A"}`)
	rec := dispatchABSWithParams(http.MethodGet, "/api/playlists", nil, nil, "1", pB, hb.H.handleListPlaylists)
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	list, _ := env["playlists"].([]any)
	if len(list) != 0 {
		t.Errorf("profile B sees %d playlists, want 0", len(list))
	}
}

func TestPlaylist_Get_Owner_ReturnsFullShape(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"mine"}`)

	rec := dispatchABSWithParams(http.MethodGet, "/api/playlists/"+id, map[string]string{"id": id}, nil, "1", "", hb.H.handleGetPlaylist)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["name"] != "mine" {
		t.Errorf("name = %v, want 'mine'", got["name"])
	}
	if _, has := got["items"]; !has {
		t.Errorf("items missing on full-shape: %v", got)
	}
}

func TestPlaylist_Get_NonOwner_Private_404(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"private"}`)

	rec := dispatchABSWithParams(http.MethodGet, "/api/playlists/"+id, map[string]string{"id": id}, nil, "2", "", hb.H.handleGetPlaylist)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestPlaylist_Get_NonOwner_Public_OK(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"public","isPublic":true}`)

	rec := dispatchABSWithParams(http.MethodGet, "/api/playlists/"+id, map[string]string{"id": id}, nil, "2", "", hb.H.handleGetPlaylist)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestPlaylist_Get_Unknown_404(t *testing.T) {
	hb := newPlaylistsHarness(t)
	rec := dispatchABSWithParams(http.MethodGet, "/api/playlists/01HZZZ", map[string]string{"id": "01HZZZ"}, nil, "1", "", hb.H.handleGetPlaylist)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestPlaylist_Patch_UpdatesCover(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"x"}`)

	body := []byte(`{"cover_item":"01HCOVER"}`)
	rec := dispatchABSWithParams(http.MethodPatch, "/api/playlists/"+id, map[string]string{"id": id}, body, "1", "", hb.H.handleUpdatePlaylist)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["coverPath"] != "01HCOVER" {
		t.Errorf("coverPath = %v, want 01HCOVER", got["coverPath"])
	}
}

func TestPlaylist_Patch_FiresUpdatedEvent(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "7", "", `{"name":"x"}`)
	// snapshot count after create
	before := len(hb.Pub.snapshot())

	_ = dispatchABSWithParams(http.MethodPatch, "/api/playlists/"+id, map[string]string{"id": id}, []byte(`{"name":"renamed"}`), "7", "", hb.H.handleUpdatePlaylist)

	evts := hb.Pub.snapshot()
	if len(evts) != before+1 {
		t.Fatalf("events = %d (delta %d), want exactly 1 new event", len(evts), len(evts)-before)
	}
	if evts[len(evts)-1].Event != "playlist_updated" {
		t.Errorf("event = %q, want playlist_updated", evts[len(evts)-1].Event)
	}
}

func TestPlaylist_Patch_NonOwner_404(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"mine"}`)

	rec := dispatchABSWithParams(http.MethodPatch, "/api/playlists/"+id, map[string]string{"id": id}, []byte(`{"name":"hijack"}`), "2", "", hb.H.handleUpdatePlaylist)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	p, _ := hb.Play.GetPlaylist(context.Background(), id)
	if p.Name != "mine" {
		t.Errorf("non-owner mutation leaked: name = %q", p.Name)
	}
}

func TestPlaylist_Delete_Owner_FiresRemovedEvent(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "7", "", `{"name":"x"}`)
	before := len(hb.Pub.snapshot())

	rec := dispatchABSWithParams(http.MethodDelete, "/api/playlists/"+id, map[string]string{"id": id}, nil, "7", "", hb.H.handleDeletePlaylist)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	evts := hb.Pub.snapshot()
	if len(evts) != before+1 {
		t.Fatalf("events = %d, want exactly 1 new event", len(evts)-before)
	}
	if evts[len(evts)-1].Event != "playlist_removed" {
		t.Errorf("event = %q, want playlist_removed", evts[len(evts)-1].Event)
	}
}

func TestPlaylist_Delete_NonOwner_404(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"mine"}`)

	rec := dispatchABSWithParams(http.MethodDelete, "/api/playlists/"+id, map[string]string{"id": id}, nil, "2", "", hb.H.handleDeletePlaylist)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if _, err := hb.Play.GetPlaylist(context.Background(), id); err != nil {
		t.Errorf("playlist wrongly deleted: %v", err)
	}
}

func TestPlaylist_AddItem_AudiobookHydrates(t *testing.T) {
	hb := newPlaylistsHarness(t, "book-1")
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)

	body := []byte(`{"libraryItemId":"book-1"}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, body, "1", "", hb.H.handleAddPlaylistItem)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	entry := items[0].(map[string]any)
	if entry["libraryItemId"] != "book-1" {
		t.Errorf("libraryItemId = %v, want book-1", entry["libraryItemId"])
	}
	if _, has := entry["title"]; !has {
		t.Errorf("audiobook item missing 'title' hydration: %v", entry)
	}
	if pos, _ := entry["position"].(float64); pos != 1 {
		t.Errorf("first item position = %v, want 1", entry["position"])
	}
}

func TestPlaylist_AddItem_AppendsAtNextPosition(t *testing.T) {
	hb := newPlaylistsHarness(t, "book-1", "book-2", "book-3")
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)

	for _, b := range []string{"book-1", "book-2", "book-3"} {
		body := []byte(`{"libraryItemId":"` + b + `"}`)
		_ = dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
			map[string]string{"id": id}, body, "1", "", hb.H.handleAddPlaylistItem)
	}
	rec := dispatchABSWithParams(http.MethodGet, "/api/playlists/"+id, map[string]string{"id": id}, nil, "1", "", hb.H.handleGetPlaylist)
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, _ := got["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("items len = %d, want 3", len(items))
	}
	for i, raw := range items {
		entry := raw.(map[string]any)
		wantPos := float64(i + 1)
		if entry["position"] != wantPos {
			t.Errorf("items[%d] position = %v, want %v", i, entry["position"], wantPos)
		}
	}
}

func TestPlaylist_AddItem_Idempotent(t *testing.T) {
	hb := newPlaylistsHarness(t, "book-1")
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)

	body := []byte(`{"libraryItemId":"book-1"}`)
	_ = dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item", map[string]string{"id": id}, body, "1", "", hb.H.handleAddPlaylistItem)
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item", map[string]string{"id": id}, body, "1", "", hb.H.handleAddPlaylistItem)
	if rec.Code != http.StatusOK {
		t.Fatalf("second add status = %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Errorf("items len = %d, want 1 (idempotent)", len(items))
	}
}

func TestPlaylist_AddItem_Episode_AcceptsAndEchoes(t *testing.T) {
	hb := newPlaylistsHarness(t /* no known items - episode skips validation */)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)

	body := []byte(`{"libraryItemId":"podcast-x","episodeId":"ep-1"}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, body, "1", "", hb.H.handleAddPlaylistItem)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	entry := items[0].(map[string]any)
	if entry["episodeId"] != "ep-1" {
		t.Errorf("episodeId = %v, want ep-1", entry["episodeId"])
	}
	if _, has := entry["title"]; has {
		t.Errorf("episode item must NOT be hydrated: %v", entry)
	}
}

func TestPlaylist_AddItem_UnknownAudiobook_404(t *testing.T) {
	hb := newPlaylistsHarness(t /* no known items */)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)

	body := []byte(`{"libraryItemId":"ghost"}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, body, "1", "", hb.H.handleAddPlaylistItem)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (item not found); body=%s", rec.Code, rec.Body.String())
	}
}

func TestPlaylist_AddItem_LibraryItemIdRequired_400(t *testing.T) {
	hb := newPlaylistsHarness(t, "book-1")
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)

	body := []byte(`{"libraryItemId":""}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, body, "1", "", hb.H.handleAddPlaylistItem)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestPlaylist_AddItem_NonOwner_404(t *testing.T) {
	hb := newPlaylistsHarness(t, "book-1")
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"mine"}`)

	body := []byte(`{"libraryItemId":"book-1"}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, body, "2", "", hb.H.handleAddPlaylistItem)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestPlaylist_AddItem_FiresUpdatedEvent(t *testing.T) {
	hb := newPlaylistsHarness(t, "book-1")
	id := createPlaylistForUser(t, hb, "7", "", `{"name":"q"}`)
	before := len(hb.Pub.snapshot())

	body := []byte(`{"libraryItemId":"book-1"}`)
	_ = dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, body, "7", "", hb.H.handleAddPlaylistItem)

	evts := hb.Pub.snapshot()
	if len(evts) != before+1 {
		t.Fatalf("events = %d, want exactly 1 new event", len(evts)-before)
	}
	if evts[len(evts)-1].Event != "playlist_updated" {
		t.Errorf("event = %q, want playlist_updated", evts[len(evts)-1].Event)
	}
}

func TestPlaylist_RemoveItem_Single(t *testing.T) {
	hb := newPlaylistsHarness(t, "book-1", "book-2")
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)
	_ = dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, []byte(`{"libraryItemId":"book-1"}`), "1", "", hb.H.handleAddPlaylistItem)
	_ = dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, []byte(`{"libraryItemId":"book-2"}`), "1", "", hb.H.handleAddPlaylistItem)

	rec := dispatchABSWithParams(http.MethodDelete, "/api/playlists/"+id+"/item/book-1",
		map[string]string{"id": id, "libraryItemId": "book-1"}, nil, "1", "", hb.H.handleRemovePlaylistItem)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	if items[0].(map[string]any)["libraryItemId"] != "book-2" {
		t.Errorf("remaining item = %v, want book-2", items[0])
	}
}

func TestPlaylist_RemoveItem_Idempotent(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)

	rec := dispatchABSWithParams(http.MethodDelete, "/api/playlists/"+id+"/item/book-99",
		map[string]string{"id": id, "libraryItemId": "book-99"}, nil, "1", "", hb.H.handleRemovePlaylistItem)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (idempotent)", rec.Code)
	}
}

func TestPlaylist_RemoveItem_WithEpisode(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)

	// Seed two items at the same libraryItemId — one with episode, one without.
	_ = dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, []byte(`{"libraryItemId":"podcast-x","episodeId":"ep-1"}`), "1", "", hb.H.handleAddPlaylistItem)
	_ = dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, []byte(`{"libraryItemId":"podcast-x","episodeId":"ep-2"}`), "1", "", hb.H.handleAddPlaylistItem)

	// Remove ep-1 specifically.
	rec := dispatchABSWithParams(http.MethodDelete, "/api/playlists/"+id+"/item/podcast-x/ep-1",
		map[string]string{"id": id, "libraryItemId": "podcast-x", "episodeId": "ep-1"}, nil, "1", "", hb.H.handleRemovePlaylistEpisode)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1 (ep-2 should remain)", len(items))
	}
	if items[0].(map[string]any)["episodeId"] != "ep-2" {
		t.Errorf("remaining item episodeId = %v, want ep-2", items[0])
	}
}

func TestPlaylist_RemoveItem_NonOwner_404(t *testing.T) {
	hb := newPlaylistsHarness(t, "book-1")
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"mine"}`)
	_ = dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, []byte(`{"libraryItemId":"book-1"}`), "1", "", hb.H.handleAddPlaylistItem)

	rec := dispatchABSWithParams(http.MethodDelete, "/api/playlists/"+id+"/item/book-1",
		map[string]string{"id": id, "libraryItemId": "book-1"}, nil, "2", "", hb.H.handleRemovePlaylistItem)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	items, _ := hb.Play.ListPlaylistItems(context.Background(), id)
	if len(items) != 1 {
		t.Errorf("items len = %d, want 1 (non-owner remove leaked)", len(items))
	}
}
