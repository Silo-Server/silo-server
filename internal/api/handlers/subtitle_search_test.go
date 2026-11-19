package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type stubSubtitleMediaResolver struct {
	meta *MediaFileMetadata
}

func (s stubSubtitleMediaResolver) GetMediaFileWithMetadata(context.Context, int) (*MediaFileMetadata, error) {
	return s.meta, nil
}

type stubMediaFileResolver struct {
	file *models.MediaFile
	err  error
}

func (s stubMediaFileResolver) GetByID(context.Context, int) (*models.MediaFile, error) {
	return s.file, s.err
}

type stubItemAccessChecker struct {
	err error
}

func (s stubItemAccessChecker) EnsureAccessible(context.Context, string, catalog.AccessFilter) error {
	return s.err
}

type handlerMockSubtitleRepo struct {
	subtitles map[int]*subtitles.DownloadedSubtitle
	nextID    int
	byKey     map[string]*subtitles.DownloadedSubtitle
	// getErr, when set, is returned by GetDownloadedSubtitle to simulate a
	// backing-store failure.
	getErr error
	// listErr, when set, is returned by ListDownloadedSubtitles to simulate a
	// backing-store failure.
	listErr     error
	list        []subtitles.DownloadedSubtitle
	listResults [][]subtitles.DownloadedSubtitle
	listCalls   int
}

func newMockSubtitleRepoForHandler() *handlerMockSubtitleRepo {
	return &handlerMockSubtitleRepo{
		subtitles: make(map[int]*subtitles.DownloadedSubtitle),
		byKey:     make(map[string]*subtitles.DownloadedSubtitle),
	}
}

func (m *handlerMockSubtitleRepo) InsertDownloadedSubtitle(_ context.Context, sub *subtitles.DownloadedSubtitle) error {
	m.nextID++
	sub.ID = m.nextID
	m.subtitles[sub.ID] = sub
	m.byKey[sub.S3Key] = sub
	return nil
}

func (m *handlerMockSubtitleRepo) GetDownloadedSubtitle(_ context.Context, id int) (*subtitles.DownloadedSubtitle, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	if sub, ok := m.subtitles[id]; ok {
		copy := *sub
		return &copy, nil
	}
	return nil, nil
}

func (m *handlerMockSubtitleRepo) ListDownloadedSubtitles(context.Context, int) ([]subtitles.DownloadedSubtitle, error) {
	m.listCalls++
	if m.listErr != nil {
		return nil, m.listErr
	}
	if len(m.listResults) > 0 {
		index := min(m.listCalls-1, len(m.listResults)-1)
		return append([]subtitles.DownloadedSubtitle(nil), m.listResults[index]...), nil
	}
	return append([]subtitles.DownloadedSubtitle(nil), m.list...), nil
}

func (m *handlerMockSubtitleRepo) DeleteDownloadedSubtitle(_ context.Context, id int) (*subtitles.DownloadedSubtitle, error) {
	sub := m.subtitles[id]
	delete(m.subtitles, id)
	return sub, nil
}

func (m *handlerMockSubtitleRepo) GetDownloadedSubtitleByS3Key(_ context.Context, s3Key string) (*subtitles.DownloadedSubtitle, error) {
	if sub, ok := m.byKey[s3Key]; ok {
		copy := *sub
		return &copy, nil
	}
	return nil, nil
}

func (m *handlerMockSubtitleRepo) UpdateDownloadedSubtitle(_ context.Context, id int, update subtitles.SubtitleMetadataUpdate) (*subtitles.DownloadedSubtitle, error) {
	sub, ok := m.subtitles[id]
	if !ok {
		return nil, nil
	}
	if update.Language != nil {
		sub.Language = *update.Language
	}
	if update.ReleaseName != nil {
		sub.ReleaseName = *update.ReleaseName
	}
	if update.HearingImpaired != nil {
		sub.HearingImpaired = *update.HearingImpaired
	}
	if update.ContentSHA256 != "" {
		sub.ContentSHA256 = update.ContentSHA256
	}
	if update.Timing != nil {
		sub.Timing = *update.Timing
	}
	sub.Revision++
	copy := *sub
	return &copy, nil
}

func (m *handlerMockSubtitleRepo) ListProviderConfigs(context.Context) ([]subtitles.ProviderConfig, error) {
	return nil, nil
}

func (m *handlerMockSubtitleRepo) GetProviderConfig(context.Context, string) (*subtitles.ProviderConfig, error) {
	return nil, nil
}

func (m *handlerMockSubtitleRepo) UpsertProviderConfig(context.Context, *subtitles.ProviderConfig) error {
	return nil
}

type handlerMockBlobStore struct{}

func newMockBlobStoreForHandler() *handlerMockBlobStore {
	return &handlerMockBlobStore{}
}

func (handlerMockBlobStore) Put(context.Context, string, []byte) error {
	return nil
}

func (handlerMockBlobStore) Get(context.Context, string) ([]byte, error) {
	return nil, nil
}

func (handlerMockBlobStore) Delete(context.Context, string) error {
	return nil
}

func (m *handlerMockSubtitleRepo) GetDownloadedSubtitleByContent(_ context.Context, content *subtitles.DownloadedSubtitle) (*subtitles.DownloadedSubtitle, error) {
	for _, sub := range m.subtitles {
		if content.ContentSHA256 != "" && sub.MediaFileID == content.MediaFileID && sub.Provider == content.Provider && sub.Language == content.Language && sub.Format == content.Format && sub.ContentSHA256 == content.ContentSHA256 {
			copy := *sub
			return &copy, nil
		}
	}
	return nil, nil
}
