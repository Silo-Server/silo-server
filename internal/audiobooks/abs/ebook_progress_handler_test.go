package abs

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type recordingEbookProgressStore struct {
	row        *EbookProgress
	upserted   *EbookProgress
	hidden     *bool
	hiddenItem string
	committed  *EbookProgress
	rows       []EbookProgress
}

func (s *recordingEbookProgressStore) GetEbookProgress(context.Context, string, string, string) (*EbookProgress, error) {
	return s.row, nil
}

func (s *recordingEbookProgressStore) ListEbookProgress(context.Context, string, string, int) ([]EbookProgress, error) {
	return s.rows, nil
}

func (s *recordingEbookProgressStore) UpsertEbookProgress(_ context.Context, progress EbookProgress) (*EbookProgress, error) {
	s.upserted = &progress
	if s.committed != nil {
		return s.committed, nil
	}
	return &progress, nil
}

func (s *recordingEbookProgressStore) DeleteEbookProgress(context.Context, string, string, string) error {
	return nil
}

func (s *recordingEbookProgressStore) SetEbookHidden(_ context.Context, _, _, contentID string, hide bool) error {
	s.hidden = &hide
	s.hiddenItem = contentID
	return nil
}

func TestSetItemProgressRejectsOversizedBody(t *testing.T) {
	body := []byte(`{"isFinished":false}` + strings.Repeat(" ", int(maxProgressBodyBytes)))
	h := New(Dependencies{MediaStore: noopMediaStore{}})
	rec := dispatchABSWithParams(http.MethodPost, "/api/me/progress/ebook-1",
		map[string]string{"libraryItemId": testEbookID}, body, "1", testProfileID, h.handleSetItemProgress) //nolint:goconst // External ABS route key.
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", rec.Code, rec.Body.String())
	}
}

func TestSetEbookProgressFinishedFlagForcesCompletion(t *testing.T) {
	store := &recordingEbookProgressStore{row: &EbookProgress{
		UserID: "1", ProfileID: testProfileID, ContentID: testEbookID, FileID: 7, Progress: 0.4,
	}}
	media := &stubMediaStore{known: map[string]*models.MediaItem{
		testEbookID: {ContentID: testEbookID, Type: mediaTypeEbook},
	}}
	h := New(Dependencies{MediaStore: media, EbookProgressStore: store})
	rec := dispatchABSWithParams(http.MethodPost, "/api/me/progress/ebook-1",
		map[string]string{"libraryItemId": testEbookID}, []byte(`{"isFinished":true}`), "1", testProfileID, h.handleSetItemProgress)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if store.upserted == nil || store.upserted.Progress != 1 {
		t.Fatalf("upserted progress = %#v, want 1", store.upserted)
	}
}

func TestSetEbookProgressFinishedFalseExplicitlyUnfinishes(t *testing.T) {
	store := &recordingEbookProgressStore{row: &EbookProgress{
		UserID: "1", ProfileID: testProfileID, ContentID: testEbookID, FileID: 7, Progress: 1,
	}}
	media := &stubMediaStore{known: map[string]*models.MediaItem{
		testEbookID: {ContentID: testEbookID, Type: mediaTypeEbook},
	}}
	h := New(Dependencies{MediaStore: media, EbookProgressStore: store})
	rec := dispatchABSWithParams(http.MethodPost, "/api/me/progress/ebook-1",
		map[string]string{"libraryItemId": testEbookID}, []byte(`{"isFinished":false}`), "1", testProfileID, h.handleSetItemProgress)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if store.upserted == nil || store.upserted.Progress != 0 || !store.upserted.AllowFinishedRegression {
		t.Fatalf("upserted progress = %#v, want explicit atomic completion regression", store.upserted)
	}
}

func TestSetEbookProgressRoutineAutosavePreservesCompletion(t *testing.T) {
	completed := &EbookProgress{UserID: "1", ProfileID: testProfileID, ContentID: testEbookID, FileID: 7, Progress: 1}
	store := &recordingEbookProgressStore{row: completed, committed: completed}
	media := &stubMediaStore{known: map[string]*models.MediaItem{
		testEbookID: {ContentID: testEbookID, Type: mediaTypeEbook},
	}}
	h := New(Dependencies{MediaStore: media, EbookProgressStore: store})
	rec := dispatchABSWithParams(http.MethodPost, "/api/me/progress/ebook-1",
		map[string]string{"libraryItemId": testEbookID}, []byte(`{"ebookProgress":0.2}`), "1", testProfileID, h.handleSetItemProgress)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if store.upserted == nil || store.upserted.Progress != 0.2 || store.upserted.AllowFinishedRegression {
		t.Fatalf("upsert input = %#v, want routine autosave without regression permission", store.upserted)
	}
	if !strings.Contains(rec.Body.String(), `"ebookProgress":1`) {
		t.Fatalf("response did not preserve atomically committed completion: %s", rec.Body.String())
	}
}

func TestSetEbookProgressReturnsAtomicallyCommittedCompletion(t *testing.T) {
	store := &recordingEbookProgressStore{
		row: &EbookProgress{
			UserID: "1", ProfileID: testProfileID, ContentID: testEbookID, FileID: 7, Progress: 0.4,
		},
		committed: &EbookProgress{
			UserID: "1", ProfileID: testProfileID, ContentID: testEbookID, FileID: 7, Progress: 1,
		},
	}
	media := &stubMediaStore{known: map[string]*models.MediaItem{
		testEbookID: {ContentID: testEbookID, Type: mediaTypeEbook},
	}}
	h := New(Dependencies{MediaStore: media, EbookProgressStore: store})
	rec := dispatchABSWithParams(http.MethodPost, "/api/me/progress/ebook-1",
		map[string]string{"libraryItemId": testEbookID}, []byte(`{"ebookProgress":0.2}`), "1", testProfileID, h.handleSetItemProgress)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ebookProgress":1`) || !strings.Contains(rec.Body.String(), `"isFinished":true`) {
		t.Fatalf("response did not reflect committed completion: %s", rec.Body.String())
	}
}

func TestEbookProgressWireIncludesOfficialClientRequiredFields(t *testing.T) {
	updated := time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC)
	got := ebookProgressToABS(EbookProgress{UserID: "42", ContentID: testEbookID, Progress: 1, UpdatedAt: updated})
	if got[userIDKey] != "42" || got[lastUpdateKey] != updated.UnixMilli() || got["startedAt"] != updated.UnixMilli() || got["finishedAt"] != updated.UnixMilli() {
		t.Fatalf("required progress fields missing or wrong: %#v", got)
	}
}

func TestContinueToggleUsesEbookHistoryWatermark(t *testing.T) {
	store := &recordingEbookProgressStore{row: &EbookProgress{UpdatedAt: time.Now()}}
	media := &stubMediaStore{known: map[string]*models.MediaItem{
		testEbookID: {ContentID: testEbookID, Type: mediaTypeEbook},
	}}
	h := New(Dependencies{MediaStore: media, EbookProgressStore: store})
	rec := dispatchABSWithParams(http.MethodGet, "/api/me/progress/ebook-1/remove-from-continue-listening",
		map[string]string{itemIDParam: testEbookID}, nil, "1", testProfileID, h.handleRemoveFromContinueListening)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if store.hidden == nil || !*store.hidden || store.hiddenItem != testEbookID {
		t.Fatalf("hidden call = (%v, %q), want (true, ebook-1)", store.hidden, store.hiddenItem)
	}
}

func TestDeleteProgressDoesNotHideItemLookupFailure(t *testing.T) {
	media := &stubMediaStore{lookupErr: errors.New("database unavailable")}
	h := New(Dependencies{MediaStore: media, ProgressStore: &fakeProgressStore{}})
	rec := dispatchABSWithParams(http.MethodDelete, "/api/me/progress/book-1",
		map[string]string{"libraryItemId": testBookID}, nil, "1", testProfileID, h.handleDeleteItemProgress)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetAudioProgressWithoutAudioStoreReturnsNotFound(t *testing.T) {
	media := &stubMediaStore{known: map[string]*models.MediaItem{
		testBookID: {ContentID: testBookID, Type: mediaTypeAudiobook},
	}}
	h := New(Dependencies{MediaStore: media, EbookProgressStore: &recordingEbookProgressStore{}})
	rec := dispatchABSWithParams(http.MethodGet, "/api/me/progress/book-1",
		map[string]string{"libraryItemId": testBookID}, nil, "1", testProfileID, h.handleGetItemProgress)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}
