package chatgpt

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type memoryStore struct {
	mu       sync.Mutex
	values   map[string]string
	failNext bool
}

func (m *memoryStore) Get(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.values[key], nil
}
func (m *memoryStore) UpdateValueAtomic(ctx context.Context, key string, update func(string) (string, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failNext {
		m.failNext = false
		return errors.New("simulated interrupted commit")
	}
	value, err := update(m.values[key])
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.values[key] = value
	return nil
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(code int, value any) *http.Response {
	data, _ := json.Marshal(value)
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data)))}
}
func stored(t *testing.T, store *memoryStore) credentials {
	t.Helper()
	raw, err := store.Get(t.Context(), CredentialsKey)
	if err != nil {
		t.Fatal(err)
	}
	c, err := decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func save(t *testing.T, store *memoryStore, c credentials) {
	t.Helper()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.values[CredentialsKey] = string(data)
	store.mu.Unlock()
}

func TestLoginValidatesIdentityAndConsumesCallback(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name                                                        string
		nonce, audience, subject, scope, clientID                   string
		expired                                                     bool
		omitClientID                                                bool
		superseded, interrupted, expiryCrossed, omitRefresh         bool
		retainRegistration, preserveActive, reconsent, invalidGrant bool
		want                                                        bool
	}{
		{name: "copied browser callback", scope: planScope, want: true},
		{name: "accepted callback crosses expiry", scope: planScope, expiryCrossed: true, want: true},
		{name: "initial grant requires refresh token", scope: planScope, omitRefresh: true},
		{name: "returning browser callback without client ID", scope: planScope, omitClientID: true, clientID: "oaiapp_registered", want: true},
		{name: "superseded during exchange", scope: planScope, superseded: true},
		{name: "interrupted completion commit", scope: planScope, interrupted: true},
		{name: "wrong nonce", nonce: "other", scope: planScope},
		{name: "wrong audience", audience: "different-app", scope: planScope},
		{name: "missing plan consent", scope: "openid profile email", retainRegistration: true, reconsent: true},
		{name: "invalid initial code retains registration for verified retry", scope: planScope, invalidGrant: true, retainRegistration: true, reconsent: true},
		{name: "missing consent without refresh token retains identity", scope: "openid profile email", omitRefresh: true, retainRegistration: true},
		{name: "missing consent without refresh token rejects invalid identity", scope: "openid profile email", omitRefresh: true, nonce: "other"},
		{name: "missing consent preserves another active account", scope: "openid profile email", retainRegistration: true, preserveActive: true},
		{name: "declined reconnect preserves active credentials", scope: "openid profile email", clientID: "oaiapp_registered", preserveActive: true},
		{name: "missing consent with invalid nonce discards registration", nonce: "other", scope: "openid profile email"},
		{name: "missing consent cannot change returning subject", subject: "another-user", scope: "openid profile email", clientID: "oaiapp_registered", preserveActive: true},
		{name: "superseded missing consent does not retain registration", scope: "openid profile email", superseded: true},
		{name: "expired identity", scope: planScope, expired: true},
		{name: "different returning account", subject: "another-user", scope: planScope, clientID: "oaiapp_registered"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryStore{values: map[string]string{}}
			s := NewService(store)
			if test.clientID != "" {
				save(t, store, credentials{HostID: "urn:uuid:existing", Accounts: []account{{ClientID: test.clientID, Subject: "user"}}})
			}
			if test.preserveActive {
				activeID := test.clientID
				if activeID == "" {
					activeID = "oaiapp_active"
				}
				save(t, store, credentials{Active: activeID, Accounts: []account{{ClientID: activeID, Subject: "user", AccessToken: "old-access", RefreshToken: "old-refresh", Scopes: planScope, ExpiresAt: time.Now().Add(time.Hour)}}})
			}
			activeBefore := stored(t, store).Active
			grantScope := test.scope
			login, err := s.StartLogin(t.Context(), test.clientID)
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(login.AuthorizationURL)
			if err != nil || login.CallbackURI != browserCallbackURI || u.Query().Get("redirect_uri") != login.CallbackURI || u.Query().Get("state") != login.AttemptID {
				t.Fatalf("browser sign-in was not prepared: %+v", login)
			}
			pending := stored(t, store).Pending
			clientID := test.clientID
			if clientID == "" {
				clientID = "oaiapp_new"
			}
			nonce := pending.Nonce
			if test.nonce != "" {
				nonce = test.nonce
			}
			audience := clientID
			if test.audience != "" {
				audience = test.audience
			}
			subject := "user"
			if test.subject != "" {
				subject = test.subject
			}
			expires := time.Now().Add(time.Hour)
			if test.expired {
				expires = time.Now().Add(-time.Minute)
			}
			claims := identityClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: issuer, Subject: subject, Audience: jwt.ClaimStrings{audience}, ExpiresAt: jwt.NewNumericDate(expires)}, Nonce: nonce, Email: "user@example.test"}
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			token.Header["kid"] = "test-key"
			signed, err := token.SignedString(key)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			s.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/api/accounts/oauth/token":
					calls++
					if test.expiryCrossed {
						c := stored(t, store)
						c.LoginExpiresAt = time.Now().Add(-time.Minute)
						save(t, store, c)
					}
					if test.superseded {
						if _, err := s.StartLogin(t.Context(), ""); err != nil {
							t.Fatal(err)
						}
					}
					if test.interrupted {
						store.mu.Lock()
						store.failNext = true
						store.mu.Unlock()
					}
					if test.invalidGrant && calls == 1 {
						return response(400, map[string]string{"error": oauthInvalidGrant}), nil
					}
					_ = r.ParseForm()
					if r.Form.Get("client_id") != clientID || r.Form.Get("code_verifier") != pending.Verifier || r.Form.Get("redirect_uri") != pending.RedirectURI || r.Form.Get("resource") != resource {
						t.Errorf("invalid token exchange: %v", r.Form)
					}
					refreshToken := "refresh"
					if test.omitRefresh {
						refreshToken = ""
					}
					return response(200, tokenResponse{AccessToken: "access", RefreshToken: refreshToken, IDToken: signed, TokenType: "Bearer", ExpiresIn: 3600, Scope: grantScope}), nil
				case "/.well-known/openid-configuration":
					return response(200, discovery{JWKSURI: issuer + "/keys", RevocationEndpoint: issuer + "/revoke"}), nil
				case "/keys":
					return response(200, map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "test-key", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}}), nil
				default:
					t.Fatalf("unexpected request: %s", r.URL)
					return nil, nil
				}
			})
			callback := Callback{State: pending.State, Code: "auth-code", ClientID: clientID}
			if test.omitClientID {
				callback.ClientID = ""
			}
			callbackURL := "  " + login.CallbackURI + "?" + url.Values{"state": {callback.State}, "code": {callback.Code}, "client_id": {callback.ClientID}}.Encode() + "\n"
			err = s.CompleteLogin(t.Context(), callbackURL)
			if (err == nil) != test.want {
				t.Fatalf("completion: %v; want success %v", err, test.want)
			}
			if err = s.CompleteLogin(t.Context(), callbackURL); !errors.Is(err, ErrInvalidLogin) {
				t.Fatalf("replay: %v", err)
			}
			if calls != 1 {
				t.Fatalf("authorization code exchanged %d times", calls)
			}
			state, err := s.Status(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if test.want {
				if state.ActiveClientID != clientID || len(state.Accounts) != 1 || !state.Accounts[0].Connected || state.LoginResult != "connected" {
					t.Fatalf("status: %+v", state)
				}
				data, _ := json.Marshal(state)
				if strings.Contains(string(data), "access") || strings.Contains(string(data), "refresh") {
					t.Fatal("status exposed credentials")
				}
			} else if test.superseded || test.interrupted {
				if state.ActiveClientID != "" || state.LoginResult != "pending" {
					t.Fatalf("old completion overwrote status: %+v", state)
				}
			} else if state.ActiveClientID != activeBefore || state.LoginResult != "failed" {
				t.Fatalf("failed login changed active account: %+v", state)
			}
			if test.preserveActive {
				activeToken, err := s.BearerToken(t.Context())
				if err != nil || activeToken != "old-access" {
					t.Fatalf("active credentials changed: %q, %v", activeToken, err)
				}
			}
			if test.retainRegistration {
				c := stored(t, store)
				i := slices.IndexFunc(c.Accounts, func(a account) bool { return a.ClientID == clientID })
				if i < 0 {
					t.Fatal("verified disabled registration was discarded")
				}
				a := c.Accounts[i]
				if test.invalidGrant {
					if a != (account{ClientID: clientID, NeedsLogin: true}) {
						t.Fatalf("unverified registration retained identity or tokens: %+v", a)
					}
				} else if a.Subject != "user" || a.Email != claims.Email || a.IDToken != signed || !a.NeedsLogin || a.AccessToken != "" || a.RefreshToken != "" || !a.ExpiresAt.IsZero() {
					t.Fatalf("disabled registration retained inference credentials or lost identity: %+v", a)
				}
				if err := s.SelectAccount(t.Context(), clientID); !errors.Is(err, ErrSignInRequired) {
					t.Fatalf("disabled account selected: %v", err)
				}
				if !test.preserveActive {
					if _, err := s.BearerToken(t.Context()); !errors.Is(err, ErrSignInRequired) {
						t.Fatalf("disabled registration authorized inference: %v", err)
					}
				}
			} else if !test.want && test.clientID == "" && !test.preserveActive && !test.superseded && !test.interrupted {
				if len(stored(t, store).Accounts) != 0 {
					t.Fatal("unverified registration retained")
				}
			}
			if test.reconsent {
				login, err = s.StartLogin(t.Context(), clientID)
				if err != nil {
					t.Fatal(err)
				}
				u, _ = url.Parse(login.AuthorizationURL)
				wantPrompt, wantHint, wantEmail := "consent", signed, claims.Email
				if test.invalidGrant {
					wantPrompt, wantHint, wantEmail = "", "", ""
				}
				if u.Query().Get("client_id") != clientID || u.Query().Get("prompt") != wantPrompt || !hasPlanScope(u.Query().Get("scope")) || u.Query().Get("id_token_hint") != wantHint || u.Query().Get("login_hint") != wantEmail {
					t.Fatal("re-consent lost saved registration, identity hints or permission request")
				}
				pending = stored(t, store).Pending
				claims.Nonce = pending.Nonce
				token = jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
				token.Header["kid"] = "test-key"
				signed, err = token.SignedString(key)
				if err != nil {
					t.Fatal(err)
				}
				grantScope = planScope
				callbackURL = login.CallbackURI + "?" + url.Values{"state": {pending.State}, "code": {"reconsent-code"}, "client_id": {clientID}}.Encode()
				if err := s.CompleteLogin(t.Context(), callbackURL); err != nil {
					t.Fatal(err)
				}
				state, err = s.Status(t.Context())
				if err != nil || state.ActiveClientID != clientID || len(state.Accounts) != 1 || !state.Accounts[0].Connected || state.LoginResult != loginConnected || calls != 2 {
					t.Fatalf("re-consent failed: %+v, %v, exchanges=%d", state, err, calls)
				}
			}
		})
	}
}

func TestBrowserCallbackRejectsUnrelatedURLsWithoutConsumingAttempt(t *testing.T) {
	store := &memoryStore{values: map[string]string{}}
	s := NewService(store)
	login, err := s.StartLogin(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	pending := stored(t, store).Pending
	query := url.Values{"state": {pending.State}, "code": {"auth-code"}, "client_id": {"oaiapp_new"}}.Encode()
	callbackURL := login.CallbackURI + "?" + query
	s.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid callback reached the network")
		return nil, nil
	})
	for _, test := range []struct {
		name     string
		callback string
		want     error
	}{
		{"authorization link", login.AuthorizationURL, ErrInvalidCallbackURL},
		{"remote host", strings.Replace(callbackURL, "127.0.0.1", "example.test", 1), ErrInvalidCallbackURL},
		{"localhost", strings.Replace(callbackURL, "127.0.0.1", "localhost", 1), ErrInvalidCallbackURL},
		{"different scheme", strings.Replace(callbackURL, "http:", "https:", 1), ErrInvalidCallbackURL},
		{"different port", strings.Replace(callbackURL, "51121", "51122", 1), ErrInvalidCallbackURL},
		{"different path", strings.Replace(callbackURL, "/auth/callback", "/callback", 1), ErrInvalidCallbackURL},
		{"userinfo", strings.Replace(callbackURL, "127.0.0.1", "user@127.0.0.1", 1), ErrInvalidCallbackURL},
		{"fragment", callbackURL + "#secret", ErrInvalidCallbackURL},
		{"duplicate state", callbackURL + "&state=other", ErrInvalidCallbackURL},
		{"malformed query", callbackURL + "&scope=%zz", ErrInvalidCallbackURL},
		{"missing authorization code", login.CallbackURI + "?state=" + pending.State, ErrInvalidCallbackURL},
		{"oversized code", strings.Replace(callbackURL, "auth-code", strings.Repeat("x", 8193), 1), ErrInvalidCallbackURL},
		{"wrong state", strings.Replace(callbackURL, pending.State, "other", 1), ErrInvalidLogin},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := s.CompleteLogin(t.Context(), test.callback); !errors.Is(err, test.want) {
				t.Fatalf("completion: %v; want %v", err, test.want)
			}
			if c := stored(t, store); c.Pending == nil || c.Pending.State != pending.State || c.LoginResult != loginPending {
				t.Fatal("invalid paste consumed the pending sign-in")
			}
		})
	}
	if err := s.CompleteLogin(t.Context(), login.CallbackURI+"?state="+pending.State+"&error=access_denied"); !errors.Is(err, ErrIdentity) {
		t.Fatalf("declined consent: %v", err)
	}
	if c := stored(t, store); c.Pending != nil || c.LoginResult != loginFailed {
		t.Fatal("declined consent did not end sign-in")
	}
}

func TestModelsPreserveVisibleAccountCatalogOrder(t *testing.T) {
	store := &memoryStore{values: map[string]string{}}
	save(t, store, credentials{Active: "app", Accounts: []account{{ClientID: "app", AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour)}}})
	s := NewService(store)
	s.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != resource+"/models" || r.Header.Get("Authorization") != "Bearer access" {
			t.Error("wrong catalog request")
		}
		return response(200, map[string]any{"models": []map[string]string{
			{"slug": "gpt-6-astra", "display_name": "GPT-6 Astra", "visibility": "list"},
			{"slug": "hidden", "visibility": "hidden"},
			{"slug": "gpt-6.1-sol", "display_name": "GPT-6.1 Sol", "visibility": "list"},
		}}), nil
	})
	models, err := s.Models(t.Context())
	if err != nil || len(models) != 2 || models[0].ID != "gpt-6-astra" || models[1].ID != "gpt-6.1-sol" {
		t.Fatalf("models = %+v, err = %v", models, err)
	}
}

func TestLoginStateExpiryConsentAndReauthorization(t *testing.T) {
	store := &memoryStore{values: map[string]string{}}
	s := NewService(store)
	login, err := s.StartLogin(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(login.AuthorizationURL)
	q := u.Query()
	if q.Get("client_id") != dynamicClientID || q.Get("agent_name_hint") != "Silo" || q.Get("code_challenge") != hash(stored(t, store).Pending.Verifier) || q.Get("redirect_uri") != browserCallbackURI {
		t.Fatal("invalid registration")
	}
	if err := s.CompleteLogin(t.Context(), login.CallbackURI+"?state=wrong&error=access_denied"); !errors.Is(err, ErrInvalidLogin) {
		t.Fatalf("wrong state: %v", err)
	}
	if err := s.CompleteLogin(t.Context(), login.CallbackURI+"?state="+q.Get("state")+"&error=access_denied"); !errors.Is(err, ErrIdentity) {
		t.Fatalf("consent: %v", err)
	}
	c := stored(t, store)
	host := c.HostID
	c.Accounts = []account{{ClientID: "registered", Subject: "user", IDToken: "retained-identity", Email: "user@example.test"}, {ClientID: "other", IDToken: "another-identity", Email: "other@example.test"}}
	save(t, store, c)
	login, err = s.StartLogin(t.Context(), "registered")
	if err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(login.AuthorizationURL)
	q = u.Query()
	if q.Get("client_id") != "registered" || q.Get("agent_name_hint") != "" || q.Get("ext_agent_host_id") != host || q.Get("id_token_hint") != "retained-identity" || q.Get("login_hint") != "user@example.test" || q.Get("prompt") != "" {
		t.Fatal("reauthorization lost registration, host or account hints")
	}
	c = stored(t, store)
	c.Pending.ExpiresAt = time.Now().Add(-time.Minute)
	save(t, store, c)
	if err := s.CompleteLogin(t.Context(), login.CallbackURI+"?state="+q.Get("state")+"&code=auth-code"); !errors.Is(err, ErrInvalidLogin) {
		t.Fatalf("expired attempt: %v", err)
	}
}

func TestRefreshSerializesAcrossServiceInstances(t *testing.T) {
	store := &memoryStore{values: map[string]string{}}
	save(t, store, credentials{Active: "app", Accounts: []account{{ClientID: "app", Subject: "user", RefreshToken: "old-refresh", AccessToken: "expired", Scopes: planScope, ExpiresAt: time.Now().Add(-time.Minute)}}})
	var calls atomic.Int32
	transport := transportFunc(func(r *http.Request) (*http.Response, error) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "old-refresh" || r.Form.Get("client_id") != "app" {
			t.Errorf("refresh used wrong credentials")
		}
		calls.Add(1)
		return response(200, tokenResponse{AccessToken: "new-access", RefreshToken: "new-refresh", TokenType: "Bearer", ExpiresIn: 3600}), nil
	})
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			s := NewService(store)
			s.http.Transport = transport
			token, err := s.BearerToken(t.Context())
			if err != nil || token != "new-access" {
				t.Errorf("token = %q, err = %v", token, err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("rotating token refreshed %d times", calls.Load())
	}
	c := stored(t, store)
	if c.Accounts[0].RefreshToken != "new-refresh" || c.Accounts[0].AccessToken != "new-access" {
		t.Fatal("replacement token bundle not saved")
	}
}

func TestRevokedRefreshAndDisconnectPreserveRegistration(t *testing.T) {
	store := &memoryStore{values: map[string]string{}}
	save(t, store, credentials{HostID: "host", Active: "app", Accounts: []account{{ClientID: "app", Subject: "user", RefreshToken: "refresh", Scopes: planScope}}})
	s := NewService(store)
	s.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		return response(400, map[string]string{"error": oauthInvalidGrant}), nil
	})
	if _, err := s.BearerToken(t.Context()); !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("refresh: %v", err)
	}
	if !stored(t, store).Accounts[0].NeedsLogin {
		t.Fatal("revoked refresh not marked")
	}
	s.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })
	revoked, err := s.Disconnect(t.Context(), "app")
	if err != nil || revoked {
		t.Fatalf("disconnect: %v, %v", revoked, err)
	}
	c := stored(t, store)
	if c.Active != "" || c.HostID != "host" || c.Accounts[0].ClientID != "app" || c.Accounts[0].RefreshToken != "" {
		t.Fatal("disconnect lost registration or retained credentials")
	}
}

func TestRefreshPreservesRotationOnCallerCancellation(t *testing.T) {
	store := &memoryStore{values: map[string]string{}}
	save(t, store, credentials{Active: "app", Accounts: []account{{ClientID: "app", Subject: "user", IDToken: "validated-at-login", RefreshToken: "old-refresh", Scopes: planScope}}})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := NewService(store)
	s.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != s.tokenURL {
			t.Error("refresh made an unnecessary identity request")
			return response(503, nil), nil
		}
		cancel()
		if r.Context().Err() != nil {
			return nil, r.Context().Err()
		}
		return response(200, tokenResponse{AccessToken: "new-access", RefreshToken: "new-refresh", IDToken: "unused-refresh-identity", TokenType: "Bearer", ExpiresIn: 3600}), nil
	})
	token, err := s.BearerToken(ctx)
	if err != nil || token != "new-access" {
		t.Fatalf("refresh: %q, %v", token, err)
	}
	a := stored(t, store).Accounts[0]
	if a.RefreshToken != "new-refresh" || a.IDToken != "validated-at-login" || a.NeedsLogin {
		t.Fatal("successful rotation lost credentials or identity")
	}
}

func TestTransientRefreshFailureKeepsSessionRetryable(t *testing.T) {
	store := &memoryStore{values: map[string]string{}}
	save(t, store, credentials{Active: "app", Accounts: []account{{ClientID: "app", RefreshToken: "old-refresh", Scopes: planScope}}})
	s := NewService(store)
	s.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) { return response(503, nil), nil })
	if _, err := s.BearerToken(t.Context()); !errors.Is(err, ErrProvider) {
		t.Fatalf("refresh error: %v", err)
	}
	a := stored(t, store).Accounts[0]
	if a.NeedsLogin || a.RefreshToken != "old-refresh" {
		t.Fatal("temporary outage discarded renewable session")
	}
}

func TestRejectedTokenProtectsReplacementAndOtherAccounts(t *testing.T) {
	store := &memoryStore{values: map[string]string{}}
	save(t, store, credentials{Active: "second", Accounts: []account{
		{ClientID: "first", AccessToken: "replacement", RefreshToken: "first-refresh", ExpiresAt: time.Now().Add(time.Hour)},
		{ClientID: "second", AccessToken: "second-token", RefreshToken: "second-refresh", ExpiresAt: time.Now().Add(time.Hour)},
	}})
	s := NewService(store)
	if err := s.RejectToken(t.Context(), "old-token", true); err != nil {
		t.Fatal(err)
	}
	c := stored(t, store)
	if c.Accounts[0].AccessToken != "replacement" || c.Accounts[0].NeedsLogin || c.Active != "second" || c.Accounts[1].AccessToken != "second-token" {
		t.Fatal("late rejection changed replacement credentials or active account")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.RejectToken(ctx, "replacement", true); err != nil {
		t.Fatal(err)
	}
	c = stored(t, store)
	if c.Accounts[0].AccessToken != "" || !c.Accounts[0].ExpiresAt.IsZero() || !c.Accounts[0].NeedsLogin || c.Accounts[0].RefreshToken != "first-refresh" || c.Active != "second" || c.Accounts[1].AccessToken != "second-token" {
		t.Fatal("rejection was lost on cancellation or changed another account")
	}
}

func TestModelsRecoverEarlyUnauthorizedOnce(t *testing.T) {
	for _, finalUnauthorized := range []bool{false, true} {
		t.Run(fmt.Sprintf("finalUnauthorized=%t", finalUnauthorized), func(t *testing.T) {
			store := &memoryStore{values: map[string]string{}}
			save(t, store, credentials{Active: "app", Accounts: []account{{ClientID: "app", AccessToken: "old-access", RefreshToken: "old-refresh", Scopes: planScope, ExpiresAt: time.Now().Add(time.Hour)}}})
			s := NewService(store)
			modelsCalls, refreshCalls := 0, 0
			s.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() == s.tokenURL {
					refreshCalls++
					return response(200, tokenResponse{AccessToken: "new-access", RefreshToken: "new-refresh", TokenType: "Bearer", ExpiresIn: 3600}), nil
				}
				modelsCalls++
				want := "old-access"
				if modelsCalls == 2 {
					want = "new-access"
				}
				if r.Header.Get("Authorization") != "Bearer "+want {
					t.Fatal("model retry used stale token")
				}
				if modelsCalls == 1 || finalUnauthorized {
					return response(401, nil), nil
				}
				return response(200, map[string]any{"models": []map[string]string{{"slug": "model", "visibility": "list"}}}), nil
			})
			models, err := s.Models(t.Context())
			if finalUnauthorized {
				if !errors.Is(err, ErrSignInRequired) {
					t.Fatalf("final rejection: %v", err)
				}
			} else if err != nil || len(models) != 1 {
				t.Fatalf("models: %v, %v", models, err)
			}
			if modelsCalls != 2 || refreshCalls != 1 {
				t.Fatalf("calls: models=%d refresh=%d", modelsCalls, refreshCalls)
			}
			a := stored(t, store).Accounts[0]
			if a.NeedsLogin != finalUnauthorized || a.RefreshToken != "new-refresh" {
				t.Fatal("recovery lost rotation or final sign-in status")
			}
		})
	}
}

func TestRefreshWithoutReplacementPreservesRenewableToken(t *testing.T) {
	store := &memoryStore{values: map[string]string{}}
	save(t, store, credentials{Active: "app", Accounts: []account{{ClientID: "app", RefreshToken: "original-refresh", Scopes: planScope}}})
	s := NewService(store)
	s.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		_ = r.ParseForm()
		if r.Form.Get("refresh_token") != "original-refresh" {
			t.Fatal("refresh lost original credential")
		}
		return response(200, tokenResponse{AccessToken: "renewed-access", TokenType: "Bearer", ExpiresIn: 3600}), nil
	})
	token, err := s.BearerToken(t.Context())
	if err != nil || token != "renewed-access" {
		t.Fatalf("refresh: %q, %v", token, err)
	}
	a := stored(t, store).Accounts[0]
	if a.RefreshToken != "original-refresh" || a.NeedsLogin {
		t.Fatal("nonrotating refresh lost renewable credentials")
	}
}

func TestInvalidCodeRegistrationRetentionRequiresMatchingAttempt(t *testing.T) {
	for _, test := range []struct {
		name, code                              string
		superseded, mismatch, returning, retain bool
	}{
		{name: "initial invalid grant", code: oauthInvalidGrant, retain: true},
		{name: "superseded invalid grant", code: oauthInvalidGrant, superseded: true},
		{name: "other client failure", code: "invalid_client"},
		{name: "returning invalid grant preserves identity", code: oauthInvalidGrant, returning: true},
		{name: "returning mismatched registration", code: oauthInvalidGrant, returning: true, mismatch: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryStore{values: map[string]string{}}
			active := account{ClientID: "active", Subject: "verified", Email: "verified@example.test", IDToken: "verified-hint", AccessToken: "active-token", RefreshToken: "active-refresh", Scopes: planScope, ExpiresAt: time.Now().Add(time.Hour)}
			save(t, store, credentials{Active: active.ClientID, Accounts: []account{active}})
			active = stored(t, store).Accounts[0] // Compare the stored representation after JSON removes monotonic time.
			s := NewService(store)
			selected := ""
			if test.returning {
				selected = active.ClientID
			}
			login, err := s.StartLogin(t.Context(), selected)
			if err != nil {
				t.Fatal(err)
			}
			issued := "issued-app"
			if test.returning {
				issued = active.ClientID
			}
			if test.mismatch {
				issued = "mismatched-app"
			}
			exchanges := 0
			s.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != s.tokenURL {
					t.Fatal("failed code exchange requested identity metadata")
				}
				exchanges++
				if test.superseded {
					if _, err := s.StartLogin(t.Context(), ""); err != nil {
						t.Fatal(err)
					}
				}
				return response(400, map[string]string{"error": test.code}), nil
			})
			callback := login.CallbackURI + "?" + url.Values{"state": {login.AttemptID}, "code": {"expired-code"}, "client_id": {issued}}.Encode()
			if err := s.CompleteLogin(t.Context(), callback); err == nil {
				t.Fatal("invalid code became connected")
			}
			c := stored(t, store)
			wantCount := 1
			if test.retain {
				wantCount = 2
			}
			if len(c.Accounts) != wantCount || c.Active != active.ClientID || c.Accounts[0] != active {
				t.Fatalf("failed grant changed active account or retained unwanted registration: %+v", c)
			}
			if test.retain && c.Accounts[1] != (account{ClientID: issued, NeedsLogin: true}) {
				t.Fatal("unverified registration retained more than issued ID")
			}
			wantExchanges := 1
			if test.mismatch {
				wantExchanges = 0
			}
			if exchanges != wantExchanges {
				t.Fatalf("exchanges=%d", exchanges)
			}
			if test.superseded && c.LoginID == login.AttemptID {
				t.Fatal("superseded attempt replaced new login")
			}
		})
	}
}

func TestPinnedTokenSourcesRefreshInitialAccountAcrossServices(t *testing.T) {
	store := &memoryStore{values: map[string]string{}}
	save(t, store, credentials{Active: "a", Accounts: []account{{ClientID: "a", Subject: "a-user", AccessToken: "a-token", RefreshToken: "a-refresh", Scopes: planScope, ExpiresAt: time.Now().Add(time.Hour)}, {ClientID: "b", Subject: "b-user", AccessToken: "b-token", RefreshToken: "b-refresh", Scopes: planScope, ExpiresAt: time.Now().Add(time.Hour)}}})
	first, second := NewService(store), NewService(store)
	one, err := first.TokenSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	two, err := second.TokenSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := second.SelectAccount(t.Context(), "b"); err != nil {
		t.Fatal(err)
	}
	beforeB := stored(t, store).Accounts[1]
	if err := second.RejectToken(t.Context(), "a-token", false); err != nil {
		t.Fatal(err)
	}
	var refreshes atomic.Int32
	transport := transportFunc(func(r *http.Request) (*http.Response, error) {
		_ = r.ParseForm()
		if r.Form.Get("client_id") != "a" || r.Form.Get("refresh_token") != "a-refresh" {
			t.Error("pinned refresh used switched account")
		}
		refreshes.Add(1)
		return response(200, tokenResponse{AccessToken: "a-renewed", RefreshToken: "a-rotated", TokenType: "Bearer", ExpiresIn: 3600}), nil
	})
	first.http.Transport, second.http.Transport = transport, transport
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() {
			source := one
			if i%2 == 1 {
				source = two
			}
			token, err := source(t.Context())
			if err != nil || token != "a-renewed" {
				t.Errorf("pinned token=%q, %v", token, err)
			}
		})
	}
	wg.Wait()
	c := stored(t, store)
	if refreshes.Load() != 1 || c.Active != "b" || c.Accounts[1] != beforeB || c.Accounts[0].RefreshToken != "a-rotated" {
		t.Fatal("pinned refresh changed active account, lost rotation or reused token")
	}
	// A rotation observed from another node remains visible to the pinned source.
	c.Accounts[0].AccessToken = "a-other-node"
	save(t, store, c)
	if token, err := one(t.Context()); err != nil || token != "a-other-node" {
		t.Fatalf("pinned source cached stale credentials: %q, %v", token, err)
	}
	if token, err := second.BearerToken(t.Context()); err != nil || token != "b-token" {
		t.Fatalf("next request failed to use active account: %q, %v", token, err)
	}
}

func TestPinnedTokenSourceStopsAfterDisconnectOrRemoval(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(fmt.Sprintf("remove=%t", remove), func(t *testing.T) {
			store := &memoryStore{values: map[string]string{}}
			save(t, store, credentials{Active: "a", Accounts: []account{{ClientID: "a", AccessToken: "a-token", RefreshToken: "a-refresh", ExpiresAt: time.Now().Add(time.Hour)}, {ClientID: "b", AccessToken: "b-token", RefreshToken: "b-refresh", ExpiresAt: time.Now().Add(time.Hour)}}})
			s := NewService(store)
			source, err := s.TokenSource(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SelectAccount(t.Context(), "b"); err != nil {
				t.Fatal(err)
			}
			if remove {
				c := stored(t, store)
				c.Accounts = c.Accounts[1:]
				save(t, store, c)
			} else {
				s.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })
				if _, err := s.Disconnect(t.Context(), "a"); err != nil {
					t.Fatal(err)
				}
			}
			if token, err := source(t.Context()); !errors.Is(err, ErrSignInRequired) || token != "" {
				t.Fatalf("unavailable pinned account fell through: %q, %v", token, err)
			}
			if token, err := s.BearerToken(t.Context()); err != nil || token != "b-token" {
				t.Fatalf("unrelated active account changed: %q, %v", token, err)
			}
		})
	}
}

func TestModelsRecoveryPinsInitialAccount(t *testing.T) {
	for _, finalUnauthorized := range []bool{false, true} {
		t.Run(fmt.Sprintf("finalUnauthorized=%t", finalUnauthorized), func(t *testing.T) {
			store := &memoryStore{values: map[string]string{}}
			save(t, store, credentials{Active: "a", Accounts: []account{{ClientID: "a", AccessToken: "a-token", RefreshToken: "a-refresh", Scopes: planScope, ExpiresAt: time.Now().Add(time.Hour)}, {ClientID: "b", AccessToken: "b-token", RefreshToken: "b-refresh", Scopes: planScope, ExpiresAt: time.Now().Add(time.Hour)}}})
			s := NewService(store)
			calls, refreshes := 0, 0
			s.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() == s.tokenURL {
					refreshes++
					_ = r.ParseForm()
					if r.Form.Get("client_id") != "a" {
						t.Fatal("model recovery refreshed switched account")
					}
					return response(200, tokenResponse{AccessToken: "a-renewed", RefreshToken: "a-rotated", TokenType: "Bearer", ExpiresIn: 3600}), nil
				}
				calls++
				expected := "a-token"
				if calls > 1 {
					expected = "a-renewed"
				}
				if r.Header.Get("Authorization") != "Bearer "+expected {
					t.Fatal("model retry consumed switched account")
				}
				if calls == 1 {
					if err := s.SelectAccount(t.Context(), "b"); err != nil {
						t.Fatal(err)
					}
					return response(401, nil), nil
				}
				if finalUnauthorized {
					return response(401, nil), nil
				}
				return response(200, map[string]any{"models": []map[string]string{{"slug": "a-model", "visibility": "list"}}}), nil
			})
			models, err := s.Models(t.Context())
			if finalUnauthorized {
				if !errors.Is(err, ErrSignInRequired) {
					t.Fatal(err)
				}
			} else if err != nil || len(models) != 1 || models[0].ID != "a-model" {
				t.Fatalf("models=%v, %v", models, err)
			}
			c := stored(t, store)
			if calls != 2 || refreshes != 1 || c.Active != "b" || c.Accounts[1].AccessToken != "b-token" || c.Accounts[1].NeedsLogin || c.Accounts[0].NeedsLogin != finalUnauthorized {
				t.Fatal("model recovery changed active account or incorrect retry status")
			}
		})
	}
}
