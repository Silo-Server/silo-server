package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeEbookReadStateStore is an in-memory EbookReaderProgressReadWriter keyed
// by content ID for a single (user, profile) scope.
type fakeEbookReadStateStore struct {
	rows map[string]EbookReaderProgress
}

func (f *fakeEbookReadStateStore) Get(_ context.Context, _ int, _ string, contentID string) (*EbookReaderProgress, error) {
	row, ok := f.rows[contentID]
	if !ok {
		return nil, nil
	}
	return &row, nil
}

func (f *fakeEbookReadStateStore) Upsert(_ context.Context, progress EbookReaderProgress) error {
	f.rows[progress.ContentID] = progress
	return nil
}

func (f *fakeEbookReadStateStore) Delete(_ context.Context, _ int, _ string, contentID string) error {
	delete(f.rows, contentID)
	return nil
}

func (f *fakeEbookReadStateStore) ListByContentIDs(_ context.Context, _ int, _ string, contentIDs []string) (map[string]EbookReaderProgress, error) {
	result := make(map[string]EbookReaderProgress, len(contentIDs))
	for _, contentID := range contentIDs {
		if row, ok := f.rows[contentID]; ok {
			result[contentID] = row
		}
	}
	return result, nil
}

// fakeEbookFileProvider implements EpisodeFileProvider for ebook file lookups.
type fakeEbookFileProvider struct {
	files map[string][]*models.MediaFile
	err   error
}

func (f *fakeEbookFileProvider) GetByContentID(_ context.Context, contentID string) ([]*models.MediaFile, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.files[contentID], nil
}

func (f *fakeEbookFileProvider) GetByEpisodeID(context.Context, string) ([]*models.MediaFile, error) {
	return nil, nil
}

func (f *fakeEbookFileProvider) ListByContentIDs(context.Context, []string) (map[string][]*models.MediaFile, error) {
	return nil, nil
}
