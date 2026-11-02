package historyimport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
