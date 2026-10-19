package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/cache"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/logstream"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/opslog"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

// Exercise the actual bridge connection loops with synthetic authority and no
// media. A protocol read timeout is a failure, not evidence of disconnection.
func TestV1SocketsEndAtAccessExpiry(t *testing.T) {
	for _, surface := range []string{"events", "logs", "playback", "room"} {
		t.Run(surface, func(t *testing.T) {
			t.Parallel()
			claims := &auth.Claims{UserID: 7, Role: "user", SessionID: "session", TokenType: auth.TokenTypeAccess}
			claims.ExpiresAt = &jwt.NumericDate{Time: time.Now().Add(300 * time.Millisecond)}
			conn := openSocketV1(t, surface, claims, nil)
			assertSocketEnds(t, conn)
		})
	}
}

func openSocketV1(t *testing.T, surface string, claims *auth.Claims, authority *SocketAuthorityV1) *websocket.Conn {
	t.Helper()
	mux := chi.NewRouter()
	mux.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(apimw.SetActingAdminDecider(apimw.SetClaims(r.Context(), claims), actingAdminIs(true))))
		})
	})
	path := "/socket"
	switch surface {
	case "events":
		h := &EventsHandler{SocketAuthority: authority, hub: evt.NewHub("expiry", &cache.NoopEventBus{})}
		mux.Get(path, h.HandleWebSocket)
		path += "?channels=user_settings"
	case "logs":
		h := &AdminLogsHandler{SocketAuthority: authority, opsRepo: emptySocketLogs{}, streamHub: logstream.NewHub("expiry", nil)}
		mux.Get(path, h.HandleLogStreamWebSocket)
		path += "?stream=app"
	case "playback":
		manager := playback.NewSessionManager(0, 0)
		session, err := manager.StartSession(7, "profile", 1, playback.PlayDirect, false)
		if err != nil {
			t.Fatal(err)
		}
		h := NewPlaybackHandler(manager)
		h.RealtimeHub = playback.NewRealtimeHub()
		h.SocketAuthority = authority
		mux.Get("/socket/{session_id}", h.HandleSessionWebSocket)
		path += "/" + session.ID
	case "room":
		service := watchtogether.NewService(new(roomSocketRepo), nil, nil, nil, nil, nil)
		t.Cleanup(service.Close)
		h := &WatchTogetherHandler{SocketAuthority: authority, Service: service, ScopeResolver: &socketViewerFixture{scope: access.Scope{UserID: 7, ProfileID: "profile", ProfileVerified: true}}}
		mux.Get("/socket/{room_id}", h.HandleRoomWebSocket)
		path += "/room?profile_id=profile"
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	conn, resp, err := websocket.DefaultDialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(); _ = resp.Body.Close() })
	return conn
}

type emptySocketLogs struct{}

func (emptySocketLogs) List(context.Context, opslog.ListOptions) (opslog.ListResult, error) {
	return opslog.ListResult{}, nil
}

type v1AuthorityFixture struct {
	mu                                               sync.Mutex
	role                                             string
	enabled                                          bool
	revoked, keyDeleted, down, pinRevoked, secondary bool
	revision                                         int64
	language                                         string
	checks                                           chan struct{}
	input                                            access.ResolveInput
	device                                           string
}

func newV1AuthorityFixture() *v1AuthorityFixture {
	return &v1AuthorityFixture{role: "admin", enabled: true, checks: make(chan struct{}, 100)}
}

func (f *v1AuthorityFixture) ActiveSession(context.Context, string) (auth.SessionAuthority, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return auth.SessionAuthority{}, false, errors.New("synthetic store failure")
	}
	return auth.SessionAuthority{Role: f.role, Purpose: models.SessionPurposeAccount}, !f.revoked, nil
}

func (f *v1AuthorityFixture) GetByID(context.Context, int) (*models.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errors.New("synthetic store failure")
	}
	return &models.User{ID: 7, Role: f.role, Enabled: f.enabled}, nil
}

func (f *v1AuthorityFixture) GetMetadataByID(context.Context, int64) (*models.APIKeyMetadata, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errors.New("synthetic store failure")
	}
	if f.keyDeleted {
		return nil, auth.ErrAPIKeyNotFound
	}
	return &models.APIKeyMetadata{ID: 1, UserID: 7}, nil
}

func (f *v1AuthorityFixture) Resolve(_ context.Context, in access.ResolveInput) (access.Scope, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.input = in
	select {
	case f.checks <- struct{}{}:
	default:
	}
	if f.down {
		return access.Scope{}, errors.New("synthetic store failure")
	}
	if f.pinRevoked && !in.SkipPINVerification {
		return access.Scope{}, access.ErrProfileUnverified
	}
	return access.Scope{UserID: in.UserID, ProfileID: in.ProfileID, ProfileVerified: true, PolicyRevision: f.revision, PreferredMetadataLanguage: f.language}, nil
}

func (f *v1AuthorityFixture) acting(_ context.Context, in apimw.ActingAdminRequest) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.device = in.DeviceID
	if f.down {
		return false, errors.New("synthetic store failure")
	}
	return !f.secondary, nil
}

func (f *v1AuthorityFixture) monitor() *SocketAuthorityV1 {
	a := NewSocketAuthorityV1(f, f, f, f, f.acting)
	a.checkInterval = time.Millisecond
	return a
}

func v1SocketRequest(tokenType string) *http.Request {
	claims := &auth.Claims{UserID: 7, Role: "admin", SessionID: "session", TokenType: tokenType}
	if tokenType == auth.TokenTypeAPIKey {
		claims.SessionID = ""
		claims.APIKeyID = 1
	} else {
		claims.ExpiresAt = &jwt.NumericDate{Time: time.Now().Add(time.Minute)}
	}
	r := httptest.NewRequest(http.MethodGet, "/socket", nil)
	r.Header.Set("X-Silo-Device-Id", "synthetic-device")
	return r.WithContext(apimw.SetClaims(r.Context(), claims))
}

func TestSocketAuthorityV1RechecksCurrentAuthority(t *testing.T) {
	for _, credential := range []string{auth.TokenTypeAccess, auth.TokenTypeAPIKey} {
		for _, cause := range []string{"credential", "disabled", "role", "policy", "store", "acting"} {
			t.Run(credential+"/"+cause, func(t *testing.T) {
				f := newV1AuthorityFixture()
				r, cancel, err := f.monitor().bind(v1SocketRequest(credential), "profile", "proof", time.Minute, time.Time{}, "admin", nil)
				if err != nil {
					t.Fatal(err)
				}
				defer cancel()
				// Observe several successful checks; unchanged authority stays live.
				for range 3 {
					select {
					case <-f.checks:
					case <-r.Context().Done():
						t.Fatal("unchanged authority ended")
					case <-time.After(time.Second):
						t.Fatal("authority not checked")
					}
				}
				if r.Context().Err() != nil {
					t.Fatal("unchanged authority ended")
				}
				f.mu.Lock()
				if f.input.SkipPINVerification != (credential == auth.TokenTypeAPIKey) || f.input.ProfileID != "profile" || f.device != "synthetic-device" {
					f.mu.Unlock()
					t.Fatal("opening authority input changed")
				}
				switch cause {
				case "credential":
					f.revoked = true
					f.keyDeleted = true
				case "disabled":
					f.enabled = false
				case "role":
					f.role = "user"
				case "policy":
					f.revision++
				case "store":
					f.down = true
				case "acting":
					f.secondary = true
				}
				f.mu.Unlock()
				select {
				case <-r.Context().Done():
				case <-time.After(time.Second):
					t.Fatal("authority loss did not cancel")
				}
				// The same durable state also refuses a fresh handshake when
				// it invalidates credentials, as opposed to an access snapshot.
				if cause != "policy" && cause != "acting" {
					_, stop, err := f.monitor().bind(v1SocketRequest(credential), "profile", "proof", time.Minute, time.Time{}, "admin", nil)
					stop()
					if err == nil {
						t.Fatal("lost credential admitted fresh connection")
					}
				}
			})
		}
	}
}

func TestSocketAuthorityV1PreservesPresentationAndProfilelessPlayback(t *testing.T) {
	f := newV1AuthorityFixture()
	r, cancel, err := f.monitor().bind(v1SocketRequest(auth.TokenTypeAccess), "", "", time.Minute, time.Time{}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	f.mu.Lock()
	f.language = "fr"
	f.secondary = true
	f.mu.Unlock()
	for range 5 {
		select {
		case <-f.checks:
		case <-r.Context().Done():
			t.Fatal("presentation or effective role ended playback")
		case <-time.After(time.Second):
			t.Fatal("authority not checked")
		}
	}
	if r.Context().Err() != nil {
		t.Fatal("presentation or effective role ended playback")
	}
}

func TestSocketAuthorityV1CapsLifetimeAndRefusesMissingDependencies(t *testing.T) {
	f := newV1AuthorityFixture()
	for _, credential := range []string{auth.TokenTypeAccess, auth.TokenTypeAPIKey} {
		r, cancel, err := f.monitor().bind(v1SocketRequest(credential), "profile", "proof", 20*time.Millisecond, time.Time{}, "admin", nil)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
			t.Fatal("maximum lifetime ignored")
		}
		cancel()
		_, stop, err := NewSocketAuthorityV1(nil, nil, nil, nil, nil).bind(v1SocketRequest(credential), "", "", time.Minute, time.Time{}, "admin", nil)
		stop()
		if err == nil {
			t.Fatal("missing dependencies admitted connection")
		}
	}
	proofExpiry := time.Now().Add(30 * time.Millisecond)
	r, cancel, err := f.monitor().bind(v1SocketRequest(auth.TokenTypeAPIKey), "profile", "proof", time.Minute, proofExpiry, "admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if deadline, ok := r.Context().Deadline(); !ok || !deadline.Equal(proofExpiry) {
		t.Fatal("room proof did not cap lifetime")
	}
}

func TestV1SocketsCloseOnCredentialRevocation(t *testing.T) {
	for _, credential := range []string{auth.TokenTypeAccess, auth.TokenTypeAPIKey} {
		for _, surface := range []string{"events", "logs", "playback", "room"} {
			t.Run(credential+"/"+surface, func(t *testing.T) {
				t.Parallel()
				f := newV1AuthorityFixture()
				claims := apimw.GetClaims(v1SocketRequest(credential).Context())
				conn := openSocketV1(t, surface, claims, f.monitor())
				for range 3 {
					select {
					case <-f.checks:
					case <-time.After(time.Second):
						t.Fatal("authority not rechecked")
					}
				}
				f.mu.Lock()
				f.revoked = true
				f.keyDeleted = true
				f.mu.Unlock()
				assertSocketEnds(t, conn)
			})
		}
	}
}

func assertSocketEnds(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			if strings.Contains(err.Error(), "timeout") {
				t.Fatal("socket survived authority expiry or revocation")
			}
			return
		}
	}
}

func TestSocketAuthorityV1RefusesEffectiveRoleChangedBeforeUpgrade(t *testing.T) {
	for _, credential := range []string{auth.TokenTypeAccess, auth.TokenTypeAPIKey} {
		t.Run(credential, func(t *testing.T) {
			f := newV1AuthorityFixture()
			f.secondary = true
			_, cancel, err := f.monitor().bind(v1SocketRequest(credential), "profile", "proof", time.Minute, time.Time{}, "admin", nil)
			cancel()
			if err == nil {
				t.Fatal("lost acting-admin authority admitted privileged connection")
			}
		})
	}
}
