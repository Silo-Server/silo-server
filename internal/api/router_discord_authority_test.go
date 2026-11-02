package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type discordWiringStore struct{ userstore.UserStore }

func (discordWiringStore) GetProfile(_ context.Context, id string) (*userstore.Profile, error) {
	return &userstore.Profile{ID: id, IsPrimary: id == "parent", PINHash: "synthetic-hash", PINRevision: 2}, nil
}

type discordWiringStores struct{ userstore.UserStoreProvider }

func (discordWiringStores) ForUser(context.Context, int) (userstore.UserStore, error) {
	return discordWiringStore{}, nil
}

func TestDiscordNotificationConstructorWiresHouseholdAuthority(t *testing.T) {
	tokens := access.NewProfileTokenService("synthetic-discord-wiring-signing-key", 0)
	h := newDiscordNotificationsHandler(Dependencies{UserStoreProvider: discordWiringStores{}}, tokens)
	if h.Stores == nil || h.ProfileTokens != tokens {
		t.Fatal("household dependencies missing from shared v1/v2 constructor")
	}
	proof, _, err := tokens.Mint(access.ProfileTokenClaims{UserID: 1, SessionID: "s1", ProfileID: "parent", PINRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		profile, proof string
		status         int
	}{{"parent", proof, 200}, {"parent", "", 403}, {"member", proof, 403}} {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/notifications/discord-link", nil)
		ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, Role: "admin", SessionID: "s1"})
		req = req.WithContext(ctx)
		req.Header.Set("X-Profile-Id", tc.profile)
		req.Header.Set("X-Profile-Token", tc.proof)
		rec := httptest.NewRecorder()
		h.HandleUnlink(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("profile %s status=%d body=%s", tc.profile, rec.Code, rec.Body.String())
		}
	}
}
