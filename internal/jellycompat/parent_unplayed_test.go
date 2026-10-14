package jellycompat

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

// seasonContentService lists the season's episodes that have a library file,
// as the catalog does.
type seasonContentService struct {
	stubContentService
	onDisk []string
}

func (s *seasonContentService) ListEpisodesBySeasonID(context.Context, *Session, string, *int) ([]upstreamEpisode, error) {
	episodes := make([]upstreamEpisode, 0, len(s.onDisk))
	for _, id := range s.onDisk {
		episodes = append(episodes, upstreamEpisode{ContentID: id})
	}
	return episodes, nil
}

type stubEpisodePartitioner struct {
	withFile, fileless []string
}

func (p stubEpisodePartitioner) PartitionIDsBySeries(context.Context, string) ([]string, []string, error) {
	return p.withFile, p.fileless, nil
}

func (p stubEpisodePartitioner) PartitionIDsBySeason(context.Context, string) ([]string, []string, error) {
	return p.withFile, p.fileless, nil
}

// A Jellyfin client marking a season unplayed must also clear a watched
// episode whose file was deleted; the episode listing leaves it out, and its
// completed progress would keep the series in Next Up.
func TestParentUnplayedClearsWatchedEpisodesWithoutFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*UserDataHandler, *httptest.ResponseRecorder, string, *Session)
	}{
		{"mark unplayed", func(h *UserDataHandler, rec *httptest.ResponseRecorder, id string, session *Session) {
			h.HandleMarkUnplayed(rec, viewerRequest("DELETE", "/", "", "itemId", id, session))
		}},
		{"update user data", func(h *UserDataHandler, rec *httptest.ResponseRecorder, id string, session *Session) {
			h.HandleUpdateUserData(rec, viewerRequest("POST", "/", `{"Played":false}`, "itemId", id, session))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newJellycompatUserStore(t)
			provider := notifications.WrapUserStoreProvider(compatTestUserStoreProvider{store: store}, &notifications.System{})
			service := &directUserDataService{
				storeProvider: provider,
				watchState:    watchstate.NewService(provider),
				episodes:      stubEpisodePartitioner{withFile: []string{"on-disk"}, fileless: []string{"deleted", "metadata-only"}},
			}
			if err := service.watchState.RecordJellycompatMarkPlayedBatch(t.Context(), 1, "profile-1", []string{"on-disk", "deleted"}, time.Now().UTC().Add(-time.Hour)); err != nil {
				t.Fatal(err)
			}
			codec := NewResourceIDCodec()
			content := &seasonContentService{stubContentService: stubContentService{detail: &upstreamItemDetail{ContentID: "season-1", Type: "season"}}, onDisk: []string{"on-disk"}}
			h := NewUserDataHandler(content, service, codec, &config.Config{})
			session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}

			rec := httptest.NewRecorder()
			tc.run(h, rec, codec.EncodeStringID(EncodedIDSeason, "season-1"), session)
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			for _, id := range []string{"on-disk", "deleted"} {
				progress, err := store.GetProgress(t.Context(), "profile-1", id)
				if err != nil {
					t.Fatal(err)
				}
				if progress != nil {
					t.Fatalf("%s progress after unplayed = %+v, want cleared", id, progress)
				}
			}
		})
	}
}
