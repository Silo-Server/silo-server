package nfo

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

// The NFO provider reads Rotten Tomatoes scores into the rt_critic and
// rt_audience columns, so it declares both for an administrator to show.
func TestProviderDeclaresRottenTomatoes(t *testing.T) {
	var declarer metadata.RatingSourceDeclarer = NewProvider()
	var ids []string
	for _, source := range declarer.RatingSources() {
		ids = append(ids, source.Source)
		if source.Name == "" || source.Scale != 100 || !source.Percent {
			t.Errorf("declaration %+v, want a named percentage on a 100 scale", source)
		}
	}
	if len(ids) != 2 || ids[0] != models.RatingSourceRTCritic || ids[1] != models.RatingSourceRTAudience {
		t.Fatalf("declared %q, want rt_critic and rt_audience", ids)
	}
}
