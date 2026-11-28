package historyimport

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestPersonalPreparationExchangesPasswordForToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Users/AuthenticateByName" {
			http.NotFound(w, r)
			return
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["Pw"] != "input-password" {
			t.Error("password exchange was not performed")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"AccessToken":"returned-token","User":{"Id":"external-user"}}`))
	}))
	defer upstream.Close()
	// The upstream listens on loopback, so the admin must allow local servers.
	svc := &Service{jellyfin: NewJellyfinClient(), localNetwork: allowLocalNetworkForEveryone()}
	prepared, err := svc.preparePersonalRun(t.Context(), 7, CreateRunInput{Source: SourceTypeJellyfin, ProfileID: "target", JellyfinBaseURL: upstream.URL, JellyfinUsername: "username", JellyfinPassword: "input-password"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.UserID != 7 || prepared.ProfileID != "target" || prepared.Credentials.ExternalUserID != "external-user" || prepared.Credentials.ServerToken != "returned-token" {
		t.Fatalf("wrong resolved target/credential")
	}
	encoded, err := json.Marshal(prepared.Credentials)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "input-password") || strings.Contains(string(encoded), "username") {
		t.Fatal("raw login retained in credential envelope")
	}
}
func TestPersonalPreparationPlexTokensStayDistinct(t *testing.T) {
	// Preparation checks and stores this address without sending a request.
	// A numeric public address keeps the test independent of DNS.
	svc := &Service{}
	prepared, err := svc.preparePersonalRun(t.Context(), 7, CreateRunInput{Source: SourceTypePlex, ProfileID: "target", PlexBaseURL: "https://8.8.8.8", PlexToken: "server-token", PlexAccountToken: "account-token"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Credentials.ServerToken != "server-token" || prepared.Credentials.AccountToken != "account-token" {
		t.Fatal("Plex server/account credentials conflated")
	}
}

func TestPersonalPreparationCustomEmbyAuthenticatesAndTrims(t *testing.T) {
	var (
		mu      sync.Mutex
		gotBody map[string]string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users/AuthenticateByName":
			http.NotFound(w, r)
		case "/emby/Users/AuthenticateByName":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			mu.Lock()
			gotBody = body
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"AccessToken":"emby-token","User":{"Id":"emby-user-1"}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	svc := &Service{emby: NewEmbyClient(), localNetwork: allowLocalNetworkForEveryone()}
	prepared, err := svc.preparePersonalRun(t.Context(), 7, CreateRunInput{
		Source:    SourceTypeEmby,
		ProfileID: "target",
		ServerURL: "  " + upstream.URL + "/  ",
		Username:  "  alice  ",
		Password:  "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ConnectionMode != ConnectionModeCustom {
		t.Fatalf("ConnectionMode = %q, want %q", prepared.ConnectionMode, ConnectionModeCustom)
	}
	if prepared.Credentials.BaseURL != upstream.URL+"/emby" ||
		prepared.Credentials.ExternalUserID != "emby-user-1" ||
		prepared.Credentials.ServerToken != "emby-token" {
		t.Fatalf("unexpected credentials: %+v", prepared.Credentials)
	}
	mu.Lock()
	username, password := gotBody["Username"], gotBody["Pw"]
	mu.Unlock()
	if username != "alice" || password != "" {
		t.Fatalf("auth payload = (%q, %q), want trimmed username and empty password", username, password)
	}
	encoded, err := json.Marshal(prepared.Credentials)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "alice") {
		t.Fatal("raw login retained in credential envelope")
	}
}

func TestPersonalPreparationRejectsMixedOrMismatchedEmbyBaseURL(t *testing.T) {
	svc := &Service{emby: NewEmbyClient(), jellyfin: NewJellyfinClient(), localNetwork: allowLocalNetworkForEveryone()}
	cases := []struct {
		name  string
		input CreateRunInput
	}{
		{
			name: "emby server_url with source_id",
			input: CreateRunInput{
				Source: SourceTypeEmby, ProfileID: "target",
				ServerURL: "https://emby.example.test", SourceID: 1, Username: "alice",
			},
		},
		{
			name: "emby server_url with connect_session_id",
			input: CreateRunInput{
				Source: SourceTypeEmby, ProfileID: "target",
				ServerURL: "https://emby.example.test", ConnectSessionID: "sess-1", ServerID: "srv-1", Username: "alice",
			},
		},
		{
			name: "emby whitespace-only server_url",
			input: CreateRunInput{
				Source: SourceTypeEmby, ProfileID: "target",
				ServerURL: "   ", Username: "alice",
			},
		},
		{
			name: "jellyfin with emby_base_url",
			input: CreateRunInput{
				Source: SourceTypeJellyfin, ProfileID: "target",
				ServerURL: "https://emby.example.test", JellyfinBaseURL: "https://jf.example.test",
				JellyfinUsername: "alice", JellyfinPassword: "pw",
			},
		},
		{
			name: "plex with emby_base_url",
			input: CreateRunInput{
				Source: SourceTypePlex, ProfileID: "target",
				ServerURL: "https://emby.example.test", PlexBaseURL: "https://8.8.8.8", PlexToken: "tok",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.preparePersonalRun(t.Context(), 7, tc.input)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestPersonalPreparationValidatesServerURLBeforeDialing(t *testing.T) {
	svc := &Service{emby: NewEmbyClient(), jellyfin: NewJellyfinClient(), localNetwork: allowLocalNetworkForEveryone()}
	for _, tc := range []struct {
		name      string
		input     CreateRunInput
		wantField string
	}{
		{
			name:      "emby missing scheme",
			input:     CreateRunInput{Source: SourceTypeEmby, ProfileID: "target", ServerURL: "emby.example.test", Username: "alice"},
			wantField: "Emby server URL",
		},
		{
			name:      "emby hostless port",
			input:     CreateRunInput{Source: SourceTypeEmby, ProfileID: "target", ServerURL: "http://:8096", Username: "alice"},
			wantField: "Emby server URL",
		},
		{
			name:      "emby bare fragment",
			input:     CreateRunInput{Source: SourceTypeEmby, ProfileID: "target", ServerURL: "https://emby.example.test#", Username: "alice"},
			wantField: "Emby server URL",
		},
		{
			name:      "jellyfin missing scheme",
			input:     CreateRunInput{Source: SourceTypeJellyfin, ProfileID: "target", JellyfinBaseURL: "jellyfin.example.test", JellyfinUsername: "alice", JellyfinPassword: "pw"},
			wantField: "Jellyfin server URL",
		},
		{
			name:      "jellyfin userinfo",
			input:     CreateRunInput{Source: SourceTypeJellyfin, ProfileID: "target", JellyfinBaseURL: "https://user:pass@jellyfin.example.test", JellyfinUsername: "alice", JellyfinPassword: "pw"},
			wantField: "Jellyfin server URL",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.preparePersonalRun(t.Context(), 7, tc.input)
			if !errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrSourceUnreachable) {
				t.Fatalf("err = %v, want ErrInvalidInput (not unreachable)", err)
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Fatalf("err = %q, want field name %s", err.Error(), tc.wantField)
			}
		})
	}
}
