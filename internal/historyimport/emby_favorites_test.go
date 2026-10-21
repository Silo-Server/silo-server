package historyimport

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestAddFavoriteUsesAtomicInsertResult(t *testing.T) {
	store := &favoriteInsertTestStore{}
	service := &Service{stores: favoriteInsertTestProvider{store: store}}

	inserted, err := service.addFavorite(context.Background(), 42, "profile-1", "movie-1")
	if err != nil {
		t.Fatalf("addFavorite: %v", err)
	}
	if inserted {
		t.Fatal("addFavorite reported an existing favorite as newly inserted")
	}
	if store.addCalls != 1 {
		t.Fatalf("AddFavoriteAt calls = %d, want 1", store.addCalls)
	}
	if store.isFavoriteCalls != 0 {
		t.Fatalf("IsFavorite calls = %d, want 0", store.isFavoriteCalls)
	}
}

type favoriteInsertTestProvider struct {
	store userstore.UserStore
}

func (p favoriteInsertTestProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (favoriteInsertTestProvider) Close() error { return nil }

type favoriteInsertTestStore struct {
	userstore.UserStore
	addCalls        int
	isFavoriteCalls int
}

func (s *favoriteInsertTestStore) AddFavoriteAt(context.Context, string, string, time.Time) (bool, error) {
	s.addCalls++
	return false, nil
}

func (s *favoriteInsertTestStore) IsFavorite(context.Context, string, string) (bool, error) {
	s.isFavoriteCalls++
	return false, nil
}
