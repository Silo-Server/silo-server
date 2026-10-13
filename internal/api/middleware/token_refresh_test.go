package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// An access token carries the role it was minted with, and the admin gates
// trust it. When an admin changes the account's role the session stays valid,
// so RequireAuth compares the token's role with the account's current one and
// refuses a stale token with a reason that tells the client to refresh rather
// than sign out.
func TestRequireAuthRefusesTokensMintedBeforeARoleChange(t *testing.T) {
	impersonator := 7
	tests := []struct {
		name        string
		claims      auth.Claims
		currentRole string
		active      bool
		wantStatus  int
		wantReason  string
	}{
		{name: "role unchanged", claims: auth.Claims{UserID: 42, Role: models.RoleAdmin, SessionID: "sess", TokenType: auth.TokenTypeAccess}, currentRole: models.RoleAdmin, active: true, wantStatus: http.StatusNoContent},
		{name: "demoted admin", claims: auth.Claims{UserID: 42, Role: models.RoleAdmin, SessionID: "sess", TokenType: auth.TokenTypeAccess}, currentRole: models.RoleUser, active: true, wantStatus: http.StatusUnauthorized, wantReason: ReasonTokenRefreshRequired},
		{name: "promoted user", claims: auth.Claims{UserID: 42, Role: models.RoleUser, SessionID: "sess", TokenType: auth.TokenTypeAccess}, currentRole: models.RoleAdmin, active: true, wantStatus: http.StatusUnauthorized, wantReason: ReasonTokenRefreshRequired},
		// An impersonation token carries the role of the account being viewed
		// as, which is the account the session belongs to.
		{name: "impersonation with the viewed account's role", claims: auth.Claims{UserID: 42, Role: models.RoleUser, SessionID: "sess", TokenType: auth.TokenTypeAccess, ImpersonatorUserID: &impersonator}, currentRole: models.RoleUser, active: true, wantStatus: http.StatusNoContent},
		{name: "revoked session", claims: auth.Claims{UserID: 42, Role: models.RoleAdmin, SessionID: "sess", TokenType: auth.TokenTypeAccess}, wantStatus: http.StatusUnauthorized, wantReason: ReasonSessionInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessions := &fakeSessionValidator{roles: map[string]string{}}
			if tt.active {
				sessions.roles["sess"] = tt.currentRole
			}
			am := NewAuthMiddleware(claimsValidator{tt.claims}, sessions, nil, nil)
			h := am.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
			req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
			req.Header.Set("Authorization", "Bearer token")
			rec := newReasonWriter()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if rec.reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", rec.reason, tt.wantReason)
			}
			if tt.wantStatus == http.StatusUnauthorized {
				// The v1 body stays {"error":"unauthorized"}: v1 clients
				// already refresh on any 401.
				if got := decodeDenial(t, rec); got.Error != "unauthorized" {
					t.Fatalf("error = %q, want unauthorized", got.Error)
				}
			}
		})
	}
}
