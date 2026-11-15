package notifications

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

type appendTestStore struct {
	userstore.UserStore
	appended []string
}

func (s *appendTestStore) AppendCollectionItem(_ context.Context, collectionID, mediaItemID string) error {
	s.appended = append(s.appended, collectionID+"/"+mediaItemID)
	return nil
}

func (s *appendTestStore) CollectionFeatures() userstore.CollectionFeatures {
	return userstore.CollectionFeatures{ItemAppend: true}
}

// Every production user store is wrapped, so a capability the wrapper reports
// must also reach the wrapped store, or append answers 501.
func TestCollectionAppendForwarding(t *testing.T) {
	inner := &appendTestStore{}
	wrapped := &interestTrackingStore{UserStore: inner}
	if !wrapped.CollectionFeatures().ItemAppend {
		t.Fatal("wrapped store hides the append capability")
	}
	appender, ok := any(wrapped).(interface {
		AppendCollectionItem(context.Context, string, string) error
	})
	if !ok {
		t.Fatal("wrapped store reports item_append but can't append")
	}
	if err := appender.AppendCollectionItem(t.Context(), "c", "m"); err != nil {
		t.Fatal(err)
	}
	if len(inner.appended) != 1 || inner.appended[0] != "c/m" {
		t.Fatalf("append reached the store as %v", inner.appended)
	}

	bare := &interestTrackingStore{UserStore: catalogSQLTestStore{}}
	if err := bare.AppendCollectionItem(t.Context(), "c", "m"); !errors.Is(err, userstore.ErrCollectionAppendUnsupported) {
		t.Fatalf("append without store support = %v", err)
	}
}
