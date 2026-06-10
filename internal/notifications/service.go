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

// PublishAnnouncement stores the announcement and fans it out to notifications
// rows at publish time, resolving the audience to user ids now.
func (s *Service) PublishAnnouncement(ctx context.Context, a *Announcement) error {
	if a.Title == "" {
		return fmt.Errorf("notifications: announcement title required")
	}
	set := 0
	if a.Audience.All {
		set++
	}
	if len(a.Audience.UserIDs) > 0 {
		set++
	}
	if len(a.Audience.LibraryIDs) > 0 {
		set++
	}
	if set != 1 {
		return fmt.Errorf("notifications: audience must set exactly one of all/user_ids/library_ids")
	}

	if err := s.store.InsertAnnouncement(ctx, a); err != nil {
		return fmt.Errorf("notifications: insert announcement: %w", err)
	}

	userIDs, err := s.resolveAudience(ctx, a.Audience)
	if err != nil {
		return err
	}
	for _, uid := range userIDs {
		in := CreateInput{
			UserID:    uid,
			Category:  CategoryAnnouncement,
			Type:      "announcement",
			Title:     a.Title,
			Body:      a.Body,
			DedupRef:  fmt.Sprintf("announcement-%d", a.ID),
			ExpiresAt: a.ExpiresAt,
		}
		if err := s.Create(ctx, in); err != nil {
			slog.WarnContext(ctx, "notifications: announcement fan-out failed", "user_id", uid, "error", err)
		}
	}
	return nil
}

func (s *Service) resolveAudience(ctx context.Context, a Audience) ([]int, error) {
	switch {
	case a.All:
		return s.store.AllEnabledUserIDs(ctx)
	case len(a.UserIDs) > 0:
		return a.UserIDs, nil
	default:
		seen := map[int]struct{}{}
		var out []int
		for _, lib := range a.LibraryIDs {
			ids, err := s.store.UserIDsWithLibraryAccess(ctx, lib)
			if err != nil {
				return nil, err
			}
			for _, id := range ids {
				if _, ok := seen[id]; !ok {
					seen[id] = struct{}{}
					out = append(out, id)
				}
			}
		}
		return out, nil
	}
}

// DeleteAnnouncement removes the announcement and dismisses its unread rows.
func (s *Service) DeleteAnnouncement(ctx context.Context, id int64) error {
	if err := s.store.DeleteAnnouncement(ctx, id); err != nil {
		return err
	}
	return s.store.DismissUnreadByTypeRef(ctx, "announcement", fmt.Sprintf("announcement-%d", id))
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
		slog.WarnContext(ctx, "notifications: publish failed", "error", err, "notification_id", n.ID)
	}
	return nil
}
