package historyimport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func assertJellyfinAuthorization(t *testing.T, r *http.Request, token string) {
	t.Helper()

	want := `MediaBrowser Client="watch-importer", Device="Silo", DeviceId="silo-history-import", Version="1.0.0"`
	if token != "" {
		want += `, Token="` + token + `"`
	}
	if got := r.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
	if got := r.Header.Get("X-Emby-Authorization"); got != "" {
		t.Errorf("X-Emby-Authorization = %q, want empty", got)
	}
}

func TestJellyfinAuthenticateServerUser_UsesStandardAuthorizationHeader(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/Users/AuthenticateByName" {
			t.Fatalf("path = %q, want /Users/AuthenticateByName", got)
		}
		assertJellyfinAuthorization(t, r, "")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"AccessToken":"user-token","User":{"Id":"user-1"}}`))
	}))
	defer server.Close()

	client := NewJellyfinClient()
	auth, err := client.AuthenticateServerUser(trustLoopback(context.Background()), server.URL, "alice", "password")
	if err != nil {
		t.Fatalf("AuthenticateServerUser returned error: %v", err)
	}
	if auth.UserID != "user-1" || auth.AccessToken != "user-token" {
		t.Fatalf("auth = %+v, want user-1 with user-token", auth)
	}
}

// A rejected password must reach the caller as Jellyfin's 401 after exactly
// one sign-in attempt: Jellyfin 12 answers the old "/emby" prefix with 404,
// and older servers count a retried password as a second failed login.
func TestJellyfinAuthenticateServerUser_RejectedPasswordIsNotRetried(t *testing.T) {
	t.Parallel()

	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/Users/AuthenticateByName" {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "Error processing request.", http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := NewJellyfinClient().AuthenticateServerUser(trustLoopback(context.Background()), server.URL+"/", "alice", "wrong")
	if got := UpstreamHTTPStatus(err); got != http.StatusUnauthorized {
		t.Fatalf("UpstreamHTTPStatus = %d (err %v), want 401", got, err)
	}
	if len(paths) != 1 || paths[0] != "/Users/AuthenticateByName" {
		t.Fatalf("requested paths = %v, want one /Users/AuthenticateByName", paths)
	}
}

func TestJellyfinAuthenticateServerUser_ClassifiesUnexpectedResponses(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "html landing page", body: "<html><body>Not Jellyfin</body></html>"},
		{name: "incomplete json", body: `{"User":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			_, err := newUnthrottledJellyfinClient().AuthenticateServerUser(trustLoopback(t.Context()), server.URL, "alice", "pw")
			if !errors.Is(err, ErrUnexpectedSourceResponse) {
				t.Fatalf("err = %v, want ErrUnexpectedSourceResponse", err)
			}
			if IsReachabilityError(err) {
				t.Fatalf("unexpected response misclassified as reachability error: %v", err)
			}
		})
	}
}

// Local HTTP fixtures exercise decoding and paging without the production rate limit.
// Keep the guarded transport so address-policy checks still run.
func newUnthrottledJellyfinClient() *JellyfinClient {
	client := NewJellyfinClient()
	client.limiter = nil
	return client
}
