package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/cache"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// eventsWSTestConn dials the events websocket against a handler authenticated
// as the given claims, and returns a frame reader. These tests go through the
// real socket rather than calling the handler directly because the behavior
// under test — what a connection is subscribed to before it has said anything,
// and whether it survives the grace period — only exists in the connection
// loop.
func eventsWSTestConn(t *testing.T, hub *evt.Hub, claims *auth.Claims, query string) (
	*websocket.Conn,
	func(wantType string) map[string]json.RawMessage,
) {
	t.Helper()
	return eventsWSTestConnWithHandler(t, &EventsHandler{hub: hub}, claims, query)
}

func eventsWSTestConnWithHandler(
	t *testing.T,
	handler *EventsHandler,
	claims *auth.Claims,
	query string,
) (*websocket.Conn, func(wantType string) map[string]json.RawMessage) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := apimw.SetClaims(r.Context(), claims)
		handler.HandleWebSocket(w, r.WithContext(ctx))
	}))
	t.Cleanup(server.Close)

	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+query, nil)
	if err != nil {
		t.Fatalf("dialing events websocket: %v", err)
	}
	if resp != nil && resp.Body != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	t.Cleanup(func() { _ = conn.Close() })

	readFrame := func(wantType string) map[string]json.RawMessage {
		t.Helper()
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("setting read deadline: %v", err)
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

	return conn, readFrame
}

// TestEventsWebSocketDeclaredChannelsSkipHandshake is the point of the feature:
// a connection that named its channels on the URL receives events without ever
// writing to the socket.
func TestEventsWebSocketDeclaredChannelsSkipHandshake(t *testing.T) {
	hub := evt.NewHub("test", &cache.NoopEventBus{})
	_, readFrame := eventsWSTestConn(t, hub,
		&auth.Claims{UserID: 1, Role: "user"}, "?channels=user_settings")

	hello := readFrame("hello")
	if string(hello["required_action"]) != `"none"` {
		t.Errorf("required_action = %s, want \"none\"", hello["required_action"])
	}

	subscribed := readFrame("subscribed")
	if !strings.Contains(string(subscribed["channels"]), `"user_settings"`) {
		t.Fatalf("declared channel was not accepted: %s", subscribed["channels"])
	}

	// Accepted channels hydrate with a snapshot, exactly as the handshake does.
	if snapshot := readFrame("snapshot"); string(snapshot["channel"]) != `"user_settings"` {
		t.Fatalf("snapshot channel = %s, want user_settings", snapshot["channel"])
	}

	publishUserSettingsEvent(context.Background(), hub, 1, "profile-1",
		"playback.subtitle_language", "profile")

	if event := readFrame("event"); string(event["channel"]) != `"user_settings"` {
		t.Errorf("event channel = %s, want user_settings", event["channel"])
	}
}

// TestEventsWebSocketDeclaredChannelsSurviveGracePeriod guards the core promise
// of the change: an observer that connects and never speaks stays connected.
// Before this, it was closed with a policy violation after five seconds.
func TestEventsWebSocketDeclaredChannelsSurviveGracePeriod(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the subscribe grace period in real time")
	}

	hub := evt.NewHub("test", &cache.NoopEventBus{})
	conn, readFrame := eventsWSTestConn(t, hub,
		&auth.Claims{UserID: 1, Role: "user"}, "?channels=user_settings")

	readFrame("hello")
	readFrame("subscribed")
	readFrame("snapshot")

	// Stay silent well past the deadline that would have closed a
	// handshake-style connection, then confirm the socket still delivers.
	time.Sleep(subscribeGracePeriod + time.Second)

	publishUserSettingsEvent(context.Background(), hub, 1, "profile-1",
		"playback.subtitle_language", "profile")

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("setting read deadline: %v", err)
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("connection did not survive the grace period: %v", err)
	}
	var frame map[string]json.RawMessage
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("frame is not JSON: %v (%s)", err, data)
	}
	if string(frame["type"]) != `"event"` {
		t.Fatalf("frame type = %s, want \"event\" (frame: %s)", frame["type"], data)
	}
}

// TestEventsWebSocketEmptyDeclarationStillClosed pins what disarms the grace
// period: holding a subscription, not having spelled ?channels=. A declaration
// that resolved to nothing leaves the connection in the exact state the clock
// exists to reap — no subscriptions, no reason to expect a frame, but still a
// hub subscriber, two goroutines, and an envelope channel every published
// event fans into.
func TestEventsWebSocketEmptyDeclarationStillClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the subscribe grace period in real time")
	}

	tests := []struct {
		name  string
		query string
	}{
		{name: "declares no channels", query: "?channels="},
		// Every name refused: a non-admin naming only an admin channel is
		// subscribed to nothing, exactly as if it had named nothing.
		{name: "every declared channel refused", query: "?channels=sessions"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := evt.NewHub("test", &cache.NoopEventBus{})
			conn, readFrame := eventsWSTestConn(t, hub,
				&auth.Claims{UserID: 1, Role: "user"}, tt.query)

			hello := readFrame("hello")
			// The obligation is real, so the hello frame has to say so rather
			// than sending the client off to wait silently on a doomed socket.
			if string(hello["required_action"]) != `"subscribe"` {
				t.Errorf("required_action = %s, want \"subscribe\"", hello["required_action"])
			}
			readFrame("subscribed")

			if err := conn.SetReadDeadline(time.Now().Add(subscribeGracePeriod + 5*time.Second)); err != nil {
				t.Fatalf("setting read deadline: %v", err)
			}
			// The error frame, then the close.
			if _, _, err := conn.ReadMessage(); err != nil {
				t.Fatalf("reading error frame: %v", err)
			}
			if _, _, err := conn.ReadMessage(); !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
				t.Fatalf("close error = %v, want policy violation", err)
			}
		})
	}
}

// TestEventsWebSocketPartialDeclarationSurvives is the boundary case on the
// other side: one accepted channel among refusals is a live subscription, so
// the connection is not on the clock.
func TestEventsWebSocketPartialDeclarationSurvives(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the subscribe grace period in real time")
	}

	hub := evt.NewHub("test", &cache.NoopEventBus{})
	conn, readFrame := eventsWSTestConn(t, hub,
		&auth.Claims{UserID: 1, Role: "user"}, "?channels=sessions,user_settings")

	hello := readFrame("hello")
	if string(hello["required_action"]) != `"none"` {
		t.Errorf("required_action = %s, want \"none\"", hello["required_action"])
	}
	readFrame("subscribed")
	readFrame("snapshot")

	time.Sleep(subscribeGracePeriod + time.Second)

	publishUserSettingsEvent(context.Background(), hub, 1, "profile-1",
		"playback.subtitle_language", "profile")

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("setting read deadline: %v", err)
	}
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("a partially accepted declaration did not survive the grace period: %v", err)
	}
}

// TestEventsWebSocketRepeatedChannelsParameter covers the other natural
// spelling of a selection. Honoring only the first occurrence dropped the rest
// with an empty rejected array — the connection came up subscribed to less than
// it asked for and reported nothing wrong.
func TestEventsWebSocketRepeatedChannelsParameter(t *testing.T) {
	hub := evt.NewHub("test", &cache.NoopEventBus{})
	_, readFrame := eventsWSTestConn(t, hub, &auth.Claims{UserID: 1, Role: "user"},
		"?channels=catalog&channels=user_settings")

	readFrame("hello")
	subscribed := readFrame("subscribed")

	for _, want := range []string{`"catalog"`, `"user_settings"`} {
		if !strings.Contains(string(subscribed["channels"]), want) {
			t.Errorf("channel %s was dropped: %s", want, subscribed["channels"])
		}
	}
}

// TestEventsWebSocketSilentConnectionStillClosed pins the other half: the grace
// period still applies to a connection that declared nothing, so the URL path
// relaxes the rule rather than removing it.
func TestEventsWebSocketSilentConnectionStillClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the subscribe grace period in real time")
	}

	hub := evt.NewHub("test", &cache.NoopEventBus{})
	conn, readFrame := eventsWSTestConn(t, hub, &auth.Claims{UserID: 1, Role: "user"}, "")

	hello := readFrame("hello")
	if string(hello["required_action"]) != `"subscribe"` {
		t.Errorf("required_action = %s, want \"subscribe\"", hello["required_action"])
	}

	if err := conn.SetReadDeadline(time.Now().Add(subscribeGracePeriod + 5*time.Second)); err != nil {
		t.Fatalf("setting read deadline: %v", err)
	}
	// The error frame, then the close.
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("reading error frame: %v", err)
	}
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("silent connection was not closed after the grace period")
	}
	if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("close error = %v, want policy violation", err)
	}
}

// TestEventsWebSocketDeclaredChannelsCannotEscalate is the authorization
// guarantee: naming an admin-only channel on the URL must not grant it, and
// must not grant the events published to it either.
func TestEventsWebSocketDeclaredChannelsCannotEscalate(t *testing.T) {
	hub := evt.NewHub("test", &cache.NoopEventBus{})
	_, readFrame := eventsWSTestConn(t, hub,
		&auth.Claims{UserID: 1, Role: "user"}, "?channels=sessions,user_settings")

	readFrame("hello")
	subscribed := readFrame("subscribed")

	if strings.Contains(string(subscribed["channels"]), `"sessions"`) {
		t.Fatalf("non-admin was granted the sessions channel: %s", subscribed["channels"])
	}
	if !strings.Contains(string(subscribed["rejected"]), `"forbidden"`) {
		t.Errorf("sessions was not reported as forbidden: %s", subscribed["rejected"])
	}
	// The permitted channel in the same request still landed.
	if !strings.Contains(string(subscribed["channels"]), `"user_settings"`) {
		t.Errorf("a forbidden channel denied the rest of the request: %s", subscribed["channels"])
	}

	readFrame("snapshot") // user_settings

	// An admin-only event on the refused channel must not be delivered. Publish
	// it first, then a permitted event; receiving the second without the first
	// proves the first was filtered rather than merely slow.
	if err := hub.PublishJSON(context.Background(), evt.ChannelSessions, "sessions.replaced", nil,
		evt.PublishOptions{AdminOnly: true}); err != nil {
		t.Fatalf("publishing admin-only event: %v", err)
	}
	publishUserSettingsEvent(context.Background(), hub, 1, "profile-1",
		"playback.subtitle_language", "profile")

	event := readFrame("event")
	if string(event["channel"]) != `"user_settings"` {
		t.Fatalf("received an event on a channel this connection was refused: %s", event["channel"])
	}
}

// TestEventsWebSocketUnknownChannelDoesNotCloseConnection covers the third
// change: an unrecognized channel name used to close the socket outright,
// taking down every other channel the client held over one bad name.
func TestEventsWebSocketUnknownChannelDoesNotCloseConnection(t *testing.T) {
	hub := evt.NewHub("test", &cache.NoopEventBus{})
	conn, readFrame := eventsWSTestConn(t, hub, &auth.Claims{UserID: 1, Role: "user"}, "")

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

	// The connection is still usable.
	readFrame("snapshot")
	publishUserSettingsEvent(context.Background(), hub, 1, "profile-1",
		"playback.subtitle_language", "profile")
	if event := readFrame("event"); string(event["channel"]) != `"user_settings"` {
		t.Errorf("event channel = %s, want user_settings", event["channel"])
	}
}

// TestEventsWebSocketRejectsOversizeFrame covers the one case that is still
// fatal, and has to be: a frame is buffered whole before its type can be read,
// so an oversize frame cannot be answered with a rejection the way a bad
// channel name can — refusing it politely would mean first doing the thing the
// limit exists to prevent.
func TestEventsWebSocketRejectsOversizeFrame(t *testing.T) {
	hub := evt.NewHub("test", &cache.NoopEventBus{})
	conn, readFrame := eventsWSTestConn(t, hub, &auth.Claims{UserID: 1, Role: "user"}, "")
	readFrame("hello")

	oversize := `{"type":"subscribe","channels":["` +
		strings.Repeat("x", maxEventsFrameBytes*2) + `"]}`
	// The write itself may fail once the server has already torn the connection
	// down, which is the same outcome; only accepting the frame is a failure.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(oversize)); err != nil {
		return
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("setting read deadline: %v", err)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("a frame past the read limit was accepted")
	}
}

// slowTaskLister stalls the tasks snapshot long enough to outlast the read
// deadline configureWebSocket installs at connect.
type slowTaskLister struct{ delay time.Duration }

func (s slowTaskLister) ListTasks(bool) []taskmanager.TaskInfo {
	time.Sleep(s.delay)
	return []taskmanager.TaskInfo{}
}

// TestEventsWebSocketDeclaredChannelsSurviveSlowSnapshot pins the ordering the
// declared path depends on. configureWebSocket sets an absolute read deadline
// that only pongs extend, and gorilla processes pongs solely inside
// ReadMessage — so if snapshot queries ran before the reader goroutine started,
// a snapshot slower than the deadline would kill a healthy connection the
// instant reading began. The handshake path gets this for free by building
// snapshots downstream of an active reader; the declared path arranges it
// deliberately.
func TestEventsWebSocketDeclaredChannelsSurviveSlowSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("stalls a snapshot past the websocket read deadline in real time")
	}

	hub := evt.NewHub("test", &cache.NoopEventBus{})
	handler := &EventsHandler{
		hub:   hub,
		tasks: slowTaskLister{delay: wsPingInterval + wsPongTimeout + 2*time.Second},
	}

	conn, readFrame := eventsWSTestConnWithHandler(t, handler,
		&auth.Claims{UserID: 1, Role: "admin"}, "?channels=tasks")

	readFrame("hello")
	readFrame("subscribed")

	// The snapshot arrives late by design; allow for the stall plus slack.
	if err := conn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout + 15*time.Second)); err != nil {
		t.Fatalf("setting read deadline: %v", err)
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("connection died during a slow snapshot: %v", err)
	}
	var frame map[string]json.RawMessage
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("frame is not JSON: %v (%s)", err, data)
	}
	if string(frame["type"]) != `"snapshot"` {
		t.Fatalf("frame type = %s, want \"snapshot\" (frame: %s)", frame["type"], data)
	}

	// And the connection is still live afterwards.
	if err := hub.PublishJSON(context.Background(), evt.ChannelTasks, "tasks.changed",
		map[string]string{"id": "t1"}, evt.PublishOptions{}); err != nil {
		t.Fatalf("publishing: %v", err)
	}
	if event := readFrame("event"); string(event["channel"]) != `"tasks"` {
		t.Errorf("event channel = %s, want tasks", event["channel"])
	}
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

// TestResolveChannelSelectionDeduplicatesRejections covers the other half of
// dedup: a channel asked for twice is answered once whether it was accepted or
// refused. Only the accepted side deduplicated before.
func TestResolveChannelSelectionDeduplicatesRejections(t *testing.T) {
	_, _, rejected := resolveChannelSelection(
		[]evt.EventChannel{
			evt.ChannelSessions, evt.ChannelSessions, // forbidden for a user
			"bogus", "bogus", // unknown
		},
		allowedChannelsForRole("user"),
		"",
	)

	if len(rejected) != 2 {
		t.Fatalf("rejected = %v, want one entry per distinct channel", rejected)
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
