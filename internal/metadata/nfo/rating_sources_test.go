package nfo

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

// The NFO provider declares every rating source beyond IMDb and TMDB it can
// read from local .nfo files, so an administrator can choose to show them:
// the Rotten Tomatoes scores it stores in the rt_critic and rt_audience
// columns, and the other named ratings Kodi writers carry.
func TestProviderDeclaresItsRatingSources(t *testing.T) {
	var declarer metadata.RatingSourceDeclarer = NewProvider()
	var ids []string
	for _, source := range declarer.RatingSources() {
		ids = append(ids, source.Source)
		if source.Name == "" || source.Label == "" || source.Scale <= 0 || source.Scale > 100 {
			t.Errorf("declaration %+v, want a name, a label, and a scale up to 100", source)
		}
		if !models.ValidRatingSourceID(source.Source) || models.IsBuiltinRatingSource(source.Source) {
			t.Errorf("declaration %+v names a source a plugin may not declare", source)
		}
		percent := source.Source == models.RatingSourceRTCritic || source.Source == models.RatingSourceRTAudience ||
			source.Source == ratingSourceTrakt
		if percent != source.Percent {
			t.Errorf("declaration %+v: only Rotten Tomatoes and Trakt show percentages", source)
		}
	}
	want := []string{
		models.RatingSourceRTCritic, models.RatingSourceRTAudience,
		ratingSourceMetacritic, ratingSourceTrakt, ratingSourceTVDB, ratingSourceLetterboxd,
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("declared %q, want %q", ids, want)
	}
	// Every source a <rating name> maps to is one the registry can show.
	declared := make(map[string]bool)
	for _, id := range ids {
		declared[id] = true
	}
	for name, spec := range nfoRatingNames {
		if !declared[spec.source] && !models.IsBuiltinRatingSource(spec.source) {
			t.Errorf("<rating name=%q> maps to undeclared source %q", name, spec.source)
		}
	}
}
