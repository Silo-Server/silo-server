package notifications

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
)

func TestPublishCatalogItemChangedAlsoPublishesLegacyMetadataUpdated(t *testing.T) {
	hub := NewHub("test", &cache.NoopEventBus{})
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	err := hub.PublishCatalogItemChanged(context.Background(), MetadataUpdateEvent{
		LibraryID: 12,
		ContentID: "item-1",
		Change:    "metadata_updated",
	})
	if err != nil {
		t.Fatalf("PublishCatalogItemChanged() error = %v", err)
	}

	got := map[Type]Envelope{}
	for len(got) < 2 {
		select {
		case event := <-events:
			got[event.Type] = event
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for events, got %#v", got)
		}
	}

	for _, eventType := range []Type{TypeCatalogItemChanged, TypeMetadataUpdated} {
		event, ok := got[eventType]
		if !ok {
			t.Fatalf("missing event %q in %#v", eventType, got)
		}
		if event.LibraryID != 12 || event.ContentID != "item-1" {
			t.Fatalf("event %q payload = library %d content %q", eventType, event.LibraryID, event.ContentID)
		}
	}
}
