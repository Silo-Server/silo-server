package handlers

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/subtitles/subsource"
)

type downloadRecordingProvider struct {
	calls int
	err   error
}

func (*downloadRecordingProvider) Name() string { return "example" }
func (*downloadRecordingProvider) Search(context.Context, subtitles.SearchRequest) ([]subtitles.SubtitleResult, error) {
	return nil, nil
}
func (p *downloadRecordingProvider) Download(context.Context, string, subtitles.DownloadTarget) ([]byte, subtitles.SubtitleFormat, error) {
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
	request := subtitles.UploadRequest{MediaFileID: 42, UserID: new(999), Filename: "synthetic.en.srt", Language: "fr", PreferUserLanguage: true, Data: []byte("1\n00:00:01,000 --> 00:00:02,000\nSynthetic\n")}
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

// A provider result ID in the wrong shape is a client input problem: the
// native service answers 400 at subtitle_id, and the frozen v1 bridge keeps
// its existing 500 download_error.
func TestSubtitleDownloadInvalidSubtitleIDIsBadRequest(t *testing.T) {
	repo := newMockSubtitleRepoForHandler()
	manager := subtitles.NewManager(repo, newMockBlobStoreForHandler())
	manager.RegisterProvider(&downloadRecordingProvider{err: subtitles.ErrInvalidSubtitleID})
	h := NewSubtitleSearchHandler(manager, repo, nil)
	h.FileAuthorizer = &MediaFileAuthorizer{FileResolver: stubMediaFileResolver{file: &models.MediaFile{ID: 42, ContentID: "movie"}}, ItemAccess: stubItemAccessChecker{}}

	_, err := h.DownloadStoredSubtitle(t.Context(), catalog.AccessFilter{UserID: 1}, subtitles.DownloadRequest{MediaFileID: 42, ProviderName: "example", SubtitleID: "malformed", Language: "en"})
	failure, ok := errors.AsType[*APIError](err)
	if !ok || failure.Status != http.StatusBadRequest || failure.Code != "bad_request" || failure.Field != "subtitle_id" {
		t.Fatalf("service error = %v", err)
	}
	if !errors.Is(err, subtitles.ErrInvalidSubtitleID) {
		t.Fatalf("service error lost the invalid-ID cause: %v", err)
	}

	rec := httptest.NewRecorder()
	h.HandleDownload(rec, newSubtitleAuthRequest(http.MethodPost, "/subtitles/download", strings.NewReader(`{"media_file_id":42,"provider":"example","subtitle_id":"malformed","language":"en"}`)))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), `"error":"download_error"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

// A client can resend a SubDL result ID issued while IDs still carried the
// signed api_key query; neither that query nor a credential quoted in the
// provider's error reaches the failure log.
func TestSubtitleDownloadFailureLogOmitsCredentials(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	repo := newMockSubtitleRepoForHandler()
	manager := subtitles.NewManager(repo, newMockBlobStoreForHandler())
	manager.RegisterProvider(&downloadRecordingProvider{err: errors.New("upstream said api_key=synthetic-error-key")})
	h := NewSubtitleSearchHandler(manager, repo, nil)
	h.FileAuthorizer = &MediaFileAuthorizer{FileResolver: stubMediaFileResolver{file: &models.MediaFile{ID: 42, ContentID: "movie"}}, ItemAccess: stubItemAccessChecker{}}

	_, err := h.DownloadStoredSubtitle(t.Context(), catalog.AccessFilter{UserID: 1}, subtitles.DownloadRequest{MediaFileID: 42, ProviderName: "example", SubtitleID: "/subtitle/1-2.zip?api_key=synthetic-id-key", Language: "en"})
	if failure, ok := errors.AsType[*APIError](err); !ok || failure.Code != "download_error" {
		t.Fatalf("service error = %v", err)
	}
	got := logs.String()
	if !strings.Contains(got, "subtitle_id=/subtitle/1-2.zip ") || strings.Contains(got, "synthetic-id-key") || strings.Contains(got, "synthetic-error-key") {
		t.Fatalf("failure log = %q", got)
	}
}

// A season-pack result downloads one archive holding every episode; the file
// the user is subtitling decides which entry is stored.
func TestSubtitleDownloadPicksFileEpisodeFromSeasonPack(t *testing.T) {
	cue := func(text string) string { return "1\n00:00:01,000 --> 00:00:02,000\n" + text + "\n" }
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	for _, name := range []string{"Show.S01E01.Pilot.ar.srt", "Show.S01E02.Second.ar.srt", "Show.S01E03.Third.ar.srt"} {
		w, err := zw.Create("Show.S01/" + name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, cue(name))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(archive.Bytes())
	}))
	t.Cleanup(server.Close)

	repo := newMockSubtitleRepoForHandler()
	manager := subtitles.NewManager(repo, newMockBlobStoreForHandler())
	manager.RegisterProvider(subsource.New(subsource.Config{APIKey: "synthetic", BaseURL: server.URL}))
	h := NewSubtitleSearchHandler(manager, repo, stubSubtitleMediaResolver{meta: &MediaFileMetadata{FileID: 42, FilePath: "/tv/Show/Show - 1x02.mkv", Season: 1, Episode: 2}})
	h.FileAuthorizer = &MediaFileAuthorizer{FileResolver: stubMediaFileResolver{file: &models.MediaFile{ID: 42, ContentID: "series"}}, ItemAccess: stubItemAccessChecker{}}

	row, err := h.DownloadStoredSubtitle(t.Context(), catalog.AccessFilter{UserID: 1}, subtitles.DownloadRequest{MediaFileID: 42, ProviderName: "subsource", SubtitleID: "7", Language: "ar"})
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("%x", sha256.Sum256([]byte(cue("Show.S01E02.Second.ar.srt")))); row.ContentSHA256 != want {
		t.Fatalf("stored content hash %s, want the S01E02 entry (%s)", row.ContentSHA256, want)
	}
}
