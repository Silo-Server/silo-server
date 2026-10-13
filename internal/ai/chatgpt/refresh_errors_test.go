package chatgpt

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRefreshTerminalOAuthErrorsRequireSignIn(t *testing.T) {
	for _, code := range []string{oauthInvalidGrant, "invalid_refresh_token", "token_expired", "refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused"} {
		for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized} {
			t.Run(fmt.Sprintf("%s/%d", code, status), func(t *testing.T) {
				store := &memoryStore{values: map[string]string{}}
				save(t, store, credentials{Active: "app", Accounts: []account{{ClientID: "app", Subject: "verified", IDToken: "validated-hint", RefreshToken: "old-refresh", Scopes: planScope}}})
				service := NewService(store)
				calls := 0
				service.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					_ = r.ParseForm()
					if r.Form.Get("grant_type") != oauthRefreshToken {
						t.Fatal("expected refresh grant")
					}
					return response(status, map[string]string{"error": code}), nil
				})
				_, err := service.BearerToken(t.Context())
				if !errors.Is(err, ErrSignInRequired) || errors.Is(err, errInvalidAuthorizationGrant) {
					t.Fatalf("terminal refresh classification: %v", err)
				}
				c := stored(t, store)
				if !c.Accounts[0].NeedsLogin || c.Accounts[0].ClientID != "app" || c.Accounts[0].Subject != "verified" || c.Accounts[0].IDToken != "validated-hint" || c.Active != "app" {
					t.Fatal("terminal error lost registration or failed to require sign-in")
				}
				if _, err := service.BearerToken(t.Context()); !errors.Is(err, ErrSignInRequired) || calls != 1 {
					t.Fatal("terminal refresh attempted unusable credentials again")
				}
			})
		}
	}
}

func TestLateUnauthorizedPreservesTerminalRefreshState(t *testing.T) {
	store := &memoryStore{values: map[string]string{}}
	save(t, store, credentials{Active: "app", Accounts: []account{{ClientID: "app", Subject: "verified", IDToken: "validated-hint", AccessToken: "in-flight-access", RefreshToken: "old-refresh", Scopes: planScope, ExpiresAt: time.Now().Add(-time.Minute)}}})
	service := NewService(store)
	calls := 0
	service.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return response(http.StatusBadRequest, map[string]string{"error": oauthInvalidGrant}), nil
	})
	if _, err := service.BearerToken(t.Context()); !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("terminal refresh did not require sign-in: %v", err)
	}
	// Another node receives the first 401 for a token sent before that refresh.
	if err := NewService(store).RejectToken(t.Context(), "in-flight-access", false); err != nil {
		t.Fatal(err)
	}
	state, err := service.Status(t.Context())
	if err != nil || !state.Accounts[0].NeedsLogin || state.Accounts[0].Connected {
		t.Fatalf("delayed 401 restored terminal credentials: %+v, %v", state, err)
	}
	if _, err := service.BearerToken(t.Context()); !errors.Is(err, ErrSignInRequired) || calls != 1 {
		t.Fatalf("delayed 401 retried a terminal refresh token: %v, calls=%d", err, calls)
	}
}

func TestNonterminalRefreshErrorsPreserveCredentialsAndRecover(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		failures   int
	}{
		{"temporary400", `{"error":"temporarily_unavailable"}`, 400, 3},
		{"temporary401", `{"error":"temporarily_unavailable"}`, 401, 3},
		{"server error", `{"error":"server_error"}`, 400, 3},
		{"invalid client", `{"error":"invalid_client"}`, 401, 1},
		{"unknown error", `{"error":"future_provider_error"}`, 400, 1},
		{"malformed", `{"error":`, 400, 1},
		{"empty", "", 401, 1},
		{"wrong error type", `{"error":{"code":"invalid_grant"}}`, 400, 1},
		{"bounded body", strings.Repeat(" ", 128<<10) + `{"error":"invalid_grant"}`, 400, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryStore{values: map[string]string{}}
			save(t, store, credentials{Active: "app", Accounts: []account{{ClientID: "app", Subject: "verified", Email: "verified@example.test", IDToken: "validated-hint", AccessToken: "expired-access", RefreshToken: "old-refresh", Scopes: planScope, ExpiresAt: time.Now().Add(-time.Minute)}}})
			before := stored(t, store)
			service := NewService(store)
			calls := 0
			service.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				_ = r.ParseForm()
				if r.Form.Get("client_id") != "app" || r.Form.Get("refresh_token") != "old-refresh" {
					t.Fatal("retry changed renewable credentials")
				}
				if calls <= test.failures {
					return &http.Response{StatusCode: test.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}, nil
				}
				return response(200, tokenResponse{AccessToken: "new-access", RefreshToken: "new-refresh", TokenType: "Bearer", ExpiresIn: 3600}), nil
			})
			token, err := service.BearerToken(t.Context())
			if token != "" || !errors.Is(err, ErrProvider) || errors.Is(err, ErrSignInRequired) || calls != test.failures {
				t.Fatalf("nonterminal classification: %q, %v", token, err)
			}
			after := stored(t, store)
			if after.Active != before.Active || after.Accounts[0] != before.Accounts[0] {
				t.Fatal("nonterminal refresh modified saved credentials")
			}
			token, err = service.BearerToken(t.Context())
			if err != nil || token != "new-access" || calls != test.failures+1 {
				t.Fatalf("retry did not recover: %q, %v, calls=%d", token, err, calls)
			}
			a := stored(t, store).Accounts[0]
			if a.RefreshToken != "new-refresh" || a.NeedsLogin || a.Subject != "verified" || a.IDToken != "validated-hint" {
				t.Fatal("recovery lost rotation or verified registration")
			}
		})
	}
}

func TestReducedRefreshScopePersistsRotationAndRequestsConsent(t *testing.T) {
	store := &memoryStore{values: map[string]string{}}
	save(t, store, credentials{Active: "app", Accounts: []account{{ClientID: "app", Subject: "verified", IDToken: "validated-hint", RefreshToken: "old-refresh", Scopes: planScope}}})
	service := NewService(store)
	service.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		return response(200, tokenResponse{AccessToken: "reduced-access", RefreshToken: "rotated-refresh", Scope: "openid offline_access", TokenType: "Bearer", ExpiresIn: 3600}), nil
	})
	if token, err := service.BearerToken(t.Context()); token != "" || !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("reduced grant supplied inference token: %q, %v", token, err)
	}
	a := stored(t, store).Accounts[0]
	if !a.NeedsLogin || a.Scopes != "openid offline_access" || a.RefreshToken != "rotated-refresh" || a.Subject != "verified" || a.IDToken != "validated-hint" {
		t.Fatalf("reduced grant lost scope, rotation or identity: %+v", a)
	}
	login, err := service.StartLogin(t.Context(), "app")
	if err != nil || !strings.Contains(login.AuthorizationURL, "prompt=consent") {
		t.Fatalf("reconnect skipped consent: %+v, %v", login, err)
	}
}
