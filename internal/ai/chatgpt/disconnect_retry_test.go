package chatgpt

import (
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestDisconnectRetryKeepsDepartingTokenAfterReconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newDisconnectStore(t)
		service, other := NewService(store), NewService(store)
		firstAttempt := make(chan struct{})
		responseRelease := make(chan struct{})
		releaseResponse := sync.OnceFunc(func() { close(responseRelease) })
		defer releaseResponse()
		var attempts []time.Time
		service.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() == service.discoveryURL {
				return response(http.StatusOK, discovery{JWKSURI: issuer + "/keys", RevocationEndpoint: issuer + "/revoke"}), nil
			}
			attempts = append(attempts, time.Now())
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("token") != "a-refresh" || r.Form.Get(oauthClientID) != "a" {
				t.Fatal("retry revoked credentials from the newer login")
			}
			if len(attempts) == 1 {
				resp := response(http.StatusTooManyRequests, nil)
				resp.Header.Set("Retry-After", "4")
				close(firstAttempt)
				<-responseRelease
				return resp, nil
			}
			return response(http.StatusOK, nil), nil
		})
		done := make(chan struct{})
		var revoked bool
		var disconnectErr error
		go func() {
			revoked, disconnectErr = service.Disconnect(t.Context(), "a")
			close(done)
		}()
		<-firstAttempt
		if !store.credentials(t).Accounts[0].NeedsLogin {
			t.Fatal("disconnect did not commit deactivation before provider revocation")
		}
		// Commit the verified reconnect before releasing the response that starts
		// the retry delay. The departing token stays pinned across both requests.
		reconnected := account{ClientID: "a", Subject: "user-a", AccessToken: "new-access", RefreshToken: "new-refresh", Scopes: planScope, ExpiresAt: time.Now().Add(time.Hour)}
		if err := other.mutate(t.Context(), func(c *credentials) error {
			c.Accounts[0] = reconnected
			c.Active = "a"
			c.LoginResult = loginConnected
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		reconnected = store.credentials(t).Accounts[0]
		releaseResponse()
		<-done
		if disconnectErr != nil || !revoked || len(attempts) != 2 || attempts[1].Sub(attempts[0]) != 4*time.Second {
			t.Fatalf("revocation retry: revoked=%t error=%v attempts=%v", revoked, disconnectErr, attempts)
		}
		c := store.credentials(t)
		if c.Active != "a" || c.Accounts[0] != reconnected || c.LoginResult != loginConnected {
			t.Fatal("late cleanup erased credentials from the successful reconnect")
		}
	})
}

func TestDisconnectProviderTimeoutPreservesCleanupBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newDisconnectStore(t)
		service := NewService(store)
		service.http.Timeout = 0
		start := time.Now()
		service.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
			deadline, ok := r.Context().Deadline()
			if !ok || deadline.Sub(start) != 55*time.Second {
				t.Fatal("provider request did not retain the bounded revocation deadline")
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		if revoked, err := service.Disconnect(t.Context(), "a"); err != nil || revoked || time.Since(start) != 55*time.Second {
			t.Fatalf("provider timeout: revoked=%t error=%v elapsed=%v", revoked, err, time.Since(start))
		}
		c := store.credentials(t)
		if c.Active != "" || !c.Accounts[0].NeedsLogin || c.Accounts[0].RefreshToken != "" || c.Accounts[0].DisconnectID != "" {
			t.Fatal("provider timeout consumed the local cleanup budget")
		}
	})
}

func TestDisconnectRetryHonorsProviderDelay(t *testing.T) {
	for _, target := range []string{"discovery", "revocation"} {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
			for _, format := range []string{"seconds", "date", "at deadline", "beyond deadline"} {
				t.Run(target+"/"+http.StatusText(status)+"/"+format, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						store := newDisconnectStore(t)
						service := NewService(store)
						start := time.Now()
						var attempts []time.Time
						service.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
							c := store.credentials(t)
							a := c.Accounts[0]
							if c.Active != "" || !a.NeedsLogin || a.AccessToken != "" || a.RefreshToken != "a-refresh" || a.DisconnectID == "" {
								t.Fatal("provider retry preceded committed deactivation or lost its token")
							}
							discoveryRequest := r.URL.String() == service.discoveryURL
							if !discoveryRequest {
								if err := r.ParseForm(); err != nil {
									t.Fatal(err)
								}
								if r.URL.Path != "/revoke" || r.Form.Get("token") != "a-refresh" || r.Form.Get(oauthClientID) != "a" {
									t.Fatal("revocation retry changed its endpoint, registration or token")
								}
							}
							if discoveryRequest == (target == "discovery") {
								attempts = append(attempts, time.Now())
								if len(attempts) < 3 {
									resp := response(status, nil)
									resp.Header.Set("Retry-After", "4")
									switch format {
									case "date":
										resp.Header.Set("Retry-After", time.Now().Add(4*time.Second).UTC().Format(http.TimeFormat))
									case "at deadline":
										resp.Header.Set("Retry-After", "55")
									case "beyond deadline":
										resp.Header.Set("Retry-After", "120")
									}
									if len(attempts) == 2 {
										resp.Header.Set("Retry-After", "1")
									}
									return resp, nil
								}
							}
							if discoveryRequest {
								return response(http.StatusOK, discovery{JWKSURI: issuer + "/keys", RevocationEndpoint: issuer + "/revoke"}), nil
							}
							return response(http.StatusOK, nil), nil
						})
						revoked, err := service.Disconnect(t.Context(), "a")
						if format == "at deadline" || format == "beyond deadline" {
							if err != nil || revoked || len(attempts) != 1 || time.Since(start) != 0 {
								t.Fatalf("retry could not fit provider budget: revoked=%t error=%v calls=%d elapsed=%v", revoked, err, len(attempts), time.Since(start))
							}
						} else if err != nil || !revoked || len(attempts) != 3 || attempts[1].Sub(attempts[0]) != 4*time.Second || attempts[2].Sub(attempts[1]) != 2*time.Second {
							t.Fatalf("provider delay ignored: revoked=%t error=%v attempts=%v", revoked, err, attempts)
						}
						c := store.credentials(t)
						if c.Accounts[0].RefreshToken != "" || c.Accounts[0].DisconnectID != "" || c.Accounts[1].RefreshToken != "b-refresh" {
							t.Fatal("bounded revocation failed cleanup or changed another registration")
						}
					})
				})
			}
		}
	}
}
