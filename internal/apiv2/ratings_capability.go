package apiv2

import (
	"context"
	"net/http"
)

// RatingsCapability reports that item detail carries ratings, the external
// ratings list a title page renders, and which sources clients show.
type RatingsCapability struct {
	Capability
	Sources []RatingsCapabilitySource `json:"sources" doc:"The rating sources title pages and cards show, in display order: imdb and tmdb, then each source a metadata plugin declares and an administrator turned on. A card or detail carries rating_rt_critic or rating_rt_audience only when its source is listed. Empty, never null"`
}

// RatingsCapabilitySource is one rating source clients show.
type RatingsCapabilitySource struct {
	Source string `json:"source" doc:"Rating source name, as in CatalogRating.source" example:"imdb"`
	Name   string `json:"name" doc:"The source's plain-text mark" example:"IMDb"`
}

// RatingsCapabilityOutput is the getRatingsCapability response.
type RatingsCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         RatingsCapability
}

func registerRatingsCapability(reg *Registry) {
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/capabilities/ratings", "getRatingsCapability", "catalog",
		"Whether item detail carries the ratings list, and the rating sources title pages and cards show.")), reg.getRatingsCapability)
}

func (reg *Registry) getRatingsCapability(ctx context.Context, _ *CapabilityInput) (*RatingsCapabilityOutput, error) {
	shown := reg.ratingSelection(ctx).Shown()
	doc := RatingsCapability{Capability: Capability{State: StateAvailable}, Sources: make([]RatingsCapabilitySource, 0, len(shown))}
	for _, definition := range shown {
		doc.Sources = append(doc.Sources, RatingsCapabilitySource{Source: definition.Source, Name: definition.Name})
	}
	return &RatingsCapabilityOutput{CacheControl: cacheControlPrivateNoCache, Body: doc}, nil
}
