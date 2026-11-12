package jellycompat

import (
	"context"
	"testing"

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
			m.ratings = tc.ratings
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
