package handlers

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// dialEventsSocketV2 opens the v2 events socket for account 7 with role and
// query, and returns the connection and a reader that fails the test unless
// the next frame has the wanted type.
func dialEventsSocketV2(t *testing.T, h *EventsSocketV2, role, query string) (*websocket.Conn, func(wantType string) map[string]json.RawMessage) {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	ticket, err := h.Tickets.Mint(t.Context(), evt.SocketIdentity{
		UserID: 7, Role: role, SessionID: "synthetic-session", ProfileID: "a",
		AccessExpiresAt: time.Now().Add(time.Minute), AccessFingerprint: "synthetic-scope",
	})
	if err != nil {
		t.Fatal(err)
	}
	dialer := websocket.Dialer{Subprotocols: []string{EventsSocketProtocol, eventsTicketProtocolPrefix + ticket}}
	conn, resp, err := dialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+query, nil)
	if resp != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, func(wantType string) map[string]json.RawMessage {
		t.Helper()
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("reading %s frame: %v", wantType, err)
		}
		var frame map[string]json.RawMessage
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("frame is not JSON: %v (%s)", err, data)
		}
		if string(frame["type"]) != `"`+wantType+`"` {
			t.Fatalf("frame type = %s, want %q (frame: %s)", frame["type"], wantType, data)
		}
		return frame
	}
}

// An unrecognized channel in a subscribe frame is rejected by name and leaves
// the connection and the valid channels in the same frame working.
func TestEventsSocketV2UnknownChannelKeepsConnection(t *testing.T) {
	h, _ := socketTestHandler()
	conn, readFrame := dialEventsSocketV2(t, h, "user", "")
	readFrame("hello")

	if err := conn.WriteJSON(evt.EventsSubscribeMessage{
		Type:      "subscribe",
		RequestID: "r1",
		Channels:  []evt.EventChannel{"not_a_channel", evt.ChannelUserSettings},
	}); err != nil {
		t.Fatalf("sending subscribe: %v", err)
	}
	subscribed := readFrame("subscribed")
	if !strings.Contains(string(subscribed["rejected"]), `"unknown_channel"`) {
		t.Errorf("unknown channel was not reported as such: %s", subscribed["rejected"])
	}
	if !strings.Contains(string(subscribed["channels"]), `"user_settings"`) {
		t.Fatalf("an unknown channel denied the valid one alongside it: %s", subscribed["channels"])
	}
	readFrame("snapshot")
	publishUserSettingsEvent(t.Context(), h.Events.hub, 7, "", "playback.subtitle_language", "account")
	if event := readFrame("event"); string(event["channel"]) != `"user_settings"` {
		t.Errorf("event channel = %s, want user_settings", event["channel"])
	}
}

// A frame past the read limit ends the connection: it is buffered whole
// before its type is known, so it cannot be answered with a rejection.
func TestEventsSocketV2RejectsOversizeFrame(t *testing.T) {
	h, _ := socketTestHandler()
	conn, readFrame := dialEventsSocketV2(t, h, "user", "")
	readFrame("hello")

	oversize := `{"type":"subscribe","channels":["` + strings.Repeat("x", maxEventsFrameBytes*2) + `"]}`
	// The write may fail once the server has torn the connection down, which
	// is the same outcome; only accepting the frame is a failure.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(oversize)); err != nil {
		return
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("a frame past the read limit was accepted")
	}
}

// The socket must answer control frames while a declared channel's snapshot
// query is blocked, then deliver the snapshot and later events in order.
func TestEventsSocketV2DeclaredChannelsSurviveSlowSnapshot(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	releaseSnapshot := sync.OnceFunc(func() { close(release) })
	defer releaseSnapshot()

	h, _ := socketTestHandler()
	hub := h.Events.hub
	h.Events = NewEventsHandler(hub, nil, nil, blockedTaskLister{entered: entered, release: release}, nil, nil, nil)
	conn, readFrame := dialEventsSocketV2(t, h, "admin", "?channels=tasks")
	readFrame("hello")
	readFrame("subscribed")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot query did not start")
	}

	pong := make(chan struct{}, 1)
	conn.SetPongHandler(func(payload string) error {
		if payload == "snapshot-probe" {
			pong <- struct{}{}
		}
		return nil
	})
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	type snapshotResult struct {
		data []byte
		err  error
	}
	snapshot := make(chan snapshotResult, 1)
	go func() {
		_, data, err := conn.ReadMessage()
		snapshot <- snapshotResult{data: data, err: err}
	}()
	if err := conn.WriteControl(websocket.PingMessage, []byte("snapshot-probe"), time.Now().Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pong:
	case result := <-snapshot:
		t.Fatalf("socket ended before answering ping while snapshot was blocked: %v", result.err)
	case <-time.After(5 * time.Second):
		t.Fatal("socket reader did not answer ping while snapshot was blocked")
	}
	releaseSnapshot()
	result := <-snapshot
	if result.err != nil {
		t.Fatalf("reading released snapshot: %v", result.err)
	}
	var frame map[string]json.RawMessage
	if err := json.Unmarshal(result.data, &frame); err != nil {
		t.Fatal(err)
	}
	if string(frame["type"]) != `"snapshot"` || string(frame["channel"]) != `"tasks"` {
		t.Fatalf("snapshot frame = %s", result.data)
	}
	if err := hub.PublishJSON(t.Context(), evt.ChannelTasks, "tasks.changed", map[string]string{"id": "t1"}, evt.PublishOptions{}); err != nil {
		t.Fatalf("publishing: %v", err)
	}
	if event := readFrame("event"); string(event["channel"]) != `"tasks"` {
		t.Errorf("event channel = %s, want tasks", event["channel"])
	}
}

// blockedTaskLister keeps snapshot work pending until the test observes the
// socket reader answering a ping.
type blockedTaskLister struct {
	entered chan struct{}
	release <-chan struct{}
}

func (s blockedTaskLister) ListTasks(bool) []taskmanager.TaskInfo {
	close(s.entered)
	<-s.release
	return []taskmanager.TaskInfo{}
}

func TestParseDeclaredChannels(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		want     []evt.EventChannel
		declared bool
	}{
		{
			name:     "absent parameter keeps the handshake",
			query:    "",
			want:     nil,
			declared: false,
		},
		{
			name:     "empty value declares nothing, but still declares",
			query:    "channels=",
			want:     []evt.EventChannel{},
			declared: true,
		},
		{
			name:     "whitespace and empty entries are dropped",
			query:    "channels=catalog,%20,,user_state%20",
			want:     []evt.EventChannel{evt.ChannelCatalog, evt.ChannelUserState},
			declared: true,
		},
		{
			// Repeating the parameter is as natural a spelling as one comma
			// list; reading only the first occurrence lost the rest silently.
			name:     "every occurrence of the parameter is read",
			query:    "channels=catalog&channels=user_state,user_settings",
			want:     []evt.EventChannel{evt.ChannelCatalog, evt.ChannelUserState, evt.ChannelUserSettings},
			declared: true,
		},
		{
			name:     "a repeated parameter with only blank values still declares",
			query:    "channels=&channels=%20",
			want:     []evt.EventChannel{},
			declared: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatalf("parsing query: %v", err)
			}
			got, declared := parseDeclaredChannels(query)
			if declared != tt.declared {
				t.Fatalf("declared = %v, want %v", declared, tt.declared)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("channels = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("channels = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestResolveChannelSelectionDeniesByDefault(t *testing.T) {
	allowed := allowedChannelsForRole("user")

	subs, accepted, rejected := resolveChannelSelection(
		[]evt.EventChannel{evt.ChannelSessions, "bogus", evt.ChannelNotifications},
		allowed,
		"", // unbound: notifications requires a profile-bound ticket
	)

	if len(subs) != 0 || len(accepted) != 0 {
		t.Fatalf("nothing should have been accepted: subs=%v accepted=%v", subs, accepted)
	}
	codes := make(map[string]evt.EventChannel, len(rejected))
	for _, r := range rejected {
		codes[r.Code] = r.Channel
	}
	if codes["forbidden"] != evt.ChannelSessions {
		t.Errorf("sessions not rejected as forbidden: %v", rejected)
	}
	if codes["unknown_channel"] != "bogus" {
		t.Errorf("bogus not rejected as unknown: %v", rejected)
	}
	if codes["profile_required"] != evt.ChannelNotifications {
		t.Errorf("notifications not rejected as profile_required: %v", rejected)
	}
}

func TestResolveChannelSelectionDeduplicates(t *testing.T) {
	subs, accepted, rejected := resolveChannelSelection(
		[]evt.EventChannel{evt.ChannelJobs, evt.ChannelJobs},
		allowedChannelsForRole("admin"),
		"",
	)

	if len(subs) != 1 || len(accepted) != 1 {
		t.Fatalf("duplicate channel was not collapsed: subs=%v accepted=%v", subs, accepted)
	}
	if len(rejected) != 0 {
		t.Fatalf("unexpected rejections: %v", rejected)
	}
}

// TestResolveChannelSelectionBoundsTheAnswer is the amplification guard.
// Refusals quote the name they refuse, so once an unknown channel stopped
// closing the connection, the response grew with the request: a large selection
// of distinct garbage names produced a far larger subscribed frame, buffered
// server-side. The answer has to be bounded independently of the request.
func TestResolveChannelSelectionBoundsTheAnswer(t *testing.T) {
	requested := make([]evt.EventChannel, 0, 5000)
	for i := range 5000 {
		requested = append(requested, evt.EventChannel("bogus-"+strconv.Itoa(i)))
	}

	subs, accepted, rejected := resolveChannelSelection(requested, allowedChannelsForRole("user"), "")

	if len(subs) != 0 || len(accepted) != 0 {
		t.Fatalf("garbage names were accepted: subs=%v accepted=%v", subs, accepted)
	}
	// Every considered name is refused, plus exactly one entry for the overrun.
	if len(rejected) != maxRequestedChannels+1 {
		t.Fatalf("rejected %d entries, want %d", len(rejected), maxRequestedChannels+1)
	}
	overrun := rejected[len(rejected)-1]
	if overrun.Code != "too_many_channels" {
		t.Errorf("last rejection code = %q, want too_many_channels", overrun.Code)
	}

	// The response must not scale with the request, whatever the constants are.
	encoded, err := json.Marshal(evt.EventsSubscribedMessage{
		Type:     "subscribed",
		Channels: accepted,
		Rejected: rejected,
	})
	if err != nil {
		t.Fatalf("encoding subscribed frame: %v", err)
	}
	if len(encoded) > 8*1024 {
		t.Errorf("subscribed frame is %d bytes for a garbage selection", len(encoded))
	}
}

// TestResolveChannelSelectionTruncatesLongNames covers the per-name half of the
// same concern: one enormous name is as good an amplifier as many small ones.
func TestResolveChannelSelectionTruncatesLongNames(t *testing.T) {
	long := evt.EventChannel(strings.Repeat("x", 4096))

	_, _, rejected := resolveChannelSelection(
		[]evt.EventChannel{long}, allowedChannelsForRole("user"), "")

	if len(rejected) != 1 {
		t.Fatalf("rejected = %v, want one entry", rejected)
	}
	if len(rejected[0].Channel) != maxChannelNameLength {
		t.Errorf("echoed name is %d bytes, want it truncated to %d",
			len(rejected[0].Channel), maxChannelNameLength)
	}
}

// TestResolveChannelSelectionRefusesPluginsChannel pins that the host-to-plugin
// dispatch channel is not reachable from a client connection, for any role.
func TestResolveChannelSelectionRefusesPluginsChannel(t *testing.T) {
	for _, role := range []string{"user", "admin"} {
		t.Run(role, func(t *testing.T) {
			subs, accepted, rejected := resolveChannelSelection(
				[]evt.EventChannel{evt.ChannelPlugins}, allowedChannelsForRole(role), "profile-1")

			if len(subs) != 0 || len(accepted) != 0 {
				t.Fatalf("%s was granted the plugins channel: %v", role, accepted)
			}
			if len(rejected) != 1 || rejected[0].Code != "unknown_channel" {
				t.Errorf("rejected = %v, want a single unknown_channel entry", rejected)
			}
		})
	}
}
