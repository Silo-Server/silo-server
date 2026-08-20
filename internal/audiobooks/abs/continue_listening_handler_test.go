package abs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

type recordingProgressFake struct {
	fakeProgressStore
	mu   sync.Mutex
	last string
}

func (f *recordingProgressFake) SetHideFromContinue(_ context.Context, userID, profileID, contentID string, hide bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if hide {
		f.last = "hide:" + contentID
	} else {
		f.last = "show:" + contentID
	}
	return nil
}

func TestContinue_Remove_SetsHide(t *testing.T) {
	prog := &recordingProgressFake{}
	media := &stubMediaStore{known: map[string]*models.MediaItem{testBookID: nil}}
	h := New(Dependencies{MediaStore: media, ProgressStore: prog})

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/progress/book-1/remove-from-continue-listening",
		map[string]string{itemIDParam: testBookID}, nil, "1", "", h.handleRemoveFromContinueListening)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["ok"] != true {
		t.Errorf("ok = %v", got["ok"])
	}
	if prog.last != "hide:book-1" {
		t.Errorf("last = %q, want hide:book-1", prog.last)
	}
}

func TestContinue_Readd_SetsShow(t *testing.T) {
	prog := &recordingProgressFake{}
	media := &stubMediaStore{known: map[string]*models.MediaItem{testBookID: nil}}
	h := New(Dependencies{MediaStore: media, ProgressStore: prog})

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/progress/book-1/readd-to-continue-listening",
		map[string]string{itemIDParam: testBookID}, nil, "1", "", h.handleReaddToContinueListening)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if prog.last != "show:book-1" {
		t.Errorf("last = %q, want show:book-1", prog.last)
	}
}

func TestContinue_UnknownItem_404(t *testing.T) {
	prog := &recordingProgressFake{}
	media := &stubMediaStore{known: map[string]*models.MediaItem{}}
	h := New(Dependencies{MediaStore: media, ProgressStore: prog})

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/progress/ghost/remove-from-continue-listening",
		map[string]string{itemIDParam: testGhostID}, nil, "1", "", h.handleRemoveFromContinueListening)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestContinue_MediaLookupError_500(t *testing.T) {
	prog := &recordingProgressFake{}
	media := &stubMediaStore{
		known:     map[string]*models.MediaItem{testBookID: nil},
		lookupErr: errors.New("media lookup failed"),
	}
	h := New(Dependencies{MediaStore: media, ProgressStore: prog})

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/progress/book-1/remove-from-continue-listening",
		map[string]string{itemIDParam: testBookID}, nil, "1", "", h.handleRemoveFromContinueListening)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}

func TestContinue_EbookWithoutProgressStore_503(t *testing.T) {
	media := &stubMediaStore{known: map[string]*models.MediaItem{
		testEbookID: {ContentID: testEbookID, Type: mediaTypeEbook},
	}}
	h := New(Dependencies{MediaStore: media})
	rec := dispatchABSWithParams(http.MethodGet, "/api/me/progress/ebook-1/remove-from-continue-listening",
		map[string]string{itemIDParam: testEbookID}, nil, "1", testProfileID, h.handleRemoveFromContinueListening)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}
