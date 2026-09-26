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

	type groupSummary struct {
		maxPartIndex int
	}

	groupTotals := make(map[string]*groupSummary)
	for _, file := range files {
		if file == nil || file.MissingSince != nil {
			continue
		}
		ownerKey := stableOwnerKey(file)
		if ownerKey == "" {
			continue
		}
		hints := naming.ParseVariantHints(file.FilePath, folder.Type, folder.Paths...)
		if file.EditionSource == "import" && file.EditionKey != "" {
			hints = &naming.VariantHints{
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
		if hints == nil {
			continue
		}
		if (hints.PresentationKind != "multipart_movie" && hints.PresentationKind != "split_episode") ||
			hints.PresentationGroupKey == "" || hints.PresentationPartIndex <= 0 {
			continue
		}
		groupKey := ownerKey + "|" + hints.EditionKey + "|" + hints.PresentationKind + "|" + hints.PresentationGroupKey
		summary := groupTotals[groupKey]
		if summary == nil {
			summary = &groupSummary{}
			groupTotals[groupKey] = summary
		}
		if hints.PresentationPartIndex > summary.maxPartIndex {
			summary.maxPartIndex = hints.PresentationPartIndex
		}
	}

	for _, file := range files {
		if file == nil || file.MissingSince != nil {
			continue
		}
		ownerKey := stableOwnerKey(file)
		if ownerKey == "" {
			continue
		}

		hints := naming.ParseVariantHints(file.FilePath, folder.Type, folder.Paths...)
		if file.EditionSource == "import" && file.EditionKey != "" {
			hints = &naming.VariantHints{
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
		if hints == nil {
			hints = &naming.VariantHints{}
		}
		partTotal := 0
		if (hints.PresentationKind == "multipart_movie" || hints.PresentationKind == "split_episode") &&
			hints.PresentationGroupKey != "" && hints.PresentationPartIndex > 0 {
			groupKey := ownerKey + "|" + hints.EditionKey + "|" + hints.PresentationKind + "|" + hints.PresentationGroupKey
			if summary := groupTotals[groupKey]; summary != nil {
				partTotal = summary.maxPartIndex
			}
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
