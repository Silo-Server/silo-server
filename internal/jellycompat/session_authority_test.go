package jellycompat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/clientip"
)

// authoritySessionRepo adds a read counter and inert failures to the shared
// row fake. All schedules below are finite and synchronous.
type authoritySessionRepo struct {
	rowSessionRepo
	reads   int
	readErr error
}

func (r *authoritySessionRepo) GetByToken(ctx context.Context, token string, now time.Time) (*Session, error) {
	r.reads++
	if r.readErr != nil {
		return nil, r.readErr
	}
	return r.rowSessionRepo.GetByToken(ctx, token, now)
}

func TestPersistentLookupObservesTokenLogoutAcrossStores(t *testing.T) {
	repo := &authoritySessionRepo{rowSessionRepo: rowSessionRepo{rows: map[string]Session{}}}
	logins := &loginSessionsFake{live: map[string]bool{"login": true}}
	newStore := func() *SessionStore {
		return NewPersistentSessionStore(time.Hour, fixedNow, repo).WithLoginSessions(logins)
	}
	writer, replica := newStore(), newStore()
	for _, session := range []Session{
		{Token: "logout-fixture", StreamAppUserID: 1, ProfileID: "profile-a", LoginSessionID: "login"},
		{Token: "same-profile-fixture", StreamAppUserID: 1, ProfileID: "profile-a"},
		{Token: "other-profile-fixture", StreamAppUserID: 1, ProfileID: "profile-b"},
		{Token: "other-account-fixture", StreamAppUserID: 2, ProfileID: "profile-c"},
	} {
		if err := writer.Put(session); err != nil {
			t.Fatal(err)
		}
		if _, err := replica.Lookup(t.Context(), session.Token); err != nil {
			t.Fatal(err)
		}
	}
	writer.Delete("logout-fixture")
	// No eviction event is delivered. A new store also models reconnecting
	// through a replica that has never seen this session.
	for _, store := range []*SessionStore{writer, replica, newStore()} {
		if _, err := store.Lookup(t.Context(), "logout-fixture"); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("lookup after durable deletion = %v, want ErrSessionNotFound", err)
		}
		if _, cached := store.sessions["logout-fixture"]; cached {
			t.Fatal("missing row left a cached copy")
		}
		for _, token := range []string{"same-profile-fixture", "other-profile-fixture", "other-account-fixture"} {
			if _, err := store.Lookup(t.Context(), token); err != nil {
				t.Fatalf("unrelated session ended: %v", err)
			}
		}
	}
	if !logins.live["login"] || repo.deletes != 1 || len(repo.rows) != 3 {
		t.Fatal("token logout changed unrelated authority")
	}
}

func TestPersistentAuthRefusesDeletedOrUnavailableAuthority(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		for _, surface := range []string{"ordinary", "media token", "playback grant", "static grant", "socket reconnect"} {
			t.Run(surface+map[bool]string{false: "/deleted", true: "/unavailable"}[unavailable], func(t *testing.T) {
				repo := &authoritySessionRepo{rowSessionRepo: rowSessionRepo{rows: map[string]Session{}}}
				writer := NewPersistentSessionStore(time.Hour, fixedNow, repo)
				replica := NewPersistentSessionStore(time.Hour, fixedNow, repo)
				if err := writer.Put(Session{Token: "refusal-fixture"}); err != nil {
					t.Fatal(err)
				}
				if _, err := replica.Lookup(t.Context(), "refusal-fixture"); err != nil {
					t.Fatal(err)
				}
				wantCode := http.StatusUnauthorized
				if unavailable {
					repo.readErr = errors.New("inert repository read failure")
					wantCode = http.StatusServiceUnavailable
				} else {
					writer.Delete("refusal-fixture")
				}
				playback := NewPlaybackSessionStore(time.Hour, fixedNow)
				playback.PutNegotiated(PlaybackSession{
					ID: "grant-fixture", CompatToken: "refusal-fixture", RouteItemID: "item-fixture",
					ClientIP: "192.0.2.1", ClientPeer: "192.0.2.1", MediaSources: []PlaybackMediaSource{{ID: "source-fixture"}},
				})
				refuseHandler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
					t.Fatal("handler reached without authority")
				})
				var handler http.Handler
				path := "/Users/Me"
				withToken := true
				switch surface {
				case "ordinary":
					handler = (&Authenticator{sessions: replica, now: fixedNow}).RequireSession(refuseHandler)
				case "socket reconnect":
					handler = NewSocketHandler(replica, nil)
					path = "/socket"
				default:
					router := chi.NewRouter()
					router.With(PlaybackSessionAuth(replica, playback, nil)).Get("/Videos/{id}/stream", refuseHandler)
					handler = clientip.Middleware(clientip.NewResolver(nil))(router)
					path = "/Videos/item-fixture/stream"
					switch surface {
					case "playback grant":
						path += "?PlaySessionId=grant-fixture"
						withToken = false
					case "static grant":
						path += "?Static=true&MediaSourceId=source-fixture"
						withToken = false
					}
				}
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.RemoteAddr = "192.0.2.1:1234"
				if withToken {
					req.Header.Set("X-Emby-Token", "refusal-fixture")
				}
				before := repo.reads
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != wantCode || repo.reads != before+1 {
					t.Fatalf("status=%d reads=%d, want status=%d and one authority read", rec.Code, repo.reads-before, wantCode)
				}
				if unavailable && rec.Header().Get("Retry-After") == "" {
					t.Fatal("503 omitted existing retry guidance")
				}
				if unavailable && len(repo.rows) != 1 {
					t.Fatal("read outage deleted authority")
				}
			})
		}
	}
}

func TestPersistentLookupLateCacheInsertionCannotAuthorizeNextLookup(t *testing.T) {
	repo := &rowSessionRepo{rows: map[string]Session{}}
	writer := NewPersistentSessionStore(time.Hour, fixedNow, repo)
	replica := NewPersistentSessionStore(time.Hour, fixedNow, repo)
	if err := writer.Put(Session{Token: "late-fixture"}); err != nil {
		t.Fatal(err)
	}
	// Get has captured the row. Complete logout and a later refusal before
	// releasing that earlier read to insert its copy. No event is delivered.
	repo.afterGet = func() {
		repo.afterGet = nil
		writer.Delete("late-fixture")
		if _, err := replica.Lookup(t.Context(), "late-fixture"); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("lookup ordered after deletion = %v", err)
		}
	}
	if _, err := replica.Lookup(t.Context(), "late-fixture"); err != nil {
		t.Fatalf("authority read preceding logout may finish: %v", err)
	}
	if _, cached := replica.sessions["late-fixture"]; !cached {
		t.Fatal("test did not schedule a late cache insertion")
	}
	if _, err := replica.Lookup(t.Context(), "late-fixture"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("lookup after late insertion = %v, want ErrSessionNotFound", err)
	}
}

func TestPersistentExtensionAfterLogoutCannotRestoreRow(t *testing.T) {
	now := fixedNow()
	repo := &rowSessionRepo{rows: map[string]Session{}}
	store := NewPersistentSessionStore(time.Hour, func() time.Time { return now }, repo)
	if err := store.Put(Session{Token: "extension-fixture"}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(40 * time.Minute)
	repo.afterGet = func() {
		repo.afterGet = nil
		store.Delete("extension-fixture")
	}
	if _, err := store.Lookup(t.Context(), "extension-fixture"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("extension after deletion = %v, want ErrSessionNotFound", err)
	}
	if len(repo.rows) != 0 || len(store.sessions) != 0 {
		t.Fatal("extension restored deleted authority")
	}
}
