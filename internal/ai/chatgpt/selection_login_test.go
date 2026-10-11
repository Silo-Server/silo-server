package chatgpt

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"testing"
	"testing/synctest"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestLoginActivationReconcilesModelWithoutLosingAcceptedTokens(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, clientID, authMode string
		status                   int
		empty, timeout           bool
		wantActive, wantModel    string
	}{
		{name: "new account", clientID: "b", authMode: "chatgpt", status: 200, wantActive: "b", wantModel: "gpt-4o-mini"},
		{name: "failed new account catalog", clientID: "b", authMode: "chatgpt", status: 503, wantActive: "a", wantModel: "a-model"},
		{name: "catalog timeout preserves accepted tokens", clientID: "b", authMode: "chatgpt", status: 503, timeout: true, wantActive: "a", wantModel: "a-model"},
		{name: "failed returning account catalog", clientID: "a", authMode: "chatgpt", status: 503, wantModel: "a-model"},
		{name: "empty returning account catalog", clientID: "a", authMode: "chatgpt", status: 200, empty: true, wantModel: "a-model"},
		{name: "API key model retained", clientID: "b", authMode: "api_key", status: 200, wantActive: "b", wantModel: "a-model"},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, store := selectionFixture(t, map[string]string{"ai.auth_mode": test.authMode, "ai.chat_model": "a-model", "ai.reasoning_effort": "high"})
				if test.timeout {
					s.http.Timeout = 0
				}
				login, err := s.StartLogin(t.Context(), test.clientID)
				if err != nil {
					t.Fatal(err)
				}
				pending := stored(t, store).Pending
				claims := identityClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: issuer, Subject: "user", Audience: jwt.ClaimStrings{test.clientID}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}, Nonce: pending.Nonce, Email: "user@example.test"}
				token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
				token.Header["kid"] = "test-key"
				signed, err := token.SignedString(key)
				if err != nil {
					t.Fatal(err)
				}
				modelsCalls := 0
				s.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
					switch r.URL.Path {
					case "/api/accounts/oauth/token":
						return response(200, tokenResponse{AccessToken: "accepted-access", RefreshToken: "accepted-refresh", IDToken: signed, Scope: planScope, TokenType: "Bearer", ExpiresIn: 3600}), nil
					case "/.well-known/openid-configuration":
						return response(200, map[string]string{"jwks_uri": "https://auth.openai.com/jwks", "revocation_endpoint": "https://auth.openai.com/revoke"}), nil
					case "/jwks":
						return response(200, map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "test-key", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}}), nil
					case "/v1/models":
						if test.timeout {
							<-r.Context().Done()
							return nil, r.Context().Err()
						}
						modelsCalls++
						if r.Header.Get("Authorization") != "Bearer accepted-access" {
							t.Fatal("login catalog used previous credentials")
						}
						models := []map[string]string{}
						if !test.empty {
							models = append(models, map[string]string{"slug": "gpt-4o-mini", "visibility": "list"})
						}
						return response(test.status, map[string]any{"models": models}), nil
					default:
						t.Fatalf("unexpected provider path %s", r.URL.Path)
						return nil, errors.New("unexpected path")
					}
				})
				callback := login.CallbackURI + "?state=" + url.QueryEscape(login.AttemptID) + "&code=code&client_id=" + test.clientID
				err = s.CompleteLogin(t.Context(), callback)
				failed := test.status != 200 || test.empty
				if (err != nil) != failed {
					t.Fatalf("completion error=%v, want failed=%t", err, failed)
				}
				c := stored(t, store)
				if c.Active != test.wantActive || store.values["ai.chat_model"] != test.wantModel {
					t.Fatalf("activation=%q, model=%q", c.Active, store.values["ai.chat_model"])
				}
				for _, a := range c.Accounts {
					if a.ClientID == test.clientID && (a.RefreshToken != "accepted-refresh" || a.NeedsLogin) {
						t.Fatal("accepted credentials were lost after catalog reconciliation")
					}
				}
				if test.authMode == "api_key" && modelsCalls != 0 {
					t.Fatal("API key login required a subscription catalog")
				}
				if failed && test.clientID == "a" {
					if _, err := s.TextConfig(t.Context()); !errors.Is(err, ErrSignInRequired) {
						t.Fatalf("failed reconnect allowed new inference: %v", err)
					}
				}
			})
		})
	}
}
