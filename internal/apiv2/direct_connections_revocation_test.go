package apiv2

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/directconn"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type directUserStores struct{ store userstore.UserStore }

func (p directUserStores) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}
func (directUserStores) Close() error { return nil }

// newDirectDeviceHandler is the real device handler over an in-memory user
// store that knows devices "tv" and "phone" for profile p-owner.
func newDirectDeviceHandler(t *testing.T, revoker handlers.DirectRevoker) *handlers.DeviceHandler {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
		t.Fatal(err)
	}
	store := userdb.NewSQLiteUserStore(db)
	for _, device := range []string{"tv", "phone"} {
		if err := store.RegisterDevice(t.Context(), userstore.DeviceEntry{ProfileID: "p-owner", DeviceID: device, DeviceName: device, DevicePlatform: "tvOS"}); err != nil {
			t.Fatal(err)
		}
	}
	h := handlers.NewDeviceHandler(directUserStores{store: store})
	h.DirectRevoker = revoker
	return h
}

// register stores a registration for an enrolled device, as the registration
// operation does, so the entry is also held by the peer cache.
func (e *directTestEnv) register(device string, key protocol.NodeKey) {
	e.registerFor("1", device, key)
}

func (e *directTestEnv) registerFor(account, device string, key protocol.NodeKey) {
	e.t.Helper()
	reg := protocol.Registration{NodeKey: key, DiscoKey: protocol.DiscoKey{1}, Endpoints: []netip.AddrPort{netip.MustParseAddrPort("203.0.113.9:41641")}}
	ref := server.DeviceRef{Account: server.AccountID(account), Device: server.DeviceID(device)}
	if err := e.store.PutRegistration(e.t.Context(), ref, reg, e.clock.Now().Add(time.Minute), e.clock.Now()); err != nil {
		e.t.Fatal(err)
	}
}

// A revocation event withdraws the store entry, so an enrolled device that
// never registered is revoked as well as one that did. Clearing a device's
// settings keeps the device registered, so it keeps its enrollment.
func TestDirectRevocationEvents(t *testing.T) {
	events := []struct {
		name, path string
		revokes    bool
	}{
		{"v2 forgetDevice", "/devices/tv", true},
		{"v2 clearDeviceSettings", "/devices/tv/settings", false},
	}
	for _, event := range events {
		for _, registered := range []bool{true, false} {
			name := event.name + "/never registered"
			if registered {
				name = event.name + "/registered"
			}
			t.Run(name, func(t *testing.T) {
				e := newDirectTestEnv(t)
				e.deps.DeviceSettings = newDirectDeviceHandler(t, e.coord)
				h := e.handler()
				tv := e.mustEnroll(h, directLoginToken, "tv", directTestKey(1))
				phone := e.mustEnroll(h, directLoginToken, "phone", directTestKey(2))
				if registered {
					e.register("tv", directTestKey(1))
				}

				rec := do(t, h, http.MethodDelete, Prefix+event.path, "", directViewer(directLoginToken))
				if rec.Code != http.StatusNoContent {
					t.Fatalf("%s = %d: %s", event.name, rec.Code, rec.Body.String())
				}

				_, _, err := e.coord.Authorize(t.Context(), tv.Ticket)
				_, storeErr := e.store.Device(t.Context(), server.DeviceRef{Account: "1", Device: "tv"}, e.clock.Now())
				if event.revokes {
					if !errors.Is(err, directconn.ErrInvalidTicket) {
						t.Fatalf("removed device's ticket: %v, want invalid", err)
					}
					if !errors.Is(storeErr, server.ErrNotFound) {
						t.Fatalf("removed device's store entry: %v, want ErrNotFound", storeErr)
					}
				} else if err != nil || storeErr != nil {
					t.Fatalf("device kept after clearing its settings: ticket %v, store entry %v", err, storeErr)
				}
				if _, _, err := e.coord.Authorize(t.Context(), phone.Ticket); err != nil {
					t.Fatalf("sibling device lost its entry: %v", err)
				}
			})
		}
	}

	t.Run("store outage does not fail the event", func(t *testing.T) {
		e := newDirectTestEnv(t)
		e.deps.DeviceSettings = newDirectDeviceHandler(t, e.coord)
		h := e.handler()
		tv := e.mustEnroll(h, directLoginToken, "tv", directTestKey(1))
		e.store.setDown(true)

		rec := do(t, h, http.MethodDelete, Prefix+"/devices/tv", "", directViewer(directLoginToken))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("forgetDevice with the store down = %d: %s", rec.Code, rec.Body.String())
		}
		if !bytes.Contains(e.logs.Bytes(), []byte("direct connections: revocation failed")) || !bytes.Contains(e.logs.Bytes(), []byte("reason=state_store_unavailable")) {
			t.Fatalf("the failure was not logged:\n%s", e.logs.String())
		}
		// The entry lapses at its lease end.
		e.store.setDown(false)
		if _, _, err := e.coord.Authorize(t.Context(), tv.Ticket); err != nil {
			t.Fatalf("entry before its lease end: %v", err)
		}
		e.clock.Advance(directTestLease)
		if _, _, err := e.coord.Authorize(t.Context(), tv.Ticket); !errors.Is(err, directconn.ErrInvalidTicket) {
			t.Fatalf("entry after its lease end: %v, want invalid", err)
		}
	})
}

// createDirectTestUser creates an account whose password is "original-password"
// and deletes it when the test ends.
func createDirectTestUser(t *testing.T, users *auth.UserRepository, prefix string) *models.User {
	t.Helper()
	user, err := users.Create(t.Context(), models.CreateUserInput{Username: prefix + uuid.NewString(), Email: uuid.NewString() + "@example.test", Password: "original-password", Role: models.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = users.Delete(context.WithoutCancel(t.Context()), user.ID) })
	return user
}

// requireDirectTicketsLive fails unless every ticket, keyed by device, still
// authorizes; what names the event that must have left them alone.
func requireDirectTicketsLive(t *testing.T, e *directTestEnv, what string, tickets map[string]string) {
	t.Helper()
	for device, ticket := range tickets {
		if _, _, err := e.coord.Authorize(t.Context(), ticket); err != nil {
			t.Fatalf("%s: %s lost its entry: %v", what, device, err)
		}
	}
}

// Sign-out withdraws the entries its login session issued. These rows run
// against Postgres because the sessions are real rows: v1 and v2 logout share
// AuthHandler.Logout, and v1 and v2 deleteSession share AuthHandler.RevokeSession.
func TestDirectRevocationSignOutDB(t *testing.T) {
	pool := scopedRecoveryDB(t)
	users := auth.NewUserRepository(pool)
	sessions := auth.NewSessionRepository(pool)
	jwt := auth.NewJWTService("synthetic-direct-revocation-jwt-secret", time.Minute, time.Hour)
	handler := handlers.NewAuthHandler(auth.NewService(auth.NewLocalProvider(users, sessions), jwt, sessions, users, nil, nil, nil), jwt, nil)

	events := []struct {
		name    string
		signOut func(ctx context.Context, userID int, sessionID string) error
	}{
		{"v2 logout", func(ctx context.Context, userID int, sessionID string) error {
			return handler.Logout(ctx, &auth.Claims{UserID: userID, SessionID: sessionID, TokenType: auth.TokenTypeAccess})
		}},
		{"v2 deleteSession", func(ctx context.Context, userID int, sessionID string) error {
			return handler.RevokeSession(ctx, sessionID, userID)
		}},
	}
	for _, event := range events {
		for _, registered := range []bool{true, false} {
			name := event.name + "/never registered"
			if registered {
				name = event.name + "/registered"
			}
			t.Run(name, func(t *testing.T) {
				e := newDirectTestEnv(t)
				handler.SetDirectRevoker(e.coord)
				ctx := t.Context()
				user := createDirectTestUser(t, users, "direct-signout-")
				account := strconv.Itoa(user.ID)
				session := func(device string) string {
					id := uuid.NewString()
					sessionCtx := ctx
					if device != "" {
						sessionCtx = auth.WithClientDevice(ctx, http.Header{auth.ClientDeviceIDHeader: {device}})
					}
					if err := sessions.Create(sessionCtx, models.AuthSession{ID: id, UserID: user.ID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
						t.Fatal(err)
					}
					return id
				}
				enroll := func(sessionID, device string, key protocol.NodeKey) string {
					enrollment, err := e.coord.Enroll(ctx, directconn.EnrollRequest{UserID: user.ID, ProfileID: "p-owner", SessionID: sessionID, DeviceID: device, NodeKey: key, ConnectionPath: "automatic"})
					if err != nil {
						t.Fatal(err)
					}
					return enrollment.Ticket
				}
				tvSession, phoneSession, noDeviceSession := session("tv"), session("phone"), session("")
				tv, phone := enroll(tvSession, "tv", directTestKey(1)), enroll(phoneSession, "phone", directTestKey(2))
				if registered {
					e.registerFor(account, "tv", directTestKey(1))
				}

				// A session that issued no entry withdraws none, whatever its device ID.
				if err := event.signOut(ctx, user.ID, noDeviceSession); err != nil {
					t.Fatal(err)
				}
				requireDirectTicketsLive(t, e, "a session with no device signed out", map[string]string{"tv": tv, "phone": phone})

				// A session that declares the tv's device ID but enrolled nothing does not
				// withdraw the entry the tv's own session issued.
				if err := event.signOut(ctx, user.ID, session("tv")); err != nil {
					t.Fatal(err)
				}
				requireDirectTicketsLive(t, e, "a session declaring the tv's device ID signed out", map[string]string{"tv": tv, "phone": phone})

				// A session that enrolled under a device ID other than its own loses that
				// entry when it ends.
				laptopSession := session("laptop")
				watch := enroll(laptopSession, "watch", directTestKey(4))
				if err := event.signOut(ctx, user.ID, laptopSession); err != nil {
					t.Fatal(err)
				}
				if _, _, err := e.coord.Authorize(ctx, watch); !errors.Is(err, directconn.ErrInvalidTicket) {
					t.Fatalf("ticket the signed-out session issued under another device ID: %v, want invalid", err)
				}
				requireDirectTicketsLive(t, e, "the laptop session signed out", map[string]string{"tv": tv, "phone": phone})

				if err := event.signOut(ctx, user.ID, tvSession); err != nil {
					t.Fatal(err)
				}
				if _, _, err := e.coord.Authorize(ctx, tv); !errors.Is(err, directconn.ErrInvalidTicket) {
					t.Fatalf("signed-out device's ticket: %v, want invalid", err)
				}
				requireDirectTicketsLive(t, e, "the tv session signed out", map[string]string{"phone": phone})

				// Ending a session that already ended withdraws nothing it did not issue,
				// so the device's newer enrollment by another session stays.
				newTV := enroll(session("tv"), "tv", directTestKey(3))
				if err := event.signOut(ctx, user.ID, tvSession); err != nil {
					t.Fatal(err)
				}
				requireDirectTicketsLive(t, e, "an ended session signed out again", map[string]string{"tv (newer enrollment)": newTV})
			})
		}
	}
}

// Ending a View as user withdraws the entries its login session issued, which
// name the viewed account, and no others; an end the server refuses withdraws
// nothing. The enrollment operation refuses a View as user session, so the
// entries are written through the coordination service, as ones enrolled before
// that refusal. It runs against Postgres because the impersonation is a real
// session row; v2 endImpersonation calls AuthHandler.EndImpersonation.
func TestDirectRevocationEndImpersonationDB(t *testing.T) {
	pool := scopedRecoveryDB(t)
	users := auth.NewUserRepository(pool)
	sessions := auth.NewSessionRepository(pool)
	jwt := auth.NewJWTService("synthetic-direct-revocation-jwt-secret", time.Minute, time.Hour)
	svc := auth.NewService(auth.NewLocalProvider(users, sessions), jwt, sessions, users, nil, nil, nil)
	handler := handlers.NewAuthHandler(svc, jwt, nil)
	e := newDirectTestEnv(t)
	handler.SetDirectRevoker(e.coord)
	ctx := t.Context()

	admin, err := users.Create(ctx, models.CreateUserInput{Username: "direct-admin-" + uuid.NewString(), Email: uuid.NewString() + "@example.test", Password: "original-password", Role: models.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = users.Delete(context.WithoutCancel(t.Context()), admin.ID) })
	viewed := createDirectTestUser(t, users, "direct-viewed-")
	impersonate := func() *auth.Claims {
		pair, _, _, err := svc.StartImpersonation(ctx, admin.ID, viewed.ID, "direct", "")
		if err != nil {
			t.Fatal(err)
		}
		claims, err := jwt.ValidateToken(pair.AccessToken)
		if err != nil {
			t.Fatal(err)
		}
		return claims
	}
	ownSession := func(userID int) string {
		id := uuid.NewString()
		if err := sessions.Create(ctx, models.AuthSession{ID: id, UserID: userID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	enroll := func(userID int, sessionID, device string, key protocol.NodeKey) string {
		enrollment, err := e.coord.Enroll(ctx, directconn.EnrollRequest{UserID: userID, ProfileID: "p-owner", SessionID: sessionID, DeviceID: device, NodeKey: key, ConnectionPath: "automatic"})
		if err != nil {
			t.Fatal(err)
		}
		return enrollment.Ticket
	}

	// viewing is the View as user that ends; another runs beside it on the same
	// account. The viewed account's own session and the administrator's own
	// session hold entries too, the administrator's under the same device ID.
	viewing, other := impersonate(), impersonate()
	viewedSession, adminSession := ownSession(viewed.ID), ownSession(admin.ID)
	viewingTV := enroll(viewed.ID, viewing.SessionID, "tv", directTestKey(1))
	others := map[string]string{
		"the other View as user's tablet": enroll(viewed.ID, other.SessionID, "tablet", directTestKey(2)),
		"the viewed account's phone":      enroll(viewed.ID, viewedSession, "phone", directTestKey(3)),
		"the administrator's tv":          enroll(admin.ID, adminSession, "tv", directTestKey(4)),
	}
	all := map[string]string{"the ending View as user's tv": viewingTV}
	for device, ticket := range others {
		all[device] = ticket
	}

	// A closed pool fails the store read, so the end fails before it commits.
	closed, err := pgxpool.NewWithConfig(ctx, pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	closedSessions := auth.NewSessionRepository(closed)
	unavailable := handlers.NewAuthHandler(auth.NewService(auth.NewLocalProvider(users, closedSessions), jwt, closedSessions, users, nil, nil, nil), jwt, nil)
	unavailable.SetDirectRevoker(e.coord)

	stranger := admin.ID + 1000000
	refusals := []struct {
		name    string
		handler *handlers.AuthHandler
		claims  *auth.Claims
		status  int
	}{
		{"a token with no impersonator", handler, &auth.Claims{UserID: viewed.ID, SessionID: viewing.SessionID, TokenType: auth.TokenTypeAccess}, http.StatusBadRequest},
		{"a session that is not a View as user", handler, &auth.Claims{UserID: viewed.ID, SessionID: viewedSession, TokenType: auth.TokenTypeAccess, ImpersonatorUserID: &admin.ID}, http.StatusBadRequest},
		{"another administrator's View as user", handler, &auth.Claims{UserID: viewed.ID, SessionID: viewing.SessionID, TokenType: auth.TokenTypeAccess, ImpersonatorUserID: &stranger}, http.StatusForbidden},
		{"a store failure", unavailable, viewing, http.StatusInternalServerError},
	}
	for _, refusal := range refusals {
		err := refusal.handler.EndImpersonation(ctx, refusal.claims)
		var apiErr *handlers.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != refusal.status {
			t.Fatalf("ending %s: %v, want status %d", refusal.name, err, refusal.status)
		}
		requireDirectTicketsLive(t, e, "ending "+refusal.name+" was refused", all)
	}

	if err := handler.EndImpersonation(ctx, viewing); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.coord.Authorize(ctx, viewingTV); !errors.Is(err, directconn.ErrInvalidTicket) {
		t.Fatalf("ticket the ended View as user issued: %v, want invalid", err)
	}
	requireDirectTicketsLive(t, e, "a View as user ended", others)
}

// A password change that signs out the other sessions keeps only the entry that
// the session which made the change issued for its own device. The hook below is
// cmd/silo's accountOtherSessionsRevoked without its compatibility steps; it runs
// against Postgres because ChangePassword revokes the other sessions in one
// transaction.
func TestDirectRevocationPasswordChangeKeepsCallerDB(t *testing.T) {
	pool := scopedRecoveryDB(t)
	users := auth.NewUserRepository(pool)
	sessions := auth.NewSessionRepository(pool)
	jwt := auth.NewJWTService("synthetic-direct-revocation-jwt-secret", time.Minute, time.Hour)
	svc := auth.NewService(auth.NewLocalProvider(users, sessions), jwt, sessions, users, nil, nil, nil)

	// enrolled is an entry the account holds before the password change, issued by
	// the session named by index into the sessions the row opens: 0 is the session
	// that changes the password (device "tv"), 1 a session on the phone, 2 a stolen
	// session whose client declared the device "laptop".
	type enrolled struct {
		session int
		device  string
		kept    bool
	}
	rows := []struct {
		name     string
		enrolled []enrolled
	}{
		{name: "the caller's entry for its own device stays and the others go",
			enrolled: []enrolled{{0, "tv", true}, {1, "phone", false}}},
		{name: "an entry a stolen session made under the caller's device ID goes",
			enrolled: []enrolled{{2, "tv", false}, {1, "phone", false}}},
		{name: "the caller's entry for another device ID goes",
			enrolled: []enrolled{{0, "watch", false}}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			// Each row gets its own coordination: an entry an earlier row kept would
			// still hold its node key and refuse this row's enrollment.
			e := newDirectTestEnv(t)
			svc.OnSessionsRevoked(func(ctx context.Context, userID int, keptSessionID string) {
				device := ""
				if session, err := sessions.LiveSession(ctx, userID, keptSessionID); err == nil {
					device = session.DeviceID
				}
				e.coord.RevokeAccountExceptSession(ctx, userID, keptSessionID, device)
			})
			ctx := t.Context()
			user := createDirectTestUser(t, users, "direct-password-")
			session := func(device string) string {
				id := uuid.NewString()
				sessionCtx := auth.WithClientDevice(ctx, http.Header{auth.ClientDeviceIDHeader: {device}})
				if err := sessions.Create(sessionCtx, models.AuthSession{ID: id, UserID: user.ID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
					t.Fatal(err)
				}
				return id
			}
			opened := []string{session("tv"), session("phone"), session("laptop")}
			tickets := make([]string, len(row.enrolled))
			for i, entry := range row.enrolled {
				enrollment, err := e.coord.Enroll(ctx, directconn.EnrollRequest{UserID: user.ID, ProfileID: "p-owner", SessionID: opened[entry.session], DeviceID: entry.device, NodeKey: directTestKey(i + 1), ConnectionPath: "automatic"})
				if err != nil {
					t.Fatal(err)
				}
				tickets[i] = enrollment.Ticket
			}

			if err := svc.ChangePassword(ctx, user.ID, opened[0], "original-password", "margin fossil quench", true); err != nil {
				t.Fatal(err)
			}

			for i, entry := range row.enrolled {
				_, _, err := e.coord.Authorize(ctx, tickets[i])
				switch {
				case entry.kept && err != nil:
					t.Errorf("the caller's entry for %q was withdrawn: %v", entry.device, err)
				case !entry.kept && !errors.Is(err, directconn.ErrInvalidTicket):
					t.Errorf("entry for %q issued by session %d: %v, want invalid", entry.device, entry.session, err)
				}
			}
		})
	}
}
