package notifications

import (
	"context"
	"testing"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

type fakeStore struct {
	Store // embed for unimplemented methods (nil-panic = test failure, intended)
	inserted []*Notification
	prefs    map[int]map[Category]bool
	admins   []int
	libUsers map[int][]int
}

func (f *fakeStore) Insert(_ context.Context, n *Notification) (bool, error) {
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
