package historyimport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

func personalEffectRepository(t *testing.T) *Repository {
	t.Helper()
	repo := personalQueueRepository(t)
	for _, statement := range []string{
		`CREATE TABLE media_items(content_id text PRIMARY KEY,type text,title text,year integer,status text,imdb_id text,tmdb_id text,tvdb_id text);INSERT INTO media_items VALUES('movie','movie','Movie',2026,'matched','tt1234567',NULL,NULL)`,
		`CREATE TABLE user_history_hidden_items(user_id integer,profile_id text,media_item_id text,hidden_before timestamptz)`,
		`CREATE TABLE user_watch_progress(user_id integer,profile_id text,media_item_id text,position_seconds double precision,duration_seconds double precision,completed boolean,updated_at timestamptz,event_at timestamptz,last_file_id text,last_resolution text,last_hdr text,last_codec_video text,last_edition_key text,PRIMARY KEY(user_id,profile_id,media_item_id))`,
		`CREATE TABLE user_watch_history(id text PRIMARY KEY,user_id integer,profile_id text,media_item_id text,watched_at timestamptz,duration_seconds double precision,completed boolean,source text,watch_identity jsonb)`,
		`CREATE TABLE user_watchlist(user_id integer,profile_id text,media_item_id text,added_at timestamptz NOT NULL DEFAULT now(),sort_index integer,PRIMARY KEY(user_id,profile_id,media_item_id))`,
	} {
		if _, err := repo.pool.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

func waitPersonalTerminal(t *testing.T, observed <-chan Run, id string) Run {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case run := <-observed:
			if run.ID == id && (run.Status == RunStatusCompleted || run.Status == RunStatusFailed || run.Status == RunStatusCancelled) {
				return run
			}
		case <-timeout.C:
			t.Fatal("personal run never reached terminal state")
			return Run{}
		}
	}
}

func TestPersonalRunnerRestartsEveryAuthenticationPath(t *testing.T) {
	for _, path := range []string{"emby-predefined", "emby-connect", "jellyfin-password", "plex-browser", "plex-session", "plex-predefined"} {
		t.Run(path, func(t *testing.T) {
			repo := personalEffectRepository(t)
			var authCalls, fetchCalls, accountCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/Users/AuthenticateByName":
					if authCalls.Add(1) != 1 {
						t.Error("restart repeated password authentication")
						http.Error(w, "no reauthentication", http.StatusUnauthorized)
						return
					}
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["Pw"] != "password" {
						t.Error("password exchange missing")
					}
					_, _ = w.Write([]byte(`{"AccessToken":"server-token","User":{"Id":"external"}}`))
					return
				case "/Connect/Exchange":
					if authCalls.Add(1) != 1 || r.Header.Get("X-Emby-Token") != "access-key" {
						t.Error("invalid/repeated Connect exchange")
					}
					_, _ = w.Write([]byte(`{"AccessToken":"server-token","LocalUserId":"external"}`))
					return
				}
				fetchCalls.Add(1)
				if strings.HasPrefix(path, "plex") {
					if r.Header.Get("X-Plex-Token") != "server-token" {
						t.Error("PMS received wrong credential")
					}
					switch r.URL.Path {
					case "/library/sections":
						_, _ = w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"1","type":"movie"}]}}`))
					case "/library/sections/1/all":
						_, _ = w.Write([]byte(`{"MediaContainer":{"totalSize":1,"Metadata":[{"ratingKey":"external","type":"movie","title":"Movie","year":2026,"Guid":[{"id":"imdb://tt1234567"}],"duration":100000,"viewCount":1,"lastViewedAt":1788220800}]}}`))
					case "/library/onDeck":
						_, _ = w.Write([]byte(`{"MediaContainer":{"Metadata":[]}}`))
					default:
						t.Errorf("unexpected PMS path %s", r.URL.Path)
						http.NotFound(w, r)
					}
					return
				}
				if r.Header.Get("X-Emby-Token") != "server-token" && !strings.Contains(r.Header.Get("Authorization"), "server-token") {
					t.Error("Emby/Jellyfin received wrong credential")
				}
				if r.URL.Query().Get("Filters") == "IsPlayed" {
					_, _ = w.Write([]byte(`{"Items":[{"Id":"external","Name":"Movie","Type":"Movie","ProviderIds":{"Imdb":"tt1234567"},"RunTimeTicks":1000000000,"UserData":{"Played":true,"LastPlayedDate":"2026-09-01T00:00:00Z"}}]}`))
				} else {
					_, _ = w.Write([]byte(`{"Items":[]}`))
				}
			}))
			defer upstream.Close()
			discover := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				accountCalls.Add(1)
				if r.Header.Get("X-Plex-Token") != "account-token" {
					t.Error("watchlist received PMS credential")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"MediaContainer":{"totalSize":1,"Metadata":[{"ratingKey":"watchlisted","type":"movie","title":"Movie","year":2026,"Guid":[{"id":"imdb://tt1234567"}]}]}}`))
			}))
			defer discover.Close()
			input := CreateRunInput{ProfileID: "p"}
			switch path {
			case "jellyfin-password":
				input.Source = SourceTypeJellyfin
				input.JellyfinBaseURL = upstream.URL
				input.JellyfinUsername = "user"
				input.JellyfinPassword = "password"
			case "emby-predefined":
				input.Source = SourceTypeEmby
				input.SourceID = 1
				input.Username = "user"
				input.Password = "password"
			case "emby-connect":
				input.Source = SourceTypeEmby
				input.ConnectSessionID = "connect"
				input.ServerID = "server"
				_, err := repo.CreateConnectSession(t.Context(), ConnectSession{ID: "connect", UserID: 1, ConnectUserID: "connect-user", ConnectAccessToken: "connect-token", Servers: []ConnectServer{{ID: "server", URL: upstream.URL, AccessKey: "access-key"}}, ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
			case "plex-browser":
				input.Source = SourceTypePlex
				input.PlexBaseURL = upstream.URL
				input.PlexToken = "server-token"
				input.PlexAccountToken = "account-token"
			case "plex-predefined":
				input.Source = SourceTypePlex
				input.SourceID = 1
				input.PlexToken = "server-token"
				input.PlexAccountToken = "account-token"
			case "plex-session":
				input.Source = SourceTypePlex
				input.PlexSessionID = "plex"
				input.PlexServerID = "server"
				_, err := repo.CreatePlexSession(t.Context(), PlexSession{ID: "plex", UserID: 1, PinID: "pin", PinCode: "code", AuthToken: "account-token", Servers: []PlexServer{{ClientIdentifier: "server", RemoteURL: upstream.URL, AccessToken: "server-token"}}, ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
			}
			if input.SourceID > 0 {
				if _, err := repo.pool.Exec(t.Context(), `UPDATE history_import_sources SET base_url=$1,source_type=$2 WHERE id=1`, upstream.URL, input.Source); err != nil {
					t.Fatal(err)
				}
			}
			firstCtx, stopFirst := context.WithCancel(t.Context())
			first := NewService(firstCtx, repo, pgstore.NewPostgresProvider(repo.pool))
			run, err := first.CreateRun(t.Context(), 1, input)
			stopFirst()
			if err != nil {
				t.Fatal(err)
			}
			if run.Status != RunStatusQueued || fetchCalls.Load() != 0 {
				t.Fatal("admission executed provider")
			}
			if _, err = repo.pool.Exec(t.Context(), `DELETE FROM history_import_connect_sessions;DELETE FROM history_import_plex_sessions`); err != nil {
				t.Fatal(err)
			}
			// A new repository/service has no provider or login session from admission.
			restartedRepo := NewRepository(repo.pool, repo.cipher)
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			observed := make(chan Run, 16)
			// Two newly configured nodes compete for the same persisted intent.
			for range 2 {
				restarted := NewService(ctx, restartedRepo, pgstore.NewPostgresProvider(repo.pool))
				restarted.plex.discoverBaseURL = discover.URL
				// The upstream stand-in is loopback HTTP, which a profile OAuth
				// run refuses in production. TestProfilePlexRunsAreRestrictedByDefault
				// covers that the restriction is on when nothing relaxes it.
				restarted.allowPrivatePlexProfileDestinations = true
				restarted.SetStableIdentityResolver(watchstate.NewStableIdentityResolver(startupIdentityItems{}, nil, startupIdentityProviders{}))
				restarted.AddObserver(queueObserverFunc(func(run Run) { observed <- run }))
				restarted.StartBackgroundWork()
			}
			completed := waitPersonalTerminal(t, observed, run.ID)
			if completed.Status != RunStatusCompleted || completed.HistoryCreated != 1 || len(completed.Warnings) != 0 {
				t.Fatalf("restart result: %+v", completed)
			}
			var owner int
			var profile, item string
			var stamp time.Time
			var identity []byte
			if err = repo.pool.QueryRow(t.Context(), `SELECT user_id,profile_id,media_item_id,watched_at,watch_identity FROM user_watch_history`).Scan(&owner, &profile, &item, &stamp, &identity); err != nil {
				t.Fatal(err)
			}
			if owner != 1 || profile != "p" || item != "movie" || !stamp.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) || !strings.Contains(string(identity), "tt1234567") {
				t.Fatalf("wrong persisted target/history: %d %s %s %s %s", owner, profile, item, stamp, identity)
			}
			var generation, historyCount int
			if err = repo.pool.QueryRow(t.Context(), `SELECT claim_generation,(SELECT count(*) FROM user_watch_history) FROM history_import_runs WHERE id=$1`, run.ID).Scan(&generation, &historyCount); err != nil || generation != 1 || historyCount != 1 {
				t.Fatalf("competing nodes generation=%d history=%d %v", generation, historyCount, err)
			}
			var secrets int
			if err = repo.pool.QueryRow(t.Context(), `SELECT count(*) FROM history_import_run_credentials WHERE run_id=$1`, run.ID).Scan(&secrets); err != nil || secrets != 0 {
				t.Fatalf("terminal secrets=%d %v", secrets, err)
			}
			if strings.HasPrefix(path, "plex") {
				var n int
				if err = repo.pool.QueryRow(t.Context(), `SELECT count(*) FROM user_watchlist WHERE user_id=1 AND profile_id='p' AND media_item_id='movie'`).Scan(&n); err != nil || n != 1 || accountCalls.Load() == 0 {
					t.Fatalf("watchlist rows=%d calls=%d %v", n, accountCalls.Load(), err)
				}
			}
		})
	}
}

// Returned records must still be fenced when configuration changes while Fetch
// is in flight, even if a provider returns successfully after cancellation.
type heldPersonalProvider struct {
	started chan struct{}
	release chan struct{}
}

func (p heldPersonalProvider) Fetch(context.Context) ([]Record, []string, error) {
	close(p.started)
	<-p.release
	return []Record{{Kind: KindMovie, IMDbID: "tt1234567", Played: true, DurationSeconds: 100, LastPlayedAt: new(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))}}, nil, nil
}

func TestPersonalRunnerFencesEffectsAfterFetch(t *testing.T) {
	for _, change := range []string{"cancel", "source", "profile-delete"} {
		t.Run(change, func(t *testing.T) {
			repo := personalEffectRepository(t)
			in := directPersonalAdmission()
			in.SourceType = SourceTypeEmby
			in.ConnectionMode = ConnectionModePredefined
			in.SourceID = 1
			in.SourceRevision = 1
			in.Credentials.BaseURL = "http://example.test"
			run, err := repo.enqueuePersonalRun(t.Context(), in)
			if err != nil {
				t.Fatal(err)
			}
			claimed, claim, err := repo.claimQueuedRun(t.Context())
			if err != nil || claimed == nil || claimed.ID != run.ID {
				t.Fatal("claim", err)
			}
			service := NewService(t.Context(), repo, pgstore.NewPostgresProvider(repo.pool))
			provider := heldPersonalProvider{make(chan struct{}), make(chan struct{})}
			done := make(chan struct{})
			go func() { service.executeRunWithClaim(claimed, provider, claim, true); close(done) }()
			select {
			case <-provider.started:
			case <-time.After(5 * time.Second):
				t.Fatal("Fetch not entered")
			}
			remote := NewRepository(repo.pool, repo.cipher)
			switch change {
			case "cancel":
				err = remote.CancelRunIfActive(t.Context(), run.ID)
			case "source":
				_, err = repo.pool.Exec(t.Context(), `UPDATE history_import_sources SET revision=revision+1,base_url='http://changed.invalid' WHERE id=1`)
			case "profile-delete":
				_, err = repo.pool.Exec(t.Context(), `DELETE FROM user_profiles WHERE user_id=1 AND id='p'`)
			}
			close(provider.release)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("worker never exited")
			}
			var effects, secrets int
			if err = repo.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM user_watch_history)+(SELECT count(*) FROM user_watch_progress),(SELECT count(*) FROM history_import_run_credentials)`).Scan(&effects, &secrets); err != nil || effects != 0 || secrets != 0 {
				t.Fatalf("effects=%d secrets=%d %v", effects, secrets, err)
			}
			if change != "profile-delete" {
				got, err := repo.GetRunByID(t.Context(), run.ID)
				expected := RunStatusFailed
				if change == "cancel" {
					expected = RunStatusCancelled
				}
				if err != nil || got.Status != expected {
					t.Fatalf("terminal=%+v %v", got, err)
				}
			}
		})
	}
}

func TestPersonalRunnerCapacityAndPreclaimSourceChange(t *testing.T) {
	repo := personalEffectRepository(t)
	in := directPersonalAdmission()
	in.SourceType = SourceTypeEmby
	in.ConnectionMode = ConnectionModePredefined
	in.SourceID = 1
	in.SourceRevision = 1
	in.Credentials.BaseURL = "http://example.test"
	run, err := repo.enqueuePersonalRun(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(t.Context(), repo, pgstore.NewPostgresProvider(repo.pool))
	service.runSemaphore = make(chan struct{}, 1)
	service.runSemaphore <- struct{}{}
	service.dispatchQueuedRuns()
	queued, err := repo.GetRunByID(t.Context(), run.ID)
	if err != nil || queued.Status != RunStatusQueued {
		t.Fatalf("claimed without capacity: %+v %v", queued, err)
	}
	if _, err = repo.pool.Exec(t.Context(), `UPDATE history_import_sources SET enabled=false WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	observed := make(chan Run, 8)
	service.AddObserver(queueObserverFunc(func(run Run) { observed <- run }))
	<-service.runSemaphore
	service.dispatchQueuedRuns()
	failed := waitPersonalTerminal(t, observed, run.ID)
	if failed.Status != RunStatusFailed || failed.ErrorMessage != ErrRunConfigurationChanged.Error() {
		t.Fatalf("unexpected preclaim failure: %+v", failed)
	}
}
