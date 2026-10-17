package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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
// A series is named only for an episode the viewer may see.
func TestSyncProgressEventsNameTheEpisodeSeries(t *testing.T) {
	scoped := access.SetScope(t.Context(), access.Scope{AllowedLibraryIDs: []int{4}})
	episodes := func(ids ...string) progressEpisodeLookupFunc {
		return func(_ context.Context, asked []string) ([]*models.Episode, error) {
			var out []*models.Episode
			for _, id := range asked {
				if slices.Contains(ids, id) {
					out = append(out, &models.Episode{ContentID: id, SeriesID: "series-of-" + id})
				}
			}
			return out, nil
		}
	}
	sync := func(t *testing.T, ctx context.Context, lookup ProgressEpisodeLookup, accessible map[string]bool, updates []ProgressSyncUpdate) map[string]string {
		t.Helper()
		hub := evt.NewHub("test", nil)
		events, unsubscribe := hub.Subscribe()
		defer unsubscribe()
		h := NewProgressHandler(&bulkProgressProvider{store: &bulkProgressStore{}})
		h.EventsHub = hub
		h.EpisodeLookup = lookup
		h.LibraryLookup = &fakeProgressLookup{accessible: accessible}
		if _, err := h.SyncProgress(ctx, 17, "child", updates); err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for range updates {
			select {
			case env := <-events:
				var p userStateEventPayload
				if err := json.Unmarshal(env.Data, &p); err != nil {
					t.Fatal(err)
				}
				if p.Change != "progress" {
					t.Fatalf("change = %q", p.Change)
				}
				got[p.ContentID] = p.SeriesID
			default:
				t.Fatalf("published %d events, want %d", len(got), len(updates))
			}
		}
		return got
	}

	// v2: the sync's own access check covers the series.
	lookups := 0
	counting := func(ctx context.Context, ids []string) ([]*models.Episode, error) {
		lookups++
		return episodes("episode-1")(ctx, ids)
	}
	got := sync(t, scoped, progressEpisodeLookupFunc(counting), map[string]bool{"episode-1": true, "movie-1": true},
		[]ProgressSyncUpdate{{MediaItemID: "episode-1", CheckAccess: true}, {MediaItemID: "movie-1", CheckAccess: true}})
	if lookups != 1 || got["episode-1"] != "series-of-episode-1" || got["movie-1"] != "" {
		t.Fatalf("checked sync: series=%v after %d lookups", got, lookups)
	}

	// v1 writes unchecked IDs; only those the viewer's scope allows get a series.
	got = sync(t, scoped, episodes("episode-in", "episode-out"), map[string]bool{"episode-in": true},
		[]ProgressSyncUpdate{{MediaItemID: "episode-in"}, {MediaItemID: "episode-out"}})
	if got["episode-in"] != "series-of-episode-in" || got["episode-out"] != "" {
		t.Fatalf("unchecked sync: series=%v, want only the in-scope episode's", got)
	}
	// Without a viewer scope there is nothing to check unchecked IDs against.
	got = sync(t, t.Context(), episodes("episode-in"), map[string]bool{"episode-in": true},
		[]ProgressSyncUpdate{{MediaItemID: "episode-in"}})
	if got["episode-in"] != "" {
		t.Fatalf("unscoped sync named series %q", got["episode-in"])
	}

	// A failed lookup still publishes, without series.
	got = sync(t, scoped, progressEpisodeLookupFunc(func(context.Context, []string) ([]*models.Episode, error) {
		return nil, errors.New("database down")
	}), map[string]bool{"episode-1": true}, []ProgressSyncUpdate{{MediaItemID: "episode-1", CheckAccess: true}})
	if got["episode-1"] != "" {
		t.Fatalf("failed lookup named series %q", got["episode-1"])
	}
}
