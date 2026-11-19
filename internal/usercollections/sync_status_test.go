package usercollections

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// mockSyncUserStore holds one stored collection and applies the sync state
// the way the Postgres store does: the next run is written only while the
// stored schedule and next run still match the ones the sync started with.
type mockSyncUserStore struct {
	userstore.UserStore
	stored        userstore.Collection
	replacedItems []userstore.CollectionItemReplacement
	syncState     userstore.UpdateCollectionSyncStateInput
}

func newMockSyncUserStore(stored userstore.Collection) *mockSyncUserStore {
	return &mockSyncUserStore{stored: stored}
}

func (m *mockSyncUserStore) ReplaceCollectionItems(ctx context.Context, collectionID string, items []userstore.CollectionItemReplacement) error {
	m.replacedItems = items
	return nil
}

func (m *mockSyncUserStore) UpdateCollectionSyncState(ctx context.Context, input userstore.UpdateCollectionSyncStateInput) error {
	if input.RequireLastSyncAt && !equalTimePtr(m.stored.LastSyncAt, input.LastSyncAtAtStart) {
		return nil
	}
	m.syncState = input
	at := input.LastSyncAt
	m.stored.LastSyncAt, m.stored.LastSyncStatus, m.stored.LastSyncMessage, m.stored.ItemCount = &at, input.Status, input.Message, input.ItemCount
	if equalPtr(m.stored.SyncSchedule, input.ScheduleAtStart) && equalTimePtr(m.stored.NextSyncAt, input.NextSyncAtAtStart) {
		m.stored.NextSyncAt = input.NextSyncAt
	}
	return nil
}

func (m *mockSyncUserStore) GetCollection(ctx context.Context, id string) (*userstore.Collection, error) {
	c := m.stored
	return &c, nil
}

func equalPtr(a, b *string) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

func equalTimePtr(a, b *time.Time) bool {
	return (a == nil) == (b == nil) && (a == nil || a.Equal(*b))
}

// TestApplyResultReturnsAScheduleEditedDuringTheSync pins the collection a
// sync returns to what the store kept: when the profile changed the schedule
// while the sync ran, the store keeps the edit, and so does the result the
// import or sync-now response is built from.
func TestApplyResultReturnsAScheduleEditedDuringTheSync(t *testing.T) {
	t.Parallel()

	svc := NewService(nil, nil, nil, nil, nil, slog.New(slog.DiscardHandler))
	daily, weekly := AllowedSyncSchedules["daily"], AllowedSyncSchedules["weekly"]
	started := time.Now().Add(-time.Minute).UTC()
	collection := &userstore.Collection{ID: "test-col-1", SyncSchedule: &daily, NextSyncAt: &started}

	editedNext := time.Now().Add(6 * 24 * time.Hour).UTC()
	edited := *collection
	edited.SyncSchedule, edited.NextSyncAt = &weekly, &editedNext
	store := newMockSyncUserStore(edited)

	_, updated, err := svc.applyResult(context.Background(), store, collection, started, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("applyResult failed: %v", err)
	}
	if updated.SyncSchedule == nil || *updated.SyncSchedule != weekly {
		t.Errorf("sync_schedule = %v, want the edited %q", updated.SyncSchedule, weekly)
	}
	if updated.NextSyncAt == nil || !updated.NextSyncAt.Equal(editedNext) {
		t.Errorf("next_sync_at = %v, want the edited %v", updated.NextSyncAt, editedNext)
	}
	if updated.LastSyncStatus != "success" {
		t.Errorf("last_sync_status = %q, want success", updated.LastSyncStatus)
	}
}
