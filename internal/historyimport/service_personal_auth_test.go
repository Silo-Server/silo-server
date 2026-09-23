package historyimport

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
	svc := &Service{jellyfin: NewJellyfinClient()}
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
	svc := &Service{}
	prepared, err := svc.preparePersonalRun(t.Context(), 7, CreateRunInput{Source: SourceTypePlex, ProfileID: "target", PlexBaseURL: "https://plex.example.test", PlexToken: "server-token", PlexAccountToken: "account-token"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Credentials.ServerToken != "server-token" || prepared.Credentials.AccountToken != "account-token" {
		t.Fatal("Plex server/account credentials conflated")
	}
}
func TestPersonalPreparationDoesNotLockSourceOverNetwork(t *testing.T) {
	repo := editorRepository(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"AccessToken":"server-token","User":{"Id":"external"}}`))
	}))
	defer upstream.Close()
	source, err := repo.CreateSource(t.Context(), CreateSourceInput{Name: "Before", SourceType: SourceTypeEmby, BaseURL: upstream.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{repo: repo, emby: NewEmbyClient()}
	type result struct {
		value personalRunAdmission
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := svc.preparePersonalRun(t.Context(), 1, CreateRunInput{Source: SourceTypeEmby, ProfileID: "p1", SourceID: source.ID, Username: "user", Password: "password"})
		done <- result{value, err}
	}()
	select {
	case <-entered:
	case <-t.Context().Done():
		t.Fatal("authentication never started")
	}
	updated, err := repo.UpdateSourceConditional(t.Context(), source.ID, UpdateSourceInput{Name: new("After")}, source.Revision)
	close(release)
	got := <-done
	if err != nil || got.err != nil {
		t.Fatal(err, got.err)
	}
	if got.value.SourceRevision != source.Revision || updated.Revision == source.Revision {
		t.Fatal("preparation did not retain original source revision")
	}
}
func TestPersonalPreparationDoesNotConsumePlexSession(t *testing.T) {
	repo := editorRepository(t)
	if _, err := repo.pool.Exec(t.Context(), `CREATE TABLE history_import_plex_sessions(LIKE public.history_import_plex_sessions INCLUDING ALL)`); err != nil {
		t.Fatal(err)
	}
	session, err := repo.CreatePlexSession(t.Context(), PlexSession{ID: "prepare-plex", UserID: 1, PinID: "pin", PinCode: "code", AuthToken: "account-token", Servers: []PlexServer{{ClientIdentifier: "server", RemoteURL: "https://plex.example.test", AccessToken: "server-token"}}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{repo: repo}
	prepared, err := svc.preparePersonalRun(t.Context(), 1, CreateRunInput{Source: SourceTypePlex, ProfileID: "p1", PlexSessionID: session.ID, PlexServerID: "server"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.PlexSession == nil || prepared.SelectedServerID != "server" || prepared.Credentials.AccountToken != "account-token" || prepared.Credentials.ServerToken != "server-token" {
		t.Fatal("selected Plex session identity lost")
	}
	stillAvailable, err := repo.GetPlexSession(t.Context(), 1, session.ID)
	if err != nil || stillAvailable.ConsumedAt != nil {
		t.Fatal("preparation consumed a session before durable admission", err)
	}
}

func TestPersonalPreparationAllowsEmbyAccountWithoutPassword(t *testing.T) {
	repo := editorRepository(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["Username"] != "kid" || body["Pw"] != "" {
			t.Errorf("login body = %v, want kid with an empty password", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"AccessToken":"server-token","User":{"Id":"external-kid"}}`))
	}))
	defer upstream.Close()
	source, err := repo.CreateSource(t.Context(), CreateSourceInput{Name: "Emby", SourceType: SourceTypeEmby, BaseURL: upstream.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{repo: repo, emby: NewEmbyClient()}
	prepared, err := svc.preparePersonalRun(t.Context(), 1, CreateRunInput{Source: SourceTypeEmby, ProfileID: "p1", SourceID: source.ID, Username: "kid"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Credentials.ExternalUserID != "external-kid" || prepared.Credentials.ServerToken != "server-token" {
		t.Fatalf("credentials = %+v", prepared.Credentials)
	}
	if _, err := svc.preparePersonalRun(t.Context(), 1, CreateRunInput{Source: SourceTypeEmby, ProfileID: "p1", SourceID: source.ID, Username: " "}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("blank username err = %v, want ErrInvalidInput", err)
	}
}
