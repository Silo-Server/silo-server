package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

func TestMediaFileAuthorizerMapsMissingFileToNotFound(t *testing.T) {
	authorizer := &MediaFileAuthorizer{
		FileResolver: stubMediaFileResolver{err: scanner.ErrFileNotFound},
		ItemAccess:   stubItemAccessChecker{},
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{
		UserID:    1,
		TokenType: auth.TokenTypeAccess,
	}))

	_, err := authorizer.Authorize(req, 99)
	if !errors.Is(err, catalog.ErrItemNotFound) {
		t.Fatalf("Authorize() error = %v, want ErrItemNotFound", err)
	}
}

func TestHandleUploadMissingMediaFileReturns404(t *testing.T) {
	repo := newMockSubtitleRepoForHandler()
	manager := subtitles.NewManager(repo, newMockS3ClientForHandler(), "test-bucket")
	handler := NewSubtitleSearchHandler(manager, repo, stubSubtitleMediaResolver{})
	handler.FileAuthorizer = &MediaFileAuthorizer{
		FileResolver: stubMediaFileResolver{err: scanner.ErrFileNotFound},
		ItemAccess:   stubItemAccessChecker{},
	}

	req := newSubtitleUploadRequest(t, 99, "en", "custom.srt", []byte("1\n00:00:01,000 --> 00:00:02,000\nHi\n"))
	rr := httptest.NewRecorder()
	handler.HandleUpload(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandleListMissingMediaFileReturns404(t *testing.T) {
	repo := newMockSubtitleRepoForHandler()
	manager := subtitles.NewManager(repo, newMockS3ClientForHandler(), "test-bucket")
	handler := NewSubtitleSearchHandler(manager, repo, stubSubtitleMediaResolver{})
	handler.FileAuthorizer = &MediaFileAuthorizer{
		FileResolver: stubMediaFileResolver{err: scanner.ErrFileNotFound},
		ItemAccess:   stubItemAccessChecker{},
	}

	req := newSubtitleAuthRequest(http.MethodGet, "/subtitles/99", nil)
	req = withProfileRouteParam(req, "media_file_id", "99")
	rr := httptest.NewRecorder()
	handler.HandleList(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rr.Code, rr.Body.String())
	}
}

func TestMediaFileAuthorizerAllowsAccessibleFile(t *testing.T) {
	authorizer := &MediaFileAuthorizer{
		FileResolver: stubMediaFileResolver{
			file: &models.MediaFile{ID: 42, ContentID: "movie-1"},
		},
		ItemAccess: stubItemAccessChecker{},
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{
		UserID:    1,
		TokenType: auth.TokenTypeAccess,
	}))

	file, err := authorizer.Authorize(req, 42)
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	if file == nil || file.ID != 42 {
		t.Fatalf("file = %#v, want id 42", file)
	}
}

func TestMapMediaFileLookupError(t *testing.T) {
	if !errors.Is(mapMediaFileLookupError(scanner.ErrFileNotFound), catalog.ErrItemNotFound) {
		t.Fatal("expected scanner.ErrFileNotFound to map to catalog.ErrItemNotFound")
	}
	if mapMediaFileLookupError(errors.New("db down")) == nil {
		t.Fatal("expected unrelated error to pass through")
	}
}
