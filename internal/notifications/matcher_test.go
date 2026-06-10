package notifications

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

func publishAndSettle(t *testing.T, hub *evt.Hub, m *Materializer, env evt.Envelope) {
	t.Helper()
	before := m.Processed()
	if err := hub.Publish(context.Background(), env); err != nil {
		t.Fatalf("publish: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for m.Processed() <= before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if m.Processed() <= before {
		t.Fatalf("materializer did not process the envelope in time")
	}
}

func TestSendContract_CreatesNotification(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	payload := map[string]any{
		"user_id": 5, "category": "request", "type": "request.fulfilled",
		"title": "Your audiobook is ready", "body": "Dune by Frank Herbert",
		"dedup_ref": "ab-req-9:fulfilled",
	}
	data, _ := json.Marshal(payload)
	publishAndSettle(t, hub, m, evt.Envelope{
		Channel: evt.ChannelPlugins, Event: "plugin.silo.audiobook-requests.notifications.send", Data: data,
	})

	if len(store.inserted) != 1 {
		t.Fatalf("inserted = %d, want 1", len(store.inserted))
	}
	n := store.inserted[0]
	if n.UserID != 5 || n.Category != CategoryRequest || n.Type != "request.fulfilled" ||
		n.Title != "Your audiobook is ready" || n.DedupRef != "ab-req-9:fulfilled" ||
		n.SourceEvent != "plugin.silo.audiobook-requests.notifications.send" {
		t.Fatalf("bad notification: %+v", n)
	}
}

func TestSendContract_MalformedDropped(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	for _, raw := range []string{
		`{"category":"request","type":"t","title":"x"}`,                  // no user_id
		`{"user_id":5,"category":"announcement","type":"t","title":"x"}`, // plugins may not create announcements
		`{"user_id":5,"category":"bogus","type":"t","title":"x"}`,        // invalid category
		`{"user_id":5,"category":"request","title":"x"}`,                 // no type
		`{"user_id":5,"category":"request","type":"t"}`,                  // no title
		`not json`,
	} {
		publishAndSettle(t, hub, m, evt.Envelope{
			Channel: evt.ChannelPlugins, Event: "notifications.send", Data: json.RawMessage(raw),
		})
	}
	if len(store.inserted) != 0 {
		t.Fatalf("malformed payloads were inserted: %d", len(store.inserted))
	}
}

func TestSendContract_IgnoresUnrelatedPluginEvents(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	data, _ := json.Marshal(map[string]any{"user_id": 5, "category": "request", "type": "t", "title": "x"})
	publishAndSettle(t, hub, m, evt.Envelope{
		Channel: evt.ChannelPlugins, Event: "plugin.silo.ebooks.request_submitted", Data: data,
	})
	if len(store.inserted) != 0 {
		t.Fatalf("unrelated plugin event created a notification")
	}
}

func TestSendContract_IgnoresNonPluginChannel(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	data, _ := json.Marshal(map[string]any{"user_id": 5, "category": "request", "type": "t", "title": "x"})
	publishAndSettle(t, hub, m, evt.Envelope{
		Channel: evt.ChannelTasks, Event: "notifications.send", Data: data,
	})
	if len(store.inserted) != 0 {
		t.Fatalf("notifications.send honored off the plugins channel")
	}
}

func TestRequestMatcher_ApprovedCreatesNotification(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)

	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	payload := RequestEventData{
		RequestID: "req-1",
		UserID:    7,
		Title:     "Dune",
		MediaType: "movie",
		Status:    "approved",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	env := evt.Envelope{
		Channel: evt.ChannelRequests,
		Event:   "request.approved",
		Data:    data,
		UserID:  7,
	}

	publishAndSettle(t, hub, m, env)

	if len(store.inserted) != 1 {
		t.Fatalf("expected 1 inserted notification, got %d", len(store.inserted))
	}
	n := store.inserted[0]
	if n.Category != CategoryRequest {
		t.Errorf("expected category %q, got %q", CategoryRequest, n.Category)
	}
	if n.Type != "request.approved" {
		t.Errorf("expected type %q, got %q", "request.approved", n.Type)
	}
	if n.UserID != 7 {
		t.Errorf("expected UserID 7, got %d", n.UserID)
	}
	if n.DedupRef != "req-1:approved" {
		t.Errorf("expected DedupRef %q, got %q", "req-1:approved", n.DedupRef)
	}
	if n.Body != "Dune" {
		t.Errorf("expected Body %q, got %q", "Dune", n.Body)
	}
	if n.Title != "Request approved" {
		t.Fatalf("title = %q", n.Title)
	}
}

func TestRequestMatcher_SubmittedDoesNotNotifyRequester(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)

	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	payload := RequestEventData{
		RequestID: "req-2",
		UserID:    7,
		Title:     "Dune",
		MediaType: "movie",
		Status:    "submitted",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	env := evt.Envelope{
		Channel: evt.ChannelRequests,
		Event:   "request.submitted",
		Data:    data,
		UserID:  7,
	}

	publishAndSettle(t, hub, m, env)

	if len(store.inserted) != 0 {
		t.Errorf("expected 0 inserted notifications for request.submitted, got %d", len(store.inserted))
	}
}

func TestMaterializer_MatcherPanicIsolated(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)

	// Prepend a panicking matcher so it runs before the request matcher.
	// This proves a panic in one matcher does not prevent subsequent matchers from running.
	m.matchers = append([]namedMatcher{{
		name: "panicker",
		fn: func(context.Context, evt.Envelope) error {
			panic("boom")
		},
	}}, m.matchers...)

	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	payload := RequestEventData{
		RequestID: "req-3",
		UserID:    7,
		Title:     "Dune",
		MediaType: "movie",
		Status:    "approved",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	env := evt.Envelope{
		Channel: evt.ChannelRequests,
		Event:   "request.approved",
		Data:    data,
		UserID:  7,
	}

	publishAndSettle(t, hub, m, env)

	// The request matcher should still have run despite the panicker.
	if len(store.inserted) != 1 {
		t.Errorf("expected 1 inserted notification despite panic, got %d", len(store.inserted))
	}
}

// fakeResolver satisfies ContentResolver for unit tests.
type fakeResolver struct {
	title     string
	seriesID  string
	library   int
	createdAt time.Time
	refs      []ProfileRef
}

func (f *fakeResolver) ItemContext(_ context.Context, _ string) (string, string, int, time.Time, error) {
	return f.title, f.seriesID, f.library, f.createdAt, nil
}

func (f *fakeResolver) InterestedProfiles(_ context.Context, _, _ string, _ int, _ time.Time) ([]ProfileRef, error) {
	return f.refs, nil
}

// catalogItemChangedEnvelope builds a raw evt.Envelope for catalog.item.changed events.
func catalogItemChangedEnvelope(libraryID int, contentID, change string) evt.Envelope {
	data, _ := json.Marshal(map[string]any{
		"library_id": libraryID,
		"content_id": contentID,
		"change":     change,
	})
	return evt.Envelope{
		Channel: evt.ChannelCatalog,
		Event:   "catalog.item.changed",
		Data:    data,
	}
}

func TestContentMatcher_NotifiesInterestedProfiles(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)

	resolver := &fakeResolver{
		title:     "Inception",
		seriesID:  "",
		library:   1,
		createdAt: time.Now(),
		refs: []ProfileRef{
			{UserID: 10, ProfileID: "profile-a"},
			{UserID: 11, ProfileID: "profile-b"},
		},
	}

	m := NewMaterializer(hub, svc, resolver)
	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	publishAndSettle(t, hub, m, catalogItemChangedEnvelope(1, "content-42", "metadata_updated"))

	if len(store.inserted) != 2 {
		t.Fatalf("expected 2 inserted notifications, got %d", len(store.inserted))
	}
	n := store.inserted[0]
	if n.Category != CategoryContent {
		t.Errorf("category = %q, want %q", n.Category, CategoryContent)
	}
	if n.Type != "content.added" {
		t.Errorf("type = %q, want %q", n.Type, "content.added")
	}
	if n.ItemID == nil || *n.ItemID != "content-42" {
		t.Errorf("item_id = %v, want %q", n.ItemID, "content-42")
	}
	if n.ProfileID == nil || *n.ProfileID != "profile-a" {
		t.Errorf("profile_id = %v, want %q", n.ProfileID, "profile-a")
	}
}

func TestContentMatcher_BurstCollapsesViaDedup(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)

	resolver := &fakeResolver{
		title:     "Episode",
		seriesID:  "series-99",
		library:   1,
		createdAt: time.Now(),
		refs: []ProfileRef{
			{UserID: 10, ProfileID: "profile-a"},
		},
	}

	m := NewMaterializer(hub, svc, resolver)
	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	// Three episodes from the same series published sequentially.
	// All share the same (series_id, profile_id, dayBucket) → same dedup_ref → only 1 insert.
	for _, epID := range []string{"ep-1", "ep-2", "ep-3"} {
		publishAndSettle(t, hub, m, catalogItemChangedEnvelope(1, epID, "metadata_updated"))
	}

	if len(store.inserted) != 1 {
		t.Fatalf("expected 1 insert (dedup collapsed burst), got %d", len(store.inserted))
	}
}

func TestContentMatcher_NonAddedChangeIgnored(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)

	resolver := &fakeResolver{
		title:     "Something",
		library:   1,
		createdAt: time.Now(),
		refs:      []ProfileRef{{UserID: 10, ProfileID: "profile-a"}},
	}

	m := NewMaterializer(hub, svc, resolver)
	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	// "library_rescan" is not a recognized addition change value → should be ignored.
	data, _ := json.Marshal(map[string]any{
		"library_id": 1,
		"content_id": "content-x",
		"change":     "library_rescan",
	})
	publishAndSettle(t, hub, m, evt.Envelope{
		Channel: evt.ChannelCatalog,
		Event:   "catalog.item.changed",
		Data:    data,
	})

	if len(store.inserted) != 0 {
		t.Fatalf("expected 0 inserts for non-addition change, got %d", len(store.inserted))
	}
}

func TestContentMatcher_NilResolverDisabled(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)

	// nil resolver → content matcher not registered
	m := NewMaterializer(hub, svc, nil)
	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	publishAndSettle(t, hub, m, catalogItemChangedEnvelope(1, "content-zzz", "metadata_updated"))

	if len(store.inserted) != 0 {
		t.Fatalf("expected 0 inserts when resolver is nil, got %d", len(store.inserted))
	}
}

// ---- Admin matcher tests ----

// jobFailedEnvelope builds a raw evt.Envelope simulating job.failed on ChannelJobs.
// The payload mirrors models.AdminJob JSON tags (id, job_type, status).
func jobFailedEnvelope(jobID, jobType string) evt.Envelope {
	data, _ := json.Marshal(map[string]any{
		"id":       jobID,
		"job_type": jobType,
		"status":   "failed",
	})
	return evt.Envelope{
		Channel:   evt.ChannelJobs,
		Event:     "job.failed",
		Data:      data,
		AdminOnly: true,
	}
}

func TestAdminMatcher_JobFailedNotifiesAdmins(t *testing.T) {
	store := &fakeStore{admins: []int{1, 2}}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)
	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	publishAndSettle(t, hub, m, jobFailedEnvelope("job-abc", "export"))

	if len(store.inserted) != 2 {
		t.Fatalf("expected 2 inserts (one per admin), got %d", len(store.inserted))
	}
	for _, n := range store.inserted {
		if n.Category != CategoryAdmin {
			t.Errorf("category = %q, want %q", n.Category, CategoryAdmin)
		}
		if n.Type != "job.failed" {
			t.Errorf("type = %q, want %q", n.Type, "job.failed")
		}
	}
	// Both admin users must be covered.
	seen := map[int]bool{}
	for _, n := range store.inserted {
		seen[n.UserID] = true
	}
	if !seen[1] || !seen[2] {
		t.Errorf("expected both admins (1, 2) notified; got %v", seen)
	}
}

func TestAdminMatcher_RepeatFailureThrottled(t *testing.T) {
	store := &fakeStore{admins: []int{1}}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)
	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	// Publish the same job.failed event 3 times; dedup_ref is hourly-bucketed
	// so all three fall in the same bucket → only 1 insert per admin.
	env := jobFailedEnvelope("job-xyz", "scan")
	for range 3 {
		publishAndSettle(t, hub, m, env)
	}
	if len(store.inserted) != 1 {
		t.Fatalf("expected 1 insert after dedup throttle, got %d", len(store.inserted))
	}
}

func TestAdminMatcher_NonFailureIgnored(t *testing.T) {
	store := &fakeStore{admins: []int{1, 2}}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)
	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	data, _ := json.Marshal(map[string]any{"id": "job-1", "job_type": "export", "status": "completed"})
	publishAndSettle(t, hub, m, evt.Envelope{
		Channel: evt.ChannelJobs,
		Event:   "job.completed",
		Data:    data,
	})

	if len(store.inserted) != 0 {
		t.Fatalf("non-failure event should produce 0 inserts, got %d", len(store.inserted))
	}
}

func TestAdminMatcher_WrongChannelIgnored(t *testing.T) {
	store := &fakeStore{admins: []int{1, 2}}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)
	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	// job.failed arriving on ChannelCatalog must be ignored.
	data, _ := json.Marshal(map[string]any{"id": "job-1", "job_type": "export", "status": "failed"})
	publishAndSettle(t, hub, m, evt.Envelope{
		Channel: evt.ChannelCatalog,
		Event:   "job.failed",
		Data:    data,
	})

	if len(store.inserted) != 0 {
		t.Fatalf("wrong channel should produce 0 inserts, got %d", len(store.inserted))
	}
}

// TestContentMatcher_OldItemMetadataRefreshIgnored verifies that a
// metadata_updated event for an item whose catalog row is older than
// newItemWindow does not produce any notifications.  This prevents periodic
// library refresh jobs from re-notifying users about years-old watchlist items.
func TestContentMatcher_OldItemMetadataRefreshIgnored(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)

	// createdAt 30 days ago — well beyond the 48-hour newItemWindow.
	resolver := &fakeResolver{
		title:     "Old Movie",
		seriesID:  "",
		library:   1,
		createdAt: time.Now().Add(-30 * 24 * time.Hour),
		refs:      []ProfileRef{{UserID: 10, ProfileID: "profile-a"}},
	}

	m := NewMaterializer(hub, svc, resolver)
	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	// metadata_updated fires (e.g. from a library refresh job) but the item is old.
	publishAndSettle(t, hub, m, catalogItemChangedEnvelope(1, "content-old", "metadata_updated"))

	if len(store.inserted) != 0 {
		t.Fatalf("expected 0 inserts for old item metadata refresh, got %d", len(store.inserted))
	}
}
