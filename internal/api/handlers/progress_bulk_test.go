package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
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

type progressEpisodeLookupFunc func(context.Context, []string) ([]*models.Episode, error)

func (f progressEpisodeLookupFunc) GetByIDs(ctx context.Context, ids []string) ([]*models.Episode, error) {
	return f(ctx, ids)
}

// Progress events name an episode's series, so a client that hasn't cached the
// episode can still refresh its series and seasons (#796). The series come
// from one lookup for the batch; a failed lookup still publishes, without them.
func TestSyncProgressEventsNameTheEpisodeSeries(t *testing.T) {
	sync := func(t *testing.T, lookup ProgressEpisodeLookup) map[string]userStateEventPayload {
		t.Helper()
		hub := evt.NewHub("test", nil)
		events, unsubscribe := hub.Subscribe()
		defer unsubscribe()
		h := NewProgressHandler(&bulkProgressProvider{store: &bulkProgressStore{}})
		h.EventsHub = hub
		h.EpisodeLookup = lookup
		if _, err := h.SyncProgress(t.Context(), 17, "child", []ProgressSyncUpdate{{MediaItemID: "episode-1"}, {MediaItemID: "movie-1"}}); err != nil {
			t.Fatal(err)
		}
		got := map[string]userStateEventPayload{}
		for range 2 {
			select {
			case env := <-events:
				var p userStateEventPayload
				if err := json.Unmarshal(env.Data, &p); err != nil {
					t.Fatal(err)
				}
				got[p.ContentID] = p
			default:
				t.Fatalf("published %d events, want 2", len(got))
			}
		}
		return got
	}

	lookups := 0
	got := sync(t, progressEpisodeLookupFunc(func(_ context.Context, ids []string) ([]*models.Episode, error) {
		lookups++
		if len(ids) != 2 {
			t.Fatalf("looked up %v, want the batch", ids)
		}
		return []*models.Episode{{ContentID: "episode-1", SeriesID: "series-1"}}, nil
	}))
	if lookups != 1 || got["episode-1"].SeriesID != "series-1" || got["movie-1"].SeriesID != "" || got["episode-1"].Change != "progress" {
		t.Fatalf("events=%+v lookups=%d", got, lookups)
	}

	got = sync(t, progressEpisodeLookupFunc(func(context.Context, []string) ([]*models.Episode, error) {
		return nil, errors.New("database down")
	}))
	if got["episode-1"].SeriesID != "" {
		t.Fatalf("failed lookup event=%+v", got["episode-1"])
	}
}
