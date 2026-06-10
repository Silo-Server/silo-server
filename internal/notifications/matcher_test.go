package notifications

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

func publishAndSettle(t *testing.T, hub *evt.Hub, m *Materializer, env evt.Envelope) {
	t.Helper()
	before := m.Processed()
	if err := hub.Publish(context.Background(), env); err != nil {
		t.Fatalf("publish: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for m.Processed() <= before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if m.Processed() <= before {
		t.Fatalf("materializer did not process the envelope in time")
	}
}

func TestSendContract_CreatesNotification(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	payload := map[string]any{
		"user_id": 5, "category": "request", "type": "request.fulfilled",
		"title": "Your audiobook is ready", "body": "Dune by Frank Herbert",
		"dedup_ref": "ab-req-9:fulfilled",
	}
	data, _ := json.Marshal(payload)
	publishAndSettle(t, hub, m, evt.Envelope{
		Channel: evt.ChannelPlugins, Event: "plugin.silo.audiobook-requests.notifications.send", Data: data,
	})

	if len(store.inserted) != 1 {
		t.Fatalf("inserted = %d, want 1", len(store.inserted))
	}
	n := store.inserted[0]
	if n.UserID != 5 || n.Category != CategoryRequest || n.Type != "request.fulfilled" ||
		n.Title != "Your audiobook is ready" || n.DedupRef != "ab-req-9:fulfilled" ||
		n.SourceEvent != "plugin.silo.audiobook-requests.notifications.send" {
		t.Fatalf("bad notification: %+v", n)
	}
}

func TestSendContract_MalformedDropped(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	for _, raw := range []string{
		`{"category":"request","type":"t","title":"x"}`,                  // no user_id
		`{"user_id":5,"category":"announcement","type":"t","title":"x"}`, // plugins may not create announcements
		`{"user_id":5,"category":"bogus","type":"t","title":"x"}`,        // invalid category
		`{"user_id":5,"category":"request","title":"x"}`,                 // no type
		`{"user_id":5,"category":"request","type":"t"}`,                  // no title
		`not json`,
	} {
		publishAndSettle(t, hub, m, evt.Envelope{
			Channel: evt.ChannelPlugins, Event: "notifications.send", Data: json.RawMessage(raw),
		})
	}
	if len(store.inserted) != 0 {
		t.Fatalf("malformed payloads were inserted: %d", len(store.inserted))
	}
}

func TestSendContract_IgnoresUnrelatedPluginEvents(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	data, _ := json.Marshal(map[string]any{"user_id": 5, "category": "request", "type": "t", "title": "x"})
	publishAndSettle(t, hub, m, evt.Envelope{
		Channel: evt.ChannelPlugins, Event: "plugin.silo.ebooks.request_submitted", Data: data,
	})
	if len(store.inserted) != 0 {
		t.Fatalf("unrelated plugin event created a notification")
	}
}

func TestSendContract_IgnoresNonPluginChannel(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	data, _ := json.Marshal(map[string]any{"user_id": 5, "category": "request", "type": "t", "title": "x"})
	publishAndSettle(t, hub, m, evt.Envelope{
		Channel: evt.ChannelTasks, Event: "notifications.send", Data: data,
	})
	if len(store.inserted) != 0 {
		t.Fatalf("notifications.send honored off the plugins channel")
	}
}

func TestRequestMatcher_ApprovedCreatesNotification(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)

	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	payload := RequestEventData{
		RequestID: "req-1",
		UserID:    7,
		Title:     "Dune",
		MediaType: "movie",
		Status:    "approved",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	env := evt.Envelope{
		Channel: evt.ChannelRequests,
		Event:   "request.approved",
		Data:    data,
		UserID:  7,
	}

	publishAndSettle(t, hub, m, env)

	if len(store.inserted) != 1 {
		t.Fatalf("expected 1 inserted notification, got %d", len(store.inserted))
	}
	n := store.inserted[0]
	if n.Category != CategoryRequest {
		t.Errorf("expected category %q, got %q", CategoryRequest, n.Category)
	}
	if n.Type != "request.approved" {
		t.Errorf("expected type %q, got %q", "request.approved", n.Type)
	}
	if n.UserID != 7 {
		t.Errorf("expected UserID 7, got %d", n.UserID)
	}
	if n.DedupRef != "req-1:approved" {
		t.Errorf("expected DedupRef %q, got %q", "req-1:approved", n.DedupRef)
	}
	if n.Body != "Dune" {
		t.Errorf("expected Body %q, got %q", "Dune", n.Body)
	}
	if n.Title != "Request approved" {
		t.Fatalf("title = %q", n.Title)
	}
}

func TestRequestMatcher_SubmittedDoesNotNotifyRequester(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)

	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	payload := RequestEventData{
		RequestID: "req-2",
		UserID:    7,
		Title:     "Dune",
		MediaType: "movie",
		Status:    "submitted",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	env := evt.Envelope{
		Channel: evt.ChannelRequests,
		Event:   "request.submitted",
		Data:    data,
		UserID:  7,
	}

	publishAndSettle(t, hub, m, env)

	if len(store.inserted) != 0 {
		t.Errorf("expected 0 inserted notifications for request.submitted, got %d", len(store.inserted))
	}
}

func TestMaterializer_MatcherPanicIsolated(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)

	// Prepend a panicking matcher so it runs before the request matcher.
	// This proves a panic in one matcher does not prevent subsequent matchers from running.
	m.matchers = append([]namedMatcher{{
		name: "panicker",
		fn: func(context.Context, evt.Envelope) error {
			panic("boom")
		},
	}}, m.matchers...)

	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	payload := RequestEventData{
		RequestID: "req-3",
		UserID:    7,
		Title:     "Dune",
		MediaType: "movie",
		Status:    "approved",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	env := evt.Envelope{
		Channel: evt.ChannelRequests,
		Event:   "request.approved",
		Data:    data,
		UserID:  7,
	}

	publishAndSettle(t, hub, m, env)

	// The request matcher should still have run despite the panicker.
	if len(store.inserted) != 1 {
		t.Errorf("expected 1 inserted notification despite panic, got %d", len(store.inserted))
	}
}
