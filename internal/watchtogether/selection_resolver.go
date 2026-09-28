package watchtogether

import (
	"context"
	"strings"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type catalogWatchDetailLookup interface {
	GetWatchDetail(ctx context.Context, contentID string, filter catalog.AccessFilter) (*catalog.WatchDetail, error)
}

type CatalogSelectionResolver struct {
	details catalogWatchDetailLookup
}

func NewCatalogSelectionResolver(details catalogWatchDetailLookup) *CatalogSelectionResolver {
	return &CatalogSelectionResolver{details: details}
}

func (r *CatalogSelectionResolver) ResolveSelection(
	ctx context.Context,
	userID int,
	profileID string,
	input SelectItemInput,
) (*ResolvedSelection, error) {
	if r == nil || r.details == nil {
		return nil, ErrInvalidSelection
	}

	detail, err := r.watchDetail(ctx, userID, profileID, input)
	if err != nil {
		return nil, err
	}
	if detail == nil || (detail.Type != suggestionMovieType && detail.Type != suggestionEpisodeType) || len(detail.Versions) == 0 {
		return nil, ErrInvalidSelection
	}

	resolved := &ResolvedSelection{
		ContentID: detail.ContentID,
		LibraryID: input.LibraryID,
	}
	if input.FileID != nil {
		for _, version := range detail.Versions {
			if version.FileID == *input.FileID {
				resolved.FileID = input.FileID
				return resolved, nil
			}
		}
		return nil, ErrInvalidSelection
	}

	resolved.FileID = new(preferredRoomVersion(detail.Versions).FileID)
	return resolved, nil
}

// A room has one source timeline. Use the catalog's quality ordering within
// its preferred edition and presentation part, without a room-specific ceiling.
// Each viewer's playback planner applies device capabilities and server policy
// to that source, including 4K transcoding and tone mapping when available.
func preferredRoomVersion(versions []catalog.FileVersion) catalog.FileVersion {
	best := versions[0]
	for _, candidate := range versions[1:] {
		if candidate.EditionKey != best.EditionKey || candidate.PresentationKind != best.PresentationKind ||
			candidate.PresentationGroupKey != best.PresentationGroupKey || candidate.PresentationPartIndex != best.PresentationPartIndex {
			continue
		}
		if roomVersionBetter(candidate, best) {
			best = candidate
		}
	}
	return best
}

func (r *CatalogSelectionResolver) watchDetail(ctx context.Context, userID int, profileID string, input SelectItemInput) (*catalog.WatchDetail, error) {
	filter := catalog.AccessFilter{
		UserID:         userID,
		ProfileID:      profileID,
		SelectedFileID: 0,
	}
	if scope, ok := access.GetScope(ctx); ok {
		filter.AllowedLibraryIDs = scope.AllowedLibraryIDs
		filter.DisabledLibraryIDs = scope.DisabledLibraryIDs
		filter.MaturityLimits = scope.MaturityLimits
		filter.MaxPlaybackQuality = scope.MaxPlaybackQuality
	}
	if input.LibraryID != nil && *input.LibraryID > 0 {
		filter.PresentationLibraryID = input.LibraryID
	}
	if input.FileID != nil && *input.FileID > 0 {
		filter.SelectedFileID = *input.FileID
	}

	return r.details.GetWatchDetail(ctx, strings.TrimSpace(input.ContentID), filter)
}

func roomVersionBetter(a, b catalog.FileVersion) bool {
	if quality := access.CompareQuality(a.Resolution, b.Resolution); quality != 0 {
		return quality > 0
	}
	if a.HDR != b.HDR {
		return a.HDR
	}
	if a.FileSize != b.FileSize {
		return a.FileSize > b.FileSize
	}
	return a.FileID < b.FileID
}

func (r *CatalogSelectionResolver) ResolveSourceFallback(ctx context.Context, userID int, profileID string, input SelectItemInput, reason string) (*ResolvedSelection, error) {
	if r == nil || r.details == nil || input.FileID == nil {
		return nil, ErrInvalidSelection
	}
	detail, err := r.watchDetail(ctx, userID, profileID, input)
	if err != nil {
		return nil, err
	}
	if detail == nil {
		return nil, ErrInvalidSelection
	}
	var current *catalog.FileVersion
	for i := range detail.Versions {
		if detail.Versions[i].FileID == *input.FileID {
			current = &detail.Versions[i]
			break
		}
	}
	if current == nil {
		return nil, ErrInvalidSelection
	}
	var best *catalog.FileVersion
	for i := range detail.Versions {
		candidate := &detail.Versions[i]
		// Strictly descend the catalog order so different viewers cannot
		// bounce the room between previously refused versions.
		if !roomVersionBetter(*current, *candidate) || candidate.EditionKey != current.EditionKey ||
			candidate.PresentationKind != current.PresentationKind || candidate.PresentationGroupKey != current.PresentationGroupKey ||
			candidate.PresentationPartIndex != current.PresentationPartIndex {
			continue
		}
		if reason == fallbackReasonLowerResolution && access.CompareQuality(candidate.Resolution, current.Resolution) >= 0 {
			continue
		}
		if reason == playback.TerminalHDRTranscodeUnsupportedV3 && candidate.HDR {
			continue
		}
		if best == nil || roomVersionBetter(*candidate, *best) {
			best = candidate
		}
	}
	if best == nil {
		return nil, ErrSourceFallbackUnavailable
	}
	return &ResolvedSelection{ContentID: detail.ContentID, FileID: new(best.FileID), LibraryID: input.LibraryID}, nil
}
