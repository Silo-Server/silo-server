package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/netguard"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// TestWebhookSyncV1HouseholdAdmission: only the household manager gets past
// the top of a v1 management handler. The handler has no service here, so a
// refused request that went on would panic, and an admitted one is told apart
// by the handler's own 400 for a request with no connection ID or body.
func TestWebhookSyncV1HouseholdAdmission(t *testing.T) {
	tokens := access.NewProfileTokenService("synthetic-webhook-sync-test-signing-key", 0)
	proof, _, err := tokens.Mint(access.ProfileTokenClaims{UserID: 1, SessionID: "session-1", ProfileID: "primary", PINRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, profile, proof string
		locked, refused      bool
	}{
		{name: "primary", profile: "primary"},
		{name: "secondary", profile: "secondary", refused: true},
		{name: "profileless member", refused: true},
		{name: "locked primary missing proof", profile: "primary", locked: true, refused: true},
		{name: "locked primary verified", profile: "primary", locked: true, proof: proof},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary := userstore.Profile{ID: "primary", IsPrimary: true, PINRevision: 2}
			if tc.locked {
				primary.PINHash = "synthetic-pin-hash"
			}
			stores := discordAuthorityStores{store: discordAuthorityStore{profiles: []userstore.Profile{primary, {ID: "secondary"}}}}
			h := NewWebhookSyncHandler(nil, stores, tokens)
			for _, op := range []struct {
				name string
				call http.HandlerFunc
				// lists reach the service with nothing to validate first, so
				// only their refusal can run without one.
				list bool
			}{
				{name: "update", call: h.HandleUpdateConnection},
				{name: "create", call: h.HandleCreateConnection},
				{name: "delete", call: h.HandleDeleteConnection},
				{name: "rotate", call: h.HandleRotateWebhook},
				{name: "events", call: h.HandleListEvents},
				{name: "mappings", call: h.HandleGetProfileMappings},
				{name: "update mappings", call: h.HandleUpdateProfileMappings},
				{name: "list", call: h.HandleListConnections, list: true},
				{name: "legacy create", call: h.HandleLegacyCreateConnection},
				{name: "legacy delete", call: h.HandleLegacyDeleteConnection},
				{name: "legacy rotate", call: h.HandleLegacyRotateWebhook},
				{name: "legacy actors", call: h.HandleLegacyGetActors},
				{name: "legacy update actors", call: h.HandleLegacyUpdateActors},
				{name: "legacy list", call: h.HandleLegacyListConnections, list: true},
			} {
				if op.list && !tc.refused {
					continue
				}
				rec := httptest.NewRecorder()
				op.call(rec, householdRequest(tc.profile, false, tc.proof))
				want := http.StatusBadRequest
				if tc.refused {
					want = http.StatusForbidden
				}
				if rec.Code != want || (tc.refused && !strings.Contains(rec.Body.String(), `"error":"forbidden"`)) {
					t.Fatalf("%s: status=%d want=%d body=%s", op.name, rec.Code, want, rec.Body.String())
				}
			}
		})
	}
}

func TestWebhookManagementErrorExplainsRefusedServerAddress(t *testing.T) {
	t.Parallel()

	for cause, message := range map[error]string{
		netguard.ErrPrivateDestination: historyimport.PrivateAddressMessage,
		netguard.ErrBlockedDestination: historyimport.BlockedAddressMessage,
	} {
		var out *APIError
		if !errors.As(webhookManagementError(fmt.Errorf("creating connection: %w", cause)), &out) {
			t.Fatal("webhookManagementError did not return an APIError")
		}
		if out.Status != http.StatusBadRequest || out.Code != policyErrorBadRequest || out.Message != message {
			t.Fatalf("webhookManagementError(%v) = %+v", cause, out)
		}
	}
}
