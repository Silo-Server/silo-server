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
		name, clientID, authMode                               string
		status                                                 int
		empty, timeout, omitScope, usagePaused, first, recover bool
		wantActive, wantModel                                  string
	}{
		{name: "renewed credentials preserve usage pause", clientID: "a", authMode: "chatgpt", status: 200, usagePaused: true, wantActive: "a", wantModel: "gpt-4o-mini"},
		{name: "new account", clientID: "b", authMode: "chatgpt", status: 200, wantActive: "b", wantModel: "gpt-4o-mini"},
		{name: "first activation", clientID: "b", authMode: "chatgpt", status: 200, first: true, wantActive: "b", wantModel: "gpt-4o-mini"},
		{name: "first activation recovers temporary catalog failure", clientID: "b", authMode: "chatgpt", status: 503, first: true, recover: true, wantActive: "b", wantModel: "gpt-4o-mini"},
		{name: "failed first catalog", clientID: "b", authMode: "chatgpt", status: 503, first: true, wantModel: "a-model"},
		{name: "omitted scope activates the requested plan grant", clientID: "b", authMode: "chatgpt", status: 200, omitScope: true, wantActive: "b", wantModel: "gpt-4o-mini"},
		{name: "failed new account catalog", clientID: "b", authMode: "chatgpt", status: 503, wantActive: "a", wantModel: "a-model"},
		{name: "catalog timeout preserves accepted tokens", clientID: "b", authMode: "chatgpt", status: 503, timeout: true, wantActive: "a", wantModel: "a-model"},
		{name: "failed returning account catalog", clientID: "a", authMode: "chatgpt", status: 503, wantModel: "a-model"},
		{name: "empty returning account catalog", clientID: "a", authMode: "chatgpt", status: 200, empty: true, wantModel: "a-model"},
		{name: "API key model retained", clientID: "b", authMode: "api_key", status: 200, wantActive: "b", wantModel: "a-model"},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, store := selectionFixture(t, map[string]string{"ai.auth_mode": test.authMode, "ai.chat_model": "a-model", "ai.reasoning_effort": "high"})
				if test.first {
					c := stored(t, store)
					save(t, store, credentials{Accounts: c.Accounts})
				}
				if test.usagePaused {
					c := stored(t, store)
					c.Accounts[0].UsageLimited = true
					c.Accounts[0].UsagePauseID = "original-pause"
					c.Accounts[0].NeedsLogin = true
					save(t, store, c)
				}
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
						if test.omitScope {
							return response(200, map[string]any{"access_token": "accepted-access", "refresh_token": "accepted-refresh", "id_token": signed, "token_type": "Bearer", "expires_in": 3600}), nil
						}
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
						status := test.status
						if test.recover && modelsCalls > 1 {
							status = http.StatusOK
						}
						return response(status, map[string]any{"models": models}), nil
					default:
						t.Fatalf("unexpected provider path %s", r.URL.Path)
						return nil, errors.New("unexpected path")
					}
				})
				callback := login.CallbackURI + "?state=" + url.QueryEscape(login.AttemptID) + "&code=code&client_id=" + test.clientID
				receipt, err := s.CompleteLoginWithSettings(t.Context(), callback)
				failed := (test.status != 200 && !test.recover) || test.empty
				if failed && receipt != nil {
					t.Fatal("failed activation returned settings receipt")
				}
				if !failed && (receipt == nil || receipt.Before["ai.chat_model"] != "a-model" || receipt.After["ai.chat_model"] != test.wantModel) {
					t.Fatal("completion lost transaction-owned settings receipt")
				}
				if (err != nil) != failed {
					t.Fatalf("completion error=%v, want failed=%t", err, failed)
				}
				c := stored(t, store)
				if c.Active != test.wantActive || store.values["ai.chat_model"] != test.wantModel {
					t.Fatalf("activation=%q, model=%q", c.Active, store.values["ai.chat_model"])
				}
				requireConnectionHistory(t, store, !test.first || !failed)
				state, err := NewService(store).Status(t.Context())
				if err != nil || state.LoginFirstConnection != (test.first && !failed) || (receipt != nil && receipt.FirstConnection != (test.first && !failed)) {
					t.Fatalf("first activation metadata: status=%+v, receipt=%+v, error=%v", state, receipt, err)
				}
				for _, a := range c.Accounts {
					if a.ClientID == test.clientID && (a.RefreshToken != "accepted-refresh" || a.NeedsLogin) {
						t.Fatal("accepted credentials were lost after catalog reconciliation")
					}
				}
				if test.usagePaused {
					if !c.Accounts[0].UsageLimited || c.Accounts[0].UsagePauseID != "original-pause" {
						t.Fatal("credential renewal cleared usage pause")
					}
					if _, err := s.TextConfig(t.Context()); !errors.Is(err, ErrUsageLimited) {
						t.Fatalf("renewal resumed inference: %v", err)
					}
					if _, err := s.CheckTextConfig(t.Context()); err != nil {
						t.Fatalf("renewed account cannot recheck usage: %v", err)
					}
				}
				if test.authMode == "api_key" && modelsCalls != 0 {
					t.Fatal("API key login required a subscription catalog")
				}
				if test.omitScope && modelsCalls != 1 {
					t.Fatal("omitted scope bypassed plan catalog reconciliation")
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
