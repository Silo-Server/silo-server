package boxsets

import (
	"context"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

// matchSettle is how long Watch waits after the last metadata change before
// starting a sync, so a scan that matches many movies starts one run.
const matchSettle = 20 * time.Second

// Watch starts a sync shortly after this node changes catalog metadata, so a
// movie matched into a box set library joins its box set without waiting for
// the periodic run. Only changes this node published count: every node sees
// every change, and one sync is enough. It returns when ctx ends.
func Watch(ctx context.Context, hub *evt.Hub, start func()) {
	if hub == nil || start == nil {
		return
	}
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	var settle <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case env, ok := <-events:
			if !ok {
				return
			}
			if env.Channel == evt.ChannelCatalog && env.Event == string(notifications.TypeCatalogItemChanged) && env.SourceID == hub.SourceID() {
				settle = time.After(matchSettle)
			}
		case <-settle:
			settle = nil
			start()
		}
	}
}
