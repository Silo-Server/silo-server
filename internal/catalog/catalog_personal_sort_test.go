package catalog

import (
	"testing"
)

func TestValidateCatalogPersonalRequest_PersonalizedSorts(t *testing.T) {
	personalizedSorts := []string{"date_viewed", "progress", "plays"}
	sources := []CatalogSource{CatalogSourceHistory, CatalogSourceFavorites, CatalogSourceWatchlist}

	for _, source := range sources {
		for _, sortField := range personalizedSorts {
			req := CatalogRequest{
				Source: source,
				Query: QueryDefinition{
					Sort: QuerySort{
						Field: sortField,
						Order: "desc",
					},
				},
			}

			allowed := source == CatalogSourceHistory && sortField == "date_viewed"
			if err := validateCatalogPersonalRequest(req, true); (err == nil) != allowed {
				t.Errorf("source=%s sort=%s: allowed=%t, err=%v", source, sortField, allowed, err)
			}
			// When allowPersonalizedSorts is false
			if err := validateCatalogPersonalRequest(req, false); err == nil {
				t.Errorf("validateCatalogPersonalRequest(source=%s, sort=%s, allow=false) should fail for personalized sort %s", source, sortField, sortField)
			}
		}
	}
}
