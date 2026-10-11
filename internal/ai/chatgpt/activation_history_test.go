package chatgpt

import (
	"net/http"
	"testing"
	"time"
)

func requireConnectionHistory(t *testing.T, store *memoryStore, want bool) {
	t.Helper()
	c := stored(t, store)
	if c.HasConnected != want {
		t.Fatalf("stored connection history=%t, want %t", c.HasConnected, want)
	}
}

func TestConnectionHistoryFromStoredActivation(t *testing.T) {
	for _, test := range []struct {
		name, raw string
		want      bool
	}{
		{name: "new installation"},
		{name: "retained failed registration", raw: `{"accounts":[{"client_id":"app","needs_login":true}]}`},
		{name: "accepted grant without activation", raw: `{"accounts":[{"client_id":"app","refresh_token":"refresh"}]}`},
		{name: "legacy active account", raw: `{"active":"app","accounts":[{"client_id":"app","refresh_token":"refresh"}]}`, want: true},
		{name: "legacy active account needing login", raw: `{"active":"app","accounts":[{"client_id":"app","needs_login":true}]}`, want: true},
		{name: "disconnected previous activation", raw: `{"has_connected":true,"accounts":[{"client_id":"app","needs_login":true}]}`, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryStore{values: map[string]string{CredentialsKey: test.raw}}
			requireConnectionHistory(t, store, test.want)
		})
	}
}

func TestFirstSelectionPersistsConnectionHistoryAcrossDisconnect(t *testing.T) {
	for _, unavailable := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(unavailable), func(t *testing.T) {
			store := &memoryStore{values: map[string]string{"ai.auth_mode": "chatgpt", "ai.chat_model": "old-model"}}
			save(t, store, credentials{Accounts: []account{{ClientID: "app", AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour)}}})
			s := NewService(store)
			s.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
				return response(unavailable, map[string]any{"models": []Model{}}), nil
			})
			requireConnectionHistory(t, store, false)
			if err := s.SelectAccount(t.Context(), "app"); err == nil {
				t.Fatal("failed catalog activated the first account")
			}
			requireConnectionHistory(t, store, false)
			s.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/v1/models":
					return response(200, map[string]any{"models": []map[string]string{{"slug": "gpt-4o-mini", "visibility": "list"}}}), nil
				case "/.well-known/openid-configuration":
					return response(200, map[string]string{"revocation_endpoint": "https://auth.openai.com/revoke"}), nil
				case "/revoke":
					return response(200, nil), nil
				default:
					t.Fatalf("unexpected provider path %s", r.URL.Path)
					return nil, ErrProvider
				}
			})
			transition, err := s.SelectAccountWithSettings(t.Context(), "app")
			if err != nil {
				t.Fatal(err)
			}
			if transition == nil || !transition.FirstConnection {
				t.Fatal("first successful selection did not own the welcome")
			}
			requireConnectionHistory(t, store, true)
			transition, err = s.SelectAccountWithSettings(t.Context(), "app")
			if err != nil || transition == nil || transition.FirstConnection {
				t.Fatalf("later selection repeated the welcome: %+v, %v", transition, err)
			}
			if _, err := s.Disconnect(t.Context(), "app"); err != nil {
				t.Fatal(err)
			}
			if c := stored(t, store); c.Active != "" || c.Accounts[0].RefreshToken != "" {
				t.Fatal("disconnect retained activation or credentials")
			}
			requireConnectionHistory(t, store, true)
		})
	}
}

func TestConcurrentFirstActivationsHaveOneWelcome(t *testing.T) {
	store := &memoryStore{values: map[string]string{"ai.auth_mode": "chatgpt", "ai.chat_model": "gpt-4o-mini"}}
	save(t, store, credentials{Accounts: []account{
		{ClientID: "a", AccessToken: "a-token", RefreshToken: "a-refresh", ExpiresAt: time.Now().Add(time.Hour)},
		{ClientID: "b", AccessToken: "b-token", RefreshToken: "b-refresh", ExpiresAt: time.Now().Add(time.Hour)},
	}})
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	type result struct {
		transition *SettingsTransition
		err        error
	}
	results := make(chan result, 2)
	for _, clientID := range []string{"a", "b"} {
		s := NewService(store)
		requireConnectionHistory(t, store, false)
		s.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
			entered <- struct{}{}
			<-release
			return response(200, map[string]any{"models": []map[string]string{{"slug": "gpt-4o-mini", "visibility": "list"}}}), nil
		})
		go func() {
			transition, err := s.SelectAccountWithSettings(t.Context(), clientID)
			results <- result{transition: transition, err: err}
		}()
	}
	for range 2 {
		<-entered
	}
	close(release)
	welcomeCount := 0
	for range 2 {
		got := <-results
		if got.err != nil || got.transition == nil {
			t.Fatalf("activation failed: %v", got.err)
		}
		if got.transition.FirstConnection {
			welcomeCount++
		}
	}
	if welcomeCount != 1 {
		t.Fatalf("concurrent first activations requested %d welcomes", welcomeCount)
	}
	requireConnectionHistory(t, store, true)
}
