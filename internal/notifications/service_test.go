package notifications

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

type fakeStore struct {
	Store // embed for unimplemented methods (nil-panic = test failure, intended)
	inserted    []*Notification
	nextID      int64
	prefs       map[int]map[Category]bool
	admins      []int
	libUsers    map[int][]int
	allUsers    []int
	insertErr   error // when non-nil, Insert returns this error

	// recorded args for dismiss calls
	dismissTyp      string
	dismissDedupRef string

	// digest support
	digestSubs  []int
	addedCounts map[int]int           // userID → count
	addedErrors map[int]error         // userID → error (optional per-user error)
}

func (f *fakeStore) Insert(_ context.Context, n *Notification) (bool, error) {
	if f.insertErr != nil {
		return false, f.insertErr
	}
	for _, existing := range f.inserted {
		if existing.UserID == n.UserID && existing.Type == n.Type &&
			n.DedupRef != "" && existing.DedupRef == n.DedupRef {
			return false, nil
		}
	}
	n.ID = int64(len(f.inserted) + 1)
	f.inserted = append(f.inserted, n)
	return true, nil
}

func (f *fakeStore) Preferences(_ context.Context, userID int) (map[Category]bool, error) {
	if p, ok := f.prefs[userID]; ok {
		return p, nil
	}
	return map[Category]bool{}, nil
}

func (f *fakeStore) AdminUserIDs(context.Context) ([]int, error) { return f.admins, nil }

func (f *fakeStore) UserIDsWithLibraryAccess(_ context.Context, lib int) ([]int, error) {
	return f.libUsers[lib], nil
}

func (f *fakeStore) AllEnabledUserIDs(context.Context) ([]int, error) { return f.allUsers, nil }

func (f *fakeStore) InsertAnnouncement(_ context.Context, a *Announcement) error {
	f.nextID++
	a.ID = f.nextID
	return nil
}

func (f *fakeStore) DismissUnreadByTypeAndRef(_ context.Context, typ, dedupRef string) error {
	f.dismissTyp = typ
	f.dismissDedupRef = dedupRef
	return nil
}

func (f *fakeStore) DeleteAnnouncement(context.Context, int64) error { return nil }

func (f *fakeStore) DigestSubscribers(context.Context) ([]int, error) {
	return f.digestSubs, nil
}

func (f *fakeStore) AddedItemCountForUser(_ context.Context, userID int, _ time.Time) (int, error) {
	if f.addedErrors != nil {
		if err, ok := f.addedErrors[userID]; ok {
			return 0, err
		}
	}
	if f.addedCounts != nil {
		return f.addedCounts[userID], nil
	}
	return 0, nil
}

func newTestService(store Store) (*Service, *evt.Hub) {
	hub := evt.NewHub("test-node", nil)
	return NewService(store, hub), hub
}

func TestCreate_InsertsAndPublishes(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)

	ch, unsub := hub.Subscribe()
	defer unsub()

	ctx := context.Background()
	err := svc.Create(ctx, CreateInput{
		UserID:   7,
		Category: CategoryRequest,
		Type:     "request.approved",
		Title:    "Approved",
		DedupRef: "req-1:approved",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	if len(store.inserted) != 1 {
		t.Fatalf("expected 1 inserted notification, got %d", len(store.inserted))
	}

	env := <-ch
	if env.Channel != evt.ChannelNotifications {
		t.Errorf("expected channel %q, got %q", evt.ChannelNotifications, env.Channel)
	}
	if env.Event != "notification.created" {
		t.Errorf("expected event %q, got %q", "notification.created", env.Event)
	}
	if env.UserID != 7 {
		t.Errorf("expected UserID 7, got %d", env.UserID)
	}
}

func TestCreate_MutedCategorySkipped(t *testing.T) {
	store := &fakeStore{
		prefs: map[int]map[Category]bool{
			7: {CategoryRequest: false},
		},
	}
	svc, _ := newTestService(store)

	ctx := context.Background()
	err := svc.Create(ctx, CreateInput{
		UserID:   7,
		Category: CategoryRequest,
		Type:     "request.approved",
		Title:    "Approved",
		DedupRef: "req-1:approved",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	if len(store.inserted) != 0 {
		t.Errorf("expected 0 inserted notifications, got %d", len(store.inserted))
	}
}

func TestCreate_AnnouncementNotMutable(t *testing.T) {
	store := &fakeStore{
		prefs: map[int]map[Category]bool{
			7: {CategoryAnnouncement: false},
		},
	}
	svc, _ := newTestService(store)

	ctx := context.Background()
	err := svc.Create(ctx, CreateInput{
		UserID:   7,
		Category: CategoryAnnouncement,
		Type:     "announcement.new",
		Title:    "New Announcement",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	if len(store.inserted) != 1 {
		t.Errorf("expected 1 inserted notification (announcement not mutable), got %d", len(store.inserted))
	}
}

func TestCreate_DedupNoDoubleInsertNoSecondPublish(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)

	ch, unsub := hub.Subscribe()
	defer unsub()

	ctx := context.Background()
	input := CreateInput{
		UserID:   7,
		Category: CategoryRequest,
		Type:     "request.approved",
		Title:    "Approved",
		DedupRef: "req-1:approved",
	}

	if err := svc.Create(ctx, input); err != nil {
		t.Fatalf("first Create returned error: %v", err)
	}
	if err := svc.Create(ctx, input); err != nil {
		t.Fatalf("second Create returned error: %v", err)
	}

	if len(store.inserted) != 1 {
		t.Errorf("expected 1 inserted notification (dedup), got %d", len(store.inserted))
	}

	// Drain the one envelope that should exist.
	<-ch

	// Assert no second envelope.
	select {
	case env, ok := <-ch:
		if ok {
			t.Errorf("expected no second envelope, got event %q on channel %q", env.Event, env.Channel)
		}
	default:
		// good: channel is empty
	}
}

func TestCreate_ValidationErrors(t *testing.T) {
	store := &fakeStore{}
	svc, _ := newTestService(store)
	ctx := context.Background()

	cases := []struct {
		name  string
		input CreateInput
	}{
		{
			name:  "no user",
			input: CreateInput{Category: CategoryRequest, Type: "request.approved", Title: "Approved"},
		},
		{
			name:  "no category",
			input: CreateInput{UserID: 7, Type: "request.approved", Title: "Approved"},
		},
		{
			name:  "bogus category",
			input: CreateInput{UserID: 7, Category: "bogus", Type: "request.approved", Title: "Approved"},
		},
		{
			name:  "no type",
			input: CreateInput{UserID: 7, Category: CategoryRequest, Title: "Approved"},
		},
		{
			name:  "no title",
			input: CreateInput{UserID: 7, Category: CategoryRequest, Type: "request.approved"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.Create(ctx, tc.input)
			if err == nil {
				t.Errorf("expected error for %q, got nil", tc.name)
			}
		})
	}
}

func TestCreate_DigestAbsentPrefSuppressed(t *testing.T) {
	store := &fakeStore{}
	svc, _ := newTestService(store)
	err := svc.Create(context.Background(), CreateInput{
		UserID: 7, Category: CategoryContent, Type: TypeContentDigest, Title: "Digest",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(store.inserted) != 0 {
		t.Fatalf("digest without opt-in was inserted")
	}
}

func TestCreate_DigestOptInInserts(t *testing.T) {
	store := &fakeStore{prefs: map[int]map[Category]bool{
		7: {CategoryContentDigest: true},
	}}
	svc, _ := newTestService(store)
	err := svc.Create(context.Background(), CreateInput{
		UserID: 7, Category: CategoryContent, Type: TypeContentDigest, Title: "Digest",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(store.inserted) != 1 {
		t.Fatalf("opted-in digest was not inserted: %d", len(store.inserted))
	}
}

func TestPublishAnnouncement_FanOutAll(t *testing.T) {
	store := &fakeStore{allUsers: []int{1, 2, 3}}
	svc, _ := newTestService(store)

	err := svc.PublishAnnouncement(context.Background(), &Announcement{
		Title:    "Maintenance",
		Audience: Audience{All: true},
	})
	if err != nil {
		t.Fatalf("PublishAnnouncement returned error: %v", err)
	}
	if len(store.inserted) != 3 {
		t.Fatalf("expected 3 inserted rows, got %d", len(store.inserted))
	}
	for _, n := range store.inserted {
		if n.Category != CategoryAnnouncement {
			t.Errorf("expected category %q, got %q", CategoryAnnouncement, n.Category)
		}
		if n.DedupRef != "announcement-1" {
			t.Errorf("expected dedup_ref %q, got %q", "announcement-1", n.DedupRef)
		}
	}
}

func TestPublishAnnouncement_FanOutUserIDs(t *testing.T) {
	store := &fakeStore{}
	svc, _ := newTestService(store)

	err := svc.PublishAnnouncement(context.Background(), &Announcement{
		Title:    "Hello",
		Audience: Audience{UserIDs: []int{5, 9}},
	})
	if err != nil {
		t.Fatalf("PublishAnnouncement returned error: %v", err)
	}
	if len(store.inserted) != 2 {
		t.Fatalf("expected 2 inserted rows, got %d", len(store.inserted))
	}
}

func TestPublishAnnouncement_FanOutLibraryIDs(t *testing.T) {
	store := &fakeStore{
		libUsers: map[int][]int{
			3: {4, 5},
			7: {5, 6},
		},
	}
	svc, _ := newTestService(store)

	err := svc.PublishAnnouncement(context.Background(), &Announcement{
		Title:    "Library News",
		Audience: Audience{LibraryIDs: []int{3, 7}},
	})
	if err != nil {
		t.Fatalf("PublishAnnouncement returned error: %v", err)
	}
	// users 4, 5, 6 — 5 is deduplicated
	if len(store.inserted) != 3 {
		t.Fatalf("expected 3 inserted rows (dedup user 5), got %d", len(store.inserted))
	}
}

func TestPublishAnnouncement_AudienceValidation(t *testing.T) {
	store := &fakeStore{}
	svc, _ := newTestService(store)
	ctx := context.Background()

	cases := []struct {
		name string
		a    *Announcement
	}{
		{
			name: "no audience fields set",
			a:    &Announcement{Title: "Test"},
		},
		{
			name: "two fields set (All+UserIDs)",
			a:    &Announcement{Title: "Test", Audience: Audience{All: true, UserIDs: []int{1}}},
		},
		{
			name: "empty title",
			a:    &Announcement{Audience: Audience{All: true}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.PublishAnnouncement(ctx, tc.a)
			if err == nil {
				t.Errorf("expected error for %q, got nil", tc.name)
			}
		})
	}
}

func TestPublishAnnouncement_ExpiryPropagates(t *testing.T) {
	store := &fakeStore{allUsers: []int{1, 2}}
	svc, _ := newTestService(store)

	exp := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	err := svc.PublishAnnouncement(context.Background(), &Announcement{
		Title:     "Expiring",
		Audience:  Audience{All: true},
		ExpiresAt: &exp,
	})
	if err != nil {
		t.Fatalf("PublishAnnouncement returned error: %v", err)
	}
	if len(store.inserted) != 2 {
		t.Fatalf("expected 2 inserted rows, got %d", len(store.inserted))
	}
	for _, n := range store.inserted {
		if n.ExpiresAt == nil || !n.ExpiresAt.Equal(exp) {
			t.Errorf("expected ExpiresAt %v, got %v", exp, n.ExpiresAt)
		}
	}
}

func TestDeleteAnnouncement_DismissesUnread(t *testing.T) {
	store := &fakeStore{}
	svc, _ := newTestService(store)

	err := svc.DeleteAnnouncement(context.Background(), 42)
	if err != nil {
		t.Fatalf("DeleteAnnouncement returned error: %v", err)
	}
	if store.dismissTyp != "announcement" {
		t.Errorf("expected dismissTyp %q, got %q", "announcement", store.dismissTyp)
	}
	if store.dismissDedupRef != "announcement-42" {
		t.Errorf("expected dismissDedupRef %q, got %q", "announcement-42", store.dismissDedupRef)
	}
}

// ---------------------------------------------------------------------------
// CreateSystem tests
// ---------------------------------------------------------------------------

func TestCreateSystem_InsertsSystemNotification(t *testing.T) {
	store := &fakeStore{}
	svc, _ := newTestService(store)

	svc.CreateSystem(context.Background(), 42, "system.password_changed", "Password changed",
		"Your account password was changed. If this wasn't you, contact your administrator.")

	if len(store.inserted) != 1 {
		t.Fatalf("expected 1 inserted notification, got %d", len(store.inserted))
	}
	n := store.inserted[0]
	if n.Category != CategorySystem {
		t.Errorf("expected category %q, got %q", CategorySystem, n.Category)
	}
	if n.Type != "system.password_changed" {
		t.Errorf("expected type %q, got %q", "system.password_changed", n.Type)
	}
	if n.UserID != 42 {
		t.Errorf("expected user_id 42, got %d", n.UserID)
	}
}

func TestCreateSystem_StoreFailureDoesNotPanic(t *testing.T) {
	store := &fakeStore{insertErr: errors.New("db down")}
	svc, _ := newTestService(store)

	// Must not panic; failure is logged and swallowed.
	svc.CreateSystem(context.Background(), 7, "system.password_changed", "Password changed", "body")

	if len(store.inserted) != 0 {
		t.Errorf("expected 0 insertions on store error, got %d", len(store.inserted))
	}
}

// ---------------------------------------------------------------------------
// RunDailyDigest tests
// ---------------------------------------------------------------------------

func TestRunDailyDigest_OptedInUserGetsOne(t *testing.T) {
	store := &fakeStore{
		digestSubs:  []int{7},
		addedCounts: map[int]int{7: 3},
		prefs: map[int]map[Category]bool{
			7: {CategoryContentDigest: true},
		},
	}
	svc, _ := newTestService(store)

	err := svc.RunDailyDigest(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("RunDailyDigest returned error: %v", err)
	}
	if len(store.inserted) != 1 {
		t.Fatalf("expected 1 inserted notification, got %d", len(store.inserted))
	}
	n := store.inserted[0]
	if n.UserID != 7 {
		t.Errorf("expected user_id 7, got %d", n.UserID)
	}
	if n.Type != TypeContentDigest {
		t.Errorf("expected type %q, got %q", TypeContentDigest, n.Type)
	}
	if n.Title != "New in your libraries" {
		t.Errorf("expected title %q, got %q", "New in your libraries", n.Title)
	}
	if !strings.Contains(n.Body, "3") {
		t.Errorf("expected body to contain count %q, got %q", "3", n.Body)
	}
}

func TestRunDailyDigest_ZeroCountSkipped(t *testing.T) {
	store := &fakeStore{
		digestSubs:  []int{7},
		addedCounts: map[int]int{7: 0},
		prefs: map[int]map[Category]bool{
			7: {CategoryContentDigest: true},
		},
	}
	svc, _ := newTestService(store)

	err := svc.RunDailyDigest(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("RunDailyDigest returned error: %v", err)
	}
	if len(store.inserted) != 0 {
		t.Errorf("expected 0 notifications for zero count, got %d", len(store.inserted))
	}
}

func TestRunDailyDigest_SameDayRerunDedups(t *testing.T) {
	store := &fakeStore{
		digestSubs:  []int{7},
		addedCounts: map[int]int{7: 5},
		prefs: map[int]map[Category]bool{
			7: {CategoryContentDigest: true},
		},
	}
	svc, _ := newTestService(store)

	since := time.Now().Add(-24 * time.Hour)
	if err := svc.RunDailyDigest(context.Background(), since); err != nil {
		t.Fatalf("first RunDailyDigest: %v", err)
	}
	if err := svc.RunDailyDigest(context.Background(), since); err != nil {
		t.Fatalf("second RunDailyDigest: %v", err)
	}

	if len(store.inserted) != 1 {
		t.Errorf("expected 1 notification after two same-day runs (dedup), got %d", len(store.inserted))
	}
}

func TestRunDailyDigest_CountErrorContinues(t *testing.T) {
	store := &fakeStore{
		digestSubs:  []int{7, 8},
		addedCounts: map[int]int{8: 2},
		addedErrors: map[int]error{7: errors.New("db timeout")},
		prefs: map[int]map[Category]bool{
			7: {CategoryContentDigest: true},
			8: {CategoryContentDigest: true},
		},
	}
	svc, _ := newTestService(store)

	err := svc.RunDailyDigest(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("RunDailyDigest returned error on count failure: %v", err)
	}
	if len(store.inserted) != 1 {
		t.Fatalf("expected 1 notification (user 7 skipped, user 8 notified), got %d", len(store.inserted))
	}
	if store.inserted[0].UserID != 8 {
		t.Errorf("expected notification for user 8, got user %d", store.inserted[0].UserID)
	}
}
