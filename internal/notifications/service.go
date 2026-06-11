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

// PushEnqueuer mirrors a created notification to push delivery. Implemented by
// internal/push; nil when push is disabled.
type PushEnqueuer interface {
	EnqueueForNotification(ctx context.Context, n *Notification)
}

type Service struct {
	store Store
	hub   *evt.Hub
	push  PushEnqueuer
}

func NewService(store Store, hub *evt.Hub) *Service {
	return &Service{store: store, hub: hub}
}

func (s *Service) SetPushEnqueuer(e PushEnqueuer) { s.push = e }

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
	if len(a.Title) > MaxAnnouncementTitleLen {
		return fmt.Errorf("notifications: announcement title exceeds %d characters", MaxAnnouncementTitleLen)
	}
	if len(a.Body) > MaxAnnouncementBodyLen {
		return fmt.Errorf("notifications: announcement body exceeds %d characters", MaxAnnouncementBodyLen)
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

	userIDs, err := s.resolveAudience(ctx, a.Audience)
	if err != nil {
		return fmt.Errorf("notifications: resolve audience: %w", err)
	}

	if err := s.store.InsertAnnouncement(ctx, a); err != nil {
		return fmt.Errorf("notifications: insert announcement: %w", err)
	}
	profilesByUser, err := s.store.ProfileIDsForUsers(ctx, userIDs)
	if err != nil {
		return fmt.Errorf("notifications: profiles for audience: %w", err)
	}
	// Fan out one row per profile so each profile reads/dismisses the
	// announcement independently (mirrors content/request notifications). The
	// profile-scoped dedup_ref keeps the unique (user_id, type, dedup_ref) index
	// satisfied across a single account's profiles.
	for _, uid := range userIDs {
		profiles := profilesByUser[uid]
		if len(profiles) == 0 {
			// No profiles on this account (unexpected): fall back to a single
			// account-wide row so the user still receives it.
			s.fanOutAnnouncement(ctx, a, uid, "", fmt.Sprintf("announcement-%d", a.ID))
			continue
		}
		for _, pid := range profiles {
			s.fanOutAnnouncement(ctx, a, uid, pid, fmt.Sprintf("announcement-%d-%s", a.ID, pid))
		}
	}
	return nil
}

// fanOutAnnouncement creates one announcement notification for a single
// (user, profile) pair, logging rather than failing the publish on error.
func (s *Service) fanOutAnnouncement(ctx context.Context, a *Announcement, userID int, profileID, dedupRef string) {
	in := CreateInput{
		UserID:    userID,
		ProfileID: profileID,
		Category:  CategoryAnnouncement,
		Type:      "announcement",
		Title:     a.Title,
		Body:      a.Body,
		DedupRef:  dedupRef,
		ExpiresAt: a.ExpiresAt,
	}
	if err := s.Create(ctx, in); err != nil {
		slog.WarnContext(ctx, "notifications: announcement fan-out failed",
			"user_id", userID, "profile_id", profileID, "error", err)
	}
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
		return fmt.Errorf("notifications: delete announcement: %w", err)
	}
	return s.store.DismissAnnouncementNotifications(ctx, id)
}

// List returns non-dismissed notifications for the given filter.
func (s *Service) List(ctx context.Context, f ListFilter) ([]*Notification, error) {
	return s.store.List(ctx, f)
}

// UnreadCount returns the number of unread, non-dismissed notifications.
func (s *Service) UnreadCount(ctx context.Context, userID int, profileID string, childSafe bool) (int, error) {
	return s.store.UnreadCount(ctx, userID, profileID, childSafe)
}

// MarkRead marks the specified notifications as read for the active profile.
func (s *Service) MarkRead(ctx context.Context, userID int, profileID string, ids []int64) error {
	return s.store.MarkRead(ctx, userID, profileID, ids)
}

// MarkAllRead marks all of the active profile's notifications as read.
func (s *Service) MarkAllRead(ctx context.Context, userID int, profileID string) error {
	return s.store.MarkAllRead(ctx, userID, profileID)
}

// Dismiss marks a single notification as dismissed for the active profile.
// Returns ErrNotFound when the notification does not exist for this profile or
// is already dismissed.
func (s *Service) Dismiss(ctx context.Context, userID int, profileID string, id int64) error {
	return s.store.Dismiss(ctx, userID, profileID, id)
}

// GetPreferences returns the full preference list for userID, merging stored
// values with per-category defaults (content_digest defaults OFF; all others ON).
func (s *Service) GetPreferences(ctx context.Context, userID int) ([]Preference, error) {
	stored, err := s.store.Preferences(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("notifications: load preferences: %w", err)
	}
	out := make([]Preference, 0, len(MutableCategories))
	for _, cat := range MutableCategories {
		enabled, ok := stored[cat]
		if !ok {
			// Apply default: content_digest is opt-in (default false); others default true.
			enabled = cat != CategoryContentDigest
		}
		out = append(out, Preference{Category: cat, Enabled: enabled})
	}
	return out, nil
}

// SetPreferences validates and persists preferences. Returns an error (without
// writing any row) if any category is not in MutableCategories.
func (s *Service) SetPreferences(ctx context.Context, userID int, prefs []Preference) error {
	mutableSet := make(map[Category]struct{}, len(MutableCategories))
	for _, cat := range MutableCategories {
		mutableSet[cat] = struct{}{}
	}
	for _, p := range prefs {
		if _, ok := mutableSet[p.Category]; !ok {
			return fmt.Errorf("notifications: category %q is not mutable", p.Category)
		}
	}
	for _, p := range prefs {
		if err := s.store.SetPreference(ctx, userID, p.Category, p.Enabled); err != nil {
			return fmt.Errorf("notifications: set preference %q: %w", p.Category, err)
		}
	}
	return nil
}

// ListAnnouncements returns all announcements (admin view).
func (s *Service) ListAnnouncements(ctx context.Context) ([]*Announcement, error) {
	return s.store.ListAnnouncements(ctx)
}

// CreateSystem records a security/account notification for userID. Failures
// are logged, never returned — security notices must not break the calling flow.
func (s *Service) CreateSystem(ctx context.Context, userID int, typ, title, body string) {
	if err := s.Create(ctx, CreateInput{
		UserID: userID, Category: CategorySystem, Type: typ, Title: title, Body: body,
	}); err != nil {
		slog.WarnContext(ctx, "notifications: system notification failed", "type", typ, "user_id", userID, "error", err)
	}
}

// RunDailyDigest creates one digest notification per opted-in user summarizing
// catalog additions in their accessible libraries over the lookback window.
func (s *Service) RunDailyDigest(ctx context.Context, since time.Time) error {
	subs, err := s.store.DigestSubscribers(ctx)
	if err != nil {
		return fmt.Errorf("notifications: digest subscribers: %w", err)
	}
	dedupRef := "digest:" + time.Now().UTC().Format("20060102")
	for _, uid := range subs {
		count, err := s.store.AddedItemCountForUser(ctx, uid, since)
		if err != nil {
			slog.WarnContext(ctx, "notifications: digest count failed", "user_id", uid, "error", err)
			continue
		}
		if count == 0 {
			continue
		}
		if err := s.Create(ctx, CreateInput{
			UserID:   uid,
			Category: CategoryContent,
			Type:     TypeContentDigest,
			Title:    "New in your libraries",
			Body:     fmt.Sprintf("%d new items were added in the last day", count),
			Link:     "/recently-added",
			DedupRef: dedupRef,
		}); err != nil {
			slog.WarnContext(ctx, "notifications: digest create failed", "user_id", uid, "error", err)
		}
	}
	return nil
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
	if s.push != nil {
		s.push.EnqueueForNotification(ctx, n)
	}
	return nil
}
