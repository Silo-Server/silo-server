package scanner

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
)

// FinalizeVariantsByPathPrefix recomputes edition/presentation metadata after
// canonical root ownership and item linkage are stable for the scanned scope.
func (s *Scanner) FinalizeVariantsByPathPrefix(
	ctx context.Context,
	folder *models.MediaFolder,
	pathPrefix string,
) error {
	if s == nil || s.fileRepo == nil || folder == nil {
		return nil
	}

	scopedFiles, err := s.fileRepo.GetByFolderAndPathPrefix(ctx, folder.ID, pathPrefix)
	if err != nil {
		return fmt.Errorf("loading files for variant finalization: %w", err)
	}
	if len(scopedFiles) == 0 {
		return nil
	}

	files, err := s.variantFinalizationFilesForScope(ctx, folder.ID, scopedFiles)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}

	partTotals := variantPartTotals(files, folder)
	for _, file := range files {
		if file == nil || file.MissingSince != nil {
			continue
		}
		ownerKey := stableOwnerKey(file)
		if ownerKey == "" {
			continue
		}

		hints := variantHintsForFile(file, folder)
		if hints == nil {
			hints = &naming.VariantHints{}
		}
		partTotal := 0
		if groupKey, ok := variantPartGroupKey(ownerKey, hints); ok {
			partTotal = partTotals[groupKey]
		}

		if !variantMetadataChanged(file, hints, partTotal) {
			continue
		}

		updated := *file
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
		if _, err := s.fileRepo.Upsert(ctx, updated); err != nil {
			return fmt.Errorf("finalizing variant metadata for %s: %w", file.FilePath, err)
		}
	}

	return nil
}

// variantPartTotals returns the part count for each group of files that are
// parts of one split movie or episode. A group needs at least two distinct part
// numbers: a lone "Part 2" file is a whole episode or movie with the part in
// its title, such as "Resurrection Ship, Part 2" or "Mockingjay - Part 2".
func variantPartTotals(files []*models.MediaFile, folder *models.MediaFolder) map[string]int {
	partsByGroup := make(map[string]map[int]struct{})
	for _, file := range files {
		if file == nil || file.MissingSince != nil {
			continue
		}
		ownerKey := stableOwnerKey(file)
		if ownerKey == "" {
			continue
		}
		hints := variantHintsForFile(file, folder)
		if hints == nil {
			continue
		}
		groupKey, ok := variantPartGroupKey(ownerKey, hints)
		if !ok {
			continue
		}
		if partsByGroup[groupKey] == nil {
			partsByGroup[groupKey] = make(map[int]struct{})
		}
		partsByGroup[groupKey][hints.PresentationPartIndex] = struct{}{}
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

func variantHintsForFile(file *models.MediaFile, folder *models.MediaFolder) *naming.VariantHints {
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

func (s *Scanner) variantFinalizationFilesForScope(
	ctx context.Context,
	folderID int,
	scopedFiles []*models.MediaFile,
) ([]*models.MediaFile, error) {
	if len(scopedFiles) == 0 {
		return nil, nil
	}

	filesByID := make(map[int]*models.MediaFile, len(scopedFiles))
	for _, file := range scopedFiles {
		if file == nil || file.MissingSince != nil {
			continue
		}
		if file.EpisodeID == "" && file.ContentID == "" {
			if file.MediaFolderID == folderID {
				filesByID[file.ID] = file
			}
			continue
		}

		var related []*models.MediaFile
		var err error
		switch {
		case file.EpisodeID != "":
			related, err = s.fileRepo.GetByEpisodeID(ctx, file.EpisodeID)
		case file.ContentID != "":
			related, err = s.fileRepo.GetByContentID(ctx, file.ContentID)
		}
		if err != nil {
			return nil, fmt.Errorf("loading related files for variant finalization: %w", err)
		}
		for _, relatedFile := range related {
			if relatedFile == nil || relatedFile.MissingSince != nil || relatedFile.MediaFolderID != folderID {
				continue
			}
			filesByID[relatedFile.ID] = relatedFile
		}
	}

	files := make([]*models.MediaFile, 0, len(filesByID))
	for _, file := range filesByID {
		files = append(files, file)
	}
	return files, nil
}

func stableOwnerKey(file *models.MediaFile) string {
	switch {
	case file == nil:
		return ""
	case file.EpisodeID != "":
		return "episode:" + file.EpisodeID
	case file.ContentID != "":
		return "content:" + file.ContentID
	default:
		return ""
	}
}

func variantMetadataChanged(file *models.MediaFile, hints *naming.VariantHints, partTotal int) bool {
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
