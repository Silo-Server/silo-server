package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
)

// resolveMovieTitleAmbiguity requires independent, normally accepted searches
// for every dated title in the group. Existing links never supply search IDs.
//
//nolint:goconst // These are catalog states and provider keys, not queue states or test constants.
func (s *MetadataService) resolveMovieTitleAmbiguity(ctx context.Context, file *models.MediaFile, skeleton *skeletonResult, libraryRoots ...string) (*naming.FolderIDHints, error) {
	if s == nil || file == nil || skeleton == nil || skeleton.ItemStatus != "ambiguous" || skeleton.Type != "movie" ||
		s.scannedGroupRepo == nil || s.fileRepo == nil || s.itemRepo == nil || skeleton.ContentGroupKey == "" {
		return nil, nil
	}
	if s.groupOverrideRepo != nil {
		override, err := s.groupOverrideRepo.Get(ctx, file.MediaFolderID, skeleton.GroupKeyVersion, skeleton.ContentGroupKey)
		if err != nil {
			return nil, err
		}
		if override != nil {
			return nil, nil
		}
	}
	group, err := s.scannedGroupRepo.Get(ctx, file.MediaFolderID, skeleton.GroupKeyVersion, skeleton.ContentGroupKey)
	if err != nil {
		return nil, fmt.Errorf("loading ambiguous movie group: %w", err)
	}
	if group == nil || group.State != "ambiguous" || group.InferredType != "movie" || group.OverrideSource == manualIdentityOverrideSource ||
		group.TmdbID != "" || group.ImdbID != "" || group.TvdbID != "" ||
		scannedGroupIdentityChanged(group, file, nil, libraryRoots...) {
		return nil, nil
	}
	files, err := s.fileRepo.ListByGroupKey(ctx, file.MediaFolderID, skeleton.GroupKeyVersion, skeleton.ContentGroupKey)
	if err != nil {
		return nil, fmt.Errorf("loading ambiguous movie group files: %w", err)
	}
	if !slices.ContainsFunc(files, func(member *models.MediaFile) bool {
		return member != nil && member.ID == file.ID && member.FilePath == file.FilePath
	}) {
		return nil, nil
	}
	hypotheses := movieGroupTitleHypotheses(files, group, libraryRoots)
	if len(hypotheses) < 2 {
		return nil, nil
	}
	chain, err := s.resolveChainCached(ctx, file.MediaFolderID, "movie")
	if err != nil {
		return nil, fmt.Errorf("resolving ambiguous movie providers: %w", err)
	}
	ids, err := confirmMovieTitleHypotheses(ctx, hypotheses, chain, s.resolveFolderLanguage(ctx, file.MediaFolderID))
	if err != nil || ids == nil {
		return nil, err
	}
	// A provider proof may resolve aliases, but must never overrule a conflicting
	// existing file link or confirmed ownership claim during skeleton dedup.
	contentIDs := map[string]bool{}
	for _, member := range files {
		contentIDs[member.ContentID] = true
	}
	if s.groupClaimRepo != nil {
		claim, err := s.groupClaimRepo.Get(ctx, file.MediaFolderID, skeleton.GroupKeyVersion, skeleton.ContentGroupKey)
		if err != nil {
			return nil, err
		}
		if claim != nil {
			contentIDs[claim.ContentID] = true
		}
	}
	if s.rootClaimRepo != nil {
		roots := map[string]bool{skeleton.RootPath: true}
		for _, member := range files {
			roots[firstNonEmpty(member.CanonicalRootPath, filepath.Dir(member.FilePath))] = true
		}
		delete(roots, "")
		for root := range roots {
			claim, err := s.rootClaimRepo.Get(ctx, file.MediaFolderID, root)
			if err != nil {
				return nil, err
			}
			if claim != nil {
				contentIDs[claim.ContentID] = true
			}
		}
	}
	delete(contentIDs, "")
	for contentID := range contentIDs {
		item, err := s.itemRepo.GetByID(ctx, contentID)
		if err != nil {
			return nil, fmt.Errorf("loading existing ambiguous movie identity: %w", err)
		}
		if item == nil || item.Type != "movie" {
			return nil, nil
		}
		stored, err := s.loadDurableProviderIDs(ctx, contentID)
		if err != nil {
			return nil, err
		}
		legacy := map[string]string{"tmdb": item.TmdbID, "imdb": item.ImdbID, "tvdb": item.TvdbID}
		if isConfirmedOwnershipStatus(item.Status) && providerIDRichness(stored)+providerIDRichness(legacy) == 0 {
			return nil, nil
		}
		for _, known := range []map[string]string{stored, legacy} {
			matches, conflicts := providerIDMergeEvidence(ids, known)
			if len(conflicts) != 0 || (providerIDRichness(known) > 0 && matches == 0) {
				return nil, nil
			}
		}
	}
	return &naming.FolderIDHints{TmdbID: ids["tmdb"], ImdbID: ids["imdb"], TvdbID: ids["tvdb"]}, nil
}

func movieGroupTitleHypotheses(files []*models.MediaFile, group *models.ScannedMediaGroup, libraryRoots []string) []MatchIdentityHint {
	var hypotheses []MatchIdentityHint
	seen := map[string]bool{}
	add := func(title string, year int) {
		key := matchIdentityKey(title, year)
		if !seen[key] {
			seen[key] = true
			hypotheses = append(hypotheses, MatchIdentityHint{Title: title, Year: year})
		}
	}
	conflict := false
	for _, file := range files {
		if file == nil || file.ExtraID != "" || file.EpisodeID != "" || file.SeasonNumber != 0 || file.EpisodeNumber != 0 ||
			file.BaseType != "movie" || file.GroupKeyVersion != group.GroupKeyVersion || file.ContentGroupKey != group.ContentGroupKey {
			return nil
		}
		path := naming.ResolvePathContext(file.FilePath, "movie", libraryRoots...)
		if path == nil || path.Type != "movie" || path.HasEpisodePattern || path.HasAirDatePattern || path.HasSeasonStructure {
			return nil
		}
		root := firstNonEmpty(file.ObservedRootPath, file.CanonicalRootPath)
		if root == "" || filepath.Clean(root) != filepath.Clean(path.RootPath) {
			return nil
		}
		identity := naming.InferGroupIdentity(file.FilePath, "movie", naming.RootAssignment{
			RootPath: root, LibraryRootPath: path.LibraryRootPath, InferredType: "movie", Title: path.Title, Year: path.Year,
		})
		if identity.ContentGroupKey != group.ContentGroupKey || identity.TmdbID != "" || identity.ImdbID != "" || identity.TvdbID != "" {
			return nil
		}
		var evidence struct {
			ParentTitle   string   `json:"parent_title"`
			ParentYear    int      `json:"parent_year"`
			ParentTrusted bool     `json:"parent_trusted"`
			StemTitle     string   `json:"stem_title"`
			StemYear      int      `json:"stem_year"`
			Reasons       []string `json:"reasons"`
		}
		if json.Unmarshal(identity.EvidenceJSON, &evidence) != nil || !evidence.ParentTrusted || evidence.ParentTitle == "" ||
			evidence.ParentYear == 0 || evidence.ParentYear != group.BaseYear || normalizeTitleForScoring(evidence.ParentTitle) != normalizeTitleForScoring(group.BaseTitle) {
			return nil
		}
		add(evidence.ParentTitle, evidence.ParentYear)
		if identity.State == "ambiguous" {
			if len(evidence.Reasons) != 1 || evidence.Reasons[0] != "unrelated_title" || evidence.StemTitle == "" || evidence.StemYear != evidence.ParentYear {
				return nil
			}
			conflict = true
		}
		if evidence.StemTitle != "" && evidence.StemYear != 0 {
			if evidence.StemYear != evidence.ParentYear {
				return nil
			}
			add(evidence.StemTitle, evidence.StemYear)
		}
	}
	if !conflict {
		return nil
	}
	return hypotheses
}

func confirmMovieTitleHypotheses(ctx context.Context, hypotheses []MatchIdentityHint, chain []Provider, language string) (map[string]string, error) {
	var agreed map[string]string
	seenIDs := map[string]string{}
	var priority []string
	for _, provider := range chain {
		priority = append(priority, provider.Slug())
	}
	for _, hypothesis := range hypotheses {
		var results []SearchResult
		for _, provider := range chain {
			search, ok := provider.(SearchProvider)
			if !ok || nonCorroboratingSources[strings.ToLower(strings.TrimSpace(provider.Slug()))] {
				continue
			}
			found, err := search.Search(ctx, SearchQuery{Title: hypothesis.Title, Year: hypothesis.Year, ContentType: "movie", Language: language})
			if err != nil {
				return nil, fmt.Errorf("confirming movie title with %s: %w", provider.Slug(), err)
			}
			results = append(results, found...)
		}
		candidates := NormalizeCandidatesForLanguage(results, "movie", language)
		winner, ok := selectInitialMatchCandidate(&MatchHints{Title: hypothesis.Title, Year: hypothesis.Year, Type: "movie"}, candidates, priority)
		if !ok || winner == nil || providerIDRichness(winner.ProviderIDs) == 0 || candidateCorroboratingSourceCount(*winner) == 0 || len(winner.ConflictingProviderIDKeys) != 0 {
			return nil, nil
		}
		if agreed == nil {
			agreed = maps.Clone(winner.ProviderIDs)
			maps.Copy(seenIDs, winner.ProviderIDs)
			continue
		}
		matches, _ := providerIDMergeEvidence(agreed, winner.ProviderIDs)
		_, conflicts := providerIDMergeEvidence(seenIDs, winner.ProviderIDs)
		if matches == 0 || len(conflicts) != 0 {
			return nil, nil
		}
		maps.Copy(seenIDs, winner.ProviderIDs)
		// Every hypothesis must retain one common canonical identity, even when
		// individual providers omit some of that work's cross-references.
		for key, value := range agreed {
			if winner.ProviderIDs[key] != value {
				delete(agreed, key)
			}
		}
	}
	return seenIDs, nil
}
