package chatgpt

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func newRejectedIdentityFixture(t *testing.T, key *rsa.PrivateKey, unbound bool) *identityRetryFixture {
	t.Helper()
	f := newIdentityRetryFixture(t, key)
	c := stored(t, f.store)
	registered := account{ClientID: "issued-app", Subject: "saved-user", Email: "saved@example.test", AccessToken: "saved-access", RefreshToken: "saved-refresh", Scopes: planScope, ExpiresAt: time.Now().Add(time.Hour)}
	if unbound {
		registered.Subject = ""
	}
	c.Accounts = append(c.Accounts, registered)
	c.Active = registered.ClientID
	save(t, f.store, c)
	login, err := f.service.StartLogin(t.Context(), registered.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	pending := stored(t, f.store).Pending
	f.callback = login.CallbackURI + "?" + url.Values{"state": {pending.State}, "code": {"issued-code"}, "client_id": {registered.ClientID}}.Encode()
	f.claims.Nonce = pending.Nonce
	f.sign(t, key)
	f.service.http.Timeout = 0
	f.transport(t, nil)
	return f
}

func TestLoginRevokesDifferentReturningIdentity(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name                                          string
		withoutConsent, unbound, superseded, failSave bool
		cancelCaller, supersededDuringRevocation      bool
	}{
		{name: "returning subject"},
		{name: "missing plan consent", withoutConsent: true},
		{name: "registration bound during exchange", unbound: true},
		{name: "newer login supersedes rejected grant", superseded: true},
		{name: "newer login starts during revocation", supersededDuringRevocation: true},
		{name: "failed outcome commit", failSave: true},
		{name: "browser request canceled", cancelCaller: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newRejectedIdentityFixture(t, key, test.unbound)
				if test.withoutConsent {
					f.tokens.Scope = "openid profile email"
				}
				before := stored(t, f.store)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				loginTransport := f.service.http.Transport
				var newer Login
				revocations := 0
				f.service.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
					switch r.URL.Path {
					case "/api/accounts/oauth/token":
						if test.unbound {
							c := stored(t, f.store)
							c.Accounts[1].Subject = "saved-user"
							save(t, f.store, c)
							before.Accounts[1] = stored(t, f.store).Accounts[1]
						}
						if test.cancelCaller {
							cancel()
						}
					case "/keys":
						if test.superseded {
							var err error
							newer, err = f.service.StartLogin(t.Context(), "")
							if err != nil {
								t.Fatal(err)
							}
						}
						if test.failSave {
							f.store.mu.Lock()
							f.store.failNext = true
							f.store.mu.Unlock()
						}
					case "/revoke":
						revocations++
						if err := r.ParseForm(); err != nil {
							t.Fatal(err)
						}
						if r.Form.Get("token") != f.tokens.RefreshToken || r.Form.Get(oauthClientID) != "issued-app" || r.Form.Get("token_type_hint") != oauthRefreshToken {
							t.Fatal("identity rejection revoked the saved grant instead of the newly issued grant")
						}
						deadline, ok := r.Context().Deadline()
						if !ok || time.Until(deadline) != time.Minute || r.Context().Err() != nil {
							t.Fatal("identity rejection did not retain an independent bounded revocation context")
						}
						// Reading the store here also requires provider work to happen
						// outside the credential/settings transaction.
						c := stored(t, f.store)
						if c.Active != before.Active || c.Accounts[0] != before.Accounts[0] || c.Accounts[1] != before.Accounts[1] {
							t.Fatal("identity rejection changed the saved account before revocation")
						}
						if test.supersededDuringRevocation {
							var err error
							newer, err = f.service.StartLogin(t.Context(), "")
							if err != nil {
								t.Fatal(err)
							}
						}
						return response(http.StatusOK, nil), nil
					}
					return loginTransport.RoundTrip(r)
				})
				_, completionErr := f.service.CompleteLoginWithSettings(ctx, f.callback)
				wantError := ErrIdentity
				if test.superseded {
					wantError = ErrInvalidLogin
				}
				if test.failSave {
					if completionErr == nil || !strings.Contains(completionErr.Error(), "simulated interrupted commit") {
						t.Fatalf("lost save failure: %v", completionErr)
					}
				} else if !errors.Is(completionErr, wantError) {
					t.Fatalf("completion error=%v, want %v", completionErr, wantError)
				}
				if revocations != 1 {
					t.Fatalf("rejected renewable grant was not revoked: calls=%d", revocations)
				}
				c := stored(t, f.store)
				if c.Active != before.Active || len(c.Accounts) != 2 || c.Accounts[0] != before.Accounts[0] || c.Accounts[1] != before.Accounts[1] {
					t.Fatal("rejected identity changed a saved registration or its credentials")
				}
				if test.superseded || test.supersededDuringRevocation {
					if c.Pending == nil || c.Pending.State != newer.AttemptID || c.LoginID != newer.AttemptID || c.LoginResult != loginPending {
						t.Fatal("rejected grant cleanup replaced the newer login attempt")
					}
				} else if c.Pending != nil || (!test.failSave && c.LoginResult != loginFailed) {
					t.Fatal("rejected callback was not consumed or failed outcome was not committed")
				}
				wantModels := 0
				if test.unbound {
					wantModels = 1
				}
				if len(f.calls["/api/accounts/oauth/token"]) != 1 || len(f.calls["/v1/models"]) != wantModels {
					t.Fatal("identity rejection repeated the code exchange or queried models for a known mismatch")
				}
				if err := f.service.CompleteLogin(t.Context(), f.callback); !errors.Is(err, ErrInvalidLogin) || revocations != 1 {
					t.Fatalf("rejected callback was reusable: %v", err)
				}
			})
		})
	}
}

func TestLoginRejectedIdentityRevocationRetriesAndBounds(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"discovery", "revocation"} {
		for _, outcome := range []string{"recovered", "exhausted", "deadline", "long delay"} {
			t.Run(target+"/"+outcome, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					f := newRejectedIdentityFixture(t, key, false)
					before := stored(t, f.store)
					loginTransport := f.service.http.Transport
					start := time.Now()
					var attempts []time.Time
					discoveries, revocations := 0, 0
					f.service.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
						discoveryRequest := r.URL.Path == "/.well-known/openid-configuration"
						if discoveryRequest {
							discoveries++
						}
						if r.URL.Path == "/revoke" {
							revocations++
							if err := r.ParseForm(); err != nil {
								t.Fatal(err)
							}
							if r.Form.Get("token") != f.tokens.RefreshToken || r.Form.Get(oauthClientID) != "issued-app" {
								t.Fatal("retry did not retain the rejected session")
							}
						}
						// The first discovery belongs to ID-token verification.
						if (discoveryRequest && discoveries > 1 && target == "discovery") || (r.URL.Path == "/revoke" && target == "revocation") {
							attempts = append(attempts, time.Now())
							if outcome == "deadline" {
								<-r.Context().Done()
								return nil, r.Context().Err()
							}
							if outcome == "recovered" && len(attempts) == 1 {
								return nil, io.ErrUnexpectedEOF
							}
							if outcome != "recovered" || len(attempts) < 3 {
								resp := response(http.StatusServiceUnavailable, nil)
								switch outcome {
								case "recovered":
									resp.Header.Set("Retry-After", "4")
								case "long delay":
									resp.Header.Set("Retry-After", "120")
								}
								return resp, nil
							}
						}
						if r.URL.Path == "/revoke" {
							return response(http.StatusOK, nil), nil
						}
						return loginTransport.RoundTrip(r)
					})
					if err := f.service.CompleteLogin(t.Context(), f.callback); !errors.Is(err, ErrIdentity) {
						t.Fatalf("revocation replaced the identity rejection: %v", err)
					}
					wantCalls, wantElapsed := 3, 3*time.Second
					switch outcome {
					case "recovered":
						wantElapsed = 5 * time.Second
					case "deadline":
						wantCalls, wantElapsed = 1, time.Minute
					case "long delay":
						wantCalls, wantElapsed = 1, 0
					}
					if len(attempts) != wantCalls || time.Since(start) != wantElapsed {
						t.Fatalf("revocation calls=%d elapsed=%v, want calls=%d elapsed=%v", len(attempts), time.Since(start), wantCalls, wantElapsed)
					}
					wantRevocations := 0
					if outcome == "recovered" {
						wantRevocations = 1
					}
					if target == "discovery" && revocations != wantRevocations {
						t.Fatal("revocation ran without valid discovery or failed after discovery recovery")
					}
					c := stored(t, f.store)
					if c.Active != before.Active || len(c.Accounts) != 2 || c.Accounts[0] != before.Accounts[0] || c.Accounts[1] != before.Accounts[1] || c.Pending != nil || c.LoginResult != loginFailed {
						t.Fatal("provider failure changed the saved account or the consumed login outcome")
					}
				})
			})
		}
	}
}

func TestLoginDoesNotRevokeUnverifiedOrMatchingGrant(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"invalid identity", "matching declined consent"} {
		t.Run(failure, func(t *testing.T) {
			f := newRejectedIdentityFixture(t, key, false)
			if failure == "invalid identity" {
				f.claims.Nonce = "wrong-nonce"
			} else {
				f.claims.Subject = "saved-user"
				f.tokens.Scope = "openid profile email"
			}
			f.sign(t, key)
			before := stored(t, f.store)
			if err := f.service.CompleteLogin(t.Context(), f.callback); !errors.Is(err, ErrIdentity) {
				t.Fatalf("rejected completion: %v", err)
			}
			c := stored(t, f.store)
			if c.Active != before.Active || len(c.Accounts) != 2 || c.Accounts[0] != before.Accounts[0] || c.Accounts[1] != before.Accounts[1] {
				t.Fatal("unverified or matching declined grant changed the saved account")
			}
		})
	}
}
