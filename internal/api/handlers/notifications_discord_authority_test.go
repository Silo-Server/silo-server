package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type discordAuthorityStore struct {
	userstore.UserStore
	profiles []userstore.Profile
	err      error
}

func (s discordAuthorityStore) GetProfile(_ context.Context, id string) (*userstore.Profile, error) {
	if s.err != nil {
		return nil, s.err
	}
	for i := range s.profiles {
		if s.profiles[i].ID == id {
			return &s.profiles[i], nil
		}
	}
	return nil, nil
}
func (s discordAuthorityStore) ListProfiles(context.Context) ([]userstore.Profile, error) {
	return s.profiles, s.err
}

type discordAuthorityStores struct {
	userstore.UserStoreProvider
	store userstore.UserStore
	err   error
}

func (p discordAuthorityStores) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, p.err
}

func TestDiscordV1HouseholdAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, profile                         string
		admin, locked, restricted, proof, key bool
		status                                int
	}{
		{name: "member primary", profile: "primary"},
		{name: "admin primary", profile: "primary", admin: true},
		{name: "secondary", profile: "secondary", status: 403},
		{name: "admin secondary", profile: "secondary", admin: true, status: 403},
		{name: "restricted secondary", profile: "secondary", restricted: true, status: 403},
		{name: "unknown profile", profile: "foreign", status: 403},
		{name: "locked primary missing proof", profile: "primary", locked: true, status: 403},
		{name: "locked primary verified", profile: "primary", locked: true, proof: true},
		{name: "key cannot prove locked primary", profile: "primary", admin: true, key: true, locked: true, proof: true, status: 403},
		{name: "profileless member", status: 403},
		{name: "profileless unrestricted admin", admin: true},
		{name: "profileless restricted admin", admin: true, restricted: true, status: 403},
		{name: "profileless locked household admin", admin: true, locked: true, status: 403},
		{name: "profileless admin key exception", admin: true, key: true, locked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary := userstore.Profile{ID: "primary", IsPrimary: true, PINRevision: 2}
			secondary := userstore.Profile{ID: "secondary"}
			if tc.locked {
				primary.PINHash = "synthetic-pin-hash"
			}
			if tc.restricted {
				secondary.MaxContentRating = "PG"
			}
			h := NewDiscordNotificationsHandler(nil, "")
			h.Stores = discordAuthorityStores{store: discordAuthorityStore{profiles: []userstore.Profile{primary, secondary}}}
			h.ProfileTokens = access.NewProfileTokenService("synthetic-discord-test-signing-key", 0)
			token := ""
			if tc.proof {
				var err error
				token, _, err = h.ProfileTokens.Mint(access.ProfileTokenClaims{UserID: 1, SessionID: "session-1", ProfileID: "primary", PINRevision: 2})
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, op := range []struct {
				call           http.HandlerFunc
				admittedStatus int
			}{
				{h.HandleLinkInit, 409},          // Integration disabled: admission succeeds without consent.
				{h.HandleUpdatePreferences, 400}, // No preference store: no external transport exists.
				{h.HandleUnlink, 200},
			} {
				r := householdRequest(tc.profile, tc.admin, token)
				r.Body = http.NoBody
				if tc.key {
					c := *apimw.GetClaims(r.Context())
					c.TokenType = auth.TokenTypeAPIKey
					c.SessionID = ""
					r = r.WithContext(apimw.SetClaims(r.Context(), &c))
				}
				rec := httptest.NewRecorder()
				op.call(rec, r)
				want := tc.status
				if want == 0 {
					want = op.admittedStatus
				}
				if rec.Code != want {
					t.Fatalf("status=%d want=%d body=%s", rec.Code, want, rec.Body.String())
				}
				if want == 403 && !strings.Contains(rec.Body.String(), `"error":"forbidden"`) {
					t.Fatalf("v1 error changed: %s", rec.Body.String())
				}
			}
		})
	}
}

func TestDiscordHouseholdAdmissionFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stores userstore.UserStoreProvider
		status int
	}{
		{"unwired", nil, 403},
		{"nil store", discordAuthorityStores{}, 403},
		{"open failure", discordAuthorityStores{err: errors.New("synthetic store failure")}, 500},
		{"read failure", discordAuthorityStores{store: discordAuthorityStore{err: errors.New("synthetic read failure")}}, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewDiscordNotificationsHandler(nil, "")
			h.Stores = tc.stores
			for _, call := range []http.HandlerFunc{h.HandleLinkInit, h.HandleUpdatePreferences, h.HandleUnlink} {
				rec := httptest.NewRecorder()
				call(rec, householdRequest("primary", false, ""))
				if rec.Code != tc.status {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
				}
			}
		})
	}
}
