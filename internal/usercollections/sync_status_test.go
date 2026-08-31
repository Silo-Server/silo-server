package usercollections

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

type mockSyncUserStore struct {
	userstore.UserStore
	replacedItems []userstore.CollectionItemReplacement
	syncState     userstore.UpdateCollectionSyncStateInput
}

func (m *mockSyncUserStore) ReplaceCollectionItems(ctx context.Context, collectionID string, items []userstore.CollectionItemReplacement) error {
	m.replacedItems = items
	return nil
}

func (m *mockSyncUserStore) UpdateCollectionSyncState(ctx context.Context, input userstore.UpdateCollectionSyncStateInput) error {
	m.syncState = input
	return nil
}

func TestApplyResult_UnmatchedItemsReportsSuccessStatus(t *testing.T) {
	t.Parallel()

	svc := NewService(nil, nil, nil, nil, slog.New(slog.DiscardHandler))
	store := &mockSyncUserStore{}
	collection := &userstore.Collection{
		ID: "test-col-1",
	}

	startedAt := time.Now().UTC().Add(-time.Second)
	matched := []userstore.CollectionItemReplacement{
		{MediaItemID: "item-1", Position: 0},
		{MediaItemID: "item-2", Position: 1},
	}

	result, updated, err := svc.applyResult(
		context.Background(),
		store,
		collection,
		startedAt,
		matched,
		10, // sourceTotal
		10, // scanned
		8,  // unmatched > 0
	)
	if err != nil {
		t.Fatalf("applyResult failed: %v", err)
	}

	if result.Status != "success" {
		t.Errorf("result.Status = %q, want %q", result.Status, "success")
	}
	if result.ItemsMatched != 2 {
		t.Errorf("result.ItemsMatched = %d, want 2", result.ItemsMatched)
	}
	if result.ItemsUnmatched != 8 {
		t.Errorf("result.ItemsUnmatched = %d, want 8", result.ItemsUnmatched)
	}
	if store.syncState.Status != "success" {
		t.Errorf("store.syncState.Status = %q, want %q", store.syncState.Status, "success")
	}
	if updated.LastSyncStatus != "success" {
		t.Errorf("updated.LastSyncStatus = %q, want %q", updated.LastSyncStatus, "success")
	}
}
