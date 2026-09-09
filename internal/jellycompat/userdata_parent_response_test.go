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
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type parentResponseContent struct{ parentAtomicContent }

func (s parentResponseContent) ListSeasons(context.Context, *Session, string, *int) ([]upstreamSeason, error) {
	return []upstreamSeason{{ContentID: "season", SeasonNumber: 1}}, nil
}
func (s parentResponseContent) ListEpisodes(context.Context, *Session, string, int, *int) ([]upstreamEpisode, error) {
	return s.episodes, nil
}

func TestParentUserDataResponsesRollUpChildren(t *testing.T) {
	for _, kind := range []string{"series", "season"} {
		for _, empty := range []bool{false, true} {
			name := kind
			if empty {
				name += "-empty"
			}
			t.Run(name, func(t *testing.T) {
				store := newJellycompatUserStore(t)
				provider := compatTestUserStoreProvider{store: store}
				service := &directUserDataService{storeProvider: provider, watchState: watchstate.NewService(provider)}
				content := parentResponseContent{parentAtomicContent{stubContentService: &stubContentService{detail: &upstreamItemDetail{ContentID: kind, Type: kind}}}}
				if !empty {
					content.episodes = []upstreamEpisode{{ContentID: "episode-1"}, {ContentID: "episode-2"}}
				}
				codec := NewResourceIDCodec()
				h := NewUserDataHandler(content, service, codec, &config.Config{})
				session := &Session{StreamAppUserID: 1, ProfileID: "profile-1", PseudoUserID: uuid.New()}
				// A stale parent progress row must never override the actual episode rollup.
				if err := store.SetProgressAt(t.Context(), session.ProfileID, kind, 50, 100, true, time.Now()); err != nil {
					t.Fatal(err)
				}
				if err := store.SetProgressAt(t.Context(), session.ProfileID, "episode-1", 0, 100, true, time.Now()); err != nil {
					t.Fatal(err)
				}
				if err := store.AddFavorite(t.Context(), session.ProfileID, kind); err != nil {
					t.Fatal(err)
				}
				run := func(handler http.HandlerFunc, body string, played bool, unplayed int, favorite bool) {
					t.Helper()
					req := viewerRequest("POST", "/", body, "itemId", codec.EncodeStringID(EncodedIDItem, kind), session)
					chi.RouteContext(req.Context()).URLParams.Add("userId", session.PseudoUserID.String())
					rec := httptest.NewRecorder()
					handler(rec, req)
					if rec.Code != 200 {
						t.Fatalf("response=%d %s", rec.Code, rec.Body.String())
					}
					var dto itemUserDataDTO
					if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
						t.Fatal(err)
					}
					if dto.Played != played || dto.UnplayedItemCount != unplayed || dto.IsFavorite != favorite || dto.PlaybackPositionTicks != 0 {
						t.Fatalf("body=%s dto=%+v want played=%v unplayed=%d favorite=%v", body, dto, played, unplayed, favorite)
					}
					count := 0
					if played {
						count = 1
					}
					if dto.PlayCount != count {
						t.Fatalf("play count=%d want=%d", dto.PlayCount, count)
					}
				}
				partial, total := 1, 2
				if empty {
					partial, total = 0, 0
				}
				run(h.HandleGetUserData, "", false, partial, true)
				run(h.HandleGetUserDataLegacy, "", false, partial, true)
				for _, pair := range [][2]http.HandlerFunc{{h.HandleMarkPlayed, h.HandleMarkUnplayed}, {h.HandleMarkPlayedLegacy, h.HandleMarkUnplayedLegacy}} {
					run(pair[0], "", !empty, 0, true)
					run(h.HandleGetUserData, "", !empty, 0, true)
					run(pair[1], "", false, total, true)
				}
				for _, update := range []http.HandlerFunc{h.HandleUpdateUserData, h.HandleUpdateUserDataLegacy} {
					run(update, `{"PlayCount":1,"IsFavorite":true}`, !empty, 0, true)
					run(update, `{"Played":false,"IsFavorite":false}`, false, total, false)
				}
				run(h.HandleAddFavorite, "", false, total, true)
				run(h.HandleRemoveFavorite, "", false, total, false)
				if err := store.CreateProfile(t.Context(), userstore.Profile{ID: "other", Name: "Other"}); err != nil {
					t.Fatal(err)
				}
				session.ProfileID = "other"
				run(h.HandleGetUserData, "", false, total, false)
			})
		}
	}
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
