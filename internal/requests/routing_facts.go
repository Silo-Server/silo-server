package requests

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

// RoutingFacts is what routing rules can match a request on. It is captured
// from TMDB when the request is created and stored with it, so rules evaluate
// the same way at approval, on another server, or after TMDB changes.
type RoutingFacts struct {
	GenreIDs         []int    `json:"genre_ids,omitempty"`
	KeywordIDs       []int    `json:"keyword_ids,omitempty"`
	OriginalLanguage string   `json:"original_language,omitempty"`
	OriginCountries  []string `json:"origin_countries,omitempty"`
	Year             int      `json:"year,omitempty"`
	// NetworkIDs are a series' networks; CompanyIDs a movie's studios.
	NetworkIDs []int `json:"network_ids,omitempty"`
	CompanyIDs []int `json:"company_ids,omitempty"`
	Anime      bool  `json:"anime,omitempty"`
	// ContentRating is the title's US rating ("PG", "TV-14"), or, when it
	// has none, its own country's prefixed with the country ("JP:PG12"); ""
	// when TMDB has neither, nil when it was never looked up (requests from
	// before it was captured).
	ContentRating *string `json:"content_rating,omitempty"`
	// CapturedAt is unset on requests from before capture, which is how
	// routing tells "no facts yet" from a title TMDB knows little about.
	CapturedAt *time.Time `json:"captured_at,omitempty"`
}

// Captured reports whether the facts were ever read from TMDB.
func (f RoutingFacts) Captured() bool { return f.CapturedAt != nil }

// routingFactsFrom reads the facts off a TMDB detail; listed says whether the
// anime list names the title. A nil detail (TMDB unreachable) yields
// uncaptured facts, so routing retries the lookup later.
func routingFactsFrom(detail *tmdb.MediaDetail, listed bool, now time.Time) RoutingFacts {
	if detail == nil {
		return RoutingFacts{}
	}
	rating := routingRating(detail.USCertification, detail.Certifications, detail.OriginCountries)
	return RoutingFacts{
		GenreIDs:         detail.GenreIDs,
		KeywordIDs:       detail.KeywordIDs,
		OriginalLanguage: detail.OriginalLanguage,
		OriginCountries:  detail.OriginCountries,
		Year:             detail.Year,
		NetworkIDs:       detail.NetworkIDs,
		CompanyIDs:       detail.CompanyIDs,
		Anime:            detectAnime(detail, listed),
		ContentRating:    &rating,
		CapturedAt:       &now,
	}
}

// routingRating picks the rating routing matches a title on. Most rules are
// written in US ratings, so the US one wins. A title never rated in the US
// (common for Japanese, Korean or European releases) falls back to its own
// country's, the strictest where that country rated it more than once,
// prefixed with the country so its age reads on that country's scale
// ("JP:PG12" is 12). So does one rated only "NR" in the US. Ratings no known
// scale reads are skipped.
func routingRating(us string, certs map[string][]string, origins []string) string {
	us = strings.TrimSpace(us)
	if _, ok := ratingAge(us); ok {
		return us
	}
	// No US rating, or only one without an age ("NR" from a festival run):
	// the title's own country's, keeping the US marker if it has none.
	for _, country := range origins {
		country = strings.ToUpper(strings.TrimSpace(country))
		picked, pickedAge := "", -1
		for _, cert := range certs[country] {
			prefixed := country + ":" + strings.TrimSpace(cert)
			if age, ok := ratingAge(prefixed); ok && age > pickedAge {
				picked, pickedAge = prefixed, age
			}
		}
		if picked != "" {
			return picked
		}
	}
	return us
}

// requestDetail fetches the TMDB detail a request is checked and routed
// against. It returns nil when TMDB cannot answer, or when the service has no
// TMDB client (a service wired for another job).
func (s *Service) requestDetail(ctx context.Context, mediaType MediaType, tmdbID int) *tmdb.MediaDetail {
	if s.tmdb == nil {
		return nil
	}
	detail, err := s.tmdb.GetMediaDetail(ctx, tmdbMediaType(mediaType), tmdbID)
	if err != nil {
		return nil
	}
	return detail
}

func encodeRoutingFacts(facts RoutingFacts) ([]byte, error) {
	raw, err := json.Marshal(facts)
	if err != nil {
		return nil, fmt.Errorf("encode routing facts: %w", err)
	}
	return raw, nil
}

func decodeRoutingFacts(raw []byte) (RoutingFacts, error) {
	var facts RoutingFacts
	if len(raw) == 0 {
		return facts, nil
	}
	if err := json.Unmarshal(raw, &facts); err != nil {
		return RoutingFacts{}, fmt.Errorf("decode routing facts: %w", err)
	}
	return facts, nil
}
