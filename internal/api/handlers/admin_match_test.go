package handlers

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

// --- Fakes ---

type fakeMatchItemLookup struct {
	items map[string]*models.MediaItem
}

func (f *fakeMatchItemLookup) GetByID(_ context.Context, contentID string) (*models.MediaItem, error) {
	if item, ok := f.items[contentID]; ok {
		return item, nil
	}
	return nil, fmt.Errorf("item not found: %s", contentID)
}

type fakeMatchFolderLookup struct {
	folders   map[string]int
	folderIDs map[string][]int
}

func (f *fakeMatchFolderLookup) GetFolderIDForItem(_ context.Context, contentID string) (int, error) {
	if fid, ok := f.folders[contentID]; ok {
		return fid, nil
	}
	return 0, fmt.Errorf("no folder for %s", contentID)
}

func (f *fakeMatchFolderLookup) GetFolderIDsForItem(_ context.Context, contentID string) ([]int, error) {
	if ids, ok := f.folderIDs[contentID]; ok {
		return ids, nil
	}
	if fid, ok := f.folders[contentID]; ok {
		return []int{fid}, nil
	}
	return nil, fmt.Errorf("no folders for %s", contentID)
}

type fakeMatchMetadataService struct {
	searchResults []metadata.MatchCandidate
	searchErr     error
	processResult *metadata.ProcessResult
	processErr    error
	lastProcess   metadata.ProcessRequest
}

func (f *fakeMatchMetadataService) SearchAndNormalize(_ context.Context, _ metadata.SearchQuery, _ int) ([]metadata.MatchCandidate, error) {
	return f.searchResults, f.searchErr
}

func (f *fakeMatchMetadataService) Process(_ context.Context, req metadata.ProcessRequest) (*metadata.ProcessResult, error) {
	f.lastProcess = req
	return f.processResult, f.processErr
}

// --- Helpers ---

// --- Tests ---

// capturingMetadataService wraps a fake to capture the query passed to SearchAndNormalize.
type capturingMetadataService struct {
	inner        *fakeMatchMetadataService
	lastQuery    metadata.SearchQuery
	lastFolderID int
}

func (c *capturingMetadataService) SearchAndNormalize(ctx context.Context, query metadata.SearchQuery, folderID int) ([]metadata.MatchCandidate, error) {
	c.lastQuery = query
	c.lastFolderID = folderID
	return c.inner.SearchAndNormalize(ctx, query, folderID)
}

func (c *capturingMetadataService) Process(ctx context.Context, req metadata.ProcessRequest) (*metadata.ProcessResult, error) {
	return c.inner.Process(ctx, req)
}
