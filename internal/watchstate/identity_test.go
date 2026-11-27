package watchstate

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type identityEpisodes map[string]*models.Episode

func (e identityEpisodes) GetByID(_ context.Context, id string) (*models.Episode, error) {
	if ep, ok := e[id]; ok {
		return ep, nil
	}
	return nil, catalog.ErrEpisodeNotFound
}

func (identityEpisodes) GetBySeriesAndNumber(context.Context, string, int, int) (*models.Episode, error) {
	return nil, catalog.ErrEpisodeNotFound
}

type identityProviderIDs struct{}

func (identityProviderIDs) GetByContentID(context.Context, string) ([]*models.MediaItemProviderID, error) {
	return []*models.MediaItemProviderID{{Provider: "tvdb", ProviderID: "72449"}}, nil
}

func (identityProviderIDs) FindContentIDByProviderIDs(context.Context, map[string]string, string, string) (string, error) {
	return "", nil
}

// A series shown in DVD order or arranged seasons still scrobbles each
// episode by its provider's default numbers, the ones watch services know.
func TestResolveHistoryIdentityUsesProviderNumbers(t *testing.T) {
	season, number := 1, 8
	episodes := identityEpisodes{
		"renumbered": {ContentID: "renumbered", SeriesID: "series-tvdb-72449", SeasonNumber: 1, EpisodeNumber: 12,
			TvdbID: "85180", ProviderSeasonNumber: &season, ProviderEpisodeNumber: &number},
		"plain": {ContentID: "plain", SeriesID: "series-tvdb-72449", SeasonNumber: 2, EpisodeNumber: 3, TvdbID: "85200"},
	}
	resolver := NewStableIdentityResolver(nil, episodes, identityProviderIDs{})
	for id, want := range map[string][2]int{"renumbered": {1, 8}, "plain": {2, 3}} {
		identity := resolver.ResolveHistoryIdentity(context.Background(), id)
		if identity.Season == nil || identity.Episode == nil || *identity.Season != want[0] || *identity.Episode != want[1] {
			t.Fatalf("%s identity = %+v, want S%dE%d", id, identity, want[0], want[1])
		}
		batch := resolver.ResolveHistoryIdentities(context.Background(), []string{id})[id]
		if batch.Season == nil || *batch.Season != want[0] || *batch.Episode != want[1] {
			t.Fatalf("%s batch identity = %+v, want S%dE%d", id, batch, want[0], want[1])
		}
	}
}
