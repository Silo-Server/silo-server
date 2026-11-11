package handlers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/diagnostics"
)

const (
	diagnosticsInternalCode = "internal_error"
	diagnosticsDisabledCode = "disabled"
	diagnosticsBusyCode     = "busy"

	diagnosticsStatusFailureMessage = "Failed to load diagnostics status"
	diagnosticsDisabledMessage      = "Diagnostics uploads are disabled"
	diagnosticsStorageMessage       = "Diagnostics storage is not configured"
	diagnosticsTooLargeCode         = "too_large"
	diagnosticsInvalidBundleCode    = "invalid_bundle"
	diagnosticsTooLargeMessage      = "Diagnostics upload is too large"
	diagnosticsBusyMessage          = "Diagnostics upload capacity is busy"
	diagnosticsUploadErrorCode      = "upload_error"

	// Exported for native API adapters.
	DiagnosticsDisabledCode           = diagnosticsDisabledCode
	DiagnosticsStorageUnavailableCode = errCodeStorageUnavailable
	diagnosticsSessionNotFoundMessage = "Upload session not found"
	diagnosticsUploadFailedMessage    = "Diagnostics upload failed"

	diagnosticsMultipartOverheadBytes = int64(128 * 1024)
	diagnosticsBusyRetryAfter         = "5"
	diagnosticsQuotaRetryAfter        = "60"
	// diagnosticsUploadReadTimeout replaces the shared 30s server ReadTimeout
	// for this route only. Bundles are up to 10 MiB (admin-tunable higher) and
	// mobile crash uploads over slow uplinks routinely exceed 30s; without this
	// net/http aborts the read mid-stream before Ingest can return a retryable
	// error. The bound stays generous but finite so a stalled upload still can't
	// hold the connection open indefinitely.
	diagnosticsUploadReadTimeout = 10 * time.Minute
)

var (
	errDiagnosticsPartTooLarge   = errors.New("diagnostics multipart part too large")
	errDiagnosticsUnexpectedPart = errors.New("diagnostics multipart contains unexpected part")
)

type DiagnosticsService interface {
	Status(ctx context.Context, userID int) (diagnostics.Status, error)
	Ingest(ctx context.Context, userID int, profileID *string, manifestJSON []byte, bundle io.Reader) (diagnostics.IngestResult, error)
}

type DiagnosticsHandler struct {
	service       DiagnosticsService
	adminService  AdminDiagnosticsService
	inflight      *diagnosticsInFlightLimiter
	chunkSessions *diagnosticsChunkSessions
	logger        *slog.Logger
}

func NewDiagnosticsHandler(service DiagnosticsService) *DiagnosticsHandler {
	handler := &DiagnosticsHandler{
		service:       service,
		inflight:      newDiagnosticsInFlightLimiter(4),
		chunkSessions: newDiagnosticsChunkSessions(filepath.Join(os.TempDir(), "silo-diagnostics-uploads")),
		logger:        slog.Default(),
	}
	// Reclaim abandoned chunk spool bytes on a timer, not only from later API
	// traffic. The handler lives for the process, so the sweeper needs no
	// stop signal.
	handler.chunkSessions.startSweeper(nil)
	if adminService, ok := service.(AdminDiagnosticsService); ok {
		handler.adminService = adminService
	}
	return handler
}

// extendDiagnosticsUploadDeadlines lifts this request's read (and, when
// includeWrite, write) deadline to diagnosticsUploadReadTimeout. The
// server-wide ReadTimeout (30s) is too short for slow bundle/chunk uploads,
// and the WriteTimeout (120s) can expire after Ingest has already stored the
// report — the lost 201 would make the client retry an upload that
// succeeded. Shared by the single-shot upload, chunk PUT (read only), and
// chunked complete (both).
func (h *DiagnosticsHandler) extendDiagnosticsUploadDeadlines(w http.ResponseWriter, r *http.Request, includeWrite bool) {
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(time.Now().Add(diagnosticsUploadReadTimeout)); err != nil {
		h.diagnosticsLogger().WarnContext(r.Context(), "diagnostics upload read deadline not extended",
			"component", "diagnostics",
			"error", err,
		)
	}
	if !includeWrite {
		return
	}
	if err := rc.SetWriteDeadline(time.Now().Add(diagnosticsUploadReadTimeout)); err != nil {
		h.diagnosticsLogger().WarnContext(r.Context(), "diagnostics upload write deadline not extended",
			"component", "diagnostics",
			"error", err,
		)
	}
}

// UploadStatus returns the same account-specific availability and quota limits
// used by both upload transports.
func (h *DiagnosticsHandler) UploadStatus(ctx context.Context, userID int) (diagnostics.Status, error) {
	return h.service.Status(ctx, userID)
}

// ExtendUploadDeadlines keeps slow uploads within the existing finite ten-minute
// read/write budget. Structured adapters call it before consuming the stream.
func (h *DiagnosticsHandler) ExtendUploadDeadlines(w http.ResponseWriter, r *http.Request) {
	h.extendDiagnosticsUploadDeadlines(w, r, true)
}

// IngestMultipart streams the ordered manifest/bundle parts through the existing
// ingest service and shared bridge/chunk completion limiter. The caller must
// authenticate a user access token; the service validates captured attribution.
// It returns classified failures without encoding an HTTP response.
func (h *DiagnosticsHandler) IngestMultipart(w http.ResponseWriter, r *http.Request, userID int, profileID *string) (diagnostics.IngestResult, error) {
	status, err := h.service.Status(r.Context(), userID)
	if err != nil {
		return diagnostics.IngestResult{}, &DiagnosticsUploadFailure{Status: http.StatusInternalServerError, Code: diagnosticsInternalCode, Message: diagnosticsStatusFailureMessage}
	}
	maxBundleBytes := status.MaxBundleBytes
	if maxBundleBytes <= 0 {
		maxBundleBytes = diagnostics.DefaultMaxBundleBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBundleBytes+diagnosticsMultipartOverheadBytes)

	switch status.Status {
	case diagnostics.StatusDisabled:
		return diagnostics.IngestResult{}, &DiagnosticsUploadFailure{Status: http.StatusForbidden, Code: diagnosticsDisabledCode, Message: diagnosticsDisabledMessage}
	case diagnostics.StatusStorageUnavailable:
		return diagnostics.IngestResult{}, &DiagnosticsUploadFailure{Status: http.StatusServiceUnavailable, Code: errCodeStorageUnavailable, Message: diagnosticsStorageMessage}
	case diagnostics.StatusAvailable:
	default:
		return diagnostics.IngestResult{}, &DiagnosticsUploadFailure{Status: http.StatusServiceUnavailable, Code: errCodeStorageUnavailable, Message: "Diagnostics storage is not available"}
	}

	release, acquired := h.inflight.acquire(userID)
	if !acquired {
		h.logRejected(r.Context(), userID, diagnosticsBusyCode)
		return diagnostics.IngestResult{}, &DiagnosticsUploadFailure{Status: http.StatusServiceUnavailable, Code: diagnosticsBusyCode, Message: diagnosticsBusyMessage, RetryAfter: diagnosticsBusyRetryAfter}
	}
	defer release()

	mr, err := r.MultipartReader()
	if err != nil {
		return diagnostics.IngestResult{}, h.diagnosticsMultipartFailure(r.Context(), userID, err)
	}

	manifestPart, err := nextDiagnosticsPart(mr, "manifest", "application/json")
	if err != nil {
		return diagnostics.IngestResult{}, h.diagnosticsMultipartFailure(r.Context(), userID, err)
	}
	manifestJSON, err := readDiagnosticsPart(manifestPart, diagnostics.MaxManifestBytes)
	_ = manifestPart.Close()
	if err != nil {
		return diagnostics.IngestResult{}, h.diagnosticsMultipartFailure(r.Context(), userID, err)
	}

	bundlePart, err := nextDiagnosticsPart(mr, "bundle", diagnostics.BundleContentType)
	if err != nil {
		return diagnostics.IngestResult{}, h.diagnosticsMultipartFailure(r.Context(), userID, err)
	}
	defer bundlePart.Close()

	result, err := h.service.Ingest(
		r.Context(),
		userID,
		profileID,
		manifestJSON,
		&exactlyTwoPartBundleReader{part: bundlePart, mr: mr},
	)
	if err != nil {
		return diagnostics.IngestResult{}, diagnosticsServiceFailure(err)
	}
	return result, nil
}

func nextDiagnosticsPart(mr *multipart.Reader, expectedName, expectedContentType string) (*multipart.Part, error) {
	part, err := mr.NextPart()
	if err != nil {
		return nil, err
	}
	// Reject a mismatched part without calling part.Close(): Close drains the
	// unread part body first, so a max-size wrongly-named/typed first part would
	// stream up to the bundle limit — holding the per-user/global in-flight slot —
	// before we return 400. Abandoning the part returns immediately; net/http then
	// discards only a small bounded prefix of the request before closing the
	// connection, so malformed uploads fail promptly under load.
	if part.FormName() != expectedName {
		return nil, errDiagnosticsUnexpectedPart
	}
	if !diagnosticsContentTypeMatches(part.Header.Get("Content-Type"), expectedContentType) {
		return nil, errDiagnosticsUnexpectedPart
	}
	return part, nil
}

func diagnosticsContentTypeMatches(raw, expected string) bool {
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return false
	}
	return strings.EqualFold(mediaType, expected)
}

func readDiagnosticsPart(part *multipart.Part, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(part, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errDiagnosticsPartTooLarge
	}
	return data, nil
}

type exactlyTwoPartBundleReader struct {
	part    *multipart.Part
	mr      *multipart.Reader
	checked bool
}

func (r *exactlyTwoPartBundleReader) Read(p []byte) (int, error) {
	n, err := r.part.Read(p)
	if errors.Is(err, io.EOF) && !r.checked {
		r.checked = true
		next, nextErr := r.mr.NextPart()
		if errors.Is(nextErr, io.EOF) {
			return n, io.EOF
		}
		if nextErr != nil {
			return n, nextErr
		}
		_ = next.Close()
		return n, errDiagnosticsUnexpectedPart
	}
	return n, err
}

func (h *DiagnosticsHandler) diagnosticsMultipartFailure(ctx context.Context, userID int, err error) error {
	_, maxBytesExceeded := errors.AsType[*http.MaxBytesError](err)
	switch {
	case maxBytesExceeded, errors.Is(err, errDiagnosticsPartTooLarge):
		h.logRejected(ctx, userID, diagnosticsTooLargeCode)
		return &DiagnosticsUploadFailure{Status: http.StatusRequestEntityTooLarge, Code: diagnosticsTooLargeCode, Message: diagnosticsTooLargeMessage}
	default:
		h.logRejected(ctx, userID, diagnosticsInvalidBundleCode)
		return &DiagnosticsUploadFailure{Status: http.StatusBadRequest, Code: diagnosticsInvalidBundleCode, Message: "Invalid diagnostics upload"}
	}
}

func (h *DiagnosticsHandler) logRejected(ctx context.Context, userID int, reason string) {
	logger := h.logger
	if logger == nil {
		logger = slog.Default()
	}
	args := []any{
		"component", "diagnostics",
		"result", "rejected",
		"reason", reason,
	}
	if userID > 0 {
		args = append(args, "user_id", userID)
	}
	logger.InfoContext(ctx, "diagnostic report rejected", args...)
}

// DiagnosticsUploadFailure preserves the bridge's public error classification.
// The v2 adapter translates it into its catalogued Problem Details response.
type DiagnosticsUploadFailure struct {
	Status     int
	Code       string
	Message    string
	RetryAfter string
}

func (e *DiagnosticsUploadFailure) Error() string { return e.Message }

func diagnosticsServiceFailure(err error) error {
	_, maxBytesExceeded := errors.AsType[*http.MaxBytesError](err)
	switch {
	case maxBytesExceeded, errors.Is(err, diagnostics.ErrTooLarge):
		return &DiagnosticsUploadFailure{Status: http.StatusRequestEntityTooLarge, Code: diagnosticsTooLargeCode, Message: diagnosticsTooLargeMessage}
	case errors.Is(err, diagnostics.ErrDisabled):
		return &DiagnosticsUploadFailure{Status: http.StatusForbidden, Code: diagnosticsDisabledCode, Message: diagnosticsDisabledMessage}
	case errors.Is(err, diagnostics.ErrStorageUnavailable):
		return &DiagnosticsUploadFailure{Status: http.StatusServiceUnavailable, Code: errCodeStorageUnavailable, Message: diagnosticsStorageMessage}
	case errors.Is(err, diagnostics.ErrQuotaExceeded):
		return &DiagnosticsUploadFailure{Status: http.StatusTooManyRequests, Code: "quota_exceeded", Message: "Diagnostics upload quota exceeded", RetryAfter: diagnosticsQuotaRetryAfter}
	case errors.Is(err, diagnostics.ErrUnsupportedSchema):
		return &DiagnosticsUploadFailure{Status: http.StatusBadRequest, Code: "unsupported_schema", Message: "Diagnostics schema version is not supported"}
	case errors.Is(err, diagnostics.ErrDestinationMismatch):
		return &DiagnosticsUploadFailure{Status: http.StatusBadRequest, Code: "destination_mismatch", Message: "Diagnostics destination does not match this server"}
	case errors.Is(err, diagnostics.ErrStaleConsent):
		return &DiagnosticsUploadFailure{Status: http.StatusBadRequest, Code: "stale_consent", Message: "Diagnostics consent notice is stale"}
	case errors.Is(err, diagnostics.ErrArchiveMismatch):
		return &DiagnosticsUploadFailure{Status: http.StatusBadRequest, Code: "archive_mismatch", Message: "Diagnostics archive metadata does not match"}
	case errors.Is(err, diagnostics.ErrProfileMismatch):
		return &DiagnosticsUploadFailure{Status: http.StatusBadRequest, Code: "profile_mismatch", Message: "Diagnostics profile does not match the captured report"}
	case errors.Is(err, diagnostics.ErrChildProfileForbidden):
		return &DiagnosticsUploadFailure{Status: http.StatusForbidden, Code: "child_profile_forbidden", Message: "Diagnostics cannot be attributed to a child profile"}
	case errors.Is(err, diagnostics.ErrInvalidBundle):
		return &DiagnosticsUploadFailure{Status: http.StatusBadRequest, Code: diagnosticsInvalidBundleCode, Message: "Invalid diagnostics bundle"}
	default:
		return &DiagnosticsUploadFailure{Status: http.StatusInternalServerError, Code: diagnosticsInternalCode, Message: diagnosticsUploadFailedMessage}
	}
}

type diagnosticsInFlightLimiter struct {
	mu     sync.Mutex
	active map[int]struct{}
	global chan struct{}
}

func newDiagnosticsInFlightLimiter(globalLimit int) *diagnosticsInFlightLimiter {
	if globalLimit <= 0 {
		globalLimit = 1
	}
	return &diagnosticsInFlightLimiter{
		active: make(map[int]struct{}),
		global: make(chan struct{}, globalLimit),
	}
}

func (l *diagnosticsInFlightLimiter) acquire(userID int) (func(), bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.active[userID]; ok {
		return nil, false
	}
	select {
	case l.global <- struct{}{}:
		l.active[userID] = struct{}{}
	default:
		return nil, false
	}
	return func() {
		l.mu.Lock()
		delete(l.active, userID)
		l.mu.Unlock()
		<-l.global
	}, true
}
