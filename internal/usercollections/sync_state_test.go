package usercollections

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

type syncStateRecordingStore struct {
	userstore.UserStore
	state userstore.UpdateCollectionSyncStateInput
}

func (s *syncStateRecordingStore) ReplaceCollectionItems(context.Context, string, []userstore.CollectionItemReplacement) error {
	return nil
}

func (s *syncStateRecordingStore) UpdateCollectionSyncState(_ context.Context, input userstore.UpdateCollectionSyncStateInput) error {
	s.state = input
	return nil
}

func TestApplyResultCarriesExpectedSyncSchedule(t *testing.T) {
	t.Parallel()

	schedule := "0 */6 * * *"
	collection := &userstore.Collection{ID: "collection-1", SyncSchedule: &schedule}
	store := &syncStateRecordingStore{}
	service := &Service{logger: slog.New(slog.DiscardHandler)}

	_, _, err := service.applyResult(
		context.Background(), store, collection, time.Now().UTC(), nil, 0, 0, 0,
	)
	if err != nil {
		t.Fatalf("apply result: %v", err)
	}
	if store.state.ExpectedSyncSchedule == nil || *store.state.ExpectedSyncSchedule != schedule {
		t.Fatalf("expected schedule = %v, want %q", store.state.ExpectedSyncSchedule, schedule)
	}
	if store.state.NextSyncAt == nil {
		t.Fatal("next_sync_at was not computed")
	}
}
