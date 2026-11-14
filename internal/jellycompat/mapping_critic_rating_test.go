package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/ratingsources"
)

// criticRatingContent serves one rated movie or series through the
// production converters, so the test covers the rating copy in each.
type criticRatingContent struct {
	stubContentService
	kind string
}

func (s *criticRatingContent) BrowseItems(context.Context, *Session, url.Values) (*upstreamBrowseResponse, error) {
	item := mediaItemToListItem(&models.MediaItem{ContentID: "rated", Type: s.kind, Title: "Rated", RatingIMDB: new(7.5), RatingRTCritic: new(93)})
	return &upstreamBrowseResponse{Items: []upstreamListItem{item}, Total: 1}, nil
}

func (s *criticRatingContent) GetItemDetail(context.Context, *Session, string, *int) (*upstreamItemDetail, error) {
	detail := itemDetailToUpstream(&catalog.ItemDetail{ContentID: "rated", Type: s.kind, Title: "Rated", RatingIMDB: new(7.5), RatingRTCritic: new(93)})
	return &detail, nil
}

func (s *criticRatingContent) ListSeasons(context.Context, *Session, string, *int) ([]upstreamSeason, error) {
	return []upstreamSeason{}, nil
}

// Jellyfin clients render CriticRating as the Rotten Tomatoes percentage, so
// list and detail responses carry the stored rt_critic score, but only while
// a metadata plugin declares that source and the administrator shows it.
func TestCriticRatingFollowsShownRatingSources(t *testing.T) {
	for _, kind := range []string{"movie", "series"} {
		for _, tc := range []struct {
			name            string
			shown, declared bool
			want            any
		}{
			{name: "shown", shown: true, declared: true, want: 93.0},
			{name: "not turned on", declared: true},
			{name: "not declared", shown: true},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				extra := ""
				if tc.shown {
					extra = models.RatingSourceRTCritic
				}
				policy := ratingsources.NewPolicy(stubSettingsReader{values: map[string]string{config.CatalogExtraRatingSourcesSettingKey: extra}},
					func(context.Context) ([]ratingsources.DeclaredSource, error) {
						if !tc.declared {
							return nil, nil
						}
						return []ratingsources.DeclaredSource{{RatingSourceDefinition: models.RatingSourceDefinition{Source: models.RatingSourceRTCritic, Name: "RT", Scale: 100, Percent: true}}}, nil
					})
				sessions := NewSessionStore(time.Hour, time.Now)
				if err := sessions.Put(Session{Token: "critic", StreamAppUserID: 1, ProfileID: "p1", PseudoUserID: PseudoUserID(1, "p1")}); err != nil {
					t.Fatal(err)
				}
				codec := NewResourceIDCodec()
				router := NewRouter(Dependencies{Config: &config.Config{}, IDCodec: codec, SessionStore: sessions, ContentService: &criticRatingContent{kind: kind}, UserDataService: &mockUserDataService{}, RatingSources: policy})

				id := codec.EncodeStringID(EncodedIDItem, "rated")
				for _, path := range []string{
					"/Items?Recursive=true&IncludeItemTypes=" + kind,
					"/Items/Latest",
					"/Items/" + id,
					"/Users/" + PseudoUserID(1, "p1").String() + "/Items/" + id,
				} {
					req := httptest.NewRequest(http.MethodGet, path, nil)
					req.Header.Set("X-Emby-Token", "critic")
					rec := httptest.NewRecorder()
					router.ServeHTTP(rec, req)
					if rec.Code != http.StatusOK {
						t.Fatalf("%s: status %d: %s", path, rec.Code, rec.Body.String())
					}
					// Lists answer {"Items": [...]}, Latest a bare array, and
					// detail the item itself.
					var raw any
					if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
						t.Fatalf("%s: %v", path, err)
					}
					if items, ok := raw.([]any); ok && len(items) > 0 {
						raw = items[0]
					}
					item, _ := raw.(map[string]any)
					if items, ok := item["Items"].([]any); ok && len(items) > 0 {
						item, _ = items[0].(map[string]any)
					}
					if got := item["CriticRating"]; got != tc.want {
						t.Errorf("%s CriticRating = %v, want %v", path, got, tc.want)
					}
					if got := item["CommunityRating"]; got != 7.5 {
						t.Errorf("%s CommunityRating = %v, want the IMDb rating 7.5", path, got)
					}
				}
			})
		}
	}
}

// failingSettings fails every read after taking delay on the clock.
type failingSettings struct {
	reads atomic.Int32
	clock *time.Time
	delay time.Duration
}

func (s *failingSettings) Get(context.Context, string) (string, error) {
	s.reads.Add(1)
	*s.clock = s.clock.Add(s.delay)
	return "", errors.New("database unavailable")
}

// The policy caches nothing while its reads fail, and each retry can wait on
// the database until it times out, so a list response must not retry once
// per item, however long the failed read took.
func TestCriticRatingReadsFailingPolicyOncePerList(t *testing.T) {
	clock := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	settings := &failingSettings{clock: &clock, delay: 5 * time.Second}
	m := newMapper(NewResourceIDCodec(), &config.Config{})
	m.ratings = newShownRatings(ratingsources.NewPolicy(settings, nil))
	m.ratings.now = func() time.Time { return clock }
	critic := 80
	for range 20 {
		if dto := m.itemFromList(upstreamListItem{ContentID: "movie", Type: "movie", RatingRTCritic: &critic}, false, nil, nil); dto.CriticRating != nil {
			t.Fatalf("CriticRating = %v while the policy cannot be read", *dto.CriticRating)
		}
	}
	if got := settings.reads.Load(); got != 1 {
		t.Fatalf("mapping 20 items read the settings %d times, want 1", got)
	}
}
