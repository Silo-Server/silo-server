package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// householdGateStore serves the profile reads viewer resolution and the
// household gate make; any other store call hits the nil embedded interface.
type householdGateStore struct {
	userstore.UserStore
	profiles []userstore.Profile
}

func (s householdGateStore) GetProfile(_ context.Context, id string) (*userstore.Profile, error) {
	for i := range s.profiles {
		if s.profiles[i].ID == id {
			return &s.profiles[i], nil
		}
	}
	return nil, nil
}

func (s householdGateStore) ListProfiles(context.Context) ([]userstore.Profile, error) {
	return s.profiles, nil
}

func (householdGateStore) ListSettingValuesForResolution(context.Context, userstore.SettingResolutionQuery) ([]userstore.SettingValue, error) {
	return nil, nil
}

type householdGateStores struct{ store userstore.UserStore }

func (p householdGateStores) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (householdGateStores) Close() error { return nil }

// matureItemAccess hides the mature series from any filter that carries a
// rating ceiling, the way the catalog hides a TV-MA title from a TV-Y7
// profile.
type matureItemAccess struct{}

func (matureItemAccess) EnsureAccessible(_ context.Context, contentID string, filter catalog.AccessFilter) error {
	if contentID == "series-mature" && access.HasCeiling(filter.MaxContentRating) {
		return catalog.ErrItemNotFound
	}
	return nil
}

// TestV1ViewerReadWithoutProfileHeader drives a frozen v1 viewer read
// (GET /api/v1/markers/items/{id}) through the chain the router mounts:
// RequireViewerAccess with the policy resolver, then the household gate.
// Omitting X-Profile-Id must not unlock what the child profile cannot see.
func TestV1ViewerReadWithoutProfileHeader(t *testing.T) {
	engine, err := policy.NewEngine(context.Background())
	if err != nil {
		t.Fatalf("NewEngine() error: %v", err)
	}
	user := &models.User{ID: 7, Role: "user", Enabled: true}
	parent := userstore.Profile{ID: "parent", IsPrimary: true, PINHash: "hash"}
	kid := userstore.Profile{ID: "kid", MaxContentRating: "TV-Y7"}
	guest := userstore.Profile{ID: "guest"}

	file := &models.MediaFile{ID: 11, EpisodeID: "episode-mature", Duration: 1800}
	files := fakeMarkerFiles{byID: map[int]*models.MediaFile{11: file}, episodeFiles: []*models.MediaFile{file}}
	markersHandler := NewMarkersHandler(files, nil, nil, nil, nil, nil)
	markersHandler.Authorizer = &MediaFileAuthorizer{
		FileResolver:  files,
		ItemAccess:    matureItemAccess{},
		EpisodeLookup: fakeMarkerEpisodeLookup{"episode-mature": {ContentID: "episode-mature", SeriesID: "series-mature"}},
	}

	route := func(profiles []userstore.Profile) http.Handler {
		stores := householdGateStores{store: householdGateStore{profiles: profiles}}
		viewer := apimw.NewViewerAccessMiddleware(policy.NewViewerResolver(fakeMarkerUsers{user.ID: user}, stores, nil, policy.NewPDP(engine)))
		router := chi.NewRouter()
		router.With(viewer.RequireViewerAccess, apimw.NewHouseholdProfileGate(stores).Require).
			Get("/api/v1/markers/items/{id}", markersHandler.HandleGetItemMarkers)
		return router
	}

	for _, tc := range []struct {
		name       string
		profiles   []userstore.Profile
		tokenType  string
		profileID  string
		wantStatus int
	}{
		{"child profile cannot see the mature series", []userstore.Profile{parent, kid}, auth.TokenTypeAccess, "kid", http.StatusNotFound},
		{"omitting the header no longer unlocks it", []userstore.Profile{parent, kid}, auth.TokenTypeAccess, "", http.StatusBadRequest},
		{"PIN-locked parent still needs its token", []userstore.Profile{parent, kid}, auth.TokenTypeAccess, "parent", http.StatusForbidden},
		{"unlimited household keeps account scope", []userstore.Profile{{ID: "parent", IsPrimary: true}, guest}, auth.TokenTypeAccess, "", http.StatusOK},
		{"API key keeps profile-less access", []userstore.Profile{parent, kid}, auth.TokenTypeAPIKey, "", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/markers/items/episode-mature", nil)
			req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: user.ID, Role: user.Role, SessionID: "session-1", TokenType: tc.tokenType}))
			if tc.profileID != "" {
				req.Header.Set("X-Profile-Id", tc.profileID)
			}
			rec := httptest.NewRecorder()
			route(tc.profiles).ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus == http.StatusBadRequest {
				const want = `{"error":"bad_request","message":"X-Profile-Id header is required"}` + "\n"
				if rec.Body.String() != want {
					t.Fatalf("body = %q, want the v1 RequireProfile shape %q", rec.Body.String(), want)
				}
			}
		})
	}
}

// TestV1PeopleRoutesWithoutProfileHeader drives the v1 people routes through
// the chain the router mounts on them: RequireViewerAccess, then the household
// gate. A device on a restricted household cannot read or refresh people at
// account scope by omitting X-Profile-Id.
func TestV1PeopleRoutesWithoutProfileHeader(t *testing.T) {
	engine, err := policy.NewEngine(context.Background())
	if err != nil {
		t.Fatalf("NewEngine() error: %v", err)
	}
	user := &models.User{ID: 7, Role: "user", Enabled: true}
	restricted := []userstore.Profile{{ID: "parent", IsPrimary: true, PINHash: "hash"}, {ID: "kid", MaxContentRating: "TV-Y7"}}
	unrestricted := []userstore.Profile{{ID: "parent", IsPrimary: true}, {ID: "guest"}}

	route := func(profiles []userstore.Profile) http.Handler {
		stores := householdGateStores{store: householdGateStore{profiles: profiles}}
		viewer := apimw.NewViewerAccessMiddleware(policy.NewViewerResolver(fakeMarkerUsers{user.ID: user}, stores, nil, policy.NewPDP(engine)))
		people := &PeopleHandler{
			personRepo:     &adminPeopleRepo{person: models.Person{ID: 42, Name: "Person"}},
			itemsHandler:   &ItemsHandler{},
			refreshQueue:   &recordingPersonRefreshQueue{},
			refreshLimiter: ratelimit.NewMemoryLimiter(),
		}
		router := chi.NewRouter()
		router.Group(func(r chi.Router) {
			r.Use(viewer.RequireViewerAccess, apimw.NewHouseholdProfileGate(stores).Require)
			r.Get("/api/v1/people", people.HandleSearch)
			r.Get("/api/v1/people/{id}", people.HandleGetPerson)
			r.Post("/api/v1/people/{id}/refresh", people.HandleRefreshPerson)
		})
		return router
	}

	for _, endpoint := range []struct {
		method string
		path   string
		passed int
	}{
		{http.MethodGet, "/api/v1/people?q=person", http.StatusOK},
		{http.MethodGet, "/api/v1/people/42", http.StatusOK},
		{http.MethodPost, "/api/v1/people/42/refresh", http.StatusAccepted},
	} {
		for _, tc := range []struct {
			name       string
			profiles   []userstore.Profile
			profileID  string
			wantStatus int
		}{
			{"restricted household without header is refused", restricted, "", http.StatusBadRequest},
			{"restricted household with header passes", restricted, "kid", endpoint.passed},
			{"unrestricted household keeps account scope", unrestricted, "", endpoint.passed},
		} {
			t.Run(endpoint.method+" "+endpoint.path+"/"+tc.name, func(t *testing.T) {
				req := httptest.NewRequest(endpoint.method, endpoint.path, nil)
				req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: user.ID, Role: user.Role, SessionID: "session-1", TokenType: auth.TokenTypeAccess}))
				if tc.profileID != "" {
					req.Header.Set("X-Profile-Id", tc.profileID)
				}
				rec := httptest.NewRecorder()
				route(tc.profiles).ServeHTTP(rec, req)

				if rec.Code != tc.wantStatus {
					t.Fatalf("status = %d, want %d; body = %s", rec.Code, tc.wantStatus, rec.Body.String())
				}
				if tc.wantStatus == http.StatusBadRequest {
					const want = `{"error":"bad_request","message":"X-Profile-Id header is required"}` + "\n"
					if rec.Body.String() != want {
						t.Fatalf("body = %q, want the v1 RequireProfile shape %q", rec.Body.String(), want)
					}
				}
			})
		}
	}
}
