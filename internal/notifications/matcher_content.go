package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

// changeIsAddition returns true for change values that represent a newly-added
// catalog item. All callers in this codebase use "metadata_updated" for both
// new and re-scanned items; there is no separate "item_added" value.
func changeIsAddition(change string) bool {
	return change == "metadata_updated" || change == "item_added"
}

const inProgressWindow = 21 * 24 * time.Hour

// newItemWindow is the maximum age of a catalog row that is still considered a
// new arrival.  metadata_updated events fired by periodic library refreshes on
// older items are suppressed.
const newItemWindow = 48 * time.Hour

// ProfileRef identifies a specific user+profile pair to notify.
type ProfileRef struct {
	UserID    int
	ProfileID string
}

// ContentResolver answers "who cares about this item" from catalog state.
type ContentResolver interface {
	// ItemContext resolves the added item's display title, its series/parent
	// content id (empty when standalone), library id, and catalog row creation
	// time. createdAt is the catalog row's creation time, used to distinguish
	// new arrivals from metadata refreshes of old items.
	ItemContext(ctx context.Context, contentID string) (title string, seriesID string, libraryID int, createdAt time.Time, err error)
	// InterestedProfiles returns (user_id, profile_id) pairs whose watchlist
	// or favorites contain contentID or seriesID, plus profiles with playback
	// of the series within the in-progress window, restricted to users with
	// access to libraryID.
	InterestedProfiles(ctx context.Context, contentID, seriesID string, libraryID int, inProgressSince time.Time) ([]ProfileRef, error)
}

// catalogItemPayload is the JSON shape of a catalog.item.changed event.
type catalogItemPayload struct {
	LibraryID int    `json:"library_id"`
	ContentID string `json:"content_id"`
	Change    string `json:"change"`
}

// matchContent handles catalog-channel catalog.item.changed events and creates
// content.added notifications for every interested profile.
func (m *Materializer) matchContent(ctx context.Context, env evt.Envelope) error {
	if env.Channel != evt.ChannelCatalog || env.Event != "catalog.item.changed" {
		return nil
	}

	var payload catalogItemPayload
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		return fmt.Errorf("content matcher: decode catalog.item.changed: %w", err)
	}

	if !changeIsAddition(payload.Change) {
		return nil
	}

	contentID := payload.ContentID
	if contentID == "" {
		return nil
	}

	resolver, _ := m.content.(ContentResolver)
	if resolver == nil {
		return nil
	}

	title, seriesID, libraryID, createdAt, err := resolver.ItemContext(ctx, contentID)
	if err != nil {
		return fmt.Errorf("content matcher: item context for %s: %w", contentID, err)
	}
	// Suppress notifications for items whose catalog row is older than
	// newItemWindow: metadata_updated also fires during periodic library
	// refreshes of existing items, which would otherwise re-notify users.
	if time.Since(createdAt) > newItemWindow {
		return nil // metadata refresh of an old item, not a new arrival
	}
	// Fall back to the event's library_id when the resolver returns 0.
	if libraryID == 0 {
		libraryID = payload.LibraryID
	}

	inProgressSince := time.Now().UTC().Add(-inProgressWindow)
	refs, err := resolver.InterestedProfiles(ctx, contentID, seriesID, libraryID, inProgressSince)
	if err != nil {
		return fmt.Errorf("content matcher: interested profiles for %s: %w", contentID, err)
	}

	hourBucket := time.Now().UTC().Format("2006010215")
	// groupID collapses all episodes of a series into a single burst group.
	groupID := contentID
	if seriesID != "" {
		groupID = seriesID
	}

	for _, ref := range refs {
		dedupRef := fmt.Sprintf("%s:%s:%s", groupID, ref.ProfileID, hourBucket)
		if err := m.svc.Create(ctx, CreateInput{
			UserID:      ref.UserID,
			ProfileID:   ref.ProfileID,
			Category:    CategoryContent,
			Type:        "content.added",
			Title:       "New content for you",
			Body:        title,
			Link:        "/items/" + contentID,
			ItemID:      contentID,
			SourceEvent: env.Event,
			DedupRef:    dedupRef,
		}); err != nil {
			return fmt.Errorf("content matcher: create notification for user %d profile %s: %w", ref.UserID, ref.ProfileID, err)
		}
	}
	return nil
}
