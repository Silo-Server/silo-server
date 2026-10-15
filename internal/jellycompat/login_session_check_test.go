package jellycompat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
)

// loginSessionsFake answers login-session checks from a set of live IDs, or
// fails every check with err.
type loginSessionsFake struct {
	live   map[string]bool
	err    error
	checks int
}

func (f *loginSessionsFake) LoginSessionActive(_ context.Context, id string) (bool, error) {
	f.checks++
	if f.err != nil {
		return false, f.err
	}
	return f.live[id], nil
}

// TestLookupChecksTheLoginSession: every lookup, from memory or from the
// stored row, asks whether the session's login session is live. A signed-out
// one ends the compat session; a check that fails keeps it and reports an
// error that is not ErrSessionNotFound; a session with no login session is
// not checked.
func TestLookupChecksTheLoginSession(t *testing.T) {
	stores := map[string]func() (*SessionStore, *rowSessionRepo){
		"memory": func() (*SessionStore, *rowSessionRepo) {
			return NewSessionStore(time.Hour, fixedNow), nil
		},
		"stored row": func() (*SessionStore, *rowSessionRepo) {
			repo := &rowSessionRepo{rows: map[string]Session{}}
			return NewPersistentSessionStore(time.Hour, fixedNow, repo), repo
		},
	}
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			store, repo := newStore()
			checker := &loginSessionsFake{live: map[string]bool{"login-1": true}}
			store.WithLoginSessions(checker)
			for _, s := range []Session{
				{Token: "live", StreamAppUserID: 1, LoginSessionID: "login-1"},
				{Token: "signed-out", StreamAppUserID: 1, LoginSessionID: "login-2"},
				{Token: "unchecked", StreamAppUserID: 1},
			} {
				if repo != nil {
					// Only stored, so the lookup loads the row.
					repo.rows[s.Token] = store.withLifetime(s)
				} else if err := store.Put(s); err != nil {
					t.Fatal(err)
				}
			}

			if _, err := store.Lookup(t.Context(), "live"); err != nil {
				t.Fatalf("live login session: %v", err)
			}
			if _, err := store.Lookup(t.Context(), "signed-out"); !errors.Is(err, ErrSessionNotFound) {
				t.Fatalf("signed-out login session = %v, want ErrSessionNotFound", err)
			}
			if _, ok := store.sessions["signed-out"]; ok {
				t.Fatal("signed-out session still cached")
			}
			if repo != nil {
				if _, ok := repo.rows["signed-out"]; ok {
					t.Fatal("signed-out session still stored")
				}
			}
			before := checker.checks
			if _, err := store.Lookup(t.Context(), "unchecked"); err != nil {
				t.Fatalf("session with no login session: %v", err)
			}
			if checker.checks != before {
				t.Fatal("a session with no login session was checked")
			}

			checker.err = errors.New("database down")
			if _, err := store.Lookup(t.Context(), "live"); err == nil || errors.Is(err, ErrSessionNotFound) {
				t.Fatalf("failed check = %v, want a retryable error", err)
			}
			checker.err = nil
			if _, err := store.Lookup(t.Context(), "live"); err != nil {
				t.Fatalf("after the outage: %v, want the session kept", err)
			}
		})
	}
}

// TestRequireSession_LoginSessionCheckOutageIsRetryable: when the login
// session cannot be checked because the database is unreachable, the request
// is a retryable 503 and the compat session survives.
func TestRequireSession_LoginSessionCheckOutageIsRetryable(t *testing.T) {
	svc := unreachableAuthService(t)
	store := NewSessionStore(time.Hour, fixedNow).WithLoginSessions(svc)
	if err := store.Put(Session{Token: "tok", StreamAppUserID: 1, LoginSessionID: "login-1"}); err != nil {
		t.Fatal(err)
	}
	status := serveCompat(NewAuthenticator(store, nil).RequireSession(okHandler()), compatRequest("/Users/Me", "tok"))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
	if _, ok := store.sessions["tok"]; !ok {
		t.Fatal("an unreachable database ended the compat session")
	}
}

// TestSocketRechecksTheLoginSession: the socket refuses a session whose login
// session was signed out, on a store with no database too.
func TestSocketRechecksTheLoginSession(t *testing.T) {
	store := NewSessionStore(time.Hour, fixedNow).WithLoginSessions(&loginSessionsFake{live: map[string]bool{"login-1": true}})
	for _, s := range []Session{
		{Token: "live", StreamAppUserID: 1, LoginSessionID: "login-1"},
		{Token: "signed-out", StreamAppUserID: 1, LoginSessionID: "login-2"},
	} {
		if err := store.Put(s); err != nil {
			t.Fatal(err)
		}
	}
	socket := NewSocketHandler(store, nil)
	if status := serveCompat(socket, compatRequest("/socket", "signed-out")); status != http.StatusUnauthorized {
		t.Fatalf("signed-out session: status = %d, want 401", status)
	}
	// A recorder cannot be upgraded: 400 means the token was accepted.
	if status := serveCompat(socket, compatRequest("/socket", "live")); status == http.StatusUnauthorized {
		t.Fatalf("live session: status = %d", status)
	}
}

// TestSocketKeepsTheSessionWhenTheCheckFails: a login session that cannot be
// checked answers a new socket with a retryable 503 rather than 401.
func TestSocketKeepsTheSessionWhenTheCheckFails(t *testing.T) {
	store := NewSessionStore(time.Hour, fixedNow).WithLoginSessions(&loginSessionsFake{err: errors.New("database down")})
	if err := store.Put(Session{Token: "tok", StreamAppUserID: 1, LoginSessionID: "login-1"}); err != nil {
		t.Fatal(err)
	}
	if status := serveCompat(NewSocketHandler(store, nil), compatRequest("/socket", "tok")); status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
	if _, ok := store.sessions["tok"]; !ok {
		t.Fatal("a failed check ended the session")
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

func compatRequest(target, token string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	return req
}

func serveCompat(h http.Handler, req *http.Request) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// TestStaticStreamGrantSkipsASignedOutCaller: two clients behind one address
// negotiated the same source. When the more recently active one is signed
// out, the other's credential-less static stream is still granted by its own
// negotiation.
func TestStaticStreamGrantSkipsASignedOutCaller(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	sessions := NewSessionStore(time.Hour, clock).WithLoginSessions(&loginSessionsFake{live: map[string]bool{"login-kept": true}})
	for _, s := range []Session{
		{Token: "kept", StreamAppUserID: 1, LoginSessionID: "login-kept"},
		{Token: "signed-out", StreamAppUserID: 1, LoginSessionID: "login-signed-out"},
	} {
		if err := sessions.Put(s); err != nil {
			t.Fatal(err)
		}
	}
	playback := NewPlaybackSessionStore(time.Hour, clock)
	for _, grant := range []struct{ id, token string }{{"ps-kept", "kept"}, {"ps-signed-out", "signed-out"}} {
		playback.PutNegotiated(PlaybackSession{ID: grant.id, CompatToken: grant.token, ClientIP: "192.0.2.1", ClientPeer: "192.0.2.1",
			RouteItemID: "item123", MediaSources: []PlaybackMediaSource{{ID: "src9"}}})
		now = now.Add(time.Minute) // the signed-out client negotiated last
	}
	var reached bool
	router := clientip.Middleware(clientip.NewResolver(nil))(directPlayRouter(t, sessions, playback, nil, &reached))
	if status := serveCompat(router, compatRequest("/Videos/item123/stream?static=true&mediaSourceId=src9", "")); status != http.StatusOK || !reached {
		t.Fatalf("status = %d reached = %v, want the kept client's grant to serve it", status, reached)
	}
}

// TestCompatStoreChecksLoginSessionsByDefault: the server's session store
// checks login sessions whenever it signs clients in through Silo.
func TestCompatStoreChecksLoginSessionsByDefault(t *testing.T) {
	svc := unreachableAuthService(t)
	deps := withDefaults(Dependencies{Config: &config.Config{}, AuthService: svc})
	if deps.SessionStore.loginSessions != svc {
		t.Fatalf("session store checker = %v, want the auth service", deps.SessionStore.loginSessions)
	}
}

// TestResolveRefusesAPairWithoutALoginSession: a session that could never be
// checked is not created.
func TestResolveRefusesAPairWithoutALoginSession(t *testing.T) {
	r := NewLoginResolver(nil, nil, NewSessionStore(time.Hour, fixedNow), nil, fixedNow)
	_, err := r.newSession("tok", "alice", nil, &auth.TokenPair{AccessToken: "a", RefreshToken: "r"}, "p", "P")
	if err == nil {
		t.Fatal("newSession accepted a token pair with no login session")
	}
}

// unreachableAuthService is an auth service whose database never answers.
func unreachableAuthService(t *testing.T) *auth.Service {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), "postgres://silo@127.0.0.1:1/silo?sslmode=disable&connect_timeout=2")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return auth.NewService(nil, auth.NewJWTService("compat-login-session-secret", time.Hour, 24*time.Hour),
		auth.NewSessionRepository(pool), auth.NewUserRepository(pool), nil, nil, nil)
}
