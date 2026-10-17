package metadata

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/models"
)

// localReapplyOutcome says what checking an item's local metadata did.
type localReapplyOutcome int

const (
	// localReapplyNone: nothing was applied.
	localReapplyNone localReapplyOutcome = iota
	// localReapplyMerged: the local values were merged into the item.
	localReapplyMerged
	// localReapplyRefreshed: the local files declare a different identity, so
	// a manual refresh re-anchored the item.
	localReapplyRefreshed
	// localReapplyRecorded: nothing was applied, and a first-sight record was
	// stored under the item's lock.
	localReapplyRecorded
)

// reapplyLocalItem applies an item's changed local metadata. It reads the
// local files only and makes no remote provider call, unless the files now
// declare provider IDs they did not declare last time (previousIDs) that
// conflict with the item's, or give an item matched only locally its first
// provider ID: such an edit is a deliberate correction, and a manual refresh
// re-anchors the identity exactly as it does for an admin. The files' IDs are
// compared with the item's identity as a refresh compares them
// (localItemIdentityOutcome). It returns the provider IDs the files declare.
func (s *MetadataService) reapplyLocalItem(
	ctx context.Context,
	folderID int,
	item *models.MediaItem,
	sources localSources,
	req MetadataRequest,
	imageReq ImageRequest,
	previousIDs map[string]string,
) (localReapplyOutcome, map[string]string, error) {
	language := req.Language
	if language == "" {
		var err error
		if language, err = s.localItemLanguage(ctx, item, folderID); err != nil {
			return localReapplyNone, nil, err
		}
		req.Language, imageReq.Language = language, language
	}
	accumulator := &MetadataResult{}
	declared := map[string]string{}
	var images []RemoteImage
	for _, provider := range sources {
		if mp, ok := provider.(MetadataProvider); ok {
			result, err := mp.GetMetadata(ctx, req)
			if err != nil {
				return localReapplyNone, nil, fmt.Errorf("%s: %w", provider.Slug(), err)
			}
			addDeclaredProviderIDs(declared, localResultProviderIDs(ctx, provider, req, result))
			if result != nil && result.HasMetadata {
				_, isIdentityHinter := provider.(IdentityHintProvider)
				foldProviderResult(accumulator, result, language, provider.Slug(), isIdentityHinter, nil)
			}
		}
		if ip, ok := provider.(ImageProvider); ok && s.localArtworkUsable() {
			found, err := ip.GetImages(ctx, imageReq)
			if err != nil {
				return localReapplyNone, nil, fmt.Errorf("%s images: %w", provider.Slug(), err)
			}
			for _, image := range found {
				if isLocalImageSourcePath(image.URL) {
					images = append(images, image)
				}
			}
		}
	}

	if len(declared) > 0 {
		outcome, err := s.localItemIdentityOutcome(ctx, folderID, item, declared, previousIDs)
		if err != nil || outcome != localReapplyMerged {
			return outcome, declared, err
		}
	}

	if err := s.enrichLocalPeople(ctx, item.ContentID, accumulator.People); err != nil {
		return localReapplyNone, nil, err
	}
	if _, err := s.mergeAndPersist(ctx, ProcessRequest{
		ContentID:    item.ContentID,
		FolderID:     formatFolderID(folderID),
		Language:     language,
		Mode:         ModeScheduledRefresh,
		localReapply: true,
	}, accumulator, images, nil, nil, item.Type); err != nil {
		return localReapplyNone, nil, err
	}
	slog.InfoContext(ctx, "metadata: re-applied changed local metadata", "component", "metadata",
		"content_id", item.ContentID, "folder_id", folderID)
	return localReapplyMerged, declared, nil
}

// localItemIdentityOutcome decides what the IDs an item's changed local
// files declare (declared; previousIDs last time) allow, by the rules a
// refresh applies to local files (localIdentityConflict), where IDs recorded
// as dead count on neither side. It returns localReapplyMerged when the files
// may apply, localReapplyRefreshed after running the refresh that re-anchors
// a corrected identity, and localReapplyNone when the files describe another
// title.
func (s *MetadataService) localItemIdentityOutcome(ctx context.Context, folderID int, item *models.MediaItem, declared, previousIDs map[string]string) (localReapplyOutcome, error) {
	durable, err := s.loadDurableProviderIDs(ctx, item.ContentID)
	if err != nil {
		return localReapplyNone, err
	}
	stale, err := s.loadRecordedStaleProviderIDs(ctx, item.ContentID)
	if err != nil {
		return localReapplyNone, err
	}
	anchors := canonicalIdentityProviderIDs(storedItemIdentity(item, durable))
	stored := maps.Clone(anchors)
	suppressProviderIDValues(stored, stale)
	// The re-anchoring refresh is a manual one, which an admin's Identify
	// choice outranks: then the item keeps its identity and nothing local
	// applies.
	if kept, err := s.localIdentityConflict(ctx, ModeManualRefresh, item.ContentID, []localIdentityHint{{ids: declared}}, stored, stale); err != nil || kept {
		return localReapplyNone, err
	}
	live := maps.Clone(declared)
	suppressProviderIDValues(live, stale)
	added := newlyDeclaredIDs(canonicalIdentityProviderIDs(previousIDs), live)
	// A new ID corrects the identity when it contradicts a stored one, live
	// or recorded as dead (a dead anchor's replacement), or when the item has
	// no live ID to keep (matched only locally, or anchored only by dead IDs).
	if len(added) > 0 && (localIDsContradict(added, anchors) || len(stored) == 0 || contentid.IsLocal(item.ContentID)) {
		slog.InfoContext(ctx, "metadata: local files declare a new identity; refreshing to re-anchor the item", "component", "metadata",
			"content_id", item.ContentID, "folder_id", folderID, "previous_ids", previousIDs, "declared_ids", live, "stored_ids", stored)
		if err := s.RefreshTargetForLibrary(ctx, RefreshTargetItem, item.ContentID, folderID); err != nil {
			return localReapplyNone, err
		}
		return localReapplyRefreshed, nil
	}
	if localIDsContradict(live, stored) {
		// The files' identity did not change, so this is not a correction:
		// their text and artwork describe another title, for example IDs that
		// went stale. A manual refresh or Identify resolves it.
		slog.WarnContext(ctx, "metadata: local files name a different identity than the item; not applying them", "component", "metadata",
			"content_id", item.ContentID, "folder_id", folderID, "declared_ids", live, "stored_ids", stored)
		return localReapplyNone, nil
	}
	return localReapplyMerged, nil
}

// newlyDeclaredIDs returns the IDs in declared that previous did not hold
// with the same value: added or changed ones. A removed ID is not among them,
// so deleting one never re-anchors an item.
func newlyDeclaredIDs(previous, declared map[string]string) map[string]string {
	added := map[string]string{}
	for key, value := range declared {
		if previous[key] != value {
			added[key] = value
		}
	}
	return added
}

// enrichLocalPeople fills in what the item's stored credits know about the
// people the local files list (provider IDs, photo, character), so applying
// the files again keeps the enrichment a remote refresh added. It fails when
// the stored credits cannot be loaded: name-only credits would then replace
// enriched ones.
func (s *MetadataService) enrichLocalPeople(ctx context.Context, contentID string, people []models.ItemPerson) error {
	if len(people) == 0 {
		return nil
	}
	reader, ok := s.itemRepo.(interface {
		GetPeople(ctx context.Context, contentID string) ([]models.ItemPerson, error)
	})
	if !ok {
		return nil
	}
	stored, err := reader.GetPeople(ctx, contentID)
	if err != nil {
		return fmt.Errorf("loading stored credits of %s: %w", contentID, err)
	}
	byTmdbID := indexPeopleBy(stored, func(p models.ItemPerson) string { return p.TmdbID })
	byTvdbID := indexPeopleBy(stored, func(p models.ItemPerson) string { return p.TvdbID })
	byImdbID := indexPeopleBy(stored, func(p models.ItemPerson) string { return p.ImdbID })
	byPlexGUID := indexPeopleBy(stored, func(p models.ItemPerson) string { return p.PlexGUID })
	byNameKind := indexPeopleBy(stored, func(p models.ItemPerson) string {
		return strings.ToLower(p.Name) + "|" + strconv.Itoa(int(p.Kind))
	})
	for i := range people {
		if idx, ok := findPersonMatch(people[i], byTmdbID, byTvdbID, byImdbID, byPlexGUID, byNameKind); ok {
			mergePersonFields(&people[i], stored[idx])
		}
	}
	return nil
}

// reapplyLocalSeriesChildren applies the changed local metadata of a series'
// seasons and episodes, read from the local files only. Values the files
// supply replace the stored ones; the series' artwork lock still keeps their
// artwork.
func (s *MetadataService) reapplyLocalSeriesChildren(
	ctx context.Context,
	folderID int,
	series *models.MediaItem,
	childCtx seriesChildLocalContext,
	seasonSources localSources,
	episodeSources localSources,
	changedSeasons []int,
	changedEpisodes []episodeResultKey,
) error {
	language, err := s.localItemLanguage(ctx, series, folderID)
	if err != nil {
		return err
	}
	showFiles := showFilePaths(childCtx.episodeFilePaths)
	seasonResults := make(map[int]*SeasonResult)
	if len(changedSeasons) > 0 {
		seasonDirs := make(map[int][]string, len(changedSeasons))
		for _, number := range changedSeasons {
			seasonDirs[number] = childCtx.seasonDirectoryPaths[number]
		}
		for _, provider := range seasonSources {
			ep, ok := provider.(EpisodeProvider)
			if !ok {
				continue
			}
			seasons, err := ep.GetSeasons(ctx, SeasonsRequest{
				ContentType:          matchContentTypeSeries,
				Language:             language,
				SeriesRootPaths:      childCtx.seriesRootPaths,
				SeasonDirectoryPaths: seasonDirs,
				ShowFilePaths:        showFiles,
			})
			if err != nil {
				return fmt.Errorf("%s seasons: %w", provider.Slug(), err)
			}
			if !s.localArtworkUsable() {
				dropLocalSeasonArtwork(seasons)
			}
			for _, season := range seasons {
				if _, ok := seasonDirs[season.SeasonNumber]; ok {
					accumulateSeasonResults(seasonResults, []SeasonResult{season})
				}
			}
		}
	}

	episodeResults := make(map[episodeResultKey]*EpisodeResult)
	bySeason := make(map[int]map[int][]string)
	for _, key := range changedEpisodes {
		if bySeason[key.seasonNumber] == nil {
			bySeason[key.seasonNumber] = make(map[int][]string)
		}
		bySeason[key.seasonNumber][key.episodeNumber] = childCtx.episodeFilePaths[key.seasonNumber][key.episodeNumber]
	}
	for seasonNumber, episodeFiles := range bySeason {
		for _, provider := range episodeSources {
			ep, ok := provider.(EpisodeProvider)
			if !ok {
				continue
			}
			episodes, err := ep.GetEpisodes(ctx, EpisodesRequest{
				SeasonNumber:     seasonNumber,
				Language:         language,
				SeriesRootPaths:  childCtx.seriesRootPaths,
				EpisodeFilePaths: episodeFiles,
				ShowFilePaths:    showFiles,
			})
			if err != nil {
				return fmt.Errorf("%s episodes: %w", provider.Slug(), err)
			}
			if !s.localArtworkUsable() {
				dropLocalEpisodeArtwork(episodes)
			}
			for _, episode := range episodes {
				if _, ok := episodeFiles[episode.EpisodeNumber]; ok && episode.SeasonNumber == seasonNumber {
					accumulateEpisodeResults(episodeResults, []EpisodeResult{episode})
				}
			}
		}
	}

	seasons := flattenSeasonResults(seasonResults)
	episodes := flattenEpisodeResults(episodeResults)
	if len(seasons) == 0 && len(episodes) == 0 {
		// The files that changed supply nothing now, and removing a sidecar
		// leaves the stored values in place.
		return nil
	}
	providerIDs := canonicalIdentityProviderIDs(storedIdentityProviderIDs(series))
	if s.persistSeasonAndEpisodeRows(ctx, series, providerIDs, language, language, seasons, episodes, MergeReplaceUnlocked) {
		s.syncSeriesEpisodeStateOrDefer(ctx, series.ContentID)
	}
	slog.InfoContext(ctx, "metadata: re-applied changed local season and episode metadata", "component", "metadata",
		"series_id", series.ContentID, "seasons", len(seasons), "episodes", len(episodes))
	return nil
}
