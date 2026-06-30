package handlers

import (
	"context"
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

type fakeMediaConsumptionAuthorizer struct {
	decision  auth.AccessDecision
	decisions map[auth.ACLAction]auth.AccessDecision
	request   auth.AccessRequest
	requests  []auth.AccessRequest
	err       error
	called    bool
}

func (f *fakeMediaConsumptionAuthorizer) Authorize(_ context.Context, request auth.AccessRequest) (auth.AccessDecision, error) {
	f.called = true
	f.request = request
	f.requests = append(f.requests, request)
	if f.decisions != nil {
		if decision, ok := f.decisions[request.Action]; ok {
			return decision, f.err
		}
	}
	return f.decision, f.err
}

func (f *fakeMediaConsumptionAuthorizer) Explain(_ context.Context, request auth.AccessRequest) (auth.AccessExplanation, error) {
	decision, err := f.Authorize(context.Background(), request)
	return auth.AccessExplanation{Request: request, Decision: decision}, err
}

type stubMediaItemLookup struct {
	item *models.MediaItem
	err  error
}

func (s stubMediaItemLookup) GetByID(context.Context, string) (*models.MediaItem, error) {
	return s.item, s.err
}

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

func TestMediaFileAuthorizerPopulatesPlaybackACLFacts(t *testing.T) {
	consumptionAuthorizer := &fakeMediaConsumptionAuthorizer{
		decision: auth.AccessDecision{Allowed: true},
	}
	authorizer := &MediaFileAuthorizer{
		FileResolver: stubMediaFileResolver{
			file: &models.MediaFile{
				ID:            42,
				ContentID:     "movie-1",
				MediaFolderID: 10,
				BaseType:      "movie",
				Resolution:    "2160p",
			},
		},
		ItemAccess:            stubItemAccessChecker{},
		ItemLookup:            stubMediaItemLookup{item: &models.MediaItem{ContentID: "movie-1", ContentRating: "TV-MA"}},
		ConsumptionAuthorizer: consumptionAuthorizer,
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{
		UserID:    1,
		TokenType: auth.TokenTypeAccess,
	}))

	if _, err := authorizer.Authorize(req, 42); err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	if !consumptionAuthorizer.called {
		t.Fatal("playback ACL authorizer was not called")
	}
	if consumptionAuthorizer.request.PlaybackQuality != "2160p" {
		t.Fatalf("playback quality = %q, want 2160p", consumptionAuthorizer.request.PlaybackQuality)
	}
	if consumptionAuthorizer.request.ContentRating != "TV-MA" {
		t.Fatalf("content rating = %q, want TV-MA", consumptionAuthorizer.request.ContentRating)
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
