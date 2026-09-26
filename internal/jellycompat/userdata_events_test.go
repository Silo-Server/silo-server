package jellycompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

type recordedUserStateEvent struct {
	channel evt.EventChannel
	event   string
	payload userStateChangedPayload
	opts    evt.PublishOptions
}

type fakeUserStateEvents struct {
	mu        sync.Mutex
	published []recordedUserStateEvent
	envelopes chan evt.Envelope
}

func newFakeUserStateEvents() *fakeUserStateEvents {
	return &fakeUserStateEvents{envelopes: make(chan evt.Envelope, 16)}
}

func (f *fakeUserStateEvents) PublishJSON(_ context.Context, channel evt.EventChannel, event string, data any, opts evt.PublishOptions) error {
	payload, ok := data.(userStateChangedPayload)
	if !ok {
		return fmt.Errorf("unexpected payload %T", data)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, recordedUserStateEvent{channel: channel, event: event, payload: payload, opts: opts})
	return nil
}

func (f *fakeUserStateEvents) Subscribe() (<-chan evt.Envelope, func()) {
	return f.envelopes, func() {}
}

func userStateEnvelope(t *testing.T, userID int, profileID string, payload userStateChangedPayload) evt.Envelope {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return evt.Envelope{
		Channel:   evt.ChannelUserState,
		Event:     userStateChangedEvent,
		Data:      data,
		UserID:    userID,
		ProfileID: profileID,
	}
}

func TestPublishWatchedChangeAnnouncesEachItemUpToTheCap(t *testing.T) {
	events := newFakeUserStateEvents()
	session := &Session{StreamAppUserID: 3, ProfileID: "profile-1"}
	ids := make([]string, 0, maxWatchedEventsPerBatch+5)
	for i := range maxWatchedEventsPerBatch + 5 {
		ids = append(ids, fmt.Sprintf("episode-%d", i))
	}
	ids = append([]string{""}, ids...)

	publishWatchedChange(t.Context(), events, session, ids, true)

	if len(events.published) != maxWatchedEventsPerBatch {
		t.Fatalf("published %d events, want %d", len(events.published), maxWatchedEventsPerBatch)
	}
	first := events.published[0]
	if first.channel != evt.ChannelUserState || first.event != userStateChangedEvent {
		t.Fatalf("event %s/%s", first.channel, first.event)
	}
	if first.payload.ContentID != "episode-0" || first.payload.Change != userStateChangeWatched ||
		first.payload.Played == nil || !*first.payload.Played || first.payload.ProfileID != "profile-1" {
		t.Fatalf("payload %+v", first.payload)
	}
	if first.opts.UserID != 3 || first.opts.ProfileID != "profile-1" {
		t.Fatalf("scope %+v", first.opts)
	}
}

func TestPublishWatchedChangeSkipsUnscopedSessions(t *testing.T) {
	events := newFakeUserStateEvents()
	publishWatchedChange(t.Context(), events, &Session{StreamAppUserID: 3}, []string{"movie-1"}, false)
	publishWatchedChange(t.Context(), events, &Session{ProfileID: "profile-1"}, []string{"movie-1"}, false)
	publishWatchedChange(t.Context(), nil, &Session{StreamAppUserID: 3, ProfileID: "profile-1"}, []string{"movie-1"}, false)
	if len(events.published) != 0 {
		t.Fatalf("published %+v", events.published)
	}
}

func TestUserDataChangedForTranslatesOnlyTheSessionsOwnChanges(t *testing.T) {
	codec := NewResourceIDCodec()
	session := &Session{StreamAppUserID: 3, ProfileID: "profile-1", PseudoUserID: uuid.New()}
	played := true

	msg, ok := userDataChangedFor(userStateEnvelope(t, 3, "profile-1", userStateChangedPayload{
		ProfileID: "profile-1", ContentID: "movie-1", Change: userStateChangeWatched, Played: &played,
	}), session, codec)
	if !ok || msg.MessageType != wsUserDataChanged {
		t.Fatalf("message %+v ok=%v", msg, ok)
	}
	var data userDataChangedMessage
	if err := json.Unmarshal(msg.Data, &data); err != nil {
		t.Fatal(err)
	}
	wantItem := codec.EncodeStringID(EncodedIDItem, "movie-1")
	if data.UserID != session.PseudoUserID.String() || len(data.UserDataList) != 1 ||
		data.UserDataList[0].ItemID != wantItem || data.UserDataList[0].Played == nil || !*data.UserDataList[0].Played {
		t.Fatalf("data %+v", data)
	}

	for name, env := range map[string]evt.Envelope{
		"other account":   userStateEnvelope(t, 4, "profile-1", userStateChangedPayload{ProfileID: "profile-1", ContentID: "movie-1", Change: userStateChangeWatched}),
		"other profile":   userStateEnvelope(t, 3, "profile-2", userStateChangedPayload{ProfileID: "profile-2", ContentID: "movie-1", Change: userStateChangeWatched}),
		"no content":      userStateEnvelope(t, 3, "profile-1", userStateChangedPayload{ProfileID: "profile-1", Change: "progress"}),
		"no account":      userStateEnvelope(t, 0, "profile-1", userStateChangedPayload{ProfileID: "profile-1", ContentID: "movie-1", Change: userStateChangeWatched}),
		"another channel": {Channel: evt.ChannelCatalog, UserID: 3, ProfileID: "profile-1"},
	} {
		if msg, ok := userDataChangedFor(env, session, codec); ok {
			t.Errorf("%s: forwarded %+v", name, msg)
		}
	}
}

func TestSocketForwardsUserDataChangedForTheSession(t *testing.T) {
	sessions := NewSessionStore(time.Hour, nil)
	session := Session{Token: "token-1", StreamAppUserID: 3, ProfileID: "profile-1", PseudoUserID: uuid.New()}
	if err := sessions.Put(session); err != nil {
		t.Fatal(err)
	}
	events := newFakeUserStateEvents()
	codec := NewResourceIDCodec()
	server := httptest.NewServer(NewSocketHandlerWithUserData(sessions, nil, events, codec))
	defer server.Close()

	header := http.Header{"X-Emby-Token": []string{"token-1"}}
	conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), header)
	if response != nil {
		defer func() { _ = response.Body.Close() }()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var msg wsMessage
	if err := conn.ReadJSON(&msg); err != nil || msg.MessageType != "ForceKeepAlive" {
		t.Fatalf("initial message %+v err=%v", msg, err)
	}

	played := true
	// Another profile's change must not reach this socket; the session's own does.
	events.envelopes <- userStateEnvelope(t, 3, "profile-2", userStateChangedPayload{ProfileID: "profile-2", ContentID: "movie-2", Change: userStateChangeWatched, Played: &played})
	events.envelopes <- userStateEnvelope(t, 3, "profile-1", userStateChangedPayload{ProfileID: "profile-1", ContentID: "movie-1", Change: userStateChangeWatched, Played: &played})

	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatal(err)
	}
	if msg.MessageType != wsUserDataChanged {
		t.Fatalf("message %+v", msg)
	}
	var data userDataChangedMessage
	if err := json.Unmarshal(msg.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.UserID != session.PseudoUserID.String() || len(data.UserDataList) != 1 ||
		data.UserDataList[0].ItemID != codec.EncodeStringID(EncodedIDItem, "movie-1") {
		t.Fatalf("data %+v", data)
	}
}
