package handlers

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
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

const chunkedTestUser = 42

// spoolChunkedUpload opens a session for chunkedTestUser and uploads a whole
// 10-byte bundle, leaving it ready to complete.
func spoolChunkedUpload(t *testing.T, h *DiagnosticsHandler) string {
	t.Helper()
	session, err := h.InitDiagnosticChunks(context.Background(), chunkedTestUser,
		DiagnosticsChunkInitRequest{Manifest: []byte(`{"ok":true}`), BundleBytes: 10})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	body := []byte("0123456789")
	req := httptest.NewRequest(http.MethodPut, "/chunk", bytes.NewReader(body))
	if _, err := h.PutDiagnosticChunk(httptest.NewRecorder(), req, chunkedTestUser, session.UploadID, 0); err != nil {
		t.Fatalf("put chunk: %v", err)
	}
	return session.UploadID
}

func diagnosticsFailureStatus(t *testing.T, err error) (int, string) {
	t.Helper()
	failure, ok := errors.AsType[*DiagnosticsUploadFailure](err)
	if !ok {
		t.Fatalf("err = %T %v, want *DiagnosticsUploadFailure", err, err)
	}
	return failure.Status, failure.Code
}

// A complete that fails before the spool is consumed (ingest slot busy, or a
// transient status lookup failure) must leave the session intact, so the
// client retries complete without re-uploading every chunk.
func TestCompleteDiagnosticChunksKeepsSessionOnRetryableFailure(t *testing.T) {
	cases := []struct {
		name       string
		inject     func(*DiagnosticsHandler, *fakeDiagnosticsService) (restore func())
		wantStatus int
		wantCode   string
	}{
		{
			name: "ingest slot busy",
			inject: func(h *DiagnosticsHandler, _ *fakeDiagnosticsService) func() {
				release, acquired := h.inflight.acquire(chunkedTestUser)
				if !acquired {
					t.Fatal("could not occupy in-flight slot")
				}
				return release
			},
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   diagnosticsBusyCode,
		},
		{
			name: "transient status failure",
			inject: func(_ *DiagnosticsHandler, svc *fakeDiagnosticsService) func() {
				svc.setStatusErr(errFakeStatusUnavailable)
				return func() { svc.setStatusErr(nil) }
			},
			wantStatus: http.StatusInternalServerError,
			wantCode:   diagnosticsInternalCode,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newFakeDiagnosticsService()
			h := newChunkedTestHandler(t, svc)
			uploadID := spoolChunkedUpload(t, h)

			restore := tc.inject(h, svc)
			_, err := h.CompleteDiagnosticChunks(context.Background(), chunkedTestUser, uploadID, nil)
			restore()
			if status, code := diagnosticsFailureStatus(t, err); status != tc.wantStatus || code != tc.wantCode {
				t.Fatalf("failed complete = %d %q, want %d %q", status, code, tc.wantStatus, tc.wantCode)
			}

			result, err := h.CompleteDiagnosticChunks(context.Background(), chunkedTestUser, uploadID, nil)
			if err != nil {
				t.Fatalf("retried complete: %v", err)
			}
			if result.ShortID == "" || svc.ingestCalls != 1 || svc.lastBundleBytes != 10 {
				t.Fatalf("result %+v ingest calls %d bytes %d, want one ingest of the spooled 10 bytes",
					result, svc.ingestCalls, svc.lastBundleBytes)
			}
		})
	}
}

// Concurrent inits for one account must never leave it holding more than one
// session or a leaked cap reservation, however the race resolves.
func TestInitDiagnosticChunksConcurrentInitsHoldOneSession(t *testing.T) {
	svc := newFakeDiagnosticsService()
	h := newChunkedTestHandler(t, svc)

	const attempts = 8
	var wg sync.WaitGroup
	errs := make([]error, attempts)
	for i := range attempts {
		wg.Go(func() {
			_, errs[i] = h.InitDiagnosticChunks(context.Background(), chunkedTestUser,
				DiagnosticsChunkInitRequest{Manifest: []byte(`{"ok":true}`), BundleBytes: 10})
		})
	}
	wg.Wait()

	created := 0
	for _, err := range errs {
		if err == nil {
			created++
			continue
		}
		if status, code := diagnosticsFailureStatus(t, err); status != http.StatusServiceUnavailable || code != diagnosticsBusyCode {
			t.Fatalf("losing init = %d %q, want busy", status, code)
		}
	}
	if created < 1 {
		t.Fatal("no init succeeded")
	}
	h.chunkSessions.mu.Lock()
	owned, reserved := len(h.chunkSessions.owners), len(h.chunkSessions.reserved)
	h.chunkSessions.mu.Unlock()
	if owned > 1 || reserved != 0 {
		t.Fatalf("owners = %d (want ≤1), reserved = %d (want 0)", owned, reserved)
	}

	// No leaked slot: a fresh upload still completes end to end.
	uploadID := spoolChunkedUpload(t, h)
	if _, err := h.CompleteDiagnosticChunks(context.Background(), chunkedTestUser, uploadID, nil); err != nil {
		t.Fatalf("post-race complete: %v", err)
	}
}
