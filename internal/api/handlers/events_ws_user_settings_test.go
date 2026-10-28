package handlers

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/cache"
	evt "github.com/Silo-Server/silo-server/internal/events"
)

// loopbackEventBus delivers each published event to every subscribed handler,
// standing in for Redis between the nodes of a cluster.
type loopbackEventBus struct {
	mu       sync.Mutex
	handlers []cache.EventHandler
}

func (b *loopbackEventBus) Publish(_ context.Context, _ string, event cache.Event) error {
	b.mu.Lock()
	handlers := slices.Clone(b.handlers)
	b.mu.Unlock()
	for _, handler := range handlers {
		handler(event)
	}
	return nil
}

func (b *loopbackEventBus) Subscribe(_ context.Context, _ string, handler cache.EventHandler) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers = append(b.handlers, handler)
	return nil
}

func (b *loopbackEventBus) Close() error { return nil }

// An administrator-default change is addressed to every viewer and is made on
// another node, so the every-viewer address has to survive the event bus. A
// server setting that holds no administrator default publishes nothing.
func TestAdminDefaultChangeReachesEveryViewer(t *testing.T) {
	bus := &loopbackEventBus{}
	hub := evt.NewHub("socket-node", bus)
	if err := hub.Start(t.Context()); err != nil {
		t.Fatalf("starting hub: %v", err)
	}
	adminNode := evt.NewHub("admin-node", bus)
	h, _ := socketTestHandler()
	h.Events = NewEventsHandler(hub, nil, nil, nil, nil, nil, nil)
	_, readFrame := dialEventsSocketV2(t, h, "user", "?channels=user_settings")
	readFrame("hello")
	readFrame("subscribed")
	readFrame("snapshot")

	// Published first: if it produced an event, the read below would see it.
	publishAdminDefaultEvent(t.Context(), adminNode, "defaults.card_overlays")
	publishAdminDefaultEvent(t.Context(), adminNode, "defaults.ui.title_art")

	event := readFrame("event")
	var data userSettingsEventPayload
	if err := json.Unmarshal(event["data"], &data); err != nil {
		t.Fatalf("event data: %v (%s)", err, event["data"])
	}
	if data.Key != "ui.title_art" || data.Scope != "default" || data.ProfileID != "" {
		t.Fatalf("event data = %+v, want ui.title_art at default for every profile", data)
	}
}
