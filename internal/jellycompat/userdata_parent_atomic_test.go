package jellycompat

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

type parentAtomicContent struct {
	*stubContentService
	episodes []upstreamEpisode
}

func (s parentAtomicContent) ListEpisodesBySeasonID(context.Context, *Session, string, *int) ([]upstreamEpisode, error) {
	return s.episodes, nil
}

type parentAtomicOnlyStore struct{ atomicFavoriteOnlyStore }

func (s parentAtomicOnlyStore) ApplyJellycompatParent(ctx context.Context, profile string, edit userstore.JellycompatParentEdit) error {
	writer, ok := s.UserStore.(userstore.JellycompatParentEditor)
	if !ok {
		return errors.New("parent editor unavailable")
	}
	return writer.ApplyJellycompatParent(ctx, profile, edit)
}
func (parentAtomicOnlyStore) MarkWatchedBatch(context.Context, string, []userstore.MarkWatchedTarget, []userstore.WatchHistoryEntry) ([]userstore.WatchHistoryEntry, error) {
	return nil, errors.New("separate batch forbidden")
}
func (parentAtomicOnlyStore) RemoveHistoryItems(context.Context, string, []string, time.Time) error {
	return errors.New("separate removal forbidden")
}

func TestParentUserDataCombinedFavoriteUsesOneTransaction(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "episodes", true: "empty"}[empty], func(t *testing.T) {
			store := newJellycompatUserStore(t)
			provider := compatTestUserStoreProvider{store: parentAtomicOnlyStore{atomicFavoriteOnlyStore{store}}}
			service := &directUserDataService{storeProvider: provider, watchState: watchstate.NewService(provider)}
			content := parentAtomicContent{stubContentService: &stubContentService{detail: &upstreamItemDetail{ContentID: "season", Type: "season"}}}
			if !empty {
				content.episodes = []upstreamEpisode{{ContentID: "episode-1"}, {ContentID: "episode-2"}}
			}
			codec := NewResourceIDCodec()
			h := NewUserDataHandler(content, service, codec, &config.Config{})
			session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
			for index, body := range []string{`{"PlayCount":1,"IsFavorite":true}`, `{"Played":false,"IsFavorite":false}`} {
				rec := httptest.NewRecorder()
				h.HandleUpdateUserData(rec, viewerRequest("POST", "/", body, "itemId", codec.EncodeStringID(EncodedIDItem, "season"), session))
				if rec.Code != 200 {
					t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
				}
				favorite, err := store.IsFavorite(t.Context(), session.ProfileID, "season")
				if err != nil || favorite != (index == 0) {
					t.Fatalf("favorite=%v error=%v", favorite, err)
				}
				history, err := store.ListHistory(t.Context(), session.ProfileID, 10, 0)
				expected := 0
				if index == 0 && !empty {
					expected = 2
				}
				if err != nil || len(history) != expected {
					t.Fatalf("history count=%d want=%d error=%v", len(history), expected, err)
				}

			}
			history, err := store.ListHistory(t.Context(), session.ProfileID, 10, 0)
			if err != nil || len(history) != 0 {
				t.Fatalf("history=%v error=%v", history, err)
			}
		})
	}
}

func TestParentUserDataUnsupportedAtomicStoreDoesNotWrite(t *testing.T) {
	store := newJellycompatUserStore(t)
	// Embedding only the base interface intentionally hides the optional editor.
	provider := compatTestUserStoreProvider{store: atomicFavoriteOnlyStore{store}}
	service := &directUserDataService{storeProvider: provider, watchState: watchstate.NewService(provider)}
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	if err := service.UpdateParentUserData(t.Context(), session, "series", []string{"episode"}, true, true); err == nil {
		t.Fatal("unsupported editor succeeded")
	}
	p, err := store.GetProgress(t.Context(), session.ProfileID, "episode")
	if err != nil || p != nil {
		t.Fatalf("partial progress=%v error=%v", p, err)
	}
	history, err := store.ListHistory(t.Context(), session.ProfileID, 10, 0)
	if err != nil || len(history) != 0 {
		t.Fatalf("partial history=%v error=%v", history, err)
	}
}
