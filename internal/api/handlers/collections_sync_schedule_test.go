package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type syncScheduleTestStore struct {
	userstore.UserStore
	collection userstore.Collection
	updates    []userstore.UpdateCollectionInput
}

const syncScheduleTestCollectionType = "mdblist"

func (s *syncScheduleTestStore) GetCollection(_ context.Context, id string) (*userstore.Collection, error) {
	copy := s.collection
	copy.ID = id
	return &copy, nil
}

func (s *syncScheduleTestStore) UpdateCollection(_ context.Context, input userstore.UpdateCollectionInput) error {
	s.updates = append(s.updates, input)
	if input.ClearSyncSchedule {
		s.collection.SyncSchedule = nil
	} else if input.SyncSchedule != nil {
		value := *input.SyncSchedule
		s.collection.SyncSchedule = &value
	}
	if input.ClearNextSyncAt {
		s.collection.NextSyncAt = nil
	} else if input.NextSyncAt != nil {
		s.collection.NextSyncAt = input.NextSyncAt
	}
	return nil
}

type syncScheduleTestProvider struct {
	store userstore.UserStore
}

func (p syncScheduleTestProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (p syncScheduleTestProvider) Close() error { return nil }

func TestUpdatePersonalCollectionSyncSchedule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		role           string
		schedule       string
		collectionType string
		wantStatus     int
		wantSchedule   *string
		wantCleared    bool
	}{
		{
			name:           "server admin sets server collection preset",
			role:           "admin",
			schedule:       "0 */6 * * *",
			collectionType: syncScheduleTestCollectionType,
			wantStatus:     http.StatusOK,
			wantSchedule:   stringPointer("0 */6 * * *"),
		},
		{
			name:           "regular account changes bounded cadence",
			role:           "user",
			schedule:       "weekly",
			collectionType: syncScheduleTestCollectionType,
			wantStatus:     http.StatusOK,
			wantSchedule:   stringPointer("0 3 * * 0"),
		},
		{
			name:           "regular account cannot set cron",
			role:           "user",
			schedule:       "0 * * * *",
			collectionType: syncScheduleTestCollectionType,
			wantStatus:     http.StatusBadRequest,
		},
		{
			name:           "automatic sync can be disabled",
			role:           "user",
			schedule:       "",
			collectionType: syncScheduleTestCollectionType,
			wantStatus:     http.StatusOK,
			wantCleared:    true,
		},
		{
			name:           "manual collection rejects schedule",
			role:           "admin",
			schedule:       "0 * * * *",
			collectionType: "manual",
			wantStatus:     http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := &syncScheduleTestStore{collection: userstore.Collection{
				ID:                "collection-1",
				CreatorProfileID:  "profile-1",
				ProfileID:         "profile-1",
				AllowedProfileIDs: []string{"profile-1"},
				Name:              "Imported list",
				CollectionType:    tt.collectionType,
				QueryDefinition:   "{}",
				SortConfig:        "{}",
				SourceConfig:      "{}",
			}}
			handler := NewCollectionHandler(syncScheduleTestProvider{store: store})
			ctx := apimw.SetClaims(t.Context(), &auth.Claims{UserID: 7, Role: tt.role})
			_, err := handler.UpdatePersonalCollection(ctx, PersonalCollectionUpdateCommand{
				UserID:       7,
				ProfileID:    "profile-1",
				CollectionID: "collection-1",
				Request:      PersonalCollectionUpdateRequest{SyncSchedule: &tt.schedule},
			})
			gotStatus := http.StatusOK
			if err != nil {
				var apiErr *APIError
				if !errors.As(err, &apiErr) {
					t.Fatalf("unexpected error: %v", err)
				}
				gotStatus = apiErr.Status
			}
			if gotStatus != tt.wantStatus {
				t.Fatalf("status = %d, error = %v, want %d", gotStatus, err, tt.wantStatus)
			}
			if tt.wantStatus != http.StatusOK {
				if len(store.updates) != 0 {
					t.Fatalf("rejected request wrote %d updates", len(store.updates))
				}
				return
			}
			if len(store.updates) != 1 {
				t.Fatalf("updates = %d, want 1", len(store.updates))
			}
			got := store.updates[0]
			if got.SourceConfig != nil {
				t.Fatal("schedule-only update unexpectedly rewrote source_config")
			}
			if tt.wantCleared {
				if !got.ClearSyncSchedule || !got.ClearNextSyncAt {
					t.Fatalf("clear flags = schedule:%v next:%v", got.ClearSyncSchedule, got.ClearNextSyncAt)
				}
				return
			}
			if got.SyncSchedule == nil || *got.SyncSchedule != *tt.wantSchedule {
				t.Fatalf("sync schedule = %v, want %q", got.SyncSchedule, *tt.wantSchedule)
			}
			if got.NextSyncAt == nil {
				t.Fatal("next_sync_at was not reset for the new schedule")
			}
		})
	}
}

func TestV1UpdateDoesNotActivateSyncScheduleContract(t *testing.T) {
	store := &syncScheduleTestStore{collection: userstore.Collection{
		ID: "collection-1", CreatorProfileID: "profile-1", ProfileID: "profile-1",
		AllowedProfileIDs: []string{"profile-1"}, Name: "Imported list", CollectionType: syncScheduleTestCollectionType,
		QueryDefinition: "{}", SortConfig: "{}", SourceConfig: "{}",
	}}
	handler := NewCollectionHandler(syncScheduleTestProvider{store: store})
	req := httptest.NewRequest(http.MethodPut, "/collections/collection-1", strings.NewReader(`{"sync_schedule":"0 */6 * * *"}`))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", "collection-1")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: 7, Role: "admin"})
	ctx = apimw.SetProfileID(ctx, "profile-1")
	rec := httptest.NewRecorder()

	handler.HandleUpdateCollection(rec, req.WithContext(ctx))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(store.updates) != 1 || store.updates[0].SyncSchedule != nil || store.updates[0].ClearSyncSchedule {
		t.Fatalf("v1 schedule field became active: %+v", store.updates)
	}
}

func stringPointer(value string) *string { return &value }
