package handlers

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
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

func newSubtitleAuthRequest(method, path string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, path, body)
	ctx := apimw.SetClaims(context.Background(), &auth.Claims{
		UserID:    1,
		Role:      "user",
		TokenType: auth.TokenTypeAccess,
	})
	return req.WithContext(ctx)
}

func newSubtitleUploadRequest(t *testing.T, mediaFileID int, language, filename string, content []byte) *http.Request {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("media_file_id", strconv.Itoa(mediaFileID)); err != nil {
		t.Fatalf("write media_file_id: %v", err)
	}
	if err := writer.WriteField("language", language); err != nil {
		t.Fatalf("write language: %v", err)
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write file content: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := newSubtitleAuthRequest(http.MethodPost, "/subtitles/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestHandleUploadRejectsBadExtension(t *testing.T) {
	repo := newMockSubtitleRepoForHandler()
	manager := subtitles.NewManager(repo, newMockBlobStoreForHandler())
	handler := NewSubtitleSearchHandler(manager, repo, stubSubtitleMediaResolver{})
	handler.FileAuthorizer = &MediaFileAuthorizer{
		FileResolver: stubMediaFileResolver{
			file: &models.MediaFile{ID: 42, ContentID: "movie-1"},
		},
		ItemAccess: stubItemAccessChecker{},
	}

	req := newSubtitleUploadRequest(t, 42, "en", "notes.txt", []byte("hello"))
	rr := httptest.NewRecorder()
	handler.HandleUpload(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
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

func TestHandleDeleteRequiresAccessToMediaFile(t *testing.T) {
	repo := newMockSubtitleRepoForHandler()
	repo.subtitles[1] = &subtitles.DownloadedSubtitle{
		ID:          1,
		MediaFileID: 42,
		Provider:    subtitles.ProviderUpload,
	}
	manager := subtitles.NewManager(repo, newMockBlobStoreForHandler())
	handler := NewSubtitleSearchHandler(manager, repo, stubSubtitleMediaResolver{})
	handler.FileAuthorizer = &MediaFileAuthorizer{
		FileResolver: stubMediaFileResolver{err: scanner.ErrFileNotFound},
		ItemAccess:   stubItemAccessChecker{},
	}

	req := newSubtitleAuthRequest(http.MethodDelete, "/subtitles/1", nil)
	req = withProfileRouteParam(req, "id", "1")
	rr := httptest.NewRecorder()
	handler.HandleDelete(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestHandleUploadUnauthorizedMediaFile(t *testing.T) {
	repo := newMockSubtitleRepoForHandler()
	manager := subtitles.NewManager(repo, newMockBlobStoreForHandler())
	handler := NewSubtitleSearchHandler(manager, repo, stubSubtitleMediaResolver{})
	handler.FileAuthorizer = &MediaFileAuthorizer{
		FileResolver: stubMediaFileResolver{err: scanner.ErrFileNotFound},
		ItemAccess:   stubItemAccessChecker{},
	}

	req := newSubtitleUploadRequest(t, 99, "en", "custom.srt", []byte("hello"))
	rr := httptest.NewRecorder()
	handler.HandleUpload(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}
