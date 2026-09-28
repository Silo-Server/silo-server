package metadata

import (
	"context"
	"fmt"
	"slices"

	"github.com/Silo-Server/silo-server/internal/models"
)

func (s *MetadataService) configuredNamingRoots(ctx context.Context, folderID int) ([]string, error) {
	if s == nil || s.folderRepo == nil || folderID <= 0 {
		return nil, nil
	}
	folder, err := s.folderRepo.GetByID(ctx, folderID)
	if err != nil {
		return nil, fmt.Errorf("loading library roots for filename matching: %w", err)
	}
	if folder == nil {
		return nil, fmt.Errorf("library %d is unavailable for filename matching", folderID)
	}
	return slices.Clone(folder.Paths), nil
}

func (s *MetadataService) configuredNamingRootsForFiles(ctx context.Context, files []*models.MediaFile) (map[int][]string, error) {
	byFolder := make(map[int][]string)
	for _, file := range files {
		if file == nil {
			continue
		}
		if _, loaded := byFolder[file.MediaFolderID]; loaded {
			continue
		}
		roots, err := s.configuredNamingRoots(ctx, file.MediaFolderID)
		if err != nil {
			return nil, err
		}
		byFolder[file.MediaFolderID] = roots
	}
	return byFolder, nil
}

func (s *MetadataService) configuredNamingRootsForContent(ctx context.Context, contentID string, folderID int) ([]string, error) {
	if folderID > 0 {
		return s.configuredNamingRoots(ctx, folderID)
	}
	if s == nil || s.folderRepo == nil || s.libraryRepo == nil || contentID == "" {
		return nil, nil
	}
	folderIDs, err := s.libraryRepo.GetFolderIDsForItem(ctx, contentID)
	if err != nil {
		return nil, fmt.Errorf("loading libraries for filename matching: %w", err)
	}
	var roots []string
	for _, id := range folderIDs {
		paths, err := s.configuredNamingRoots(ctx, id)
		if err != nil {
			return nil, err
		}
		roots = append(roots, paths...)
	}
	return roots, nil
}
