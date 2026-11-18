package sections

import (
	"context"
	"errors"
	"fmt"

	"testing"

	"github.com/Silo-Server/silo-server/internal/models"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// markStore answers ListNotInterestedByMediaItems from marks, or fails.
type markStore struct {
	userstore.UserStore
	marks map[string]bool
	err   error
}

func (s markStore) ListNotInterestedByMediaItems(_ context.Context, _ string, ids []string) (map[string]bool, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := map[string]bool{}
	for _, id := range ids {
		if s.marks[id] {
			out[id] = true
		}
	}
	return out, nil
}

type markStoreProvider struct{ store markStore }

func (markStoreProvider) Close() error { return nil }

func (p markStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func titleRow(n int) []*models.MediaItem {
	items := make([]*models.MediaItem, n)
	for i := range items {
		items[i] = &models.MediaItem{ContentID: fmt.Sprintf("t%02d", i)}
	}
	return items
}

func rowIDs(items []*models.MediaItem) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ContentID
	}
	return ids
}

// A failed mark lookup leaves the row as fetched.
func TestWithoutNotInterestedFailsOpen(t *testing.T) {
	f := &Fetcher{StoreProvider: markStoreProvider{markStore{err: errors.New("store down")}}}
	row := titleRow(3)
	items, total := f.withoutNotInterested(t.Context(), ResolvedSection{ItemLimit: 3}, 1, "p", row, 3)
	if len(items) != 3 || total != 3 {
		t.Fatalf("row = %v, %d; want it unchanged", rowIDs(items), total)
	}
}
