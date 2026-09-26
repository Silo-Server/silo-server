package jellycompat

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

type atomicFavoriteOnlyStore struct{ userstore.UserStore }

func (s atomicFavoriteOnlyStore) ApplyJellycompatProgress(ctx context.Context, profile string, edit userstore.JellycompatProgressEdit) error {
	writer, ok := s.UserStore.(userstore.JellycompatProgressEditor)
	if !ok {
		return errors.New("atomic editor unavailable")
	}
	return writer.ApplyJellycompatProgress(ctx, profile, edit)
}
func (atomicFavoriteOnlyStore) AddFavorite(context.Context, string, string) error {
	return errors.New("separate favorite addition forbidden")
}
func (atomicFavoriteOnlyStore) RemoveFavorite(context.Context, string, string) error {
	return errors.New("separate favorite removal forbidden")
}

func TestUserDataCombinedFavoriteUsesOneTransaction(t *testing.T) {
	store := newJellycompatUserStore(t)
	provider := compatTestUserStoreProvider{store: atomicFavoriteOnlyStore{store}}
	service := &directUserDataService{storeProvider: provider, watchState: watchstate.NewService(provider)}
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	for _, favorite := range []bool{true, false} {
		err := service.UpdateUserData(t.Context(), session, "movie-1", 600, updateUserDataRequest{Played: new(favorite), IsFavorite: new(favorite), PlaybackPositionTicks: new(int64(770000000))})
		if err != nil {
			t.Fatal(err)
		}
		actual, err := store.IsFavorite(t.Context(), session.ProfileID, "movie-1")
		if err != nil || actual != favorite {
			t.Fatalf("favorite=%v error=%v", actual, err)
		}
		p, err := store.GetProgress(t.Context(), session.ProfileID, "movie-1")
		if err != nil || p == nil || p.Completed != favorite || p.PositionSeconds != 77 {
			t.Fatalf("progress=%+v error=%v", p, err)
		}
		history, err := store.ListHistory(t.Context(), session.ProfileID, 10, 0)
		expected := 0
		if favorite {
			expected = 1
		}
		if err != nil || len(history) != expected {
			t.Fatalf("history=%+v error=%v", history, err)
		}
	}
}
