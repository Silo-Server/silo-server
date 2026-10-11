package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/usercollections"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// unrestrictedOwners lets every collection owner access the whole catalog.
type unrestrictedOwners struct{}

func (unrestrictedOwners) OwnerFilter(context.Context, int, string) (catalog.AccessFilter, error) {
	return catalog.AccessFilter{}, nil
}

// heldTMDBList is a TMDB list source that reports each fetch on started and
// answers an empty list when release receives, so a test can act while a
// sync is in flight.
type heldTMDBList struct {
	started chan struct{}
	release chan struct{}
}

func (l heldTMDBList) GetList(ctx context.Context, _, _ int) ([]catalog.TMDBCollectionEntry, error) {
	l.started <- struct{}{}
	select {
	case <-l.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestPersonalCollectionSyncScheduleDB covers the schedule a /api/v2 update
// sets on a synced list: named cadences only, off with "", refused on manual
// and smart collections, ignored by the frozen /api/v1 update, kept when it
// lands during a sync, and #193 S2's isolation from other profiles and logins.
func TestPersonalCollectionSyncScheduleDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	ctx := t.Context()
	provider := pgstore.NewPostgresProvider(f.pool)
	store, err := provider.ForUser(ctx, f.account)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"owner", "viewer"} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	var otherLogin int
	if err := f.pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("sync-schedule-%d", time.Now().UnixNano())).Scan(&otherLogin); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, otherLogin) })
	h := NewCollectionHandler(provider)
	daily := usercollections.AllowedSyncSchedules["daily"]

	createSynced := func(t *testing.T, shared bool) *userstore.Collection {
		t.Helper()
		next := time.Now().Add(12 * time.Hour).UTC().Truncate(time.Microsecond)
		c, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
			CreatorProfileID: "owner", Name: "Synced", CollectionType: "tmdb", QueryDefinition: "{}",
			IsShared:     shared,
			SourceURL:    "https://www.themoviedb.org/list/310",
			SourceConfig: `{"mode":"tmdb_list","url":"https://www.themoviedb.org/list/310"}`,
			SyncSchedule: &daily, NextSyncAt: &next,
		})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	stored := func(t *testing.T, id string) *userstore.Collection {
		t.Helper()
		c, err := store.GetCollection(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	setSchedule := func(profileID, id, schedule string) (PersonalCollectionView, error) {
		return h.UpdatePersonalCollection(ctx, PersonalCollectionUpdateCommand{UserID: f.account, ProfileID: profileID, CollectionID: id, Request: PersonalCollectionUpdateRequest{SyncSchedule: &schedule}})
	}
	requireStatus := func(t *testing.T, err error, status int, field string) {
		t.Helper()
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != status || apiErr.Field != field {
			t.Fatalf("error = %v, want status %d on %q", err, status, field)
		}
	}
	requireUnchanged := func(t *testing.T, before *userstore.Collection) {
		t.Helper()
		after := stored(t, before.ID)
		if !equalStringPointers(after.SyncSchedule, before.SyncSchedule) || !equalTimePointers(after.NextSyncAt, before.NextSyncAt) {
			t.Fatalf("schedule %v next %v, want %v next %v", after.SyncSchedule, after.NextSyncAt, before.SyncSchedule, before.NextSyncAt)
		}
	}

	t.Run("a named cadence sets the schedule and its next run", func(t *testing.T) {
		c := createSynced(t, false)
		for _, tc := range []struct {
			cadence string
			within  time.Duration
			matches func(time.Time) bool
		}{
			{"weekly", 7 * 24 * time.Hour, func(local time.Time) bool { return local.Weekday() == time.Sunday }},
			{"monthly", 31 * 24 * time.Hour, func(local time.Time) bool { return local.Day() == 1 }},
			{"daily", 24 * time.Hour, func(time.Time) bool { return true }},
		} {
			before := time.Now()
			view, err := setSchedule("owner", c.ID, tc.cadence)
			if err != nil {
				t.Fatalf("%s: %v", tc.cadence, err)
			}
			want := usercollections.AllowedSyncSchedules[tc.cadence]
			got := stored(t, c.ID)
			if view.SyncSchedule != want || got.SyncSchedule == nil || *got.SyncSchedule != want {
				t.Fatalf("%s: view schedule %q, stored %v; want %q", tc.cadence, view.SyncSchedule, got.SyncSchedule, want)
			}
			// The next run is the schedule's next 04:30 on the node's clock,
			// plus up to 15 minutes of jitter.
			next := got.NextSyncAt
			if next == nil || view.NextSyncAt == "" || !next.After(before) || next.After(before.Add(tc.within+15*time.Minute)) {
				t.Fatalf("%s: next_sync_at = %v (view %q), want within %s of %v", tc.cadence, next, view.NextSyncAt, tc.within, before)
			}
			local := next.In(time.Local)
			if minutes := local.Hour()*60 + local.Minute(); minutes < 4*60+30 || minutes >= 4*60+45 || !tc.matches(local) {
				t.Fatalf("%s: next_sync_at %v is not the schedule's next run", tc.cadence, local)
			}
		}
	})

	t.Run("an empty schedule turns syncing off", func(t *testing.T) {
		c := createSynced(t, false)
		view, err := setSchedule("owner", c.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		got := stored(t, c.ID)
		if view.SyncSchedule != "" || view.NextSyncAt != "" || got.SyncSchedule != nil || got.NextSyncAt != nil {
			t.Fatalf("view %q %q, stored %v %v; want syncing off", view.SyncSchedule, view.NextSyncAt, got.SyncSchedule, got.NextSyncAt)
		}
	})

	t.Run("a cron expression is refused", func(t *testing.T) {
		c := createSynced(t, false)
		for _, expr := range []string{daily, "*/15 * * * *", "hourly"} {
			_, err := setSchedule("owner", c.ID, expr)
			requireStatus(t, err, http.StatusBadRequest, "sync_schedule")
			requireUnchanged(t, c)
		}
	})

	t.Run("manual and smart collections take no schedule", func(t *testing.T) {
		for _, kind := range []string{"manual", "smart"} {
			c, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: kind, CollectionType: kind, QueryDefinition: "{}"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = setSchedule("owner", c.ID, "daily")
			requireStatus(t, err, http.StatusBadRequest, "sync_schedule")
			requireUnchanged(t, c)
		}
	})

	v1 := func(t *testing.T, method, target, body string, handle http.HandlerFunc) []byte {
		t.Helper()
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		reqCtx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: f.account})
		reqCtx = apimw.SetProfileID(reqCtx, "owner")
		routeCtx := chi.NewRouteContext()
		if i := strings.LastIndex(target, "/"); method == http.MethodPut {
			routeCtx.URLParams.Add("id", target[i+1:])
		}
		reqCtx = context.WithValue(reqCtx, chi.RouteCtxKey, routeCtx)
		rec := httptest.NewRecorder()
		handle(rec, req.WithContext(reqCtx))
		if rec.Code != http.StatusOK {
			t.Fatalf("v1 %s %s: %d %s", method, target, rec.Code, rec.Body.String())
		}
		return rec.Body.Bytes()
	}
	// The members the frozen /api/v1 collection view has always had; a key
	// outside this set would change its bytes.
	v1Keys := []string{"id", "profile_id", "creator_profile_id", "name", "description", "collection_type", "is_shared",
		"allowed_profile_ids", "query_definition", "sort_config", "sort_order", "group_id", "source_url", "source_config",
		"sync_schedule", "next_sync_at", "last_sync_at", "last_sync_status", "last_sync_message", "display_query_definition",
		"item_count", "include_in_server_collections", "poster_url", "poster_thumbhash", "created_at", "updated_at"}
	requireV1View := func(t *testing.T, raw json.RawMessage) {
		t.Helper()
		var members map[string]json.RawMessage
		if err := json.Unmarshal(raw, &members); err != nil {
			t.Fatal(err)
		}
		for key := range members {
			if !slices.Contains(v1Keys, key) {
				t.Fatalf("v1 collection view has %q: %s", key, raw)
			}
		}
		if string(members["sync_schedule"]) != `"`+daily+`"` {
			t.Fatalf("v1 sync_schedule = %s, want %q", members["sync_schedule"], daily)
		}
	}

	t.Run("v1 update ignores sync_schedule", func(t *testing.T) {
		c := createSynced(t, false)
		body := v1(t, http.MethodPut, "/api/v1/collections/"+c.ID, `{"name":"Renamed","sync_schedule":"weekly"}`, h.HandleUpdateCollection)
		requireV1View(t, body)
		if got := stored(t, c.ID); got.Name != "Renamed" {
			t.Fatalf("name = %q, want the v1 update applied", got.Name)
		}
		requireUnchanged(t, c)
	})

	t.Run("v1 list carries no cadence", func(t *testing.T) {
		c := createSynced(t, false)
		var list struct {
			Collections []json.RawMessage `json:"collections"`
		}
		if err := json.Unmarshal(v1(t, http.MethodGet, "/api/v1/collections", "", h.HandleListCollections), &list); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, raw := range list.Collections {
			var id struct{ ID string }
			_ = json.Unmarshal(raw, &id)
			if id.ID == c.ID {
				found = true
				requireV1View(t, raw)
			}
		}
		if !found {
			t.Fatalf("v1 list lacks %s", c.ID)
		}
	})

	t.Run("an edit made during a sync wins", func(t *testing.T) {
		source := heldTMDBList{started: make(chan struct{}), release: make(chan struct{})}
		svc := usercollections.NewService(provider, catalog.NewItemRepository(f.pool), catalog.NewLibraryItemRepository(f.pool), unrestrictedOwners{}, nil, slog.New(slog.DiscardHandler))
		svc.TMDBLists = source
		// syncWhile runs a sync and applies edit while the sync is between
		// reading the collection and recording its result.
		syncWhile := func(t *testing.T, id string, edit func()) *userstore.Collection {
			t.Helper()
			done := make(chan error, 1)
			go func() {
				_, err := svc.SyncCollection(ctx, f.account, id)
				done <- err
			}()
			select {
			case <-source.started:
			case err := <-done:
				t.Fatalf("sync finished before fetching: %v", err)
			}
			edit()
			source.release <- struct{}{}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			got := stored(t, id)
			if got.LastSyncAt == nil || got.LastSyncStatus != "success" {
				t.Fatalf("sync result not recorded: last %v status %q", got.LastSyncAt, got.LastSyncStatus)
			}
			return got
		}

		c := createSynced(t, false)
		got := syncWhile(t, c.ID, func() {
			if _, err := setSchedule("owner", c.ID, ""); err != nil {
				t.Fatal(err)
			}
		})
		if got.SyncSchedule != nil || got.NextSyncAt != nil {
			t.Fatalf("daily to off during a sync: schedule %v next %v, want both null", got.SyncSchedule, got.NextSyncAt)
		}

		c = createSynced(t, false)
		var edited *userstore.Collection
		got = syncWhile(t, c.ID, func() {
			if _, err := setSchedule("owner", c.ID, "weekly"); err != nil {
				t.Fatal(err)
			}
			edited = stored(t, c.ID)
		})
		if got.SyncSchedule == nil || *got.SyncSchedule != usercollections.AllowedSyncSchedules["weekly"] || !equalTimePointers(got.NextSyncAt, edited.NextSyncAt) {
			t.Fatalf("daily to weekly during a sync: schedule %v next %v, want weekly next %v", got.SyncSchedule, got.NextSyncAt, edited.NextSyncAt)
		}

		// Without an edit, the sync moves the next run on as before.
		got = syncWhile(t, c.ID, func() {})
		if got.NextSyncAt == nil || !got.NextSyncAt.After(time.Now()) {
			t.Fatalf("unedited sync: next %v", got.NextSyncAt)
		}
	})

	// #193 S2: only the creator changes a collection's schedule. A profile
	// that can see a shared collection is refused, one that can't see a
	// private collection and another login don't find it, and the row stays
	// as it was.
	t.Run("only the creator changes the schedule", func(t *testing.T) {
		shared := createSynced(t, true)
		_, err := setSchedule("viewer", shared.ID, "weekly")
		requireStatus(t, err, http.StatusForbidden, "")
		requireUnchanged(t, shared)

		private := createSynced(t, false)
		_, err = setSchedule("viewer", private.ID, "weekly")
		requireStatus(t, err, http.StatusNotFound, "")
		weekly := "weekly"
		for _, id := range []string{private.ID, shared.ID} {
			_, err = h.UpdatePersonalCollection(ctx, PersonalCollectionUpdateCommand{UserID: otherLogin, ProfileID: "owner", CollectionID: id, Request: PersonalCollectionUpdateRequest{SyncSchedule: &weekly}})
			requireStatus(t, err, http.StatusNotFound, "")
		}
		requireUnchanged(t, private)
		requireUnchanged(t, shared)
	})
}

func equalStringPointers(a, b *string) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

func equalTimePointers(a, b *time.Time) bool {
	return (a == nil) == (b == nil) && (a == nil || a.Equal(*b))
}
