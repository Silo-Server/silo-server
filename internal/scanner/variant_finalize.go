package scanner

import (
	"context"
	"fmt"
	"sort"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
)

// FinalizeVariantsByPathPrefix recomputes edition/presentation metadata after
// canonical root ownership and item linkage are stable for the scanned scope.
//
// Every present file in the scope is re-parsed, so parser and folder changes
// reach the whole scope. A file's part total is the only value that depends on
// other files of the same owner, so files outside the scope are loaded only as
// variantSiblingOwners and variantOwnersToWiden choose. Other out-of-scope
// files are finalized when their own scope runs.
func (s *Scanner) FinalizeVariantsByPathPrefix(
	ctx context.Context,
	folder *models.MediaFolder,
	pathPrefix string,
) error {
	if s == nil || s.fileRepo == nil || folder == nil {
		return nil
	}

	scopedFiles, err := s.fileRepo.ListVariantFilesByPathPrefix(ctx, folder.ID, pathPrefix)
	if err != nil {
		return fmt.Errorf("loading files for variant finalization: %w", err)
	}
	if len(scopedFiles) == 0 {
		return nil
	}

	candidates, err := s.variantFinalizationCandidates(ctx, folder, scopedFiles)
	if err != nil {
		return err
	}
	updates := variantMetadataUpdates(candidates)
	if len(updates) == 0 {
		return nil
	}
	if _, err := s.fileRepo.UpdateVariantMetadata(ctx, folder.ID, updates); err != nil {
		return fmt.Errorf("finalizing variant metadata for %d files under %s: %w", len(updates), pathPrefix, err)
	}
	return nil
}

// variantCandidate is one file variant finalization considers, with its owner
// and the hints parsed from its path once.
type variantCandidate struct {
	file     VariantFile
	ownerKey string
	hints    *naming.VariantHints
}

func newVariantCandidates(files []VariantFile, folder *models.MediaFolder) []variantCandidate {
	candidates := make([]variantCandidate, 0, len(files))
	for i := range files {
		hints := variantHintsForFile(&files[i], folder)
		if hints == nil {
			hints = &naming.VariantHints{}
		}
		candidates = append(candidates, variantCandidate{
			file:     files[i],
			ownerKey: stableOwnerKey(files[i].EpisodeID, files[i].ContentID),
			hints:    hints,
		})
	}
	return candidates
}

func (s *Scanner) variantFinalizationCandidates(
	ctx context.Context,
	folder *models.MediaFolder,
	scopedFiles []VariantFile,
) ([]variantCandidate, error) {
	candidates := newVariantCandidates(scopedFiles, folder)
	loaded := make(map[int]struct{}, len(candidates))
	for _, candidate := range candidates {
		loaded[candidate.file.ID] = struct{}{}
	}

	episodes, contents := variantSiblingOwners(candidates)
	found, err := s.loadVariantCandidates(ctx, folder, episodes, contents, loaded)
	if err != nil {
		return nil, err
	}
	candidates = append(candidates, found...)

	episodes, contents = variantOwnersToWiden(found, episodes, contents)
	more, err := s.loadVariantCandidates(ctx, folder, episodes, contents, loaded)
	if err != nil {
		return nil, err
	}
	return append(candidates, more...), nil
}

// loadVariantCandidates reads the files the owners name and returns those not
// loaded yet, marking them loaded.
func (s *Scanner) loadVariantCandidates(
	ctx context.Context,
	folder *models.MediaFolder,
	episodes, contents VariantOwners,
	loaded map[int]struct{},
) ([]variantCandidate, error) {
	if len(episodes.IDs) == 0 && len(contents.IDs) == 0 {
		return nil, nil
	}
	related, err := s.fileRepo.ListVariantFilesByOwners(ctx, folder.ID, episodes, contents)
	if err != nil {
		return nil, fmt.Errorf("loading related files for variant finalization: %w", err)
	}
	var files []VariantFile
	for _, file := range related {
		if _, ok := loaded[file.ID]; !ok {
			loaded[file.ID] = struct{}{}
			files = append(files, file)
		}
	}
	return newVariantCandidates(files, folder), nil
}

// variantSiblingOwners returns the owners of the in-scope files, episode
// owners apart from content owners without an episode link. Every file of an
// owner is read when one of its in-scope files is in a multipart group, under
// its new hints, its stored values or a stored part total. For the other owners
// only their files stored as parts are read: when a part leaves its group, for
// example by a rename, the parts left behind need a new total even though
// nothing in the scope is a part any more.
func variantSiblingOwners(candidates []variantCandidate) (episodes, contents VariantOwners) {
	episodeIDs := make(map[string]struct{})
	contentIDs := make(map[string]struct{})
	episodes.Whole = make(map[string]struct{})
	contents.Whole = make(map[string]struct{})
	for _, candidate := range candidates {
		if candidate.ownerKey == "" {
			continue
		}
		ids, whole, id := episodeIDs, episodes.Whole, candidate.file.EpisodeID
		if id == "" {
			ids, whole, id = contentIDs, contents.Whole, candidate.file.ContentID
		}
		ids[id] = struct{}{}
		if variantCandidateInPartGroup(candidate) {
			whole[id] = struct{}{}
		}
	}
	episodes.IDs = sortedVariantOwnerIDs(episodeIDs)
	contents.IDs = sortedVariantOwnerIDs(contentIDs)
	return episodes, contents
}

// variantOwnersToWiden returns the owners that were read only for their
// stored parts but turned out to have a file in a multipart group, to be read
// in full. The rest of that group can be stored without part columns, as after
// a parser change, and the group's total needs every part.
func variantOwnersToWiden(found []variantCandidate, episodes, contents VariantOwners) (widerEpisodes, widerContents VariantOwners) {
	episodeIDs := make(map[string]struct{})
	contentIDs := make(map[string]struct{})
	for _, candidate := range found {
		if candidate.ownerKey == "" || !variantCandidateInPartGroup(candidate) {
			continue
		}
		ids, whole, id := episodeIDs, episodes.Whole, candidate.file.EpisodeID
		if id == "" {
			ids, whole, id = contentIDs, contents.Whole, candidate.file.ContentID
		}
		if _, ok := whole[id]; !ok {
			ids[id] = struct{}{}
		}
	}
	widerEpisodes = VariantOwners{IDs: sortedVariantOwnerIDs(episodeIDs), Whole: episodeIDs}
	widerContents = VariantOwners{IDs: sortedVariantOwnerIDs(contentIDs), Whole: contentIDs}
	return widerEpisodes, widerContents
}

func variantCandidateInPartGroup(candidate variantCandidate) bool {
	if _, ok := variantPartGroupKey(candidate.ownerKey, candidate.hints); ok {
		return true
	}
	stored := &naming.VariantHints{
		EditionKey:            candidate.file.EditionKey,
		PresentationKind:      candidate.file.PresentationKind,
		PresentationGroupKey:  candidate.file.PresentationGroupKey,
		PresentationPartIndex: candidate.file.PresentationPartIndex,
	}
	if _, ok := variantPartGroupKey(candidate.ownerKey, stored); ok {
		return true
	}
	return candidate.file.PresentationPartTotal > 0
}

// variantMetadataUpdates returns, in id order, the files whose stored variant
// metadata differs from their parsed hints and part totals, carrying the new
// values. Files without an owner are left as they are.
func variantMetadataUpdates(candidates []variantCandidate) []VariantFile {
	partTotals := variantPartTotals(candidates)
	var updates []VariantFile
	for _, candidate := range candidates {
		if candidate.ownerKey == "" {
			continue
		}
		hints := candidate.hints
		partTotal := 0
		if groupKey, ok := variantPartGroupKey(candidate.ownerKey, hints); ok {
			partTotal = partTotals[groupKey]
		}
		if !variantMetadataChanged(&candidate.file, hints, partTotal) {
			continue
		}

		updated := candidate.file
		updated.EditionRaw = hints.EditionRaw
		updated.EditionKey = hints.EditionKey
		updated.EditionConfidence = hints.EditionConfidence
		updated.EditionSource = hints.EditionSource
		updated.PresentationKind = hints.PresentationKind
		updated.PresentationGroupKey = hints.PresentationGroupKey
		updated.PresentationPartIndex = hints.PresentationPartIndex
		updated.PresentationPartTotal = partTotal
		updated.MultiEpisodeStart = hints.MultiEpisodeStart
		updated.MultiEpisodeEnd = hints.MultiEpisodeEnd
		updates = append(updates, updated)
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].ID < updates[j].ID })
	return updates
}

// variantPartTotals returns the part count for each group of files that are
// parts of one split movie or episode. A group needs at least two distinct part
// numbers: a lone "Part 2" file is a whole episode or movie with the part in
// its title, such as "Resurrection Ship, Part 2" or "Mockingjay - Part 2".
func variantPartTotals(candidates []variantCandidate) map[string]int {
	partsByGroup := make(map[string]map[int]struct{})
	for _, candidate := range candidates {
		groupKey, ok := variantPartGroupKey(candidate.ownerKey, candidate.hints)
		if !ok {
			continue
		}
		if partsByGroup[groupKey] == nil {
			partsByGroup[groupKey] = make(map[int]struct{})
		}
		partsByGroup[groupKey][candidate.hints.PresentationPartIndex] = struct{}{}
	}

	totals := make(map[string]int, len(partsByGroup))
	for groupKey, parts := range partsByGroup {
		if len(parts) < 2 {
			continue
		}
		for partIndex := range parts {
			totals[groupKey] = max(totals[groupKey], partIndex)
		}
	}
	return totals
}

func variantPartGroupKey(ownerKey string, hints *naming.VariantHints) (string, bool) {
	if (hints.PresentationKind != "multipart_movie" && hints.PresentationKind != "split_episode") ||
		hints.PresentationGroupKey == "" || hints.PresentationPartIndex <= 0 {
		return "", false
	}
	return ownerKey + "|" + hints.EditionKey + "|" + hints.PresentationKind + "|" + hints.PresentationGroupKey, true
}

// editionSourceImport marks edition and presentation fields set by an import
// rather than parsed from the filename; scans keep them as they are.
const editionSourceImport = "import"

func variantHintsForFile(file *VariantFile, folder *models.MediaFolder) *naming.VariantHints {
	if file.EditionSource == editionSourceImport && file.EditionKey != "" {
		return &naming.VariantHints{
			EditionRaw:            file.EditionRaw,
			EditionKey:            file.EditionKey,
			EditionSource:         file.EditionSource,
			EditionConfidence:     file.EditionConfidence,
			PresentationKind:      file.PresentationKind,
			PresentationGroupKey:  file.PresentationGroupKey,
			PresentationPartIndex: file.PresentationPartIndex,
			MultiEpisodeStart:     file.MultiEpisodeStart,
			MultiEpisodeEnd:       file.MultiEpisodeEnd,
		}
	}
	return naming.ParseVariantHints(file.FilePath, folder.Type, folder.Paths...)
}

func sortedVariantOwnerIDs(ids map[string]struct{}) []string {
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func stableOwnerKey(episodeID, contentID string) string {
	switch {
	case episodeID != "":
		return "episode:" + episodeID
	case contentID != "":
		return "content:" + contentID
	default:
		return ""
	}
}

func variantMetadataChanged(file *VariantFile, hints *naming.VariantHints, partTotal int) bool {
	if file == nil {
		return false
	}
	if hints == nil {
		hints = &naming.VariantHints{}
	}
	if file.EditionRaw != hints.EditionRaw ||
		file.EditionKey != hints.EditionKey ||
		file.EditionSource != hints.EditionSource ||
		file.PresentationKind != hints.PresentationKind ||
		file.PresentationGroupKey != hints.PresentationGroupKey ||
		file.PresentationPartIndex != hints.PresentationPartIndex ||
		file.PresentationPartTotal != partTotal ||
		file.MultiEpisodeStart != hints.MultiEpisodeStart ||
		file.MultiEpisodeEnd != hints.MultiEpisodeEnd {
		return true
	}
	switch {
	case file.EditionConfidence == nil && hints.EditionConfidence == nil:
		return false
	case file.EditionConfidence == nil || hints.EditionConfidence == nil:
		return true
	default:
		return *file.EditionConfidence != *hints.EditionConfidence
	}
}
