package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/diagnostics"
)

var errFakeStatusUnavailable = errors.New("status store temporarily unavailable")

// newChunkedTestHandler returns a handler whose chunk spool lives in the
// test's temp dir so parallel test runs never share state.
func newChunkedTestHandler(t *testing.T, service DiagnosticsService) *DiagnosticsHandler {
	t.Helper()
	handler := NewDiagnosticsHandler(service)
	handler.chunkSessions = newDiagnosticsChunkSessions(t.TempDir())
	return handler
}

func chunkedRouter(handler *DiagnosticsHandler) chi.Router {
	r := chi.NewRouter()
	r.Post("/uploads", handler.HandleChunkedUploadInit)
	r.Put("/uploads/{upload_id}/chunks/{chunk_index}", handler.HandleChunkedUploadChunk)
	r.Post("/uploads/{upload_id}/complete", handler.HandleChunkedUploadComplete)
	r.Delete("/uploads/{upload_id}", handler.HandleChunkedUploadAbort)
	return r
}

func doChunked(t *testing.T, router chi.Router, method, path string, body []byte, claims *auth.Claims) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer token")
	if method == http.MethodPost && strings.HasSuffix(path, "/uploads") {
		req.Header.Set("Content-Type", "application/json")
	}
	if claims != nil {
		req = req.WithContext(apimw.SetClaims(req.Context(), claims))
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func initChunkedUpload(t *testing.T, router chi.Router, bundleBytes int, claims *auth.Claims) diagnosticsChunkInitResponse {
	t.Helper()
	body := []byte(fmt.Sprintf(`{"manifest":{"ok":true},"bundle_bytes":%d}`, bundleBytes))
	rec := doChunked(t, router, http.MethodPost, "/uploads", body, claims)
	if rec.Code != http.StatusCreated {
		t.Fatalf("init status = %d, want %d; body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var resp diagnosticsChunkInitResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode init response: %v", err)
	}
	return resp
}

func TestDiagnosticsChunkedUploadHappyPath(t *testing.T) {
	service := newFakeDiagnosticsService()
	handler := newChunkedTestHandler(t, service)
	router := chunkedRouter(handler)

	bundle := bytes.Repeat([]byte("b"), int(diagnostics.UploadChunkBytes)+512)
	session := initChunkedUpload(t, router, len(bundle), accessClaims())
	if session.ChunkBytes != diagnostics.UploadChunkBytes {
		t.Fatalf("chunk_bytes = %d, want %d", session.ChunkBytes, diagnostics.UploadChunkBytes)
	}
	if session.TotalChunks != 2 {
		t.Fatalf("total_chunks = %d, want 2", session.TotalChunks)
	}

	for index := 0; index < session.TotalChunks; index++ {
		start := int64(index) * session.ChunkBytes
		end := start + session.ChunkBytes
		if end > int64(len(bundle)) {
			end = int64(len(bundle))
		}
		rec := doChunked(t, router, http.MethodPut,
			fmt.Sprintf("/uploads/%s/chunks/%d", session.UploadID, index),
			bundle[start:end], accessClaims())
		if rec.Code != http.StatusOK {
			t.Fatalf("chunk %d status = %d; body=%s", index, rec.Code, rec.Body.String())
		}
	}

	rec := doChunked(t, router, http.MethodPost, "/uploads/"+session.UploadID+"/complete", nil, accessClaims())
	if rec.Code != http.StatusCreated {
		t.Fatalf("complete status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var resp diagnostics.IngestResult
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode complete response: %v", err)
	}
	if resp.ShortID != "SILO-ABCDEF123456" {
		t.Fatalf("short_id = %q", resp.ShortID)
	}
	if service.ingestCalls != 1 {
		t.Fatalf("ingest calls = %d, want 1", service.ingestCalls)
	}
	if got := string(service.lastManifest); got != `{"ok":true}` {
		t.Fatalf("manifest passed to ingest = %q", got)
	}
	if int(service.lastBundleBytes) != len(bundle) {
		t.Fatalf("bundle bytes passed to ingest = %d, want %d", service.lastBundleBytes, len(bundle))
	}

	// The ingest slot must be free again after completion.
	release, acquired := handler.inflight.acquire(42)
	if !acquired {
		t.Fatal("in-flight slot still held after complete")
	}
	release()
}

func TestDiagnosticsChunkedUploadInitOverBundleLimit(t *testing.T) {
	service := newFakeDiagnosticsService()
	service.status.MaxBundleBytes = 1024
	handler := newChunkedTestHandler(t, service)
	router := chunkedRouter(handler)

	rec := doChunked(t, router, http.MethodPost, "/uploads",
		[]byte(`{"manifest":{"ok":true},"bundle_bytes":2048}`), accessClaims())
	assertDiagnosticsError(t, rec, http.StatusRequestEntityTooLarge, "too_large")

	// Rejected init must not leave the limiter slot held.
	release, acquired := handler.inflight.acquire(42)
	if !acquired {
		t.Fatal("in-flight slot held after rejected init")
	}
	release()
}

func TestDiagnosticsChunkedUploadInitDisabled(t *testing.T) {
	service := newFakeDiagnosticsService()
	service.status.Status = diagnostics.StatusDisabled
	handler := newChunkedTestHandler(t, service)
	router := chunkedRouter(handler)

	rec := doChunked(t, router, http.MethodPost, "/uploads",
		[]byte(`{"manifest":{"ok":true},"bundle_bytes":10}`), accessClaims())
	assertDiagnosticsError(t, rec, http.StatusForbidden, "disabled")
}

func TestDiagnosticsChunkedUploadForeignSessionRejected(t *testing.T) {
	service := newFakeDiagnosticsService()
	handler := newChunkedTestHandler(t, service)
	router := chunkedRouter(handler)

	session := initChunkedUpload(t, router, 10, accessClaims())

	other := &auth.Claims{UserID: 7, TokenType: auth.TokenTypeAccess}
	rec := doChunked(t, router, http.MethodPut, "/uploads/"+session.UploadID+"/chunks/0",
		[]byte("0123456789"), other)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign chunk status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	rec = doChunked(t, router, http.MethodPost, "/uploads/"+session.UploadID+"/complete", nil, other)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign complete status = %d, want 404", rec.Code)
	}
	// A foreign abort reports success but must not destroy the session.
	rec = doChunked(t, router, http.MethodDelete, "/uploads/"+session.UploadID, nil, other)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("foreign abort status = %d, want 204", rec.Code)
	}
	rec = doChunked(t, router, http.MethodPut, "/uploads/"+session.UploadID+"/chunks/0",
		[]byte("0123456789"), accessClaims())
	if rec.Code != http.StatusOK {
		t.Fatalf("owner chunk after foreign abort = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestDiagnosticsChunkedUploadIncompleteComplete(t *testing.T) {
	service := newFakeDiagnosticsService()
	handler := newChunkedTestHandler(t, service)
	router := chunkedRouter(handler)

	bundle := bytes.Repeat([]byte("b"), int(diagnostics.UploadChunkBytes)+512)
	session := initChunkedUpload(t, router, len(bundle), accessClaims())

	rec := doChunked(t, router, http.MethodPut, "/uploads/"+session.UploadID+"/chunks/0",
		bundle[:diagnostics.UploadChunkBytes], accessClaims())
	if rec.Code != http.StatusOK {
		t.Fatalf("chunk status = %d", rec.Code)
	}

	rec = doChunked(t, router, http.MethodPost, "/uploads/"+session.UploadID+"/complete", nil, accessClaims())
	if rec.Code != http.StatusConflict {
		t.Fatalf("incomplete complete status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if service.ingestCalls != 0 {
		t.Fatalf("ingest calls = %d, want 0", service.ingestCalls)
	}
}

func TestDiagnosticsChunkedUploadReinitReplacesPreviousSession(t *testing.T) {
	service := newFakeDiagnosticsService()
	handler := newChunkedTestHandler(t, service)
	router := chunkedRouter(handler)

	first := initChunkedUpload(t, router, 10, accessClaims())
	second := initChunkedUpload(t, router, 10, accessClaims())
	if first.UploadID == second.UploadID {
		t.Fatal("re-init returned the same session id")
	}

	// The replaced session is gone; the new one accepts chunks.
	rec := doChunked(t, router, http.MethodPut, "/uploads/"+first.UploadID+"/chunks/0",
		[]byte("0123456789"), accessClaims())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("replaced-session chunk status = %d, want 404", rec.Code)
	}
	rec = doChunked(t, router, http.MethodPut, "/uploads/"+second.UploadID+"/chunks/0",
		[]byte("0123456789"), accessClaims())
	if rec.Code != http.StatusOK {
		t.Fatalf("new-session chunk status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	rec = doChunked(t, router, http.MethodDelete, "/uploads/"+second.UploadID, nil, accessClaims())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("abort status = %d, want 204", rec.Code)
	}
}

func TestDiagnosticsChunkedCompleteBusyKeepsSessionRetryable(t *testing.T) {
	service := newFakeDiagnosticsService()
	handler := newChunkedTestHandler(t, service)
	router := chunkedRouter(handler)

	session := initChunkedUpload(t, router, 10, accessClaims())
	rec := doChunked(t, router, http.MethodPut, "/uploads/"+session.UploadID+"/chunks/0",
		[]byte("0123456789"), accessClaims())
	if rec.Code != http.StatusOK {
		t.Fatalf("chunk status = %d", rec.Code)
	}

	// Occupy the user's ingest slot: complete must answer busy without
	// consuming the spooled session.
	release, acquired := handler.inflight.acquire(42)
	if !acquired {
		t.Fatal("could not occupy in-flight slot")
	}
	rec = doChunked(t, router, http.MethodPost, "/uploads/"+session.UploadID+"/complete", nil, accessClaims())
	assertDiagnosticsError(t, rec, http.StatusServiceUnavailable, "busy")
	release()

	rec = doChunked(t, router, http.MethodPost, "/uploads/"+session.UploadID+"/complete", nil, accessClaims())
	if rec.Code != http.StatusCreated {
		t.Fatalf("retried complete status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if service.ingestCalls != 1 {
		t.Fatalf("ingest calls = %d, want 1", service.ingestCalls)
	}
}

func TestDiagnosticsChunkedUploadOversizedChunkRejected(t *testing.T) {
	service := newFakeDiagnosticsService()
	handler := newChunkedTestHandler(t, service)
	router := chunkedRouter(handler)

	bundle := bytes.Repeat([]byte("b"), int(diagnostics.UploadChunkBytes)*2)
	session := initChunkedUpload(t, router, len(bundle), accessClaims())

	oversized := bytes.Repeat([]byte("x"), int(diagnostics.UploadChunkBytes)+1)
	rec := doChunked(t, router, http.MethodPut, "/uploads/"+session.UploadID+"/chunks/0",
		oversized, accessClaims())
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized chunk status = %d, want 400 or 413; body=%s", rec.Code, rec.Body.String())
	}
	if service.ingestCalls != 0 {
		t.Fatalf("ingest calls = %d, want 0", service.ingestCalls)
	}
}

func TestDiagnosticsChunkedUploadManifestTooLarge(t *testing.T) {
	service := newFakeDiagnosticsService()
	handler := newChunkedTestHandler(t, service)
	router := chunkedRouter(handler)

	manifest := `{"pad":"` + strings.Repeat("x", int(diagnostics.MaxManifestBytes)) + `"}`
	rec := doChunked(t, router, http.MethodPost, "/uploads",
		[]byte(`{"manifest":`+manifest+`,"bundle_bytes":10}`), accessClaims())
	assertDiagnosticsError(t, rec, http.StatusRequestEntityTooLarge, "too_large")
}

func TestDiagnosticsChunkedCompleteIngestErrorMapsLikeSingleShot(t *testing.T) {
	service := newFakeDiagnosticsService()
	service.ingestErr = diagnostics.ErrStaleConsent
	handler := newChunkedTestHandler(t, service)
	router := chunkedRouter(handler)

	session := initChunkedUpload(t, router, 10, accessClaims())
	rec := doChunked(t, router, http.MethodPut, "/uploads/"+session.UploadID+"/chunks/0",
		[]byte("0123456789"), accessClaims())
	if rec.Code != http.StatusOK {
		t.Fatalf("chunk status = %d", rec.Code)
	}

	rec = doChunked(t, router, http.MethodPost, "/uploads/"+session.UploadID+"/complete", nil, accessClaims())
	assertDiagnosticsError(t, rec, http.StatusBadRequest, "stale_consent")

	// The failed session is consumed; a retry needs a fresh init and the slot
	// must be free for it.
	initChunkedUpload(t, router, 10, accessClaims())
}

func TestDiagnosticsChunkedUploadConcurrentInitsRespectSlotAtomically(t *testing.T) {
	service := newFakeDiagnosticsService()
	handler := newChunkedTestHandler(t, service)
	router := chunkedRouter(handler)

	// Fire concurrent inits for the same user. The reservation must admit at
	// most one at a time — the losers get busy — so a single account can
	// never fan out to multiple sessions and eat the global cap.
	const attempts = 8
	var wg sync.WaitGroup
	codes := make([]int, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			rec := doChunked(t, router, http.MethodPost, "/uploads",
				[]byte(`{"manifest":{"ok":true},"bundle_bytes":10}`), accessClaims())
			codes[slot] = rec.Code
		}(i)
	}
	wg.Wait()

	created := 0
	for _, code := range codes {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusServiceUnavailable:
		default:
			t.Fatalf("unexpected init status %d", code)
		}
	}
	if created < 1 {
		t.Fatal("no init succeeded")
	}
	// However the race resolved, the user must end up with at most one live
	// session and no leaked cap slots: after aborting it, a fresh init and
	// full upload must succeed.
	handler.chunkSessions.mu.Lock()
	owned := len(handler.chunkSessions.owners)
	reserved := len(handler.chunkSessions.reserved)
	handler.chunkSessions.mu.Unlock()
	if owned > 1 || reserved != 0 {
		t.Fatalf("owners = %d (want ≤1), reserved = %d (want 0)", owned, reserved)
	}

	session := initChunkedUpload(t, router, 10, accessClaims())
	rec := doChunked(t, router, http.MethodPut, "/uploads/"+session.UploadID+"/chunks/0",
		[]byte("0123456789"), accessClaims())
	if rec.Code != http.StatusOK {
		t.Fatalf("post-race chunk status = %d; body=%s", rec.Code, rec.Body.String())
	}
	rec = doChunked(t, router, http.MethodPost, "/uploads/"+session.UploadID+"/complete", nil, accessClaims())
	if rec.Code != http.StatusCreated {
		t.Fatalf("post-race complete status = %d; body=%s", rec.Code, rec.Body.String())
	}
}

func TestDiagnosticsChunkedCompleteTransientStatusErrorKeepsSession(t *testing.T) {
	service := newFakeDiagnosticsService()
	handler := newChunkedTestHandler(t, service)
	router := chunkedRouter(handler)

	session := initChunkedUpload(t, router, 10, accessClaims())
	rec := doChunked(t, router, http.MethodPut, "/uploads/"+session.UploadID+"/chunks/0",
		[]byte("0123456789"), accessClaims())
	if rec.Code != http.StatusOK {
		t.Fatalf("chunk status = %d", rec.Code)
	}

	// A transient status failure must answer 500 WITHOUT destroying the fully
	// uploaded spool; the retried complete succeeds with no re-upload.
	service.setStatusErr(errFakeStatusUnavailable)
	rec = doChunked(t, router, http.MethodPost, "/uploads/"+session.UploadID+"/complete", nil, accessClaims())
	assertDiagnosticsError(t, rec, http.StatusInternalServerError, "internal_error")

	service.setStatusErr(nil)
	rec = doChunked(t, router, http.MethodPost, "/uploads/"+session.UploadID+"/complete", nil, accessClaims())
	if rec.Code != http.StatusCreated {
		t.Fatalf("retried complete status = %d; body=%s", rec.Code, rec.Body.String())
	}
}

func TestDiagnosticsStatusAdvertisesUploadChunkBytes(t *testing.T) {
	service := newFakeDiagnosticsService()
	service.status.UploadChunkBytes = diagnostics.UploadChunkBytes
	handler := newChunkedTestHandler(t, service)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/diagnostics/status", nil)
	req.Header.Set("Authorization", "Bearer token")
	req = req.WithContext(apimw.SetClaims(req.Context(), accessClaims()))
	rec := httptest.NewRecorder()
	handler.HandleStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var payload map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	got, ok := payload["upload_chunk_bytes"].(float64)
	if !ok || int64(got) != diagnostics.UploadChunkBytes {
		t.Fatalf("upload_chunk_bytes = %v, want %d", payload["upload_chunk_bytes"], diagnostics.UploadChunkBytes)
	}
}
