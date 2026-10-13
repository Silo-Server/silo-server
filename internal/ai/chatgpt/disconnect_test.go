package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// committedStore serializes writers while readers see only committed values,
// matching PostgreSQL readers that do not acquire the settings advisory lock.
type committedStore struct {
	writes     sync.Mutex
	mu         sync.Mutex
	value      string
	failCommit bool
}

func (m *committedStore) Get(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if key != CredentialsKey {
		return "", nil
	}
	return m.value, nil
}

func (m *committedStore) UpdateValueAtomic(ctx context.Context, _ string, update func(string) (string, error)) error {
	m.writes.Lock()
	defer m.writes.Unlock()
	raw, _ := m.Get(ctx, CredentialsKey)
	next, err := update(raw)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failCommit {
		m.failCommit = false
		return errors.New("interrupted commit")
	}
	m.value = next
	return nil
}

func (m *committedStore) credentials(t *testing.T) credentials {
	t.Helper()
	raw, err := m.Get(t.Context(), CredentialsKey)
	if err != nil {
		t.Fatal(err)
	}
	c, err := decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newDisconnectStore(t *testing.T) *committedStore {
	t.Helper()
	c := credentials{Active: "a", HostID: "host", Accounts: []account{
		{ClientID: "a", Subject: "user-a", AccessToken: "a-token", RefreshToken: "a-refresh", IDToken: "identity", Scopes: planScope, ExpiresAt: time.Now().Add(time.Hour)},
		{ClientID: "b", Subject: "user-b", AccessToken: "b-token", RefreshToken: "b-refresh", Scopes: planScope, ExpiresAt: time.Now().Add(time.Hour)},
	}}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return &committedStore{value: string(raw)}
}

func TestDisconnectWithoutTokenDoesNotConfirmRevocation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newDisconnectStore(t)
		service := NewService(store)
		calls := 0
		service.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.String() == service.discoveryURL {
				return response(http.StatusOK, discovery{JWKSURI: issuer + "/keys", RevocationEndpoint: issuer + "/revoke"}), nil
			}
			return response(http.StatusServiceUnavailable, nil), nil
		})
		confirmed, err := service.Disconnect(t.Context(), "a")
		if err != nil || confirmed || calls != 4 {
			t.Fatalf("failed remote revocation: confirmed=%t error=%v calls=%d", confirmed, err, calls)
		}
		if store.credentials(t).Accounts[0].RefreshToken != "" {
			t.Fatal("local cleanup retained token")
		}
		confirmed, err = service.Disconnect(t.Context(), "a")
		if err != nil || confirmed || calls != 4 {
			t.Fatalf("empty token falsely confirmed revocation: confirmed=%t error=%v calls=%d", confirmed, err, calls)
		}
	})
}

func TestDisconnectCommitsDeactivationBeforeProviderCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newDisconnectStore(t)
		s, other := NewService(store), NewService(store)
		pinned, err := other.TokenSource(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		releaseProvider := sync.OnceFunc(func() { close(release) })
		defer releaseProvider()
		s.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() == s.discoveryURL {
				close(entered)
				<-release
				return response(http.StatusOK, discovery{JWKSURI: issuer + "/keys", RevocationEndpoint: issuer + "/revoke"}), nil
			}
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("token") != "a-refresh" {
				t.Error("revocation lost departing token")
			}
			return response(http.StatusOK, nil), nil
		})
		go func() { _, err := s.Disconnect(t.Context(), "a"); done <- err }()
		<-entered
		c := store.credentials(t)
		a := c.Accounts[0]
		if c.Active != "" || !a.NeedsLogin || a.AccessToken != "" || a.IDToken != "" || a.Scopes != "" || !a.ExpiresAt.IsZero() || a.RefreshToken != "a-refresh" || a.DisconnectID == "" {
			t.Fatal("disabled state did not commit before provider discovery")
		}
		for _, source := range []func(context.Context) (string, error){pinned, other.BearerToken} {
			token, err := source(t.Context())
			if token != "" || !errors.Is(err, ErrSignInRequired) {
				t.Errorf("disconnect still supplies token: %q, %v", token, err)
			}
		}
		if err := other.RejectToken(t.Context(), "a-token", false); err != nil {
			t.Fatal(err)
		}
		if !store.credentials(t).Accounts[0].NeedsLogin {
			t.Fatal("late rejection reenabled account")
		}
		// Selecting a different registration commits while the provider is held.
		if err := other.SelectAccount(t.Context(), "b"); err != nil {
			t.Fatal(err)
		}
		login, err := other.StartLogin(t.Context(), "a")
		if err != nil {
			t.Fatal(err)
		}
		releaseProvider()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		c = store.credentials(t)
		if c.Active != "b" || c.Accounts[0].RefreshToken != "" || c.Accounts[0].DisconnectID != "" || c.Accounts[1].AccessToken != "b-token" || c.Pending == nil || c.Pending.State != login.AttemptID {
			t.Fatal("cleanup changed another selection or a newer login")
		}
	})
}

func TestDisconnectCleanupPreservesSuccessfulReconnect(t *testing.T) {
	store := newDisconnectStore(t)
	s, other := NewService(store), NewService(store)
	s.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == s.discoveryURL {
			return response(http.StatusOK, discovery{JWKSURI: issuer + "/keys", RevocationEndpoint: issuer + "/revoke"}), nil
		}
		// Emulate the verified login commit replacing the same registration.
		err := other.mutate(t.Context(), func(c *credentials) error {
			c.Accounts[0] = account{ClientID: "a", Subject: "user-a", AccessToken: "new-access", RefreshToken: "new-refresh", Scopes: planScope, ExpiresAt: time.Now().Add(time.Hour)}
			c.Active = "a"
			c.LoginResult = loginConnected
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return response(http.StatusOK, nil), nil
	})
	revoked, err := s.Disconnect(t.Context(), "a")
	if err != nil || !revoked {
		t.Fatalf("disconnect: %v, %v", revoked, err)
	}
	c := store.credentials(t)
	if c.Active != "a" || c.Accounts[0].AccessToken != "new-access" || c.Accounts[0].RefreshToken != "new-refresh" || c.Accounts[0].NeedsLogin || c.LoginResult != loginConnected {
		t.Fatal("late cleanup erased successful reconnect")
	}
}

func TestDisconnectCommitFailureKeepsAccountDisabledAndRetryable(t *testing.T) {
	for _, before := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleanup", true: "deactivation"}[before], func(t *testing.T) {
			store := newDisconnectStore(t)
			store.failCommit = before
			s := NewService(store)
			calls := 0
			s.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() == s.discoveryURL {
					return response(http.StatusOK, discovery{JWKSURI: issuer + "/keys", RevocationEndpoint: issuer + "/revoke"}), nil
				}
				store.mu.Lock()
				store.failCommit = true
				store.mu.Unlock()
				return response(http.StatusOK, nil), nil
			})
			_, err := s.Disconnect(t.Context(), "a")
			if err == nil {
				t.Fatal("lost cleanup commit was reported as successful")
			}
			c := store.credentials(t)
			if before {
				if calls != 0 || c.Active != "a" || c.Accounts[0].NeedsLogin {
					t.Fatal("provider called before deactivation committed")
				}
				return
			}
			if !c.Accounts[0].NeedsLogin || c.Accounts[0].RefreshToken != "a-refresh" || c.Accounts[0].AccessToken != "" || c.Active != "" {
				t.Fatal("cleanup failure reenabled account or lost retry token")
			}
			s.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() == s.discoveryURL {
					return response(http.StatusOK, discovery{JWKSURI: issuer + "/keys", RevocationEndpoint: issuer + "/revoke"}), nil
				}
				return response(http.StatusOK, nil), nil
			})
			if revoked, err := s.Disconnect(t.Context(), "a"); err != nil || !revoked {
				t.Fatalf("retry: %v, %v", revoked, err)
			}
			if c := store.credentials(t); c.Accounts[0].RefreshToken != "" || c.Accounts[0].DisconnectID != "" {
				t.Fatal("retry did not clean retained token")
			}
		})
	}
}

func TestOlderDisconnectCannotClearNewerRetryCredentials(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newDisconnectStore(t)
		first, second := NewService(store), NewService(store)
		firstEntered, secondEntered := make(chan struct{}), make(chan struct{})
		firstRelease, secondRelease := make(chan struct{}), make(chan struct{})
		releaseFirst := sync.OnceFunc(func() { close(firstRelease) })
		releaseSecond := sync.OnceFunc(func() { close(secondRelease) })
		defer releaseFirst()
		defer releaseSecond()
		transport := func(entered, release chan struct{}) transportFunc {
			return transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() == first.discoveryURL {
					return response(http.StatusOK, discovery{JWKSURI: issuer + "/keys", RevocationEndpoint: issuer + "/revoke"}), nil
				}
				close(entered)
				<-release
				return response(http.StatusOK, nil), nil
			})
		}
		first.http.Transport = transport(firstEntered, firstRelease)
		second.http.Transport = transport(secondEntered, secondRelease)
		firstDone, secondDone := make(chan error, 1), make(chan error, 1)
		go func() { _, err := first.Disconnect(t.Context(), "a"); firstDone <- err }()
		<-firstEntered
		firstID := store.credentials(t).Accounts[0].DisconnectID
		go func() { _, err := second.Disconnect(t.Context(), "a"); secondDone <- err }()
		<-secondEntered
		secondID := store.credentials(t).Accounts[0].DisconnectID
		if secondID == "" || firstID == secondID {
			t.Fatal("newer disconnect did not supersede operation")
		}
		releaseFirst()
		if err := <-firstDone; err != nil {
			t.Fatal(err)
		}
		a := store.credentials(t).Accounts[0]
		if a.DisconnectID != secondID || a.RefreshToken != "a-refresh" || !a.NeedsLogin {
			t.Fatal("old cleanup erased newer revocation credentials")
		}
		releaseSecond()
		if err := <-secondDone; err != nil {
			t.Fatal(err)
		}
		a = store.credentials(t).Accounts[0]
		if a.RefreshToken != "" || a.DisconnectID != "" {
			t.Fatal("newer disconnect did not clean up")
		}
	})
}
