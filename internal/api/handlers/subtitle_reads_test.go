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

type recordingSubtitleProvider struct {
	request *subtitles.SearchRequest
}

func (p recordingSubtitleProvider) Name() string { return "example" }
func (p recordingSubtitleProvider) Search(_ context.Context, request subtitles.SearchRequest) ([]subtitles.SubtitleResult, error) {
	*p.request = request
	return []subtitles.SubtitleResult{{ID: "result", Provider: "example", Language: "en", Format: subtitles.FormatSRT}}, nil
}
func (p recordingSubtitleProvider) Download(context.Context, string) ([]byte, subtitles.SubtitleFormat, error) {
	return nil, "", errors.New("unexpected download")
}

func TestSubtitleReadsAuthorizeBeforeStorageOrProvider(t *testing.T) {
	for _, denied := range []bool{true, false} {
		repo := newMockSubtitleRepoForHandler()
		repo.list = []subtitles.DownloadedSubtitle{{ID: 7, MediaFileID: 42}}
		var searched subtitles.SearchRequest
		manager := subtitles.NewManager(repo, newMockBlobStoreForHandler())
		manager.RegisterProvider(recordingSubtitleProvider{request: &searched})
		h := NewSubtitleSearchHandler(manager, repo, stubSubtitleMediaResolver{meta: &MediaFileMetadata{FileID: 42, FilePath: "Example.1080p.mkv", FileHash: "fixture-hash", Title: "Example", IMDbID: "tt0000001"}})
		access := stubItemAccessChecker{}
		if denied {
			access.err = catalog.ErrItemNotFound
		}
		h.FileAuthorizer = &MediaFileAuthorizer{FileResolver: stubMediaFileResolver{file: &models.MediaFile{ID: 42, ContentID: "movie"}}, ItemAccess: access}
		rows, listErr := h.ListStoredSubtitles(t.Context(), catalog.AccessFilter{UserID: 1}, 42)
		result, searchErr := h.SearchSubtitles(t.Context(), catalog.AccessFilter{UserID: 1}, 42, []string{"en"})
		if denied {
			for _, err := range []error{listErr, searchErr} {
				apiErr, ok := errors.AsType[*APIError](err)
				if !ok || apiErr.Status != 404 {
					t.Fatalf("denied error = %v", err)
				}
			}
			if repo.listCalls != 0 || searched.Title != "" {
				t.Fatal("denied read reached storage or provider")
			}
			continue
		}
		if listErr != nil || searchErr != nil || len(rows) != 1 || rows[0].ID != 7 || len(result.Results) != 1 {
			t.Fatalf("authorized reads: %v %v %v %v", rows, result, listErr, searchErr)
		}
		if searched.Title != "Example" || searched.FileHash != "fixture-hash" || len(searched.Languages) != 1 || searched.Languages[0] != "en" {
			t.Fatalf("search metadata lost: %+v", searched)
		}
	}
}

func TestSubtitleSearchBridgeMissingMetadataKeepsError(t *testing.T) {
	repo := newMockSubtitleRepoForHandler()
	h := NewSubtitleSearchHandler(subtitles.NewManager(repo, newMockBlobStoreForHandler()), repo, stubSubtitleMediaResolver{})
	h.FileAuthorizer = &MediaFileAuthorizer{FileResolver: stubMediaFileResolver{file: &models.MediaFile{ID: 42, ContentID: "movie"}}, ItemAccess: stubItemAccessChecker{}}
	r := newSubtitleAuthRequest(http.MethodPost, "/subtitles/search", strings.NewReader(`{"media_file_id":42,"languages":["en"]}`))
	w := httptest.NewRecorder()
	h.HandleSearch(w, r)
	if w.Code != 404 || !strings.Contains(w.Body.String(), `"error":"not_found"`) || !strings.Contains(w.Body.String(), `"message":"Media file not found"`) {
		t.Fatalf("bridge changed: %d %s", w.Code, w.Body.String())
	}
}

func TestSubtitleSearchCanonicalizesCompatibilityLanguages(t *testing.T) {
	for _, input := range []string{"ar", "ara", "Arabic"} {
		var searched subtitles.SearchRequest
		repo := newMockSubtitleRepoForHandler()
		manager := subtitles.NewManager(repo, newMockBlobStoreForHandler())
		manager.RegisterProvider(recordingSubtitleProvider{request: &searched})
		h := NewSubtitleSearchHandler(manager, repo, stubSubtitleMediaResolver{meta: &MediaFileMetadata{FileID: 42, FilePath: "Example.mkv", Title: "Example"}})
		h.FileAuthorizer = &MediaFileAuthorizer{FileResolver: stubMediaFileResolver{file: &models.MediaFile{ID: 42, ContentID: "movie"}}, ItemAccess: stubItemAccessChecker{}}
		if _, err := h.SearchSubtitles(t.Context(), catalog.AccessFilter{UserID: 1}, 42, []string{input, "AR"}); err != nil {
			t.Fatal(err)
		}
		if len(searched.Languages) != 1 || searched.Languages[0] != "ar" {
			t.Fatalf("input %q reached provider as %#v", input, searched.Languages)
		}
	}
	var searched subtitles.SearchRequest
	repo := newMockSubtitleRepoForHandler()
	manager := subtitles.NewManager(repo, newMockBlobStoreForHandler())
	manager.RegisterProvider(recordingSubtitleProvider{request: &searched})
	h := NewSubtitleSearchHandler(manager, repo, stubSubtitleMediaResolver{meta: &MediaFileMetadata{FileID: 42, FilePath: "Example.mkv", Title: "Example"}})
	if _, err := h.SearchSubtitles(t.Context(), catalog.AccessFilter{UserID: 1}, 42, []string{"Klingon"}); err == nil {
		t.Fatal("invalid language was accepted")
	}
}
