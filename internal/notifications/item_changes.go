package notifications

import (
	"context"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	evt "github.com/Silo-Server/silo-server/internal/events"
)

// The per-item catalog events go out at most itemChangeBatch per
// itemChangeDrainInterval across the cluster. Each events connection buffers
// 64 envelopes of every channel, so a library's first scan, which can queue
// one change per item, fills at most half of that between two drains and
// leaves room for session, job and scan frames.
const (
	itemChangeBatch         = 32
	itemChangeDrainInterval = 100 * time.Millisecond
	// itemChangeIdlePoll is how often a server checks an outbox it found
	// empty, or that another server drained.
	itemChangeIdlePoll = time.Second
	// itemChangeRetention drops changes nobody published for a day, such as
	// those queued while no API server ran.
	itemChangeRetention = 24 * time.Hour
)

// itemChangePayload is the data of a catalog.item.changed event, the same
// shape as the metadata_updated one.
type itemChangePayload struct {
	LibraryID int    `json:"library_id"`
	ContentID string `json:"content_id"`
	Change    string `json:"change"`
}

// ItemChangeOutbox is the slice of *catalog.ItemChangeOutbox the publisher
// drains.
type ItemChangeOutbox interface {
	Claim(ctx context.Context, limit int, interval, retention time.Duration) ([]catalog.ItemChange, bool, error)
}

// RunItemChangePublisher publishes the outbox's added, removed and
// files_changed changes as catalog.item.changed until ctx ends. They reach
// administrators on v2 connections only; the metadata_updated events other
// code publishes keep their audience.
func (h *Hub) RunItemChangePublisher(ctx context.Context, outbox ItemChangeOutbox) {
	if h == nil || h.inner == nil || outbox == nil {
		return
	}
	timer := time.NewTimer(itemChangeIdlePoll)
	defer timer.Stop()
	// Each failure is logged once until the next success, so an outage
	// draining a backlog ten times a second logs one line.
	claimFailing, publishFailing := false, false
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		changes, more, err := outbox.Claim(ctx, itemChangeBatch, itemChangeDrainInterval, itemChangeRetention)
		if err != nil && ctx.Err() == nil && !claimFailing {
			slog.WarnContext(ctx, "catalog item change outbox: claim failed", "component", "notifications", "error", err)
		}
		claimFailing = err != nil
		// The claimed rows are already gone: a change that fails to publish
		// reaches only this server's own connections, if any.
		var failed int
		var publishErr error
		for _, change := range changes {
			payload := itemChangePayload{LibraryID: change.LibraryID, ContentID: change.ContentID, Change: change.Change}
			if err := h.inner.PublishJSON(ctx, evt.ChannelCatalog, string(TypeCatalogItemChanged), payload,
				evt.PublishOptions{AdminOnly: true, V2Only: true}); err != nil {
				failed++
				publishErr = err
			}
		}
		if failed > 0 && ctx.Err() == nil && !publishFailing {
			slog.WarnContext(ctx, "catalog item change: publish to other servers failed; mirrors recover by re-listing",
				"component", "notifications", "failed", failed, "claimed", len(changes), "error", publishErr)
		}
		if len(changes) > 0 {
			publishFailing = failed > 0
		}
		if more {
			timer.Reset(itemChangeDrainInterval)
		} else {
			timer.Reset(itemChangeIdlePoll)
		}
	}
}
