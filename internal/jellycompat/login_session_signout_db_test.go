package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// TestSignedOutLoginSessionEndsItsJellyfinSessionDB: signing out the login
// session a Jellyfin sign-in opened, from the account's own session list or by
// an administrator revoking that one session, ends the Jellyfin session on
// every route that accepts it, on the node that holds it in memory and on one
// that loads it from Postgres. The account's other Jellyfin sign-in keeps
// working.
func TestSignedOutLoginSessionEndsItsJellyfinSessionDB(t *testing.T) {
	pool, suffix := jellyfin12CompatPool(t)
	ctx := context.Background()
	users := auth.NewUserRepository(pool)
	loginSessions := auth.NewSessionRepository(pool)
	jwt := auth.NewJWTService("jellycompat-signout-test-secret-0123456789", time.Hour, 24*time.Hour)
	svc := auth.NewService(auth.NewLocalProvider(users, loginSessions), jwt, loginSessions, users, nil, nil, nil)
	const password = "correct horse battery"
	user, err := users.Create(ctx, models.CreateUserInput{Username: fmt.Sprintf("jfsignout-%d", suffix), Email: fmt.Sprintf("jfsignout-%d@example.test", suffix),
		Password: password, Role: models.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := users.Create(ctx, models.CreateUserInput{Username: fmt.Sprintf("jfsignout-admin-%d", suffix), Email: fmt.Sprintf("jfsignout-admin-%d@example.test", suffix),
		Password: password, Role: models.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = ANY($1)`, []int{user.ID, admin.ID})
	})
	stores := pgstore.NewPostgresProvider(pool)
	profiles, err := stores.ForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := profiles.CreateProfile(ctx, userstore.Profile{ID: uuid.NewString(), Name: "Main", IsPrimary: true}); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New([]byte("jellycompat-signout-test-key-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	compatRepo := NewSessionRepository(pool, cipher)
	newNode := func() *SessionStore {
		return NewPersistentSessionStore(24*time.Hour, nil, compatRepo).WithLoginSessions(svc)
	}

	signOuts := map[string]func(loginSessionID string) error{
		"own sign-out": func(id string) error { return svc.RevokeSession(ctx, id, user.ID) },
		"admin single-session revoke": func(id string) error {
			_, err := loginSessions.RevokeAsAdmin(ctx, admin.ID, user.ID, &id)
			return err
		},
	}
	// Each route asks node, the replica that signed the client in and holds
	// the session in memory, except one that asks a new replica, which loads
	// it from Postgres.
	routes := map[string]func(t *testing.T, node *SessionStore, token string) int{
		"RequireSession": func(t *testing.T, node *SessionStore, token string) int {
			return serveCompat(NewAuthenticator(node, svc).RequireSession(okHandler()), compatRequest("/Users/me/Items", token))
		},
		"RequireSession on another node": func(t *testing.T, _ *SessionStore, token string) int {
			return serveCompat(NewAuthenticator(newNode(), svc).RequireSession(okHandler()), compatRequest("/Users/me/Items", token))
		},
		"stream token": func(t *testing.T, node *SessionStore, token string) int {
			var reached bool
			return serveCompat(directPlayRouter(t, node, NewPlaybackSessionStore(time.Hour, nil), nil, &reached), compatRequest("/Videos/item123/stream.mkv", token))
		},
		"stream PlaySessionId": func(t *testing.T, node *SessionStore, token string) int {
			playback := NewPlaybackSessionStore(time.Hour, nil)
			playback.Put(PlaybackSession{ID: "ps1", CompatToken: token, RouteItemID: "item123", MediaSources: []PlaybackMediaSource{{ID: "src9"}}})
			var reached bool
			return serveCompat(directPlayRouter(t, node, playback, nil, &reached), compatRequest("/Videos/item123/stream.mkv?PlaySessionId=ps1", ""))
		},
		"credential-less static stream": func(t *testing.T, node *SessionStore, token string) int {
			playback := NewPlaybackSessionStore(time.Hour, nil)
			playback.PutNegotiated(PlaybackSession{ID: "ps1", CompatToken: token, ClientIP: "192.0.2.1", ClientPeer: "192.0.2.1",
				RouteItemID: "item123", MediaSources: []PlaybackMediaSource{{ID: "src9"}}})
			var reached bool
			router := clientip.Middleware(clientip.NewResolver(nil))(directPlayRouter(t, node, playback, nil, &reached))
			return serveCompat(router, compatRequest("/Videos/item123/stream?static=true&mediaSourceId=src9", ""))
		},
		// A recorder cannot be upgraded, so a token the socket accepts ends
		// in 400 rather than 401.
		"socket": func(t *testing.T, node *SessionStore, token string) int {
			return serveCompat(NewSocketHandler(node, nil), compatRequest("/socket", token))
		},
	}

	resolver := NewLoginResolver(svc, stores, newNode(), nil, nil)
	for signOutName, signOut := range signOuts {
		for routeName, route := range routes {
			t.Run(signOutName+"/"+routeName, func(t *testing.T) {
				node := newNode()
				resolver.sessions = node
				signedOut, err := resolver.Resolve(ctx, user.Username, password, "Jellyfin Web", "")
				if err != nil {
					t.Fatal(err)
				}
				kept, err := resolver.Resolve(ctx, user.Username, password, "Findroid", "")
				if err != nil {
					t.Fatal(err)
				}
				loginSessionID := refreshTokenSession(t, jwt, signedOut)
				if loginSessionID == refreshTokenSession(t, jwt, kept) {
					t.Fatal("both sign-ins share one login session")
				}
				if status := route(t, node, signedOut.Token); status == http.StatusUnauthorized {
					t.Fatalf("before sign-out: status = %d", status)
				}
				if err := signOut(loginSessionID); err != nil {
					t.Fatal(err)
				}
				if status := route(t, node, signedOut.Token); status != http.StatusUnauthorized {
					t.Fatalf("signed-out Jellyfin session: status = %d, want 401", status)
				}
				if status := route(t, node, kept.Token); status == http.StatusUnauthorized {
					t.Fatalf("the account's other Jellyfin session: status = %d, want it kept", status)
				}
			})
		}
	}
}

// TestStoredSessionWithoutLoginSessionFailsClosedDB: a stored Jellyfin
// session whose refresh token names no login session cannot be checked, so it
// counts as signed out.
func TestStoredSessionWithoutLoginSessionFailsClosedDB(t *testing.T) {
	pool, suffix := jellyfin12CompatPool(t)
	ctx := context.Background()
	user, err := auth.NewUserRepository(pool).Create(ctx, models.CreateUserInput{Username: fmt.Sprintf("jfnologin-%d", suffix),
		Email: fmt.Sprintf("jfnologin-%d@example.test", suffix), Password: "correct horse battery", Role: models.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, user.ID) })
	cipher, err := secret.New([]byte("jellycompat-nologin-test-key-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewSessionRepository(pool, cipher)
	token := fmt.Sprintf("jfnologin-%d", suffix)
	if err := repo.Upsert(ctx, Session{Token: token, StreamAppUserID: user.ID, PseudoUserID: uuid.New(), StreamAppRefreshToken: "",
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPersistentSessionStore(time.Hour, nil, repo).Lookup(ctx, token); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Lookup = %v, want ErrSessionNotFound", err)
	}
}

// refreshTokenSession is the login session a Jellyfin session's bridged
// refresh token belongs to.
func refreshTokenSession(t *testing.T, jwt *auth.JWTService, session *Session) string {
	t.Helper()
	claims, err := jwt.ValidateToken(session.StreamAppRefreshToken)
	if err != nil || claims.SessionID == "" {
		t.Fatalf("bridged refresh token: claims %+v, error %v", claims, err)
	}
	return claims.SessionID
}
