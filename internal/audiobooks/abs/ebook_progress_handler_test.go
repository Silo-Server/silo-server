package abs

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// recordingEbookProgressStore mirrors the merge the real store performs in
// SQL (see ebookProgressMergeSet in internal/audiobooks/abs_ebook_progress_store.go)
// so handler tests can assert the committed row, not just the patch.
type recordingEbookProgressStore struct {
	row        *EbookProgress
	patch      *EbookProgressPatch
	upserts    int
	hidden     *bool
	hiddenItem string
	// committed, when set, is returned verbatim: it stands in for a row another
	// device committed concurrently, which the handler must echo back.
	committed *EbookProgress
	rows      []EbookProgress
}

func (s *recordingEbookProgressStore) GetEbookProgress(context.Context, string, string, string) (*EbookProgress, error) {
	return s.row, nil
}

func (s *recordingEbookProgressStore) ListEbookProgress(context.Context, string, string, int) ([]EbookProgress, error) {
	return s.rows, nil
}

func (s *recordingEbookProgressStore) UpsertEbookProgress(_ context.Context, patch EbookProgressPatch) (*EbookProgress, error) {
	s.patch = &patch
	s.upserts++
	if s.committed != nil {
		return s.committed, nil
	}
	if s.row == nil && patch.FileID == nil {
		return nil, ErrEbookProgressFileRequired
	}
	merged := mergeEbookProgressPatch(s.row, patch)
	s.row = &merged
	return &merged, nil
}

// mergeEbookProgressPatch is the Go twin of ebookProgressMergeSet: unset patch
// fields keep the stored value, a stale write may not un-finish a finished row,
// and isFinished:false only resets a row that was actually finished.
func mergeEbookProgressPatch(stored *EbookProgress, patch EbookProgressPatch) EbookProgress {
	merged := EbookProgress{UserID: patch.UserID, ProfileID: patch.ProfileID, ContentID: patch.ContentID}
	if stored != nil {
		merged = *stored
	}
	storedProgress := merged.Progress
	next := storedProgress
	if patch.Progress != nil {
		next = *patch.Progress
	}
	regresses := storedProgress >= models.EbookFinishedProgressThreshold &&
		next < models.EbookFinishedProgressThreshold
	if !patch.AllowFinishedRegression && regresses {
		return merged
	}
	if patch.FileID != nil {
		merged.FileID = *patch.FileID
	}
	if patch.Location != nil {
		merged.Location = *patch.Location
	}
	if patch.ResetWhenFinished && storedProgress >= models.EbookFinishedProgressThreshold {
		merged.Progress = 0
	} else {
		merged.Progress = next
	}
	return merged
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
	if store.patch == nil || store.patch.Progress == nil || *store.patch.Progress != 1 {
		t.Fatalf("patched progress = %#v, want 1", store.patch)
	}
	if store.row.Progress != 1 {
		t.Fatalf("committed progress = %v, want 1", store.row.Progress)
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
	if store.patch == nil || !store.patch.AllowFinishedRegression || !store.patch.ResetWhenFinished {
		t.Fatalf("patch = %#v, want explicit atomic completion regression", store.patch)
	}
	if store.patch.Progress != nil {
		t.Fatalf("patch progress = %v, want the reset decided against the stored row", *store.patch.Progress)
	}
	if store.row.Progress != 0 {
		t.Fatalf("committed progress = %v, want 0", store.row.Progress)
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
	if store.patch == nil || store.patch.Progress == nil || *store.patch.Progress != 0.2 || store.patch.AllowFinishedRegression {
		t.Fatalf("upsert input = %#v, want routine autosave without regression permission", store.patch)
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

// Upstream Audiobookshelf treats isFinished:false as "un-finish this book",
// not "reset my place": only a stored row that was actually finished is cleared.
// The decision has to be made against the *stored* progress, so the handler
// hands the store a reset flag instead of a progress value it computed itself.
func TestSetEbookProgressUnfinishOnlyResetsStoredFinishedRows(t *testing.T) {
	tests := []struct {
		name     string
		stored   float64
		body     string
		want     float64
		wantSame bool
	}{
		{name: "unfinished row keeps its place", stored: 0.4, body: `{"isFinished":false}`, want: 0.4},
		{name: "finished row is cleared", stored: 0.95, body: `{"isFinished":false}`, want: 0},
		{name: "explicit progress wins", stored: 0.4, body: `{"ebookProgress":0.95,"isFinished":false}`, want: 0.95},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &recordingEbookProgressStore{row: &EbookProgress{
				UserID: "1", ProfileID: testProfileID, ContentID: testEbookID, FileID: 7, Progress: test.stored,
			}}
			media := &stubMediaStore{known: map[string]*models.MediaItem{
				testEbookID: {ContentID: testEbookID, Type: mediaTypeEbook},
			}}
			h := New(Dependencies{MediaStore: media, EbookProgressStore: store})
			rec := dispatchABSWithParams(http.MethodPost, "/api/me/progress/ebook-1",
				map[string]string{"libraryItemId": testEbookID}, []byte(test.body), "1", testProfileID, h.handleSetItemProgress)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
			}
			if store.row.Progress != test.want {
				t.Fatalf("committed progress = %v, want %v", store.row.Progress, test.want)
			}
			if store.upserts != 1 {
				t.Fatalf("store writes = %d, want a single round trip", store.upserts)
			}
			if store.patch.ResetWhenFinished == (store.patch.Progress != nil) {
				t.Fatalf("patch = %#v, want exactly one of an explicit progress or the stored-row reset", store.patch)
			}
		})
	}
}

// A page turn that only moves the reading location must not blank the progress
// another device wrote a moment earlier, so unset body fields stay nil in the
// patch and are resolved against the stored row by the store.
func TestSetEbookProgressLeavesUnsentFieldsToTheStore(t *testing.T) {
	store := &recordingEbookProgressStore{row: &EbookProgress{
		UserID: "1", ProfileID: testProfileID, ContentID: testEbookID, FileID: 7, Progress: 0.4, Location: "epubcfi(/2)",
	}}
	media := &stubMediaStore{known: map[string]*models.MediaItem{
		testEbookID: {ContentID: testEbookID, Type: mediaTypeEbook},
	}}
	h := New(Dependencies{MediaStore: media, EbookProgressStore: store})
	rec := dispatchABSWithParams(http.MethodPost, "/api/me/progress/ebook-1",
		map[string]string{"libraryItemId": testEbookID}, []byte(`{"ebookLocation":"epubcfi(/6)"}`), "1", testProfileID, h.handleSetItemProgress)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if store.patch.Progress != nil || store.patch.FileID != nil {
		t.Fatalf("patch = %#v, want only the location set", store.patch)
	}
	if store.row.Progress != 0.4 || store.row.Location != "epubcfi(/6)" || store.row.FileID != 7 {
		t.Fatalf("committed row = %#v, want progress and file preserved", store.row)
	}
}

// The first write for an item has no row to merge into and ebook_reader_progress
// requires a file reference, so the store asks for one and the handler resolves
// it under the caller's access filter before retrying.
func TestSetEbookProgressResolvesFileOnlyForTheFirstWrite(t *testing.T) {
	store := &recordingEbookProgressStore{}
	media := &ebookFilesMediaStore{
		stubMediaStore: stubMediaStore{known: map[string]*models.MediaItem{
			testEbookID: {ContentID: testEbookID, Type: mediaTypeEbook},
		}},
		files: []*models.MediaFile{
			{ID: 3, FilePath: "/books/a.pdf"},
			{ID: 4, FilePath: "/books/a.epub"},
		},
	}
	h := New(Dependencies{MediaStore: media, EbookProgressStore: store})
	rec := dispatchABSWithParams(http.MethodPost, "/api/me/progress/ebook-1",
		map[string]string{"libraryItemId": testEbookID}, []byte(`{"ebookProgress":0.1}`), "1", testProfileID, h.handleSetItemProgress)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if store.row == nil || store.row.FileID != 4 || store.row.Progress != 0.1 {
		t.Fatalf("committed row = %#v, want the EPUB file and the reported progress", store.row)
	}
	if media.fileLoads != 1 {
		t.Fatalf("media file loads = %d, want one resolution", media.fileLoads)
	}

	// The row now exists: the next write is a single statement again.
	store.upserts = 0
	media.fileLoads = 0
	rec = dispatchABSWithParams(http.MethodPost, "/api/me/progress/ebook-1",
		map[string]string{"libraryItemId": testEbookID}, []byte(`{"ebookProgress":0.2}`), "1", testProfileID, h.handleSetItemProgress)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if store.upserts != 1 || media.fileLoads != 0 {
		t.Fatalf("writes = %d, file loads = %d, want one write and no file resolution", store.upserts, media.fileLoads)
	}
}

type ebookFilesMediaStore struct {
	stubMediaStore
	files     []*models.MediaFile
	fileLoads int
}

func (s *ebookFilesMediaStore) GetMediaFiles(context.Context, string, catalog.AccessFilter) ([]*models.MediaFile, error) {
	s.fileLoads++
	return s.files, nil
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
