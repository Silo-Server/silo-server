package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type roomWriterSocket struct {
	started   chan struct{}
	release   chan struct{}
	closed    chan struct{}
	frames    chan string
	once      sync.Once
	closeOnce sync.Once
}

func newRoomWriterSocket(block bool) *roomWriterSocket {
	s := &roomWriterSocket{started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{}), frames: make(chan string, 128)}
	if !block {
		close(s.release)
	}
	return s
}
func (*roomWriterSocket) SetWriteDeadline(time.Time) error { return nil }
func (s *roomWriterSocket) WriteMessage(_ int, payload []byte) error {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.closed:
		return errors.New("closed")
	case <-s.release:
	}
	s.frames <- string(payload)
	return nil
}
func (s *roomWriterSocket) WriteControl(_ int, _ []byte, _ time.Time) error { return nil }
func (s *roomWriterSocket) Close() error                                    { s.closeOnce.Do(func() { close(s.closed) }); return nil }

func TestWatchTogetherSlowWriterDoesNotBlockOtherViewers(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	slow, fast := newRoomWriterSocket(true), newRoomWriterSocket(false)
	a, b := newWatchTogetherRoomConn(slow), newWatchTogetherRoomConn(fast)
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	if err := a.WriteJSON(map[string]int{"sequence": 0}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-slow.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for i := range watchTogetherQueueSize {
		if err := a.WriteJSON(map[string]int{"sequence": i + 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.WriteJSON(map[string]int{"sequence": 99}); err == nil {
		t.Fatal("full queue accepted another frame")
	}
	select {
	case <-slow.closed:
	default:
		t.Fatal("slow connection was not closed")
	}
	for i := range 3 {
		if err := b.WriteJSON(map[string]int{"sequence": i}); err != nil {
			t.Fatal(err)
		}
	}
	for _, expected := range []string{`{"sequence":0}`, `{"sequence":1}`, `{"sequence":2}`} {
		select {
		case frame := <-fast.frames:
			if frame != expected {
				t.Fatalf("order: got %s want %s", frame, expected)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestWatchTogetherMemberStatusIsV2Only(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	snapshot := watchtogether.Snapshot{RoomID: "room", Members: []watchtogether.MemberSummary{
		{UserID: 7, ProfileID: "host", Connected: true, IsReady: true, IsBuffering: true, IsSyncing: true, LobbyReady: true},
		{UserID: 8, ProfileID: "guest", Connected: true},
	}}
	frame := map[string]any{"type": "snapshot", "room": snapshot}
	assertStatus := func(t *testing.T, data []byte, wantStatus bool) {
		t.Helper()
		var payload struct {
			Room struct {
				RoomID  string           `json:"room_id"`
				Members []map[string]any `json:"members"`
			} `json:"room"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Room.RoomID != "room" || len(payload.Room.Members) != 2 || payload.Room.Members[0]["connected"] != true {
			t.Fatalf("existing snapshot fields changed: %s", data)
		}
		for _, key := range []string{"is_ready", "is_buffering", "is_syncing"} {
			value, present := payload.Room.Members[0][key]
			if present != wantStatus || (present && value != true) {
				t.Fatalf("%s = %v, present %v; want status %v", key, value, present, wantStatus)
			}
		}
		for i, member := range payload.Room.Members {
			value, present := member["lobby_ready"]
			if present != wantStatus || (present && value != snapshot.Members[i].LobbyReady) {
				t.Fatalf("member %d lobby_ready = %v, present %v; want status %v", i, value, present, wantStatus)
			}
			if !wantStatus && len(member) != 6 {
				t.Fatalf("v1 member fields changed: %s", data)
			}
		}
	}
	t.Run("v1 HTTP", func(t *testing.T) {
		response, err := new(WatchTogetherHandler).buildRoomResponse(ctx, snapshot, 7, "host")
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		assertStatus(t, data, false)
	})
	t.Run("v2 adapter snapshot", func(t *testing.T) {
		response, err := new(WatchTogetherHandler).buildRoomResponse(ctx, snapshot, 7, "host")
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(map[string]any{"room": response.Room})
		if err != nil {
			t.Fatal(err)
		}
		assertStatus(t, data, true)
	})
	for _, version := range []string{"v1", "v2"} {
		t.Run(version+" socket", func(t *testing.T) {
			socket := newRoomWriterSocket(false)
			conn := newWatchTogetherRoomConn(socket)
			conn.includeMemberStatus = version == "v2"
			t.Cleanup(func() { _ = conn.Close() })
			if err := conn.WriteJSON(frame); err != nil {
				t.Fatal(err)
			}
			select {
			case data := <-socket.frames:
				assertStatus(t, []byte(data), version == "v2")
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
	if member := frame["room"].(watchtogether.Snapshot).Members[0]; !member.IsReady || !member.IsBuffering || !member.IsSyncing || !member.LobbyReady {
		t.Fatal("v1 projection mutated the snapshot shared with v2 viewers")
	}
}

func TestWatchTogetherV1RejectsLobbyReady(t *testing.T) {
	h := new(WatchTogetherHandler)
	for _, payload := range []string{`{"type":"lobby_ready","ready":true}`, `{"type":"lobby_ready","ready":false}`} {
		err := h.handleRoomClientMessage(t.Context(), new(watchTogetherRoomConn), nil, 7, "profile", []byte(payload))
		if err == nil || err.Error() != "unsupported room websocket message" {
			t.Fatalf("v1 lobby_ready error = %v", err)
		}
	}
}

func TestWatchTogetherReplacementFlushesBeforeClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	socket := newRoomWriterSocket(true)
	conn := newWatchTogetherRoomConn(socket)
	conn.includeMemberStatus = true
	t.Cleanup(func() { _ = conn.Close() })
	returned := make(chan struct{})
	go func() { _ = conn.CloseReplaced(); close(returned) }()
	select {
	case <-socket.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-socket.closed:
		t.Fatal("socket closed before the terminal write completed")
	case <-returned:
		t.Fatal("replacement returned before the terminal write completed")
	default:
	}
	close(socket.release)
	select {
	case <-returned:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case frame := <-socket.frames:
		var message map[string]string
		if err := json.Unmarshal([]byte(frame), &message); err != nil {
			t.Fatal(err)
		}
		if message["type"] != "connection_replaced" || message["reason"] != "This profile joined the Watch Party on another device." {
			t.Fatalf("terminal frame = %s", frame)
		}
	default:
		t.Fatal("close discarded terminal frame")
	}
	select {
	case <-socket.closed:
	default:
		t.Fatal("displaced socket remains open")
	}
}

func TestWatchTogetherReplacementClosesBlockedWriter(t *testing.T) {
	socket := newRoomWriterSocket(true)
	conn := newWatchTogetherRoomConn(socket)
	conn.includeMemberStatus = true
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.WriteJSON(map[string]string{"type": "snapshot"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*watchTogetherReplacementTimeout)
	defer cancel()
	select {
	case <-socket.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	done := make(chan struct{})
	go func() { _ = conn.CloseReplaced(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("blocked writer delayed replacement beyond its deadline")
	}
	select {
	case <-socket.closed:
	default:
		t.Fatal("blocked displaced socket remains open")
	}
}

func TestWatchTogetherV1ReplacementRetainsCloseBehavior(t *testing.T) {
	socket := newRoomWriterSocket(false)
	conn := newWatchTogetherRoomConn(socket)
	if err := conn.CloseReplaced(); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-socket.frames:
		t.Fatalf("v2 replacement leaked into v1: %s", frame)
	default:
	}
	select {
	case <-socket.closed:
	default:
		t.Fatal("v1 replacement did not close")
	}
}
