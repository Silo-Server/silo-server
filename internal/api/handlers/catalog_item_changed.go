package handlers

import (
	"context"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// catalogItemChangedPublishTimeout bounds the publish once it no longer
// follows the request's cancellation.
const catalogItemChangedPublishTimeout = 5 * time.Second

// publishCatalogItemChanged runs after an admin changes items' identity,
// fields or artwork outside a scan. It evicts the cached home rails that list
// them on this node at once, so the client's next read is fresh, and tells the
// other API nodes to do the same. A match passes both the old and the new
// content ID, because the cached rails still hold the old one.
//
// The change is already committed, so the publish must not depend on the
// client staying connected.
func publishCatalogItemChanged(ctx context.Context, bus cache.EventBus, contentIDs ...string) {
	sections.EvictResolvedListItems(contentIDs...)
	if bus == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), catalogItemChangedPublishTimeout)
	defer cancel()
	published := make(map[string]struct{}, len(contentIDs))
	for _, id := range contentIDs {
		if _, done := published[id]; done || id == "" {
			continue
		}
		published[id] = struct{}{}
		if err := bus.Publish(ctx, cache.ChannelCatalog, cache.Event{Type: cache.EventCatalogItemChanged, Payload: id}); err != nil {
			slog.WarnContext(ctx, "admin: failed to publish catalog item change", "component", "api", "content_id", id, "error", err)
		}
	}
}
