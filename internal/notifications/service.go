package notifications

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

const TypeContentDigest = "content.digest"

type CreateInput struct {
	UserID      int
	ProfileID   string // optional
	Category    Category
	Type        string
	Title       string
	Body        string
	Link        string
	ItemID      string
	SourceEvent string
	DedupRef    string
	ExpiresAt   *time.Time
}

type Service struct {
	store Store
	hub   *evt.Hub
}

func NewService(store Store, hub *evt.Hub) *Service {
	return &Service{store: store, hub: hub}
}

func validCategory(c Category) bool {
	switch c {
	case CategoryRequest, CategoryContent, CategoryAnnouncement, CategorySystem, CategoryAdmin:
		return true
	}
	return false
}

// Create validates, applies preferences, inserts (idempotent on DedupRef) and
// publishes notification.created for live clients. A dedup conflict is not an
// error and publishes nothing.
func (s *Service) Create(ctx context.Context, in CreateInput) error {
	if in.UserID <= 0 {
		return fmt.Errorf("notifications: user_id required")
	}
	if !validCategory(in.Category) {
		return fmt.Errorf("notifications: invalid category %q", in.Category)
	}
	if in.Type == "" || in.Title == "" {
		return fmt.Errorf("notifications: type and title required")
	}

	if in.Category != CategoryAnnouncement {
		prefs, err := s.store.Preferences(ctx, in.UserID)
		if err != nil {
			return fmt.Errorf("notifications: load preferences: %w", err)
		}
		prefCategory := in.Category
		if in.Type == TypeContentDigest {
			prefCategory = CategoryContentDigest
		}
		if prefCategory == CategoryContentDigest {
			// content_digest is opt-in: absent row means disabled.
			if enabled, ok := prefs[CategoryContentDigest]; !ok || !enabled {
				return nil
			}
		} else if enabled, ok := prefs[prefCategory]; ok && !enabled {
			return nil
		}
	}

	n := &Notification{
		UserID:      in.UserID,
		Category:    in.Category,
		Type:        in.Type,
		Title:       in.Title,
		Body:        in.Body,
		SourceEvent: in.SourceEvent,
		DedupRef:    in.DedupRef,
		ExpiresAt:   in.ExpiresAt,
	}
	if in.ProfileID != "" {
		n.ProfileID = &in.ProfileID
	}
	if in.Link != "" {
		n.Link = &in.Link
	}
	if in.ItemID != "" {
		n.ItemID = &in.ItemID
	}

	created, err := s.store.Insert(ctx, n)
	if err != nil {
		return fmt.Errorf("notifications: insert: %w", err)
	}
	if !created {
		return nil
	}

	if err := s.hub.PublishJSON(ctx, evt.ChannelNotifications, "notification.created", n, evt.PublishOptions{
		UserID:    n.UserID,
		ProfileID: in.ProfileID,
		AdminOnly: n.Category == CategoryAdmin,
	}); err != nil {
		slog.Warn("notifications: publish failed", "error", err, "notification_id", n.ID)
	}
	return nil
}
