package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/realtimeadmission"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type admissionTestStore struct {
	result ratelimit.WorkResult
	keys   []string
}

func (s *admissionTestStore) AllowWork(_ context.Context, key string, _ ratelimit.WorkLimit) ratelimit.WorkResult {
	s.keys = append(s.keys, key)
	return s.result
}

func TestRealtimeEventsRefusalPreservesSelectionAndRecoveryOrder(t *testing.T) {
	store := &admissionTestStore{result: ratelimit.WorkResult{RetryAfter: time.Second}}
	h := &EventsHandler{admission: realtimeadmission.New(store, "memory")}
	var writes []any
	var order []string
	output := eventsSelectionOutput{
		write:    func(v any) error { writes = append(writes, v); order = append(order, "frame"); return nil },
		snapshot: func(ch evt.EventChannel) error { order = append(order, string(ch)); return nil },
	}
	r := httptest.NewRequest(http.MethodGet, "/events/ws", nil)
	payload := []byte(`{"type":"subscribe","request_id":"selection-2","channels":["user_state"]}`)
	allowed := []evt.EventChannel{evt.ChannelUserState}
	result := h.applyEventsSelection(r, &auth.Claims{UserID: 7}, "profile", payload, allowed, output)
	if result.applied || result.close || result.selection != nil || result.refusal == nil || len(writes) != 1 || !slices.Equal(order, []string{"frame"}) {
		t.Fatalf("refusal started selection/snapshot: %+v %v", result, order)
	}
	refusal, ok := writes[0].(realtimeErrorFrame)
	if !ok || refusal.RequestID != "selection-2" || refusal.WorkStarted == nil || *refusal.WorkStarted || refusal.Code != "realtime_rate_limited" {
		t.Fatalf("uncorrelated refusal: %+v", writes[0])
	}
	store.result = ratelimit.WorkResult{Allowed: true}
	writes, order = nil, nil
	result = h.applyEventsSelection(r, &auth.Claims{UserID: 7}, "profile", payload, allowed, output)
	if !result.applied || result.close || !slices.Equal(order, []string{"frame", "user_state"}) {
		t.Fatalf("recovery order: %+v %v", result, order)
	}
	if _, ok := result.selection[evt.ChannelUserState]; !ok {
		t.Fatal("accepted selection not published")
	}
	// An unchanged selection remains an explicitly charged refresh.
	_ = h.applyEventsSelection(r, &auth.Claims{UserID: 7}, "profile", payload, allowed, output)
	if len(store.keys) != 3 {
		t.Fatalf("refresh admissions: %v", store.keys)
	}
}

func TestRealtimeEventsInitialCreditOnceAndOriginalDeadline(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name      string
		prepaid   bool
		until     time.Time
		wantCalls int
	}{
		{"valid", true, now.Add(time.Second), 1}, {"expired", true, now.Add(-time.Second), 2}, {"legacy", false, now.Add(time.Second), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &admissionTestStore{result: ratelimit.WorkResult{Allowed: true}}
			h := &EventsHandler{admission: realtimeadmission.New(store, "memory")}
			ctx := withEventsInitialWork(t.Context(), tc.prepaid, tc.until)
			for range 2 {
				if err := h.admitEventsSelection(ctx, 7); err != nil {
					t.Fatal(err)
				}
			}
			if len(store.keys) != tc.wantCalls {
				t.Fatalf("calls=%d want=%d", len(store.keys), tc.wantCalls)
			}
		})
	}
	ctx, cancel := context.WithCancel(withEventsInitialWork(t.Context(), true, now.Add(time.Minute)))
	cancel()
	if err := new(EventsHandler).admitEventsSelection(ctx, 7); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled prepaid work: %v", err)
	}
}

func TestRealtimeRoomRefusedBeforeServiceAndCorrelated(t *testing.T) {
	for _, message := range []string{
		`{"type":"attach_session","session_id":"playback"}`, `{"type":"state_report","session_id":"playback"}`, `{"type":"ready","session_id":"playback"}`, `{"type":"buffering","session_id":"playback"}`, `{"type":"transport_request","action":"pause"}`, `{"type":"lobby_ready","ready":true}`,
	} {
		t.Run(message, func(t *testing.T) {
			store := &admissionTestStore{result: ratelimit.WorkResult{RetryAfter: time.Second}}
			h := &WatchTogetherHandler{Admission: realtimeadmission.New(store, "memory")}
			rc := &watchTogetherRoomConn{roomID: "room", includeMemberStatus: true, outgoing: make(chan watchTogetherFrame, 2), done: make(chan struct{}), replaced: make(chan struct{})}
			var fields map[string]any
			if err := json.Unmarshal([]byte(message), &fields); err != nil {
				t.Fatal(err)
			}
			fields["request_id"] = "intent"
			data, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			// A nil service intentionally cannot be entered by any refused operation.
			err = h.handleRoomClientMessage(t.Context(), rc, nil, 7, "profile", data)
			if err == nil || len(store.keys) != 1 {
				t.Fatalf("member refusal: %v keys=%v", err, store.keys)
			}
			rc.writeClientError(err)
			frame := <-rc.outgoing
			var refusal realtimeErrorFrame
			if err := json.Unmarshal(frame.data, &refusal); err != nil {
				t.Fatal(err)
			}
			if refusal.Code != "realtime_rate_limited" || refusal.RequestID != "intent" || refusal.WorkStarted == nil || *refusal.WorkStarted {
				t.Fatalf("refusal=%s", frame.data)
			}
			if len(rc.outgoing) != 0 {
				t.Fatal("success frame on refusal")
			}
		})
	}
}

func TestRealtimeRoomInvalidInputAndPingAvoidWork(t *testing.T) {
	store := &admissionTestStore{result: ratelimit.WorkResult{Allowed: true}}
	h := &WatchTogetherHandler{Admission: realtimeadmission.New(store, "memory")}
	rc := &watchTogetherRoomConn{roomID: "room", outgoing: make(chan watchTogetherFrame, 2), done: make(chan struct{}), replaced: make(chan struct{})}
	for _, data := range []string{`{"type":"ready"}`, `{"type":"transport_request","action":"invalid"}`, `{"type":"lobby_ready","ready":true}`, `{"type":"unknown"}`} {
		if err := h.handleRoomClientMessage(t.Context(), rc, nil, 7, "profile", []byte(data)); err == nil {
			t.Fatalf("invalid accepted: %s", data)
		}
	}
	if err := h.handleRoomClientMessage(t.Context(), rc, nil, 7, "profile", []byte(`{"type":"ping","client_sent_at":"synthetic"}`)); err != nil {
		t.Fatal(err)
	}
	if len(store.keys) != 0 {
		t.Fatalf("cheap input charged: %v", store.keys)
	}
	if frame := <-rc.outgoing; !json.Valid(frame.data) {
		t.Fatal("invalid pong")
	}
}

func TestRealtimeRoomActionResultFollowsServiceSnapshot(t *testing.T) {
	socket, _, _ := roomSocketHandler(t)
	store := &admissionTestStore{result: ratelimit.WorkResult{Allowed: true}}
	socket.Room.Admission = realtimeadmission.New(store, "memory")
	rc := &watchTogetherRoomConn{roomID: "room", includeMemberStatus: true, outgoing: make(chan watchTogetherFrame, 8), done: make(chan struct{}), replaced: make(chan struct{})}
	reg, _, err := socket.Room.Service.Connect(t.Context(), "room", 7, "profile", rc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { socket.Room.Service.Disconnect(reg, false) })
	// Discard Connect's initial snapshot; exercise the actual service and dispatch.
	for len(rc.outgoing) > 0 {
		<-rc.outgoing
	}
	err = socket.Room.handleRoomClientMessage(t.Context(), rc, reg, 7, "profile", []byte(`{"type":"lobby_ready","request_id":"ready-1","ready":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(store.keys) != 2 {
		t.Fatalf("member/account gate calls: %v", store.keys)
	}
	first, second := <-rc.outgoing, <-rc.outgoing
	var snapshot struct {
		Type string                 `json:"type"`
		Room watchtogether.Snapshot `json:"room"`
	}
	if err := json.Unmarshal(first.data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Type != "snapshot" || len(snapshot.Room.Members) != 1 || !snapshot.Room.Members[0].LobbyReady {
		t.Fatalf("service snapshot=%s", first.data)
	}
	var applied map[string]string
	if err := json.Unmarshal(second.data, &applied); err != nil {
		t.Fatal(err)
	}
	if applied["type"] != "action_result" || applied["request_id"] != "ready-1" || applied["status"] != "applied" {
		t.Fatalf("result=%s", second.data)
	}
	// A no-op still acknowledges successful completion.
	if err := socket.Room.handleRoomClientMessage(t.Context(), rc, reg, 7, "profile", []byte(`{"type":"lobby_ready","request_id":"ready-2","ready":true}`)); err != nil {
		t.Fatal(err)
	}
	var last []byte
	for len(rc.outgoing) > 0 {
		last = (<-rc.outgoing).data
	}
	if !json.Valid(last) {
		t.Fatal("no no-op acknowledgement")
	}
}

type admissionShieldSocket struct{ frames []string }

func (*admissionShieldSocket) SetWriteDeadline(time.Time) error { return nil }
func (s *admissionShieldSocket) WriteMessage(_ int, data []byte) error {
	s.frames = append(s.frames, string(data))
	return nil
}
func (s *admissionShieldSocket) WriteControl(kind int, data []byte, _ time.Time) error {
	if kind == websocket.CloseMessage {
		s.frames = append(s.frames, string(data))
	}
	return nil
}
func (*admissionShieldSocket) Close() error { return nil }
func TestRealtimeRoomShieldNoticeBeforePolicyClose(t *testing.T) {
	socket := new(admissionShieldSocket)
	rc := newWatchTogetherRoomConn(socket)
	rc.closeInputViolation()
	<-rc.done
	if len(socket.frames) != 2 || !json.Valid([]byte(socket.frames[0])) || socket.frames[1] != string(websocket.FormatCloseMessage(1008, realtimeInputCloseReason)) {
		t.Fatalf("shield ordering=%q", socket.frames)
	}
}
