package notifications

import (
	"context"
	"log/slog"
	"strconv"
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

// CatalogItemChange is the data of a catalog.item.changed event. LibraryID
// is the library's v2 string ID, absent when the change names no library.
type CatalogItemChange struct {
	LibraryID string `json:"library_id,omitempty"`
	ContentID string `json:"content_id"`
	Change    string `json:"change"`
}

// NewCatalogItemChange builds a catalog.item.changed payload; a libraryID of
// 0 or less names no library.
func NewCatalogItemChange(libraryID int, contentID, change string) CatalogItemChange {
	return CatalogItemChange{LibraryID: eventLibraryID(libraryID), ContentID: contentID, Change: change}
}

// eventLibraryID is a catalog event's library_id: the string form v2
// identifiers take, or "" (left out) when the event names no library.
func eventLibraryID(id int) string {
	if id <= 0 {
		return ""
	}
	return strconv.Itoa(id)
}

// withLibraryID adds library_id to a catalog event's fields when it names a
// library.
func withLibraryID(fields map[string]any, id int) map[string]any {
	if libraryID := eventLibraryID(id); libraryID != "" {
		fields["library_id"] = libraryID
	}
	return fields
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
			payload := NewCatalogItemChange(change.LibraryID, change.ContentID, change.Change)
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
