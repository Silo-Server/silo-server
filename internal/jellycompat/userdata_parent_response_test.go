package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

type parentResponseContent struct{ parentAtomicContent }

func (s parentResponseContent) ListSeasons(context.Context, *Session, string, *int) ([]upstreamSeason, error) {
	return []upstreamSeason{{ContentID: "season", SeasonNumber: 1}}, nil
}
func (s parentResponseContent) ListEpisodes(context.Context, *Session, string, int, *int) ([]upstreamEpisode, error) {
	return s.episodes, nil
}

type parentResponseProgressService struct {
	UserDataService
	fail    bool
	batches []int
}

func (s *parentResponseProgressService) ListProgressByMediaItems(ctx context.Context, session *Session, ids []string) (map[string]*upstreamProgress, error) {
	if len(ids) > 0 && ids[0] != "season" {
		s.batches = append(s.batches, len(ids))
		if s.fail {
			return nil, errors.New("child progress unavailable")
		}
	}
	return s.UserDataService.ListProgressByMediaItems(ctx, session, ids)
}

func TestParentUserDataRollupBoundsReadsAndPropagatesFailure(t *testing.T) {
	store := newJellycompatUserStore(t)
	provider := compatTestUserStoreProvider{store: store}
	service := &parentResponseProgressService{UserDataService: &directUserDataService{storeProvider: provider}}
	content := parentResponseContent{parentAtomicContent{stubContentService: &stubContentService{detail: &upstreamItemDetail{ContentID: "season", Type: "season"}}}}
	for i := range 1001 {
		content.episodes = append(content.episodes, upstreamEpisode{ContentID: fmt.Sprintf("episode-%d", i)})
	}
	codec := NewResourceIDCodec()
	h := NewUserDataHandler(content, service, codec, &config.Config{})
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	for _, fail := range []bool{false, true} {
		service.fail = fail
		service.batches = nil
		rec := httptest.NewRecorder()
		h.HandleGetUserData(rec, viewerRequest("GET", "/", "", "itemId", codec.EncodeStringID(EncodedIDItem, "season"), session))
		if fail {
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("failed child read status=%d", rec.Code)
			}
		} else {
			if rec.Code != http.StatusOK {
				t.Fatalf("read status=%d body=%s", rec.Code, rec.Body.String())
			}
			var dto itemUserDataDTO
			if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
				t.Fatal(err)
			}
			if dto.UnplayedItemCount != 1001 || dto.Played {
				t.Fatalf("incomplete rollup=%+v", dto)
			}
			if !slices.Equal(service.batches, []int{500, 500, 1}) {
				t.Fatalf("read batches=%v", service.batches)
			}
		}
	}
}
