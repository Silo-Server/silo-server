package chatgpt

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

// Readers see committed settings while both mutation paths share admission,
// as in the production settings repository.
type loginAdmissionStore struct {
	*memoryStore
	slot chan struct{}
}

func (s *loginAdmissionStore) acquire(ctx context.Context) error {
	select {
	case s.slot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *loginAdmissionStore) UpdateValueAtomic(ctx context.Context, key string, update func(string) (string, error)) error {
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer func() { <-s.slot }()
	raw, err := s.Get(ctx, key)
	if err != nil {
		return err
	}
	next, err := update(raw)
	if err != nil {
		return err
	}
	// Production gives a completed mutation another five seconds to commit,
	// independently of its caller's deadline.
	time.Sleep(5 * time.Second)
	return s.memoryStore.UpdateValueAtomic(context.WithoutCancel(ctx), key, func(string) (string, error) { return next, nil })
}

func (s *loginAdmissionStore) UpdateValueAndSettingsAtomic(ctx context.Context, key string, update func(map[string]string) (map[string]string, error)) error {
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer func() { <-s.slot }()
	return s.memoryStore.UpdateValueAndSettingsAtomic(ctx, key, update)
}

func TestLoginPersistsAcceptedTokensAfterConcurrentRefresh(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name                 string
		timeout, slowCatalog bool
	}{
		{name: "rotation commits"},
		{name: "rotation deadline expires", timeout: true},
		{name: "catalog and rotation delay", slowCatalog: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newIdentityRetryFixture(t, key)
				c := stored(t, f.store)
				c.Accounts[0].ExpiresAt = time.Now().Add(-time.Minute)
				save(t, f.store, c)
				store := &loginAdmissionStore{memoryStore: f.store, slot: make(chan struct{}, 1)}
				f.service.store = store
				refreshService := NewService(store)
				refreshService.http.Timeout = 0
				started := make(chan struct{})
				refreshDone := make(chan error, 1)
				refreshService.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
					close(started)
					if test.timeout {
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
					select {
					case <-time.After(59 * time.Second):
						return response(http.StatusOK, tokenResponse{AccessToken: "rotated-access", RefreshToken: "rotated-refresh", Scope: planScope, TokenType: "Bearer", ExpiresIn: 3600}), nil
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
				})
				f.transport(t, nil)
				loginTransport := f.service.http.Transport
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				catalogCalls := 0
				f.service.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path == "/v1/models" {
						catalogCalls++
						if test.slowCatalog && catalogCalls == 1 {
							resp := response(http.StatusTooManyRequests, nil)
							resp.Header.Set("Retry-After", "59")
							return resp, nil
						}
						go func() {
							_, err := refreshService.BearerToken(t.Context())
							refreshDone <- err
						}()
						<-started
						cancel() // An abandoned browser must not discard the accepted grant.
					}
					return loginTransport.RoundTrip(r)
				})
				loginErr := f.service.CompleteLogin(ctx, f.callback)
				refreshErr := <-refreshDone
				if loginErr != nil {
					t.Fatalf("accepted login lost while waiting for refresh admission: %v", loginErr)
				}
				if (refreshErr != nil) != test.timeout {
					t.Fatalf("refresh error=%v, want timeout=%t", refreshErr, test.timeout)
				}
				c = stored(t, f.store)
				if c.Active != "issued-app" || c.LoginResult != loginConnected || c.Pending != nil || len(c.Accounts) != 2 || c.Accounts[1].RefreshToken != f.tokens.RefreshToken || c.Accounts[1].AccessToken != f.tokens.AccessToken {
					t.Fatal("accepted login was not persisted after the concurrent refresh")
				}
				if !test.timeout && c.Accounts[0].RefreshToken != "rotated-refresh" {
					t.Fatal("login overwrote the other account's committed rotation")
				}
				if len(f.calls["/api/accounts/oauth/token"]) != 1 {
					t.Fatal("login repeated its authorization-code exchange")
				}
				if !c.Accounts[1].ExpiresAt.Equal(f.calls["/api/accounts/oauth/token"][0].Add(time.Hour)) {
					t.Fatal("catalog or lock wait extended the issued token's expiry")
				}
				if err := f.service.CompleteLogin(t.Context(), f.callback); !errors.Is(err, ErrInvalidLogin) {
					t.Fatalf("accepted callback was reusable: %v", err)
				}
			})
		})
	}
}
