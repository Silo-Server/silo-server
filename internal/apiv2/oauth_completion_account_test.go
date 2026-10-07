package apiv2

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type completionAccountOpener struct {
	handshakeCompleter
	user *models.User
}

func (f *completionAccountOpener) OpenOAuthSession(context.Context, auth.OAuthSessionDB, auth.OAuthCompletion) (*auth.TokenPair, error) {
	return &auth.TokenPair{AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", ExpiresIn: 60, SessionID: "s1", User: f.user}, nil
}

type completionAccountViews struct {
	fakeAccounts
	reads     int
	projected *models.User
}

func (f *completionAccountViews) CurrentUser(context.Context, *auth.Claims) (handlers.UserView, error) {
	f.reads++
	return handlers.UserView{}, errStore
}

func (f *completionAccountViews) OAuthUserView(_ context.Context, user *models.User) handlers.UserView {
	f.projected = user
	return handlers.UserView{ID: user.ID, Username: user.Username, Email: user.Email, Role: user.Role, Permissions: append([]string{}, user.Permissions...), PasswordChangeRequired: user.PasswordChangeRequired}
}

func TestOAuthCompletionUsesAccountReadAtRedemption(t *testing.T) {
	store := auth.NewInMemoryOAuthStore()
	user := &models.User{ID: 1, Username: "transaction-user", Email: "transaction@example.test", Role: "user", Enabled: true, PasswordChangeRequired: true}
	accounts := &completionAccountViews{}
	var revoked []string
	svc := auth.NewOAuthHandler(auth.OAuthHandlerDeps{
		Store: store, LoginCompleter: &completionAccountOpener{user: user},
		RevokeSession: func(_ context.Context, id string) error { revoked = append(revoked, id); return nil },
	})
	challenge := sha256.Sum256([]byte(fixtureNativeVerifier))
	if err := store.InsertCompletion(t.Context(), auth.OAuthCompletion{
		Code: "transaction-code", UserID: user.ID, Kind: auth.OAuthFlowNative,
		CodeChallenge: base64.RawURLEncoding.EncodeToString(challenge[:]), ExpiresAt: time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	deps := pilotDeps(nil, nil)
	deps.OAuth, deps.Accounts = svc, accounts
	h := newTestHandler(t, deps)
	body := `{"code":"transaction-code","code_verifier":"` + fixtureNativeVerifier + `"}`
	rec := do(t, h, http.MethodPost, Prefix+"/auth/oauth/complete", body, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"username":"transaction-user"`) || !strings.Contains(rec.Body.String(), `"password_change_required":true`) || !strings.Contains(rec.Body.String(), `"access_token":"synthetic-access"`) {
		t.Fatalf("completion: %d %s", rec.Code, rec.Body.String())
	}
	if accounts.reads != 0 || accounts.projected != user {
		t.Fatalf("account reads = %d, projected = %+v", accounts.reads, accounts.projected)
	}
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/auth/oauth/complete", body, nil), TypeInvalidToken)
	if len(revoked) != 1 || revoked[0] != "s1" {
		t.Fatalf("reuse revoked = %v", revoked)
	}
}
