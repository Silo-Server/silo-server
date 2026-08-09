package historyimport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func assertJellyfinAuthorization(t *testing.T, r *http.Request, token string) {
	t.Helper()

	want := `MediaBrowser Client="watch-importer", Device="Silo", DeviceId="silo-history-import", Version="1.0.0"`
	if token != "" {
		want += `, Token="` + token + `"`
	}
	if got := r.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization = %q, want %q", got, want)
	}
	if got := r.Header.Get("X-Emby-Authorization"); got != "" {
		t.Fatalf("X-Emby-Authorization = %q, want empty", got)
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
	auth, err := client.AuthenticateServerUser(context.Background(), server.URL, "alice", "password")
	if err != nil {
		t.Fatalf("AuthenticateServerUser returned error: %v", err)
	}
	if auth.UserID != "user-1" || auth.AccessToken != "user-token" {
		t.Fatalf("auth = %+v, want user-1 with user-token", auth)
	}
}

func TestJellyfinListUsers_UsesStandardAuthorizationHeader(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/Users" {
			t.Fatalf("path = %q, want /Users", got)
		}
		assertJellyfinAuthorization(t, r, "admin-token")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"Id":"user-1","Name":"Alice"},{"Id":"","Name":"Missing ID"}]`))
	}))
	defer server.Close()

	client := NewJellyfinClient()
	users, err := client.ListUsers(context.Background(), server.URL, "admin-token")
	if err != nil {
		t.Fatalf("ListUsers returned error: %v", err)
	}
	if len(users) != 1 || users[0].ID != "user-1" || users[0].Name != "Alice" {
		t.Fatalf("users = %+v, want Alice (user-1)", users)
	}
}

func TestJellyfinFetchResumableItems_IncludesExpectedQueryAndPaginates(t *testing.T) {
	t.Parallel()

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assertJellyfinAuthorization(t, r, "token-1")

		if got := r.URL.Path; got != "/UserItems/Resume" {
			t.Fatalf("path = %q, want /UserItems/Resume", got)
		}
		if got := r.URL.Query().Get("UserId"); got != "user-1" {
			t.Fatalf("UserId = %q, want user-1", got)
		}
		if got := r.URL.Query().Get("IncludeItemTypes"); got != "Movie,Episode" {
			t.Fatalf("IncludeItemTypes = %q, want Movie,Episode", got)
		}
		if got := r.URL.Query().Get("Fields"); got == "" {
			t.Fatal("expected Fields query param")
		}
		if got := r.URL.Query().Get("Limit"); got != strconv.Itoa(jellyfinPageSize) {
			t.Fatalf("Limit = %q, want %d", got, jellyfinPageSize)
		}

		startIndex := r.URL.Query().Get("StartIndex")
		response := jellyfinItemsResponse{TotalRecordCount: jellyfinPageSize + 1}
		switch startIndex {
		case "0":
			response.Items = make([]jellyfinItem, jellyfinPageSize)
		case strconv.Itoa(jellyfinPageSize):
			response.Items = []jellyfinItem{{ID: "resume-last", Type: "Movie"}}
		default:
			t.Fatalf("unexpected StartIndex %q", startIndex)
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer server.Close()

	client := NewJellyfinClient()
	auth := jellyfinLocalAuth{BaseURL: server.URL, UserID: "user-1", AccessToken: "token-1"}

	items, err := client.FetchResumableItems(context.Background(), auth)
	if err != nil {
		t.Fatalf("FetchResumableItems returned error: %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if len(items) != jellyfinPageSize+1 {
		t.Fatalf("len(items) = %d, want %d", len(items), jellyfinPageSize+1)
	}
}

func TestJellyfinFetchItems_PaginatesPlayedItems(t *testing.T) {
	t.Parallel()

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assertJellyfinAuthorization(t, r, "token-1")

		if got := r.URL.Path; got != "/Users/user-1/Items" {
			t.Fatalf("path = %q, want /Users/user-1/Items", got)
		}
		if got := r.URL.Query().Get("Filters"); got != "IsPlayed" {
			t.Fatalf("Filters = %q, want IsPlayed", got)
		}
		if got := r.URL.Query().Get("Recursive"); got != "true" {
			t.Fatalf("Recursive = %q, want true", got)
		}
		if got := r.URL.Query().Get("Limit"); got != strconv.Itoa(jellyfinPageSize) {
			t.Fatalf("Limit = %q, want %d", got, jellyfinPageSize)
		}

		startIndex := r.URL.Query().Get("StartIndex")
		response := jellyfinItemsResponse{TotalRecordCount: jellyfinPageSize + 1}
		switch startIndex {
		case "0":
			response.Items = make([]jellyfinItem, jellyfinPageSize)
		case strconv.Itoa(jellyfinPageSize):
			response.Items = []jellyfinItem{{ID: "played-last", Type: "Movie"}}
		default:
			t.Fatalf("unexpected StartIndex %q", startIndex)
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer server.Close()

	client := NewJellyfinClient()
	auth := jellyfinLocalAuth{BaseURL: server.URL, UserID: "user-1", AccessToken: "token-1"}

	items, err := client.FetchItems(context.Background(), auth, "IsPlayed")
	if err != nil {
		t.Fatalf("FetchItems returned error: %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if len(items) != jellyfinPageSize+1 {
		t.Fatalf("len(items) = %d, want %d", len(items), jellyfinPageSize+1)
	}
}

func TestJellyfinHTTPErrorUsesJellyfinBranding(t *testing.T) {
	t.Parallel()

	err := (&jellyfinHTTPError{StatusCode: http.StatusUnauthorized}).Error()
	if err != "jellyfin http 401" {
		t.Fatalf("error = %q, want jellyfin branding", err)
	}
}
