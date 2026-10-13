package chatgpt

import (
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"testing"
)

func TestLoginWithoutPlanConsentBlocksInferenceAndRevokesAcceptedSession(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := newIdentityRetryFixture(t, key)
	f.tokens.Scope = "openid profile email"
	f.transport(t, nil)
	loginTransport := f.service.http.Transport
	revocations := 0
	f.service.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/revoke" {
			return loginTransport.RoundTrip(r)
		}
		revocations++
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("token") != f.tokens.RefreshToken || r.Form.Get(oauthClientID) != "issued-app" || r.Form.Get("token_type_hint") != oauthRefreshToken {
			t.Fatal("disconnect did not revoke the verified session issued without plan consent")
		}
		return response(http.StatusOK, nil), nil
	})
	if _, err := f.service.CompleteLoginWithSettings(t.Context(), f.callback); !errors.Is(err, ErrIdentity) {
		t.Fatalf("missing consent completion: %v", err)
	}
	status, err := f.service.Status(t.Context())
	if err != nil || len(status.Accounts) != 2 || status.Accounts[1].Connected || !status.Accounts[1].NeedsLogin {
		t.Fatalf("missing consent status: %+v, %v", status, err)
	}
	if token, err := f.service.bearerToken(t.Context(), "issued-app", false); token != "" || !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("disabled registration supplied inference credentials: %q, %v", token, err)
	}
	if err := f.service.SelectAccount(t.Context(), "issued-app"); !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("disabled registration selected: %v", err)
	}
	if _, err := f.service.Models(t.Context(), "issued-app"); !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("disabled registration authorized model lookup: %v", err)
	}
	if len(f.calls["/api/accounts/oauth/token"]) != 1 || len(f.calls["/v1/models"]) != 0 {
		t.Fatal("disabled registration called the provider for inference or refresh")
	}
	revoked, err := f.service.Disconnect(t.Context(), "issued-app")
	if err != nil || !revoked || revocations != 1 {
		t.Fatalf("missing consent disconnect: revoked=%t error=%v revocations=%d", revoked, err, revocations)
	}
	c := stored(t, f.store)
	a := c.Accounts[1]
	if c.Active != f.prior.ClientID || c.Accounts[0] != f.prior || a.AccessToken != "" || a.RefreshToken != "" || a.IDToken != "" || a.Scopes != "" || !a.ExpiresAt.IsZero() || !a.NeedsLogin || a.DisconnectID != "" {
		t.Fatal("disconnect failed to remove disabled credentials or changed the active account")
	}
}
