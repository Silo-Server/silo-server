package apiv2

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

// guestSeatSelections stands in for the catalog: any content id resolves to
// itself, so the host can stage and start an item without a library.
type guestSeatSelections struct{}

func (guestSeatSelections) ResolveSelection(_ context.Context, _ int, _ string, in watchtogether.SelectItemInput) (*watchtogether.ResolvedSelection, error) {
	return &watchtogether.ResolvedSelection{ContentID: in.ContentID}, nil
}

// guestSeatView is the part of a room answer this test reads, from a REST
// body's "room" or a socket snapshot frame's.
type guestSeatView struct {
	Code                    string `json:"code"`
	Phase                   string `json:"phase"`
	SelfCanControlTransport bool   `json:"self_can_control_transport"`
	SelfCanManageRoom       bool   `json:"self_can_manage_room"`
	InvitePath              string `json:"invite_path"`
	MemberCount             int    `json:"member_count"`
}

// guestSeatRoutes serves the room routes through the real session lookup,
// the room service on the Postgres room store and the real room socket,
// with three login sessions by token: tok-host (account A, password),
// tok-phone (account B, password) and tok-app (account B, opened by
// another app's grant-enrolled key). All act as profile p-owner.
type guestSeatRoutes struct {
	t         *testing.T
	h         http.Handler
	server    *httptest.Server
	roomID    string
	hostProof string
}

func newGuestSeatRoutes(t *testing.T) *guestSeatRoutes {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	name := "guestseat-" + uuid.NewString()[:8]
	var hostID, guestID int
	for _, u := range []struct {
		id   *int
		name string
	}{{&hostID, name + "-host"}, {&guestID, name + "-guest"}} {
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, permissions)
			VALUES ($1, $2, 'x', 'user', '{}') RETURNING id`, u.name, u.name+"@example.test").Scan(u.id); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE id = ANY($1)`, []int{hostID, guestID})
	})
	publicKey := make([]byte, 65)
	if _, err := rand.Read(publicKey); err != nil {
		t.Fatal(err)
	}
	grantKey := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_device_keys (id, user_id, public_key, key_id, kind, name, enrolled_via)
		VALUES ($1::uuid, $2, $3, $4, 'app', 'Other app', $5)`, grantKey, guestID, publicKey, "key-"+grantKey, models.AppKeyEnrolledGrant); err != nil {
		t.Fatal(err)
	}
	claims := map[string]*auth.Claims{}
	for _, s := range []struct {
		token, openedVia string
		userID           int
		deviceKey        *string
	}{{"tok-host", models.SessionOpenedPassword, hostID, nil}, {"tok-phone", models.SessionOpenedPassword, guestID, nil}, {"tok-app", models.SessionOpenedAppKey, guestID, &grantKey}} {
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, device_name, expires_at, opened_via, device_key_id)
			VALUES ($1, $2, 'test', $3, $4, $5::uuid)`, id, s.userID, time.Now().Add(time.Hour), s.openedVia, s.deviceKey); err != nil {
			t.Fatal(err)
		}
		// A socket ticket needs an access token with a bounded expiry.
		claims[s.token] = &auth.Claims{UserID: s.userID, Role: "user", SessionID: id, TokenType: auth.TokenTypeAccess,
			RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}
	}

	sessions := auth.NewSessionRepository(pool)
	rooms := watchtogether.NewService(watchtogether.NewRepository(pool), nil, nil, guestSeatSelections{}, nil, nil)
	t.Cleanup(rooms.Close)
	room := handlers.NewWatchTogetherHandler(rooms, nil, watchtogether.NewRoomTokenService("synthetic-guest-seat-secret", time.Hour))
	tickets := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = tickets.Close() })
	deps := parityDeps(false)
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{claims}, sessions, fakeAPIKeys{},
		fakeUsers{map[int]*models.User{hostID: {ID: hostID, Role: "user", Enabled: true}, guestID: {ID: guestID, Role: "user", Enabled: true}}})
	deps.WatchTogetherCreate, deps.WatchTogetherPolicy, deps.WatchTogetherSelection = room, room, room
	deps.WatchTogetherJoin, deps.WatchTogetherRoomRead = room, room
	deps.WatchTogetherSocket = handlers.NewWatchTogetherSocketV2(room, watchtogether.NewRoomSocketCredentialStore(tickets),
		sessions, auth.NewUserRepository(pool), fakeResolver{}, deps.ActingAdmin, "")
	r := &guestSeatRoutes{t: t, h: NewHandler(deps), roomID: uuid.NewString()}
	r.server = httptest.NewServer(r.h)
	t.Cleanup(r.server.Close)
	return r
}

func (r *guestSeatRoutes) headers(token, proof string) map[string]string {
	out := with(bearer(token), "X-Profile-Id", "p-owner")
	if proof != "" {
		out = with(out, "X-Room-Token", proof)
	}
	return out
}

// call sends a room request and returns the room and its proof.
func (r *guestSeatRoutes) call(token, proof, method, path, body string) (guestSeatView, string) {
	r.t.Helper()
	rec := do(r.t, r.h, method, Prefix+path, body, r.headers(token, proof))
	if rec.Code/100 != 2 {
		r.t.Fatalf("%s %s %s: status %d: %s", token, method, path, rec.Code, rec.Body.String())
	}
	var out struct {
		Room  guestSeatView `json:"room"`
		Token string        `json:"room_access_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		r.t.Fatal(err)
	}
	return out.Room, out.Token
}

func (r *guestSeatRoutes) read(token, proof string) guestSeatView {
	r.t.Helper()
	view, _ := r.call(token, proof, http.MethodGet, "/watch-together/rooms/"+r.roomID, "")
	return view
}

// dial asks for a socket ticket and connects with it.
func (r *guestSeatRoutes) dial(token, proof string) *websocket.Conn {
	r.t.Helper()
	rec := do(r.t, r.h, http.MethodPost, Prefix+"/watch-together/rooms/"+r.roomID+"/ws-ticket", "", r.headers(token, proof))
	if rec.Code != http.StatusOK {
		r.t.Fatalf("%s ticket: status %d: %s", token, rec.Code, rec.Body.String())
	}
	var ticket EventsSocketTicket
	if err := json.Unmarshal(rec.Body.Bytes(), &ticket); err != nil {
		r.t.Fatal(err)
	}
	dialer := websocket.Dialer{Subprotocols: []string{watchtogether.RoomSocketProtocol, "silo.ticket." + ticket.Ticket}}
	conn, resp, err := dialer.DialContext(r.t.Context(), "ws"+strings.TrimPrefix(r.server.URL, "http")+Prefix+"/watch-together/rooms/"+r.roomID+"/ws", nil)
	if err != nil {
		r.t.Fatalf("%s connect: %v", token, err)
	}
	_ = resp.Body.Close()
	r.t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// next reads frames until one of types arrives, failing on a
// connection_replaced along the way. It returns the frame's type and, for a
// snapshot, its room.
func (r *guestSeatRoutes) next(conn *websocket.Conn, types ...string) (string, guestSeatView) {
	r.t.Helper()
	// Each wait is bounded on its own: a deadline set when the connection
	// opened would also count the requests made since.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var frame struct {
			Type string        `json:"type"`
			Room guestSeatView `json:"room"`
			Code string        `json:"code"`
		}
		if err := conn.ReadJSON(&frame); err != nil {
			r.t.Fatalf("read %v: %v", types, err)
		}
		if frame.Type == "connection_replaced" {
			r.t.Fatal("connection replaced")
		}
		if frame.Type == "error" && frame.Code != "forbidden" {
			r.t.Fatalf("error frame of code %q", frame.Code)
		}
		if slices.Contains(types, frame.Type) {
			return frame.Type, frame.Room
		}
	}
}

func (r *guestSeatRoutes) snapshot(conn *websocket.Conn) guestSeatView {
	r.t.Helper()
	_, view := r.next(conn, "snapshot")
	return view
}

// connected waits for the room to count this many connected members (the
// host never connects), so a closed socket has reached the room.
func (r *guestSeatRoutes) connected(count int) {
	r.t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(5 * time.Second)
	for r.read("tok-host", r.hostProof).MemberCount != count {
		select {
		case <-tick.C:
		case <-deadline:
			r.t.Fatalf("room never counted %d connected members", count)
		}
	}
}

func wantGuestPowers(t *testing.T, step string, view guestSeatView, steer bool) {
	t.Helper()
	if view.SelfCanControlTransport != steer || view.SelfCanManageRoom || view.InvitePath != "" {
		t.Errorf("%s: self_can_control_transport %v (want %v), self_can_manage_room %v, invite_path %q",
			step, view.SelfCanControlTransport, steer, view.SelfCanManageRoom, view.InvitePath)
	}
}

// TestGuestSeatAnotherAppKeepsOwnDeviceViewDB: a member is the account and
// profile, so a session another app's key opened can hold the same guest
// seat as the person's own device. Through the real routes (session
// lookup, room service on the Postgres room store, ticket and socket
// handshake), with the room playing and guests allowed to play and pause:
// while the device's socket is open the app is refused a connection and
// the device's frames are untouched; once the device has left, the app's
// join, room read and socket snapshot name no power it is refused, while
// the device's own room read still says it may control transport, after
// the app disconnects too, and on its reconnected socket. Regression: the
// strip followed a flag on the seat, so the person's own device's room read
// lost transport control while the app held the seat.
func TestGuestSeatAnotherAppKeepsOwnDeviceViewDB(t *testing.T) {
	r := newGuestSeatRoutes(t)
	room := "/watch-together/rooms/" + r.roomID

	// 1. Account A hosts a playing room that lets guests play and pause.
	created, hostProof := r.call("tok-host", "", http.MethodPost, "/watch-together/rooms", `{"room_id":"`+r.roomID+`","selection_mode":"host_pick"}`)
	r.hostProof = hostProof
	r.call("tok-host", hostProof, http.MethodPatch, room+"/policy", `{"guest_control_policy":"guest_play_pause"}`)
	if playing, _ := r.call("tok-host", hostProof, http.MethodPut, room+"/selection", `{"content_id":"movie-1"}`); playing.Phase != "playing" {
		t.Fatalf("room phase %q after selection", playing.Phase)
	}
	join := `{"code":"` + created.Code + `"}`

	// 2. B's own device joins as the guest profile and connects.
	_, phoneProof := r.call("tok-phone", "", http.MethodPost, "/watch-together/join", join)
	phone := r.dial("tok-phone", phoneProof)
	wantGuestPowers(t, "own device's first snapshot", r.snapshot(phone), true)

	// While the device holds the seat, the app is refused a connection
	// (an error frame of code forbidden) and the device keeps its
	// connection and its view.
	_, appProof := r.call("tok-app", "", http.MethodPost, "/watch-together/join", join)
	if kind, _ := r.next(r.dial("tok-app", appProof), "error", "snapshot"); kind != "error" {
		t.Fatal("app connected while the device held the seat")
	}
	if err := phone.WriteJSON(map[string]any{"type": "ping", "client_sent_at": "synthetic-time"}); err != nil {
		t.Fatal(err)
	}
	for kind, view := r.next(phone, "pong", "snapshot"); kind != "pong"; kind, view = r.next(phone, "pong", "snapshot") {
		wantGuestPowers(t, "own device's snapshot after the refused app", view, true)
	}

	// 3. The device's socket closes.
	_ = phone.Close()
	r.connected(0)

	// 4. The app joins, reads and connects: no refused power anywhere.
	appJoin, appProof := r.call("tok-app", "", http.MethodPost, "/watch-together/join", join)
	wantGuestPowers(t, "app's join", appJoin, false)
	wantGuestPowers(t, "app's room read", r.read("tok-app", appProof), false)
	app := r.dial("tok-app", appProof)
	wantGuestPowers(t, "app's socket snapshot", r.snapshot(app), false)
	r.connected(1)

	// 5. The device's own room read while the app holds the seat.
	wantGuestPowers(t, "own device's room read while the app is connected", r.read("tok-phone", phoneProof), true)

	// 6. The app disconnects; the device reads again without reconnecting.
	_ = app.Close()
	r.connected(0)
	wantGuestPowers(t, "own device's room read after the app disconnected", r.read("tok-phone", phoneProof), true)

	// 7. The device reconnects.
	wantGuestPowers(t, "own device's reconnected snapshot", r.snapshot(r.dial("tok-phone", phoneProof)), true)
}
