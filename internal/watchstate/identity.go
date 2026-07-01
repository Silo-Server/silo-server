package watchstate

import (
	"context"
	"errors"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type itemLookup interface {
	GetByID(ctx context.Context, contentID string) (*models.MediaItem, error)
}

type episodeLookup interface {
	GetByID(ctx context.Context, contentID string) (*models.Episode, error)
	GetBySeriesAndNumber(ctx context.Context, seriesID string, season, episode int) (*models.Episode, error)
}

type providerIDLookup interface {
	GetByContentID(ctx context.Context, contentID string) ([]*models.MediaItemProviderID, error)
	FindContentIDByProviderIDs(ctx context.Context, providerIDs map[string]string, itemType, excludeContentID string) (string, error)
}

// StableIdentityResolver translates volatile local content IDs to provider-ID
// based identities that survive rescans or catalog rebinding.
type StableIdentityResolver struct {
	items       itemLookup
	episodes    episodeLookup
	providerIDs providerIDLookup
}

func NewStableIdentityResolver(items itemLookup, episodes episodeLookup, providerIDs providerIDLookup) *StableIdentityResolver {
	return &StableIdentityResolver{
		items:       items,
		episodes:    episodes,
		providerIDs: providerIDs,
	}
}

func (r *StableIdentityResolver) ResolveHistoryIdentity(ctx context.Context, mediaItemID string) userstore.WatchIdentity {
	if r == nil || strings.TrimSpace(mediaItemID) == "" || r.providerIDs == nil {
		return userstore.WatchIdentity{}
	}

	if r.episodes != nil {
		episode, err := r.episodes.GetByID(ctx, mediaItemID)
		if err == nil && episode != nil {
			seriesIDs := providerIDMap(r.loadProviderIDs(ctx, episode.SeriesID))
			if len(seriesIDs) == 0 {
				return userstore.WatchIdentity{}
			}
			seasonNumber := episode.SeasonNumber
			episodeNumber := episode.EpisodeNumber
			return userstore.WatchIdentity{
				StableType:        "episode",
				ProviderIDs:       episodeProviderIDs(episode),
				SeriesProviderIDs: seriesIDs,
				Season:            &seasonNumber,
				Episode:           &episodeNumber,
			}
		}
	}

	if r.items == nil {
		return userstore.WatchIdentity{}
	}
	item, err := r.items.GetByID(ctx, mediaItemID)
	if err != nil || item == nil || item.Type != "movie" {
		return userstore.WatchIdentity{}
	}

	itemIDs := providerIDMap(r.loadProviderIDs(ctx, mediaItemID))
	if len(itemIDs) == 0 {
		return userstore.WatchIdentity{}
	}
	return userstore.WatchIdentity{
		StableType:  "movie",
		ProviderIDs: itemIDs,
	}
}

func (r *StableIdentityResolver) ResolveMovieContentID(ctx context.Context, providerIDs map[string]string) (string, error) {
	if r == nil || r.providerIDs == nil {
		return "", nil
	}
	return r.providerIDs.FindContentIDByProviderIDs(ctx, providerIDs, "movie", "")
}

func (r *StableIdentityResolver) ResolveEpisodeContentID(
	ctx context.Context,
	seriesProviderIDs map[string]string,
	seasonNumber, episodeNumber int,
) (string, error) {
	if r == nil || r.providerIDs == nil || r.episodes == nil || seasonNumber < 0 || episodeNumber <= 0 {
		return "", nil
	}
	seriesID, err := r.providerIDs.FindContentIDByProviderIDs(ctx, seriesProviderIDs, "series", "")
	if err != nil || strings.TrimSpace(seriesID) == "" {
		return "", err
	}
	episode, err := r.episodes.GetBySeriesAndNumber(ctx, seriesID, seasonNumber, episodeNumber)
	if err != nil {
		if errors.Is(err, catalog.ErrEpisodeNotFound) {
			return "", nil
		}
		return "", err
	}
	if episode == nil {
		return "", nil
	}
	return episode.ContentID, nil
}

func (r *StableIdentityResolver) loadProviderIDs(ctx context.Context, contentID string) []*models.MediaItemProviderID {
	ids, err := r.providerIDs.GetByContentID(ctx, contentID)
	if err != nil {
		return nil
	}
	return ids
}

// episodeProviderIDs extracts the episode's own external IDs (populated by
// metadata enrichment on the episodes table). When present, exports can address
// the play by a real episode ID via the flat Trakt episodes[] form; when absent,
// the caller still carries SeriesProviderIDs + season/episode for the nested form.
func episodeProviderIDs(episode *models.Episode) map[string]string {
	ids := map[string]string{}
	if v := strings.TrimSpace(episode.ImdbID); v != "" {
		ids["imdb"] = v
	}
	if v := strings.TrimSpace(episode.TmdbID); v != "" {
		ids["tmdb"] = v
	}
	if v := strings.TrimSpace(episode.TvdbID); v != "" {
		ids["tvdb"] = v
	}
	return ids
}

func providerIDMap(rows []*models.MediaItemProviderID) map[string]string {
	if len(rows) == 0 {
		return map[string]string{}
	}
	result := make(map[string]string, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		provider := strings.TrimSpace(row.Provider)
		providerID := strings.TrimSpace(row.ProviderID)
		if provider == "" || providerID == "" {
			continue
		}
		result[provider] = providerID
	}
	return result
}
