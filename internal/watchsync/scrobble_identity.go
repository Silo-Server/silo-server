package watchsync

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// ScrobbleIdentityResolver resolves a local media item to the stable provider
// identity required by external scrobble APIs.
type ScrobbleIdentityResolver interface {
	ResolveHistoryIdentity(ctx context.Context, mediaItemID string) userstore.WatchIdentity
}

// ResolveScrobbleIdentity enriches an event with stable movie or episode IDs.
// Unknown identities retain the native playback fallback of treating the item
// as a movie; providers then report their normal missing-identity error instead
// of silently dropping the lifecycle event.
func ResolveScrobbleIdentity(ctx context.Context, resolver ScrobbleIdentityResolver, event ScrobbleEvent) ScrobbleEvent {
	if resolver == nil {
		if event.Kind == "" {
			event.Kind = historyimport.KindMovie
		}
		return event
	}

	identity := resolver.ResolveHistoryIdentity(ctx, event.MediaItemID)
	if identity.StableType != "" {
		event.Kind = identity.StableType
	}
	if event.Kind == "" {
		event.Kind = historyimport.KindMovie
	}
	event.SeasonNumber = optionalIntValue(identity.Season)
	event.EpisodeNumber = optionalIntValue(identity.Episode)
	if identity.ProviderIDs != nil {
		event.IMDbID = identity.ProviderIDs["imdb"]
		event.TMDBID = identity.ProviderIDs["tmdb"]
		event.TVDBID = identity.ProviderIDs["tvdb"]
	}
	if identity.SeriesProviderIDs != nil {
		event.SeriesIMDbID = identity.SeriesProviderIDs["imdb"]
		event.SeriesTMDBID = identity.SeriesProviderIDs["tmdb"]
		event.SeriesTVDBID = identity.SeriesProviderIDs["tvdb"]
	}
	return event
}

func optionalIntValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
