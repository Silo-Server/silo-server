package playback

import (
	"context"
	"testing"
	"time"
)

// fakeRecipeStore is an in-memory RecipeStore for asserting card lifetime.
type fakeRecipeStore struct {
	cards map[string]RecipeCard
}

func newFakeRecipeStore() *fakeRecipeStore {
	return &fakeRecipeStore{cards: make(map[string]RecipeCard)}
}

func (f *fakeRecipeStore) Enabled() bool { return true }
func (f *fakeRecipeStore) Save(_ context.Context, card RecipeCard) error {
	f.cards[card.SessionID] = card
	return nil
}
func (f *fakeRecipeStore) Get(_ context.Context, id string) (RecipeCard, bool, error) {
	card, ok := f.cards[id]
	return card, ok, nil
}
func (f *fakeRecipeStore) Delete(_ context.Context, id string) error {
	delete(f.cards, id)
	return nil
}
func (f *fakeRecipeStore) Refresh(_ context.Context, _ string) error { return nil }
func (f *fakeRecipeStore) ActiveSessionIDs(_ context.Context) (map[string]struct{}, error) {
	ids := make(map[string]struct{}, len(f.cards))
	for id := range f.cards {
		ids[id] = struct{}{}
	}
	return ids, nil
}
func (f *fakeRecipeStore) DeleteExpired(_ context.Context) (int64, error) { return 0, nil }

func managerWithStore(store RecipeStore) *TranscodeManager {
	m := NewTranscodeManager()
	m.StoreFn = func() RecipeStore { return store }
	return m
}

// CloseTranscodeSession must delete the recipe card only for a genuine
// user-initiated stop. Liveness teardown (expiry reap, ffmpeg crash, abort,
// transcode restart) must KEEP the card so the client can reconstruct.
func TestCloseTranscodeSession_CardLifetime(t *testing.T) {
	t.Run("user-initiated stop deletes card", func(t *testing.T) {
		store := newFakeRecipeStore()
		_ = store.Save(context.Background(), RecipeCard{SessionID: "s1"})
		m := managerWithStore(store)
		m.CloseTranscodeSession("s1", "", true)
		if _, ok, _ := store.Get(context.Background(), "s1"); ok {
			t.Fatal("card should be deleted on user-initiated stop")
		}
	})

	t.Run("liveness teardown keeps card", func(t *testing.T) {
		store := newFakeRecipeStore()
		_ = store.Save(context.Background(), RecipeCard{SessionID: "s2"})
		m := managerWithStore(store)
		m.CloseTranscodeSession("s2", "", false)
		if _, ok, _ := store.Get(context.Background(), "s2"); !ok {
			t.Fatal("card must be preserved on liveness teardown (reap/abort/crash)")
		}
	})

	// The refresh-throttle map must not grow unbounded: closing a session has to
	// drop its entry regardless of whether the card is deleted.
	t.Run("close clears refresh-throttle entry", func(t *testing.T) {
		store := newFakeRecipeStore()
		m := managerWithStore(store)
		m.recipeRefreshAt["s3"] = time.Now()
		m.CloseTranscodeSession("s3", "", false)
		m.recipeRefreshMu.Lock()
		_, present := m.recipeRefreshAt["s3"]
		m.recipeRefreshMu.Unlock()
		if present {
			t.Fatal("recipeRefreshAt entry must be removed on session close")
		}
	})
}

// fakeSessionRegistry is a GetSession + RegisterReconstructed double.
type fakeSessionRegistry struct {
	sessions map[string]*Session
}

func (f *fakeSessionRegistry) GetSession(id string) (*Session, error) {
	if s, ok := f.sessions[id]; ok {
		return s, nil
	}
	return nil, ErrSessionNotFound
}

func (f *fakeSessionRegistry) RegisterReconstructed(s *Session) *Session {
	if f.sessions == nil {
		f.sessions = map[string]*Session{}
	}
	if existing, ok := f.sessions[s.ID]; ok {
		return existing
	}
	f.sessions[s.ID] = s
	return s
}

func TestLoadOrReconstructSession(t *testing.T) {
	ctx := context.Background()

	newMgr := func(reg *fakeSessionRegistry, store RecipeStore) *TranscodeManager {
		m := managerWithStore(store)
		m.Sessions = reg
		return m
	}

	t.Run("live session, matching owner -> loaded", func(t *testing.T) {
		reg := &fakeSessionRegistry{sessions: map[string]*Session{"s": {ID: "s", UserID: 5}}}
		m := newMgr(reg, newFakeRecipeStore())
		got, status := m.LoadOrReconstructSession(ctx, reg.GetSession, "s", 5)
		if status != SessionLoaded || got == nil || got.ID != "s" {
			t.Fatalf("got status=%v session=%+v", status, got)
		}
	})

	t.Run("live session, mismatched owner -> forbidden", func(t *testing.T) {
		reg := &fakeSessionRegistry{sessions: map[string]*Session{"s": {ID: "s", UserID: 5}}}
		m := newMgr(reg, newFakeRecipeStore())
		if _, status := m.LoadOrReconstructSession(ctx, reg.GetSession, "s", 9); status != SessionForbidden {
			t.Fatalf("status = %v, want forbidden", status)
		}
	})

	t.Run("live session, zero caller -> loaded (UUID as bearer)", func(t *testing.T) {
		reg := &fakeSessionRegistry{sessions: map[string]*Session{"s": {ID: "s", UserID: 5}}}
		m := newMgr(reg, newFakeRecipeStore())
		if _, status := m.LoadOrReconstructSession(ctx, reg.GetSession, "s", 0); status != SessionLoaded {
			t.Fatalf("status = %v, want loaded", status)
		}
	})

	t.Run("miss + remux card + matching owner -> reconstructed with method", func(t *testing.T) {
		store := newFakeRecipeStore()
		_ = store.Save(ctx, NewRemuxRecipeCard("s", 5, "p", 77, true, 2))
		reg := &fakeSessionRegistry{}
		m := newMgr(reg, store)
		got, status := m.LoadOrReconstructSession(ctx, reg.GetSession, "s", 5)
		if status != SessionLoaded || got == nil {
			t.Fatalf("status=%v session=%+v", status, got)
		}
		if got.PlayMethod != PlayRemux || got.MediaFileID != 77 || !got.TranscodeAudio || got.AudioTrackIndex != 2 {
			t.Fatalf("reconstructed remux session wrong: %+v", got)
		}
		// The reconstructed session must now be live (registered).
		if _, err := reg.GetSession("s"); err != nil {
			t.Fatalf("reconstructed session not registered: %v", err)
		}
	})

	t.Run("miss + card + mismatched owner -> missing (reconstruct refuses)", func(t *testing.T) {
		store := newFakeRecipeStore()
		_ = store.Save(ctx, NewDirectRecipeCard("s", 5, "p", 77))
		reg := &fakeSessionRegistry{}
		m := newMgr(reg, store)
		if _, status := m.LoadOrReconstructSession(ctx, reg.GetSession, "s", 9); status != SessionMissing {
			t.Fatalf("status = %v, want missing", status)
		}
	})

	t.Run("miss + no card -> missing", func(t *testing.T) {
		reg := &fakeSessionRegistry{}
		m := newMgr(reg, newFakeRecipeStore())
		if _, status := m.LoadOrReconstructSession(ctx, reg.GetSession, "nope", 5); status != SessionMissing {
			t.Fatalf("status = %v, want missing", status)
		}
	})
}

// acquireReconstructSlot must bound concurrent reconstructs and let a caller
// whose request is cancelled give up its place instead of queueing dead work.
func TestAcquireReconstructSlot(t *testing.T) {
	m := &TranscodeManager{reconstructSem: make(chan struct{}, 1)}

	release, ok := m.acquireReconstructSlot(context.Background())
	if !ok {
		t.Fatal("first acquire should succeed")
	}

	// Cap is full: a cancelled request must back off rather than block forever.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := m.acquireReconstructSlot(cancelled); ok {
		t.Fatal("acquire on a full semaphore with a cancelled context must fail")
	}

	// Releasing frees the slot for the next reconstruct.
	release()
	release2, ok := m.acquireReconstructSlot(context.Background())
	if !ok {
		t.Fatal("acquire should succeed after the slot is released")
	}
	release2()
}
