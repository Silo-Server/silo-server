package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type downloadRecordingProvider struct {
	calls int
	err   error
}

func (*downloadRecordingProvider) Name() string { return "example" }
func (*downloadRecordingProvider) Search(context.Context, subtitles.SearchRequest) ([]subtitles.SubtitleResult, error) {
	return nil, nil
}
func (p *downloadRecordingProvider) Download(context.Context, string) ([]byte, subtitles.SubtitleFormat, error) {
	p.calls++
	return []byte("synthetic subtitle"), subtitles.FormatSRT, p.err
}
func TestSubtitleDownloadServiceAuthorizationAttributionAndBridge(t *testing.T) {
	repo := newMockSubtitleRepoForHandler()
	manager := subtitles.NewManager(repo, newMockBlobStoreForHandler())
	provider := new(downloadRecordingProvider)
	manager.RegisterProvider(provider)
	h := NewSubtitleSearchHandler(manager, repo, nil)
	h.FileAuthorizer = &MediaFileAuthorizer{FileResolver: stubMediaFileResolver{file: &models.MediaFile{ID: 42, ContentID: "movie"}}, ItemAccess: stubItemAccessChecker{err: catalog.ErrItemNotFound}}
	request := subtitles.DownloadRequest{MediaFileID: 42, ProviderName: "example", SubtitleID: "result", Language: "en", UserID: new(999)}
	_, err := h.DownloadStoredSubtitle(t.Context(), catalog.AccessFilter{UserID: 1}, request)
	if failure, ok := errors.AsType[*APIError](err); !ok || failure.Status != 404 || provider.calls != 0 {
		t.Fatalf("authorization: %v calls=%d", err, provider.calls)
	}
	h.FileAuthorizer.ItemAccess = stubItemAccessChecker{}
	row, err := h.DownloadStoredSubtitle(t.Context(), catalog.AccessFilter{UserID: 1}, request)
	if err != nil || row.DownloadedBy == nil || *row.DownloadedBy != 1 || provider.calls != 1 {
		t.Fatalf("attribution: %+v %v", row, err)
	}
	provider.err = errors.New("PRIVATE_UPSTREAM")
	rec := httptest.NewRecorder()
	h.HandleDownload(rec, newSubtitleAuthRequest(http.MethodPost, "/subtitles/download", strings.NewReader(`{"media_file_id":42,"provider":"example","subtitle_id":"result","language":"en"}`)))
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), `"error":"download_error"`) || strings.Contains(rec.Body.String(), "PRIVATE") || provider.calls != 2 {
		t.Fatal(rec.Code, rec.Body.String(), provider.calls)
	}
}

// A provider key nobody registered is a client input problem: both the native
// service entry point and the v1 bridge answer 404 rather than 500.
func TestSubtitleDownloadUnknownProviderIsNotFound(t *testing.T) {
	repo := newMockSubtitleRepoForHandler()
	manager := subtitles.NewManager(repo, newMockBlobStoreForHandler())
	manager.RegisterProvider(new(downloadRecordingProvider))
	h := NewSubtitleSearchHandler(manager, repo, nil)
	h.FileAuthorizer = &MediaFileAuthorizer{FileResolver: stubMediaFileResolver{file: &models.MediaFile{ID: 42, ContentID: "movie"}}, ItemAccess: stubItemAccessChecker{}}

	_, err := h.DownloadStoredSubtitle(t.Context(), catalog.AccessFilter{UserID: 1}, subtitles.DownloadRequest{MediaFileID: 42, ProviderName: "not-registered", SubtitleID: "result", Language: "en"})
	failure, ok := errors.AsType[*APIError](err)
	if !ok || failure.Status != http.StatusNotFound || failure.Code != "provider_not_found" {
		t.Fatalf("service error = %v", err)
	}
	if !errors.Is(err, subtitles.ErrUnknownProvider) {
		t.Fatalf("service error lost the unknown-provider cause: %v", err)
	}

	rec := httptest.NewRecorder()
	h.HandleDownload(rec, newSubtitleAuthRequest(http.MethodPost, "/subtitles/download", strings.NewReader(`{"media_file_id":42,"provider":"not-registered","subtitle_id":"result","language":"en"}`)))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"error":"provider_not_found"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestSubtitleUploadServiceAuthorizationAndAttribution(t *testing.T) {
	repo := newMockSubtitleRepoForHandler()
	manager := subtitles.NewManager(repo, newMockBlobStoreForHandler())
	h := NewSubtitleSearchHandler(manager, repo, nil)
	h.FileAuthorizer = &MediaFileAuthorizer{FileResolver: stubMediaFileResolver{file: &models.MediaFile{ID: 42, ContentID: "movie"}}, ItemAccess: stubItemAccessChecker{err: catalog.ErrItemNotFound}}
	request := subtitles.UploadRequest{MediaFileID: 42, UserID: new(999), Filename: "synthetic.en.srt", Language: "fr", PreferUserLanguage: true, Data: []byte("synthetic")}
	_, err := h.UploadStoredSubtitle(t.Context(), catalog.AccessFilter{UserID: 1}, request)
	if failure, ok := errors.AsType[*APIError](err); !ok || failure.Status != 404 {
		t.Fatalf("authorization: %v", err)
	}
	h.FileAuthorizer.ItemAccess = stubItemAccessChecker{}
	row, err := h.UploadStoredSubtitle(t.Context(), catalog.AccessFilter{UserID: 1}, request)
	if err != nil || row.DownloadedBy == nil || *row.DownloadedBy != 1 || row.Language != "fr" {
		t.Fatalf("upload result: %+v %v", row, err)
	}
	request.Data = nil
	_, err = h.UploadStoredSubtitle(t.Context(), catalog.AccessFilter{UserID: 1}, request)
	if failure, ok := errors.AsType[*APIError](err); !ok || failure.Status != 400 || failure.Code != "bad_request" {
		t.Fatalf("invalid upload: %v", err)
	}
}
