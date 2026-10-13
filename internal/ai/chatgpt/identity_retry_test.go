package chatgpt

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type identityRetryFixture struct {
	service  *Service
	store    *memoryStore
	callback string
	prior    account
	claims   identityClaims
	tokens   tokenResponse
	keys     any
	calls    map[string][]time.Time
}

func newIdentityRetryFixture(t *testing.T, key *rsa.PrivateKey) *identityRetryFixture {
	t.Helper()
	f := &identityRetryFixture{
		store: &memoryStore{values: map[string]string{"ai.auth_mode": "chatgpt", "ai.chat_model": "gpt-4o-mini"}},
		prior: account{ClientID: "prior", Subject: "prior-user", AccessToken: "prior-access", RefreshToken: "prior-refresh", Scopes: planScope, ExpiresAt: time.Now().Add(time.Hour)},
		calls: make(map[string][]time.Time),
	}
	save(t, f.store, credentials{Active: f.prior.ClientID, Accounts: []account{f.prior}})
	f.prior = stored(t, f.store).Accounts[0]
	f.service = NewService(f.store)
	login, err := f.service.StartLogin(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	pending := stored(t, f.store).Pending
	f.callback = login.CallbackURI + "?" + url.Values{"state": {pending.State}, "code": {"issued-code"}, "client_id": {"issued-app"}}.Encode()
	f.claims = identityClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: issuer, Subject: "issued-user", Audience: jwt.ClaimStrings{"issued-app"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}, Nonce: pending.Nonce, Email: "issued@example.test"}
	f.tokens = tokenResponse{AccessToken: "issued-access", RefreshToken: "issued-refresh", TokenType: "Bearer", ExpiresIn: 3600, Scope: planScope}
	f.sign(t, key)
	f.keys = map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "test-key", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}}
	return f
}

func (f *identityRetryFixture) sign(t *testing.T, key *rsa.PrivateKey) {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, f.claims)
	token.Header["kid"] = "test-key"
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	f.tokens.IDToken = signed
}

func (f *identityRetryFixture) transport(t *testing.T, metadata func(*http.Request, int) (*http.Response, error)) {
	t.Helper()
	f.service.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		path := r.URL.Path
		f.calls[path] = append(f.calls[path], time.Now())
		if path == "/.well-known/openid-configuration" || path == "/keys" {
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
				t.Fatal("identity metadata request carried credentials or used a different method")
			}
			if metadata != nil {
				resp, err := metadata(r, len(f.calls[path]))
				if resp != nil || err != nil {
					return resp, err
				}
			}
		}
		switch path {
		case "/api/accounts/oauth/token":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Method != http.MethodPost || r.Form.Get("grant_type") != oauthAuthorizationCode || r.Form.Get(oauthClientID) != "issued-app" || r.Form.Get(oauthCode) != "issued-code" {
				t.Fatal("identity recovery changed the issued grant")
			}
			return response(http.StatusOK, f.tokens), nil
		case "/.well-known/openid-configuration":
			return response(http.StatusOK, discovery{JWKSURI: issuer + "/keys", RevocationEndpoint: issuer + "/revoke"}), nil
		case "/keys":
			return response(http.StatusOK, f.keys), nil
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer issued-access" {
				t.Fatal("activation did not use the verified grant")
			}
			return response(http.StatusOK, map[string]any{"models": []map[string]string{{"slug": "gpt-4o-mini", "visibility": "list"}}}), nil
		default:
			t.Fatalf("unexpected provider path %s", path)
			return nil, errors.New("unexpected provider path")
		}
	})
}

type identityReadFailure struct{ err error }

func (r identityReadFailure) Read([]byte) (int, error) { return 0, r.err }

func TestLoginRetriesTransientIdentityMetadata(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/.well-known/openid-configuration", "/keys"} {
		for _, failure := range []string{"network", "rate limit", "unavailable", "interrupted body", "network body"} {
			t.Run(path+"/"+failure, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					f := newIdentityRetryFixture(t, key)
					f.transport(t, func(r *http.Request, count int) (*http.Response, error) {
						if r.URL.Path != path || count != 1 {
							return nil, nil
						}
						switch failure {
						case "network":
							return nil, io.ErrUnexpectedEOF
						case "rate limit":
							return response(http.StatusTooManyRequests, nil), nil
						case "unavailable":
							return response(http.StatusServiceUnavailable, nil), nil
						default:
							readErr := io.ErrUnexpectedEOF
							if failure == "network body" {
								readErr = &net.OpError{Op: "read", Net: "tcp", Err: io.ErrClosedPipe}
							}
							resp := response(http.StatusOK, nil)
							resp.Body = io.NopCloser(identityReadFailure{err: readErr})
							return resp, nil
						}
					})
					if err := f.service.CompleteLogin(t.Context(), f.callback); err != nil {
						t.Fatalf("temporary metadata failure lost accepted grant: %v", err)
					}
					c := stored(t, f.store)
					if c.Active != "issued-app" || c.LoginResult != loginConnected || c.Pending != nil || len(c.Accounts) != 2 || c.Accounts[0] != f.prior {
						t.Fatal("recovered login did not activate or changed the previous account")
					}
					a := c.Accounts[1]
					if a.Subject != f.claims.Subject || a.Email != f.claims.Email || a.AccessToken != f.tokens.AccessToken || a.RefreshToken != f.tokens.RefreshToken || a.IDToken != f.tokens.IDToken || a.NeedsLogin {
						t.Fatal("recovered login lost verified identity or issued credentials")
					}
					if len(f.calls[path]) != 2 || f.calls[path][1].Sub(f.calls[path][0]) != time.Second || len(f.calls["/api/accounts/oauth/token"]) != 1 || len(f.calls["/v1/models"]) != 1 {
						t.Fatalf("incorrect metadata retry, code exchange or activation count: %v", f.calls)
					}
					if err := f.service.CompleteLogin(t.Context(), f.callback); !errors.Is(err, ErrInvalidLogin) || len(f.calls["/api/accounts/oauth/token"]) != 1 {
						t.Fatalf("recovered callback was reused: %v", err)
					}
				})
			})
		}
	}
}

func TestLoginIdentityRetryHonorsRetryAfterAndExhaustion(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		calls  int
		wait   time.Duration
		failed bool
	}{
		{name: "seconds", calls: 2, wait: 2 * time.Second},
		{name: "date", calls: 2, wait: 2 * time.Second},
		{name: "at deadline", calls: 1, failed: true},
		{name: "beyond deadline", calls: 1, failed: true},
		{name: "exhausted", calls: 3, wait: 3 * time.Second, failed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newIdentityRetryFixture(t, key)
				start := time.Now()
				f.transport(t, func(r *http.Request, count int) (*http.Response, error) {
					if r.URL.Path != "/keys" || (count > 1 && test.name != "exhausted") {
						return nil, nil
					}
					resp := response(http.StatusTooManyRequests, nil)
					switch test.name {
					case "seconds":
						resp.Header.Set("Retry-After", "2")
					case "date":
						resp.Header.Set("Retry-After", time.Now().Add(2*time.Second).UTC().Format(http.TimeFormat))
					case "at deadline":
						resp.Header.Set("Retry-After", "60")
					case "beyond deadline":
						resp.Header.Set("Retry-After", "120")
					}
					return resp, nil
				})
				err := f.service.CompleteLogin(t.Context(), f.callback)
				if test.failed != errors.Is(err, ErrProvider) || (!test.failed && err != nil) || len(f.calls["/keys"]) != test.calls || len(f.calls["/api/accounts/oauth/token"]) != 1 || time.Since(start) != test.wait {
					t.Fatalf("identity retry budget: error=%v requests=%v elapsed=%v", err, f.calls, time.Since(start))
				}
				if !test.failed {
					return
				}
				c := stored(t, f.store)
				if c.Active != f.prior.ClientID || c.Accounts[0] != f.prior || c.LoginResult != loginFailed || c.Pending != nil || len(c.Accounts) != 2 || c.Accounts[1] != (account{ClientID: "issued-app", NeedsLogin: true}) || len(f.calls["/v1/models"]) != 0 {
					t.Fatal("failed verifier lost registration, retained unverified credentials or changed prior account")
				}
				if err := f.service.SelectAccount(t.Context(), "issued-app"); !errors.Is(err, ErrSignInRequired) {
					t.Fatalf("unverified registration permitted selection: %v", err)
				}
				if token, err := f.service.BearerToken(t.Context()); err != nil || token != f.prior.AccessToken {
					t.Fatalf("verifier exhaustion changed existing inference credentials: %q, %v", token, err)
				}
				login, err := f.service.StartLogin(t.Context(), "issued-app")
				if err != nil {
					t.Fatal(err)
				}
				if u, err := url.Parse(login.AuthorizationURL); err != nil || u.Query().Get(oauthClientID) != "issued-app" || u.Query().Get("id_token_hint") != "" || u.Query().Get("login_hint") != "" {
					t.Fatal("fresh authorization failed to reuse only the disabled registration")
				}
			})
		})
	}
}

func TestLoginIdentityRejectsTerminalVerificationWithoutRetry(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"malformed discovery", "empty discovery", "untrusted endpoint", "malformed keys", "invalid key", "invalid nonce", "audience", "expired", "signature"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newIdentityRetryFixture(t, key)
				switch failure {
				case "invalid nonce":
					f.claims.Nonce = "different"
				case "audience":
					f.claims.Audience = jwt.ClaimStrings{"different-app"}
				case "expired":
					f.claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
				case "invalid key":
					f.keys = map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "test-key", "n": "bad", "e": "bad"}}}
				}
				f.sign(t, key)
				if failure == "signature" {
					parts := strings.Split(f.tokens.IDToken, ".")
					parts[2] = base64.RawURLEncoding.EncodeToString(make([]byte, 256))
					f.tokens.IDToken = strings.Join(parts, ".")
				}
				f.transport(t, func(r *http.Request, _ int) (*http.Response, error) {
					if (r.URL.Path == "/.well-known/openid-configuration" && (failure == "malformed discovery" || failure == "empty discovery")) || (r.URL.Path == "/keys" && failure == "malformed keys") {
						resp := response(http.StatusOK, nil)
						body := "{]"
						if failure == "empty discovery" {
							body = ""
						}
						resp.Body = io.NopCloser(strings.NewReader(body))
						return resp, nil
					}
					if r.URL.Path == "/.well-known/openid-configuration" && failure == "untrusted endpoint" {
						return response(http.StatusOK, discovery{JWKSURI: "https://untrusted.example.test/keys", RevocationEndpoint: issuer + "/revoke"}), nil
					}
					return nil, nil
				})
				start := time.Now()
				if err := f.service.CompleteLogin(t.Context(), f.callback); err == nil {
					t.Fatal("invalid metadata or identity was accepted")
				}
				c := stored(t, f.store)
				if time.Since(start) != 0 || len(f.calls["/.well-known/openid-configuration"]) != 1 || len(f.calls["/keys"]) > 1 || len(f.calls["/api/accounts/oauth/token"]) != 1 || len(f.calls["/v1/models"]) != 0 || c.Active != f.prior.ClientID || len(c.Accounts) != 1 || c.Accounts[0] != f.prior || c.LoginResult != loginFailed {
					t.Fatal("terminal verification retried or changed saved accounts")
				}
			})
		})
	}
}

func TestLoginIdentityRetryCannotReplaceNewerAttempt(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		f := newIdentityRetryFixture(t, key)
		var newer Login
		newerDone := make(chan struct{})
		f.transport(t, func(r *http.Request, count int) (*http.Response, error) {
			if r.URL.Path != "/keys" || count != 1 {
				return nil, nil
			}
			go func() {
				defer close(newerDone)
				synctest.Wait()
				var err error
				newer, err = f.service.StartLogin(t.Context(), "")
				if err != nil {
					t.Error(err)
				}
			}()
			return response(http.StatusServiceUnavailable, nil), nil
		})
		completionErr := f.service.CompleteLogin(t.Context(), f.callback)
		<-newerDone
		if !errors.Is(completionErr, ErrInvalidLogin) {
			t.Fatalf("superseded verifier completion: %v", completionErr)
		}
		c := stored(t, f.store)
		if c.Pending == nil || c.Pending.State != newer.AttemptID || c.LoginID != newer.AttemptID || c.LoginResult != loginPending || c.Active != f.prior.ClientID || len(c.Accounts) != 1 || c.Accounts[0] != f.prior || len(f.calls["/keys"]) != 2 || len(f.calls["/api/accounts/oauth/token"]) != 1 {
			t.Fatal("stale completion replaced latest attempt or changed existing account")
		}
	})
}
