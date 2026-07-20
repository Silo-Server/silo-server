package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
)

// fakeReaderFontStore is an in-memory ReaderFontStore keyed by font ID. Each
// method bumps a call counter so tests can assert the store was never
// touched (e.g. when a handler must reject a request before reaching the
// store or filesystem).
type fakeReaderFontStore struct {
	fonts  map[int64]ReaderFont
	nextID int64

	listCalls   int
	countCalls  int
	insertCalls int
	getCalls    int
	deleteCalls int
}

func newFakeReaderFontStore() *fakeReaderFontStore {
	return &fakeReaderFontStore{fonts: map[int64]ReaderFont{}}
}

func (f *fakeReaderFontStore) totalCalls() int {
	return f.listCalls + f.countCalls + f.insertCalls + f.getCalls + f.deleteCalls
}

func (f *fakeReaderFontStore) List(_ context.Context, userID int, profileID string) ([]ReaderFont, error) {
	f.listCalls++
	var out []ReaderFont
	for _, font := range f.fonts {
		if font.UserID == userID && font.ProfileID == profileID {
			out = append(out, font)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeReaderFontStore) Count(ctx context.Context, userID int, profileID string) (int, error) {
	f.countCalls++
	var out []ReaderFont
	for _, font := range f.fonts {
		if font.UserID == userID && font.ProfileID == profileID {
			out = append(out, font)
		}
	}
	return len(out), nil
}

func (f *fakeReaderFontStore) Insert(_ context.Context, font ReaderFont) (ReaderFont, error) {
	f.insertCalls++
	f.nextID++
	font.ID = f.nextID
	font.CreatedAt = time.Now().UTC()
	f.fonts[font.ID] = font
	return font, nil
}

func (f *fakeReaderFontStore) Get(_ context.Context, userID int, profileID string, id int64) (*ReaderFont, error) {
	f.getCalls++
	font, ok := f.fonts[id]
	if !ok || font.UserID != userID || font.ProfileID != profileID {
		return nil, nil
	}
	return &font, nil
}

func (f *fakeReaderFontStore) Delete(_ context.Context, userID int, profileID string, id int64) (bool, error) {
	f.deleteCalls++
	font, ok := f.fonts[id]
	if !ok || font.UserID != userID || font.ProfileID != profileID {
		return false, nil
	}
	delete(f.fonts, id)
	return true, nil
}

// newReaderFontUploadRequest builds a POST request with a real multipart
// body carrying a single "font" field, authenticated as userID/profileID.
func newReaderFontUploadRequest(t *testing.T, userID int, profileID, filename string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("font", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write multipart content: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/ebooks/reader-fonts", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req.WithContext(readerFontAuthContext(userID, profileID))
}

func readerFontAuthContext(userID int, profileID string) context.Context {
	ctx := apimw.SetClaims(context.Background(), &auth.Claims{
		UserID:    userID,
		Role:      "user",
		TokenType: auth.TokenTypeAccess,
	})
	return apimw.SetProfileID(ctx, profileID)
}

func withReaderFontIDParam(req *http.Request, fontID int64) *http.Request {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("font_id", strconv.FormatInt(fontID, 10))
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

func decodeReaderFontError(t *testing.T, rr *httptest.ResponseRecorder) errorResponse {
	t.Helper()
	var errResp errorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode error response: %v; body = %s", err, rr.Body.String())
	}
	return errResp
}

func TestReaderFontUploadValidatesMagicBytes(t *testing.T) {
	dir := t.TempDir()
	store := newFakeReaderFontStore()
	h := &ReaderFontsHandler{Store: store, Dir: dir}

	validBody := append([]byte("wOF2"), bytes.Repeat([]byte{0x00}, 32)...)
	req := newReaderFontUploadRequest(t, 1, "profile-a", "Literata.woff2", validBody)
	rr := httptest.NewRecorder()
	h.HandleUpload(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rr.Code, rr.Body.String())
	}
	var font ReaderFont
	if err := json.Unmarshal(rr.Body.Bytes(), &font); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if font.ID == 0 {
		t.Fatal("expected a non-zero font ID")
	}
	if font.Name != "Literata" {
		t.Fatalf("name = %q, want Literata", font.Name)
	}
	if font.Filename != "Literata.woff2" {
		t.Fatalf("filename = %q, want Literata.woff2", font.Filename)
	}

	stored, ok := store.fonts[font.ID]
	if !ok {
		t.Fatal("expected font metadata to be persisted")
	}
	if stored.UserID != 1 || stored.ProfileID != "profile-a" {
		t.Fatalf("stored scope = user %d profile %q, want user 1 profile-a", stored.UserID, stored.ProfileID)
	}
	if stored.Format != "woff2" {
		t.Fatalf("format = %q, want woff2", stored.Format)
	}

	blobPath := readerFontBlobPath(dir, 1, "profile-a", font.ID, "woff2")
	if _, err := os.Stat(blobPath); err != nil {
		t.Fatalf("expected font file on disk at %s: %v", blobPath, err)
	}

	// A body that doesn't match any known font magic bytes must be rejected
	// with 415 and must leave neither a metadata row nor a file behind.
	rejectStore := newFakeReaderFontStore()
	rejectDir := t.TempDir()
	rejectHandler := &ReaderFontsHandler{Store: rejectStore, Dir: rejectDir}

	badBody := append([]byte("GIF8"), bytes.Repeat([]byte{0x00}, 16)...)
	badReq := newReaderFontUploadRequest(t, 1, "profile-a", "not-a-font.gif", badBody)
	badRR := httptest.NewRecorder()
	rejectHandler.HandleUpload(badRR, badReq)

	if badRR.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415; body = %s", badRR.Code, badRR.Body.String())
	}
	if errResp := decodeReaderFontError(t, badRR); errResp.Error != "unsupported_type" {
		t.Fatalf("error code = %q, want unsupported_type", errResp.Error)
	}
	if len(rejectStore.fonts) != 0 {
		t.Fatalf("expected no font metadata persisted, got %+v", rejectStore.fonts)
	}
	entries, err := os.ReadDir(rejectDir)
	if err != nil {
		t.Fatalf("read reject dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no files written to disk, got %+v", entries)
	}
}

func TestReaderFontUploadEnforcesCaps(t *testing.T) {
	t.Run("limit reached", func(t *testing.T) {
		dir := t.TempDir()
		store := newFakeReaderFontStore()
		for i := 0; i < readerFontMaxPerProfile; i++ {
			store.fonts[int64(i+1)] = ReaderFont{
				ID:        int64(i + 1),
				UserID:    1,
				ProfileID: "profile-a",
				Name:      "Font",
				Filename:  "font.woff2",
				Format:    "woff2",
				CreatedAt: time.Now().UTC(),
			}
		}
		store.nextID = readerFontMaxPerProfile
		h := &ReaderFontsHandler{Store: store, Dir: dir}

		body := append([]byte("wOF2"), bytes.Repeat([]byte{0x00}, 32)...)
		req := newReaderFontUploadRequest(t, 1, "profile-a", "eleventh.woff2", body)
		rr := httptest.NewRecorder()
		h.HandleUpload(rr, req)

		if rr.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409; body = %s", rr.Code, rr.Body.String())
		}
		if errResp := decodeReaderFontError(t, rr); errResp.Error != "limit_reached" {
			t.Fatalf("error code = %q, want limit_reached", errResp.Error)
		}
		if len(store.fonts) != readerFontMaxPerProfile {
			t.Fatalf("expected no new font persisted, got %d fonts", len(store.fonts))
		}
	})

	t.Run("body too large", func(t *testing.T) {
		dir := t.TempDir()
		store := newFakeReaderFontStore()
		h := &ReaderFontsHandler{Store: store, Dir: dir}

		oversized := append([]byte("wOF2"), bytes.Repeat([]byte{0x00}, readerFontMaxBytes+1024)...)
		req := newReaderFontUploadRequest(t, 1, "profile-a", "huge.woff2", oversized)
		rr := httptest.NewRecorder()
		h.HandleUpload(rr, req)

		if rr.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413; body = %s", rr.Code, rr.Body.String())
		}
		if errResp := decodeReaderFontError(t, rr); errResp.Error != "too_large" {
			t.Fatalf("error code = %q, want too_large", errResp.Error)
		}
		if len(store.fonts) != 0 {
			t.Fatalf("expected no font persisted, got %+v", store.fonts)
		}
	})
}

func TestReaderFontListScopedToProfile(t *testing.T) {
	store := newFakeReaderFontStore()
	store.fonts[1] = ReaderFont{ID: 1, UserID: 1, ProfileID: "a", Name: "Font A1", Filename: "a1.woff2", Format: "woff2"}
	store.fonts[2] = ReaderFont{ID: 2, UserID: 1, ProfileID: "a", Name: "Font A2", Filename: "a2.woff2", Format: "woff2"}
	store.fonts[3] = ReaderFont{ID: 3, UserID: 1, ProfileID: "b", Name: "Font B1", Filename: "b1.woff2", Format: "woff2"}
	store.nextID = 3

	h := &ReaderFontsHandler{Store: store, Dir: t.TempDir()}

	req := httptest.NewRequest(http.MethodGet, "/ebooks/reader-fonts", nil)
	req = req.WithContext(readerFontAuthContext(1, "a"))
	rr := httptest.NewRecorder()
	h.HandleList(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
	var resp readerFontsListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Fonts) != 2 {
		t.Fatalf("fonts = %+v, want 2 fonts scoped to profile a", resp.Fonts)
	}
	for _, font := range resp.Fonts {
		if font.Name == "Font B1" {
			t.Fatalf("list leaked profile b's font: %+v", resp.Fonts)
		}
	}
}

func TestReaderFontServeAndDeleteAuthorize(t *testing.T) {
	dir := t.TempDir()
	store := newFakeReaderFontStore()
	h := &ReaderFontsHandler{Store: store, Dir: dir}

	// Upload two fonts as profile a: one will be deleted, the other kept
	// around to exercise the authorized-GET path afterward.
	uploadBody := append([]byte("wOF2"), bytes.Repeat([]byte{0x00}, 32)...)

	uploadReq1 := newReaderFontUploadRequest(t, 1, "profile-a", "to-delete.woff2", uploadBody)
	uploadRR1 := httptest.NewRecorder()
	h.HandleUpload(uploadRR1, uploadReq1)
	if uploadRR1.Code != http.StatusCreated {
		t.Fatalf("setup upload 1 status = %d, want 201; body = %s", uploadRR1.Code, uploadRR1.Body.String())
	}
	var font1 ReaderFont
	if err := json.Unmarshal(uploadRR1.Body.Bytes(), &font1); err != nil {
		t.Fatalf("decode upload 1 response: %v", err)
	}

	uploadReq2 := newReaderFontUploadRequest(t, 1, "profile-a", "to-keep.woff2", uploadBody)
	uploadRR2 := httptest.NewRecorder()
	h.HandleUpload(uploadRR2, uploadReq2)
	if uploadRR2.Code != http.StatusCreated {
		t.Fatalf("setup upload 2 status = %d, want 201; body = %s", uploadRR2.Code, uploadRR2.Body.String())
	}
	var font2 ReaderFont
	if err := json.Unmarshal(uploadRR2.Body.Bytes(), &font2); err != nil {
		t.Fatalf("decode upload 2 response: %v", err)
	}

	// GET the first font as a different profile: must 404, not leak
	// existence of a font owned by another profile.
	wrongProfileReq := httptest.NewRequest(http.MethodGet, "/ebooks/reader-fonts/"+strconv.FormatInt(font1.ID, 10)+"/file", nil)
	wrongProfileReq = wrongProfileReq.WithContext(readerFontAuthContext(1, "profile-b"))
	wrongProfileReq = withReaderFontIDParam(wrongProfileReq, font1.ID)
	wrongProfileRR := httptest.NewRecorder()
	h.HandleServeFile(wrongProfileRR, wrongProfileReq)
	if wrongProfileRR.Code != http.StatusNotFound {
		t.Fatalf("cross-profile GET status = %d, want 404; body = %s", wrongProfileRR.Code, wrongProfileRR.Body.String())
	}

	// DELETE the first font as its owning profile: 204, row and file gone.
	deleteReq := httptest.NewRequest(http.MethodDelete, "/ebooks/reader-fonts/"+strconv.FormatInt(font1.ID, 10), nil)
	deleteReq = deleteReq.WithContext(readerFontAuthContext(1, "profile-a"))
	deleteReq = withReaderFontIDParam(deleteReq, font1.ID)
	deleteRR := httptest.NewRecorder()
	h.HandleDelete(deleteRR, deleteReq)
	if deleteRR.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204; body = %s", deleteRR.Code, deleteRR.Body.String())
	}
	if _, ok := store.fonts[font1.ID]; ok {
		t.Fatal("expected font row to be removed after delete")
	}
	deletedPath := readerFontBlobPath(dir, 1, "profile-a", font1.ID, "woff2")
	if _, err := os.Stat(deletedPath); !os.IsNotExist(err) {
		t.Fatalf("expected font file removed from disk, stat err = %v", err)
	}

	// GET the surviving font as its owning profile: 200 with the expected
	// headers and content type.
	getReq := httptest.NewRequest(http.MethodGet, "/ebooks/reader-fonts/"+strconv.FormatInt(font2.ID, 10)+"/file", nil)
	getReq = getReq.WithContext(readerFontAuthContext(1, "profile-a"))
	getReq = withReaderFontIDParam(getReq, font2.ID)
	getRR := httptest.NewRecorder()
	h.HandleServeFile(getRR, getReq)

	if getRR.Code != http.StatusOK {
		t.Fatalf("owned GET status = %d, want 200; body = %s", getRR.Code, getRR.Body.String())
	}
	if ct := getRR.Header().Get("Content-Type"); ct != "font/woff2" {
		t.Fatalf("Content-Type = %q, want font/woff2", ct)
	}
	if cc := getRR.Header().Get("Cache-Control"); cc != "private, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q, want private, max-age=31536000, immutable", cc)
	}
	if !bytes.Equal(getRR.Body.Bytes(), uploadBody) {
		t.Fatal("served font content does not match the uploaded content")
	}

	blobPath := filepath.Join(dir, "1", "profile-a", strconv.FormatInt(font2.ID, 10)+".woff2")
	if _, err := os.Stat(blobPath); err != nil {
		t.Fatalf("expected surviving font file on disk at %s: %v", blobPath, err)
	}
}

// TestReaderFontRejectsTraversalProfileID is a regression test for a path
// traversal vulnerability: profile_id is populated from the client-supplied
// X-Profile-Id header (RequireProfile only checks it is non-empty) and used
// to build filesystem paths for upload, serve, and delete. A profile ID
// containing path separators or ".." components must be rejected with 400
// before any store or filesystem operation happens, so a request such as
// `X-Profile-Id: ../../../../tmp/evil` cannot escape the fonts directory.
func TestReaderFontRejectsTraversalProfileID(t *testing.T) {
	maliciousProfileIDs := []string{
		"../../tmp/evil",
		"a/b",
		"..",
	}

	for _, profileID := range maliciousProfileIDs {
		t.Run(profileID, func(t *testing.T) {
			dir := t.TempDir()
			store := newFakeReaderFontStore()
			h := &ReaderFontsHandler{Store: store, Dir: dir}

			// Upload.
			body := append([]byte("wOF2"), bytes.Repeat([]byte{0x00}, 32)...)
			uploadReq := newReaderFontUploadRequest(t, 1, profileID, "evil.woff2", body)
			uploadRR := httptest.NewRecorder()
			h.HandleUpload(uploadRR, uploadReq)
			if uploadRR.Code != http.StatusBadRequest {
				t.Fatalf("upload status = %d, want 400; body = %s", uploadRR.Code, uploadRR.Body.String())
			}
			if errResp := decodeReaderFontError(t, uploadRR); errResp.Error != "bad_request" {
				t.Fatalf("upload error code = %q, want bad_request", errResp.Error)
			}

			// Serve.
			serveReq := httptest.NewRequest(http.MethodGet, "/ebooks/reader-fonts/1/file", nil)
			serveReq = serveReq.WithContext(readerFontAuthContext(1, profileID))
			serveReq = withReaderFontIDParam(serveReq, 1)
			serveRR := httptest.NewRecorder()
			h.HandleServeFile(serveRR, serveReq)
			if serveRR.Code != http.StatusBadRequest {
				t.Fatalf("serve status = %d, want 400; body = %s", serveRR.Code, serveRR.Body.String())
			}
			if errResp := decodeReaderFontError(t, serveRR); errResp.Error != "bad_request" {
				t.Fatalf("serve error code = %q, want bad_request", errResp.Error)
			}

			// Delete.
			deleteReq := httptest.NewRequest(http.MethodDelete, "/ebooks/reader-fonts/1", nil)
			deleteReq = deleteReq.WithContext(readerFontAuthContext(1, profileID))
			deleteReq = withReaderFontIDParam(deleteReq, 1)
			deleteRR := httptest.NewRecorder()
			h.HandleDelete(deleteRR, deleteReq)
			if deleteRR.Code != http.StatusBadRequest {
				t.Fatalf("delete status = %d, want 400; body = %s", deleteRR.Code, deleteRR.Body.String())
			}
			if errResp := decodeReaderFontError(t, deleteRR); errResp.Error != "bad_request" {
				t.Fatalf("delete error code = %q, want bad_request", errResp.Error)
			}

			// The store must never have been reached: rejection happens
			// before any List/Count/Insert/Get/Delete call.
			if calls := store.totalCalls(); calls != 0 {
				t.Fatalf("expected store to be untouched, got %d calls (%+v)", calls, store)
			}

			// Nothing must have been written to disk under the fonts dir.
			var found []string
			walkErr := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if path != dir {
					found = append(found, path)
				}
				return nil
			})
			if walkErr != nil {
				t.Fatalf("walk fonts dir: %v", walkErr)
			}
			if len(found) != 0 {
				t.Fatalf("expected no files written to fonts dir, found %+v", found)
			}
		})
	}
}
