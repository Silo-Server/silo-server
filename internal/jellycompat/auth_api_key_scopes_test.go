package jellycompat

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// A scoped API key is an allowlist credential for the versioned API; the
// compat surface is never on its allowlist, so even a key owned by an admin
// must be refused before a session is synthesized.
func TestScopedAPIKeyIsRefusedOnCompatSurface(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	validator := &fakeAPIKeyValidator{key: &models.APIKey{
		ID:     1,
		UserID: 2,
		Key:    "sa_test",
		Scopes: []string{auth.ScopeAdminUsers},
	}}
	users := &fakeAPIKeyUserLoader{user: &models.User{ID: 2, Username: "admin", Role: "admin", Enabled: true}}
	provider := &fakeUserStoreProvider{store: &fakeUserStore{profiles: []userstore.Profile{
		{ID: "p1", Name: "Parent", IsPrimary: true},
	}}}
	keyAuth := NewAdminAPIKeyAuthenticator(validator, users, provider, clock)
	sessionAuth := &Authenticator{sessions: NewSessionStore(time.Hour, clock)}

	h := RequireSessionOrAPIKeySession(sessionAuth, keyAuth)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run for a scoped key")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, apiKeyRequest())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("scoped key on compat surface: status = %d, want 403", rec.Code)
	}

	adminH := RequireSessionOrAdminAPIKey(sessionAuth, keyAuth)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run for a scoped key")
	}))
	rec = httptest.NewRecorder()
	adminH.ServeHTTP(rec, apiKeyRequest())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("scoped key on compat admin surface: status = %d, want 403", rec.Code)
	}
}
