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
			episodeIDs := episodeProviderIDs(episode)
			seriesIDs := providerIDMap(r.loadProviderIDs(ctx, episode.SeriesID))
			// Only require series IDs when the episode has no IDs of its own:
			// an episode with its own IMDb/TMDB/TVDB ID is addressable on the
			// flat episodes[].ids path without needing the nested show fallback.
			if len(episodeIDs) == 0 && len(seriesIDs) == 0 {
				return userstore.WatchIdentity{}
			}
			seasonNumber := episode.SeasonNumber
			episodeNumber := episode.EpisodeNumber
			return userstore.WatchIdentity{
				StableType:        "episode",
				ProviderIDs:       episodeIDs,
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

// batchEpisodeLookup is an optional capability on the episode lookup: fetch
// many episodes in one query. catalog.EpisodeRepository implements it.
type batchEpisodeLookup interface {
	GetByIDs(ctx context.Context, contentIDs []string) ([]*models.Episode, error)
}

// batchProviderIDLookup is the same idea for provider IDs, letting a series
// mark resolve every distinct series in one query instead of one per episode.
type batchProviderIDLookup interface {
	GetByContentIDs(ctx context.Context, contentIDs []string) (map[string][]*models.MediaItemProviderID, error)
}

// ResolveHistoryIdentities resolves stable identities for many media items at
// once. Marking a series watched previously resolved each episode on its own,
// costing an episode lookup plus a series provider-ID lookup per episode; this
// collapses that to one episode query plus one provider-ID query per distinct
// series. Items that resolve to nothing are absent from the map, matching
// ResolveHistoryIdentity's zero-value return.
func (r *StableIdentityResolver) ResolveHistoryIdentities(ctx context.Context, mediaItemIDs []string) map[string]userstore.WatchIdentity {
	result := make(map[string]userstore.WatchIdentity, len(mediaItemIDs))
	if r == nil || r.providerIDs == nil || len(mediaItemIDs) == 0 {
		return result
	}

	ids := make([]string, 0, len(mediaItemIDs))
	seen := make(map[string]struct{}, len(mediaItemIDs))
	for _, mediaItemID := range mediaItemIDs {
		mediaItemID = strings.TrimSpace(mediaItemID)
		if mediaItemID == "" {
			continue
		}
		if _, ok := seen[mediaItemID]; ok {
			continue
		}
		seen[mediaItemID] = struct{}{}
		ids = append(ids, mediaItemID)
	}
	if len(ids) == 0 {
		return result
	}

	episodes := r.loadEpisodes(ctx, ids)

	// One provider-ID lookup per distinct series covers every episode of it.
	seriesIDs := make([]string, 0, len(episodes))
	seenSeries := make(map[string]struct{}, len(episodes))
	for _, episode := range episodes {
		seriesID := strings.TrimSpace(episode.SeriesID)
		if seriesID == "" {
			continue
		}
		if _, ok := seenSeries[seriesID]; ok {
			continue
		}
		seenSeries[seriesID] = struct{}{}
		seriesIDs = append(seriesIDs, seriesID)
	}

	// Anything that is not an episode may still be a movie; resolve those
	// provider IDs in the same batch.
	movieCandidates := make([]string, 0, len(ids))
	for _, mediaItemID := range ids {
		if _, ok := episodes[mediaItemID]; !ok {
			movieCandidates = append(movieCandidates, mediaItemID)
		}
	}

	providerIDsByContent := r.loadProviderIDsBatch(ctx, append(append([]string{}, seriesIDs...), movieCandidates...))

	for _, mediaItemID := range ids {
		if episode, ok := episodes[mediaItemID]; ok {
			episodeIDs := episodeProviderIDs(episode)
			seriesProviderIDs := providerIDMap(providerIDsByContent[episode.SeriesID])
			// Mirrors ResolveHistoryIdentity: an episode carrying its own IDs
			// is addressable without the nested show fallback.
			if len(episodeIDs) == 0 && len(seriesProviderIDs) == 0 {
				continue
			}
			seasonNumber := episode.SeasonNumber
			episodeNumber := episode.EpisodeNumber
			result[mediaItemID] = userstore.WatchIdentity{
				StableType:        "episode",
				ProviderIDs:       episodeIDs,
				SeriesProviderIDs: seriesProviderIDs,
				Season:            &seasonNumber,
				Episode:           &episodeNumber,
			}
			continue
		}

		if r.items == nil {
			continue
		}
		item, err := r.items.GetByID(ctx, mediaItemID)
		if err != nil || item == nil || item.Type != "movie" {
			continue
		}
		itemIDs := providerIDMap(providerIDsByContent[mediaItemID])
		if len(itemIDs) == 0 {
			continue
		}
		result[mediaItemID] = userstore.WatchIdentity{
			StableType:  "movie",
			ProviderIDs: itemIDs,
		}
	}

	return result
}

// loadEpisodes returns the subset of ids that are episodes, batching when the
// lookup supports it and falling back to per-ID reads otherwise.
func (r *StableIdentityResolver) loadEpisodes(ctx context.Context, ids []string) map[string]*models.Episode {
	episodes := make(map[string]*models.Episode, len(ids))
	if r.episodes == nil {
		return episodes
	}
	if batch, ok := r.episodes.(batchEpisodeLookup); ok {
		found, err := batch.GetByIDs(ctx, ids)
		if err == nil {
			for _, episode := range found {
				if episode != nil {
					episodes[episode.ContentID] = episode
				}
			}
			return episodes
		}
	}
	for _, mediaItemID := range ids {
		episode, err := r.episodes.GetByID(ctx, mediaItemID)
		if err == nil && episode != nil {
			episodes[mediaItemID] = episode
		}
	}
	return episodes
}

// loadProviderIDsBatch groups provider IDs by content ID, batching when the
// lookup supports it and falling back to per-ID reads otherwise.
func (r *StableIdentityResolver) loadProviderIDsBatch(ctx context.Context, contentIDs []string) map[string][]*models.MediaItemProviderID {
	result := make(map[string][]*models.MediaItemProviderID, len(contentIDs))
	if len(contentIDs) == 0 {
		return result
	}
	if batch, ok := r.providerIDs.(batchProviderIDLookup); ok {
		found, err := batch.GetByContentIDs(ctx, contentIDs)
		if err == nil {
			return found
		}
	}
	for _, contentID := range contentIDs {
		if _, ok := result[contentID]; ok {
			continue
		}
		result[contentID] = r.loadProviderIDs(ctx, contentID)
	}
	return result
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
