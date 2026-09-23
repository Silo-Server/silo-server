package tmdb

import (
	"context"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/certification"
)

// GetCertifications keeps country provenance for library presentation and
// parental controls. The existing GetCertification remains US-only for requests.
func (c *Client) GetCertifications(ctx context.Context, mediaType string, id int) (map[string]string, error) {
	if id <= 0 {
		return nil, fmt.Errorf("tmdb: invalid certification ID")
	}
	ratings := make(map[string]string)
	switch mediaType {
	case "movie":
		var response releaseDatesResponse
		if err := c.doGet(ctx, fmt.Sprintf("/movie/%d/release_dates", id), &response); err != nil {
			return nil, err
		}
		for _, country := range response.Results {
			for _, release := range country.ReleaseDates {
				keepStrictestCertification(ratings, country.ISO3166, release.Certification)
			}
		}
	case "series", "tv":
		var response contentRatingsResponse
		if err := c.doGet(ctx, fmt.Sprintf("/tv/%d/content_ratings", id), &response); err != nil {
			return nil, err
		}
		for _, entry := range response.Results {
			keepStrictestCertification(ratings, entry.ISO3166, entry.Rating)
		}
	default:
		return nil, fmt.Errorf("tmdb: invalid certification media type %q", mediaType)
	}
	return ratings, nil
}

func keepStrictestCertification(ratings map[string]string, country, rating string) {
	country = strings.ToUpper(strings.TrimSpace(country))
	if country == "" || !certification.SupportedCertificationCountry(country) {
		return
	}
	rating = certification.NormalizeCertification(country, rating)
	if rating == "" {
		return
	}
	token := rating
	if country == "AU" {
		token = "AU-" + rating
	}
	rank, known := access.RatingRank(token)
	oldToken := ratings[country]
	if country == "AU" && oldToken != "" {
		oldToken = "AU-" + oldToken
	}
	oldRank, oldKnown := access.RatingRank(oldToken)
	// Retain an unknown-only country as unrated, but prefer recognised release
	// classifications over placeholders (NR, CTC) when both are supplied.
	if ratings[country] == "" || known && (!oldKnown || rank > oldRank) {
		ratings[country] = rating
	}
}
