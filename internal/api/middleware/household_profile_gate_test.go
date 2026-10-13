package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// householdStore answers only ListProfiles; the embedded nil interface makes
// any other store call fail the test loudly.
type householdStore struct {
	userstore.UserStore
	profiles []userstore.Profile
	err      error
}

func (s householdStore) ListProfiles(context.Context) ([]userstore.Profile, error) {
	return s.profiles, s.err
}

type householdStores struct {
	store  userstore.UserStore
	err    error
	opened *int
}

func (p householdStores) ForUser(context.Context, int) (userstore.UserStore, error) {
	if p.opened != nil {
		*p.opened++
	}
	return p.store, p.err
}

func (householdStores) Close() error { return nil }

func TestHouseholdProfileGate(t *testing.T) {
	unlimited := []userstore.Profile{{ID: "admin", IsPrimary: true}, {ID: "guest"}}
	limited := []userstore.Profile{{ID: "admin", IsPrimary: true, PINHash: "hash"}, {ID: "kid", MaxContentRating: "TV-Y7"}}
	accessClaims := &auth.Claims{UserID: 7, Role: "user", SessionID: "s1", TokenType: auth.TokenTypeAccess}

	for _, tc := range []struct {
		name       string
		profiles   []userstore.Profile
		storeErr   error
		listErr    error
		claims     *auth.Claims
		profileID  string
		wantStatus int
		wantError  string
		wantOpened int
	}{
		{name: "unlimited household without header keeps account scope", profiles: unlimited, claims: accessClaims, wantStatus: http.StatusOK, wantOpened: 1},
		{name: "limited household without header is refused", profiles: limited, claims: accessClaims, wantStatus: http.StatusBadRequest, wantError: "bad_request", wantOpened: 1},
		{name: "limited household with header passes", profiles: limited, claims: accessClaims, profileID: "kid", wantStatus: http.StatusOK},
		{
			name: "api key without header passes", profiles: limited,
			claims:     &auth.Claims{UserID: 7, Role: "admin", TokenType: auth.TokenTypeAPIKey, APIKeyID: 3},
			wantStatus: http.StatusOK,
		},
		{
			name: "impersonated session is gated like its account", profiles: limited,
			claims:     &auth.Claims{UserID: 7, Role: "user", SessionID: "s2", TokenType: auth.TokenTypeAccess, ImpersonatorUserID: new(1)},
			wantStatus: http.StatusBadRequest, wantError: "bad_request", wantOpened: 1,
		},
		{name: "missing claims", profiles: limited, wantStatus: http.StatusUnauthorized, wantError: "unauthorized"},
		{name: "store failure fails closed", storeErr: errors.New("down"), claims: accessClaims, wantStatus: http.StatusInternalServerError, wantError: "internal_error", wantOpened: 1},
		{name: "profile listing failure fails closed", listErr: errors.New("down"), claims: accessClaims, wantStatus: http.StatusInternalServerError, wantError: "internal_error", wantOpened: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opened := 0
			gate := NewHouseholdProfileGate(householdStores{
				store:  householdStore{profiles: tc.profiles, err: tc.listErr},
				err:    tc.storeErr,
				opened: &opened,
			})
			reached := false
			handler := gate.Require(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/items/series-1", nil)
			if tc.claims != nil {
				req = req.WithContext(SetClaims(req.Context(), tc.claims))
			}
			if tc.profileID != "" {
				req.Header.Set("X-Profile-Id", tc.profileID)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if reached != (tc.wantStatus == http.StatusOK) {
				t.Fatalf("next reached = %v for status %d", reached, rec.Code)
			}
			if opened != tc.wantOpened {
				t.Fatalf("store opened %d times, want %d", opened, tc.wantOpened)
			}
			if tc.wantError == "" {
				return
			}
			var body errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body.Error != tc.wantError {
				t.Fatalf("error = %q, want %q", body.Error, tc.wantError)
			}
			if tc.wantStatus == http.StatusBadRequest && body.Message != "X-Profile-Id header is required" {
				t.Fatalf("message = %q, want the RequireProfile message", body.Message)
			}
		})
	}
}

// The refusal must be byte-identical to RequireProfile's, the shape v1
// clients already handle on playback start.
func TestHouseholdProfileGateMatchesRequireProfileDenial(t *testing.T) {
	gate := NewHouseholdProfileGate(householdStores{store: householdStore{profiles: []userstore.Profile{{ID: "kid", MaxAdvisoryAge: 7}}}})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog", nil)
	req = req.WithContext(SetClaims(req.Context(), &auth.Claims{UserID: 7, SessionID: "s1", TokenType: auth.TokenTypeAccess}))
	gated := httptest.NewRecorder()
	gate.Require(next).ServeHTTP(gated, req)
	required := httptest.NewRecorder()
	RequireProfile(next).ServeHTTP(required, req)

	if gated.Code != required.Code || gated.Body.String() != required.Body.String() ||
		gated.Header().Get("Content-Type") != required.Header().Get("Content-Type") {
		t.Fatalf("gate denial = %d %q %q, RequireProfile = %d %q %q",
			gated.Code, gated.Header().Get("Content-Type"), gated.Body.String(),
			required.Code, required.Header().Get("Content-Type"), required.Body.String())
	}
}

// Without a store the gate cannot tell a limited household from an
// unlimited one, so a request without a profile is refused, as v2's gate
// chain refuses to serve a gated operation without the gate.
func TestHouseholdProfileGateWithoutStoreFailsClosed(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	for _, gate := range []*HouseholdProfileGate{nil, NewHouseholdProfileGate(nil)} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog", nil)
		req = req.WithContext(SetClaims(req.Context(), &auth.Claims{UserID: 7, SessionID: "s1", TokenType: auth.TokenTypeAccess}))
		rec := httptest.NewRecorder()
		gate.Require(next).ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
	}
}
