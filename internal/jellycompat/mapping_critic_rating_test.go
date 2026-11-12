package jellycompat

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/ratingsources"
)

type criticRatingSettings map[string]string

func (s criticRatingSettings) Get(_ context.Context, key string) (string, error) {
	return s[key], nil
}

// criticRatingPolicy is a rating-source policy whose metadata plugins declare
// the Rotten Tomatoes critic score and whose administrator shows extra.
func criticRatingPolicy(extra string) *ratingsources.Policy {
	declared := func(context.Context) ([]ratingsources.DeclaredSource, error) {
		return []ratingsources.DeclaredSource{{RatingSourceDefinition: models.RatingSourceDefinition{Source: models.RatingSourceRTCritic, Name: "RT", Scale: 100, Percent: true}}}, nil
	}
	return ratingsources.NewPolicy(criticRatingSettings{config.CatalogExtraRatingSourcesSettingKey: extra}, declared)
}

// Jellyfin clients render CriticRating as the Rotten Tomatoes percentage, so
// it carries the stored rt_critic score on list and detail items, but only
// when the administrator shows that source, as native title pages do.
func TestCriticRatingFollowsShownRatingSources(t *testing.T) {
	imdb, critic := 7.5, 93
	list := upstreamListItem{ContentID: "movie-1", Type: "movie", Title: "Movie", RatingIMDB: &imdb, RatingRTCritic: &critic}
	detail := upstreamItemDetail{ContentID: "movie-1", Type: "movie", Title: "Movie", RatingIMDB: &imdb, RatingRTCritic: &critic}

	for _, tc := range []struct {
		name    string
		ratings *ratingsources.Policy
		want    *float64
	}{
		{name: "shown", ratings: criticRatingPolicy(models.RatingSourceRTCritic), want: new(93.0)},
		{name: "not turned on", ratings: criticRatingPolicy(""), want: nil},
		{name: "no policy", ratings: nil, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMapper(NewResourceIDCodec(), &config.Config{})
			m.ratings = newShownRatings(tc.ratings)
			for name, dto := range map[string]baseItemDTO{
				"list":   m.itemFromList(list, false, nil, nil),
				"detail": m.itemFromDetail(detail, false, nil),
			} {
				if !equalFloatPtr(dto.CriticRating, tc.want) {
					t.Errorf("%s CriticRating = %v, want %v", name, deref(dto.CriticRating), deref(tc.want))
				}
				if dto.CommunityRating == nil || *dto.CommunityRating != imdb {
					t.Errorf("%s CommunityRating = %v, want the IMDb rating %v", name, deref(dto.CommunityRating), imdb)
				}
			}
		})
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

func equalFloatPtr(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func deref(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}
