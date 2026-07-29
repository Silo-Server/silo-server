package watchsync

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

type fixedScrobbleIdentityResolver struct {
	identity userstore.WatchIdentity
}

func (r fixedScrobbleIdentityResolver) ResolveHistoryIdentity(context.Context, string) userstore.WatchIdentity {
	return r.identity
}

func TestResolveScrobbleIdentityEpisode(t *testing.T) {
	season := 2
	episode := 7
	event := ResolveScrobbleIdentity(context.Background(), fixedScrobbleIdentityResolver{
		identity: userstore.WatchIdentity{
			StableType:        "episode",
			ProviderIDs:       map[string]string{"tmdb": "episode-tmdb"},
			SeriesProviderIDs: map[string]string{"tvdb": "series-tvdb"},
			Season:            &season,
			Episode:           &episode,
		},
	}, ScrobbleEvent{MediaItemID: "episode-1"})

	if event.Kind != "episode" || event.TMDBID != "episode-tmdb" || event.SeriesTVDBID != "series-tvdb" {
		t.Fatalf("resolved identity = %+v", event)
	}
	if event.SeasonNumber != season || event.EpisodeNumber != episode {
		t.Fatalf("resolved episode numbers = S%dE%d, want S%dE%d", event.SeasonNumber, event.EpisodeNumber, season, episode)
	}
}

func TestResolveScrobbleIdentityDefaultsUnknownItemToMovie(t *testing.T) {
	event := ResolveScrobbleIdentity(context.Background(), nil, ScrobbleEvent{MediaItemID: "movie-1"})
	if event.Kind != "movie" {
		t.Fatalf("kind = %q, want movie", event.Kind)
	}
}
