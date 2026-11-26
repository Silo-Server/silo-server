package apiv2

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

	// Account A hosts; account B is the guest, with its own password
	// session and a session its granted app's key opened.
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
	session := func(token string, userID int, openedVia string, deviceKey *string) {
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, device_name, expires_at, opened_via, device_key_id)
			VALUES ($1, $2, 'test', $3, $4, $5::uuid)`, id, userID, time.Now().Add(time.Hour), openedVia, deviceKey); err != nil {
			t.Fatal(err)
		}
		// A socket ticket needs an access token with a bounded expiry.
		claims[token] = &auth.Claims{UserID: userID, Role: "user", SessionID: id, TokenType: auth.TokenTypeAccess,
			RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}
	}
	session("tok-host", hostID, models.SessionOpenedPassword, nil)
	session("tok-phone", guestID, models.SessionOpenedPassword, nil)
	session("tok-app", guestID, models.SessionOpenedAppKey, &grantKey)

	sessions := auth.NewSessionRepository(pool)
	users := auth.NewUserRepository(pool)
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
		sessions, users, fakeResolver{}, deps.ActingAdmin, "")
	h := NewHandler(deps)
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)

	const profile = "p-owner"
	roomID := uuid.NewString()
	var hostProof string
	headers := func(token, proof string) map[string]string {
		out := with(bearer(token), "X-Profile-Id", profile)
		if proof != "" {
			out = with(out, "X-Room-Token", proof)
		}
		return out
	}
	call := func(token, proof, method, path, body string) (guestSeatView, string) {
		t.Helper()
		rec := do(t, h, method, Prefix+path, body, headers(token, proof))
		if rec.Code/100 != 2 {
			t.Fatalf("%s %s %s: status %d: %s", token, method, path, rec.Code, rec.Body.String())
		}
		var out struct {
			Room  guestSeatView `json:"room"`
			Token string        `json:"room_access_token"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Room, out.Token
	}
	read := func(token, proof string) guestSeatView {
		t.Helper()
		view, _ := call(token, proof, http.MethodGet, "/watch-together/rooms/"+roomID, "")
		return view
	}
	dial := func(token, proof string) *websocket.Conn {
		t.Helper()
		rec := do(t, h, http.MethodPost, Prefix+"/watch-together/rooms/"+roomID+"/ws-ticket", "", headers(token, proof))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s ticket: status %d: %s", token, rec.Code, rec.Body.String())
		}
		var ticket EventsSocketTicket
		if err := json.Unmarshal(rec.Body.Bytes(), &ticket); err != nil {
			t.Fatal(err)
		}
		dialer := websocket.Dialer{Subprotocols: []string{watchtogether.RoomSocketProtocol, "silo.ticket." + ticket.Ticket}}
		conn, resp, err := dialer.DialContext(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+Prefix+"/watch-together/rooms/"+roomID+"/ws", nil)
		if err != nil {
			t.Fatalf("%s connect: %v", token, err)
		}
		_ = resp.Body.Close()
		t.Cleanup(func() { _ = conn.Close() })
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		return conn
	}
	// next reads frames until one of the given types arrives, failing on a
	// connection_replaced along the way.
	next := func(conn *websocket.Conn, types ...string) (string, json.RawMessage) {
		t.Helper()
		for {
			var frame struct {
				Type    string          `json:"type"`
				Room    json.RawMessage `json:"room"`
				Code    string          `json:"code"`
				Message string          `json:"message"`
			}
			if err := conn.ReadJSON(&frame); err != nil {
				t.Fatalf("read %v: %v", types, err)
			}
			if frame.Type == "connection_replaced" {
				t.Fatal("connection replaced")
			}
			for _, want := range types {
				if frame.Type == want {
					if frame.Type == "error" {
						raw, _ := json.Marshal(frame)
						return frame.Type, raw
					}
					return frame.Type, frame.Room
				}
			}
		}
	}
	snapshot := func(conn *websocket.Conn) guestSeatView {
		t.Helper()
		_, raw := next(conn, "snapshot")
		var view guestSeatView
		if err := json.Unmarshal(raw, &view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	// connected waits for the room to count this many connected members
	// (the host never connects), so a closed socket has reached the room.
	connected := func(count int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for read("tok-host", hostProof).MemberCount != count {
			if time.Now().After(deadline) {
				t.Fatalf("room never counted %d connected members", count)
			}
		}
	}
	wantPowers := func(step string, view guestSeatView, steer bool) {
		t.Helper()
		if view.SelfCanControlTransport != steer || view.SelfCanManageRoom || view.InvitePath != "" {
			t.Errorf("%s: self_can_control_transport %v (want %v), self_can_manage_room %v, invite_path %q",
				step, view.SelfCanControlTransport, steer, view.SelfCanManageRoom, view.InvitePath)
		}
	}

	// 1. Account A hosts a playing room that lets guests play and pause.
	var created guestSeatView
	created, hostProof = call("tok-host", "", http.MethodPost, "/watch-together/rooms", `{"room_id":"`+roomID+`","selection_mode":"host_pick"}`)
	call("tok-host", hostProof, http.MethodPatch, "/watch-together/rooms/"+roomID+"/policy", `{"guest_control_policy":"guest_play_pause"}`)
	if playing, _ := call("tok-host", hostProof, http.MethodPut, "/watch-together/rooms/"+roomID+"/selection", `{"content_id":"movie-1"}`); playing.Phase != "playing" {
		t.Fatalf("room phase %q after selection", playing.Phase)
	}
	join := `{"code":"` + created.Code + `"}`

	// 2. B's own device joins as the guest profile and connects.
	_, phoneProof := call("tok-phone", "", http.MethodPost, "/watch-together/join", join)
	phone := dial("tok-phone", phoneProof)
	wantPowers("own device's first snapshot", snapshot(phone), true)

	// While the device holds the seat, the app is refused a connection and
	// the device keeps its connection and its view.
	_, appProof := call("tok-app", "", http.MethodPost, "/watch-together/join", join)
	refused := dial("tok-app", appProof)
	if _, raw := next(refused, "error", "snapshot"); !strings.Contains(string(raw), `"code":"forbidden"`) {
		t.Fatalf("app connected while the device held the seat: %s", raw)
	}
	if err := phone.WriteJSON(map[string]any{"type": "ping", "client_sent_at": "synthetic-time"}); err != nil {
		t.Fatal(err)
	}
	for kind := ""; kind != "pong"; {
		var raw json.RawMessage
		kind, raw = next(phone, "pong", "snapshot")
		if kind == "snapshot" {
			var view guestSeatView
			if err := json.Unmarshal(raw, &view); err != nil {
				t.Fatal(err)
			}
			wantPowers("own device's snapshot after the refused app", view, true)
		}
	}

	// 3. The device's socket closes.
	_ = phone.Close()
	connected(0)

	// 4. The app joins, reads and connects: no refused power anywhere.
	appJoin, appProof := call("tok-app", "", http.MethodPost, "/watch-together/join", join)
	wantPowers("app's join", appJoin, false)
	wantPowers("app's room read", read("tok-app", appProof), false)
	app := dial("tok-app", appProof)
	wantPowers("app's socket snapshot", snapshot(app), false)
	connected(1)

	// 5. The device's own room read while the app holds the seat.
	wantPowers("own device's room read while the app is connected", read("tok-phone", phoneProof), true)

	// 6. The app disconnects; the device reads again without reconnecting.
	_ = app.Close()
	connected(0)
	wantPowers("own device's room read after the app disconnected", read("tok-phone", phoneProof), true)

	// 7. The device reconnects.
	wantPowers("own device's reconnected snapshot", snapshot(dial("tok-phone", phoneProof)), true)
}
