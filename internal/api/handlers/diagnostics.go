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

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/diagnostics"
)

const (
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

func (h *DiagnosticsHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	userID, ok := diagnosticsUserID(w, r)
	if !ok {
		return
	}
	status, err := h.service.Status(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load diagnostics status")
		return
	}
	writeJSON(w, http.StatusOK, status)
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

func (h *DiagnosticsHandler) HandleUpload(w http.ResponseWriter, r *http.Request) {
	h.extendDiagnosticsUploadDeadlines(w, r, true)

	userID, ok := diagnosticsUserID(w, r)
	if !ok {
		claims := apimw.GetClaims(r.Context())
		if claims != nil && claims.TokenType == auth.TokenTypeAPIKey {
			h.logRejected(r.Context(), claims.UserID, "api_key_not_allowed")
		}
		return
	}

	status, err := h.service.Status(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load diagnostics status")
		return
	}
	maxBundleBytes := status.MaxBundleBytes
	if maxBundleBytes <= 0 {
		maxBundleBytes = diagnostics.DefaultMaxBundleBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBundleBytes+diagnosticsMultipartOverheadBytes)

	switch status.Status {
	case diagnostics.StatusDisabled:
		writeError(w, http.StatusForbidden, "disabled", "Diagnostics uploads are disabled")
		return
	case diagnostics.StatusStorageUnavailable:
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "Diagnostics storage is not configured")
		return
	case diagnostics.StatusAvailable:
	default:
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "Diagnostics storage is not available")
		return
	}

	release, acquired := h.inflight.acquire(userID)
	if !acquired {
		h.logRejected(r.Context(), userID, "busy")
		w.Header().Set("Retry-After", diagnosticsBusyRetryAfter)
		writeError(w, http.StatusServiceUnavailable, "busy", "Diagnostics upload capacity is busy")
		return
	}
	defer release()

	mr, err := r.MultipartReader()
	if err != nil {
		h.writeDiagnosticsMultipartError(r.Context(), userID, w, err)
		return
	}

	manifestPart, err := nextDiagnosticsPart(mr, "manifest", "application/json")
	if err != nil {
		h.writeDiagnosticsMultipartError(r.Context(), userID, w, err)
		return
	}
	manifestJSON, err := readDiagnosticsPart(manifestPart, diagnostics.MaxManifestBytes)
	_ = manifestPart.Close()
	if err != nil {
		h.writeDiagnosticsMultipartError(r.Context(), userID, w, err)
		return
	}

	bundlePart, err := nextDiagnosticsPart(mr, "bundle", diagnostics.BundleContentType)
	if err != nil {
		h.writeDiagnosticsMultipartError(r.Context(), userID, w, err)
		return
	}
	defer bundlePart.Close()

	profileID := strings.TrimSpace(r.Header.Get("X-Profile-Id"))
	var profileIDPtr *string
	if profileID != "" {
		profileIDPtr = &profileID
	}

	result, err := h.service.Ingest(
		r.Context(),
		userID,
		profileIDPtr,
		manifestJSON,
		&exactlyTwoPartBundleReader{part: bundlePart, mr: mr},
	)
	if err != nil {
		writeDiagnosticsServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func diagnosticsUserID(w http.ResponseWriter, r *http.Request) (int, bool) {
	if !hasBearerAuthorizationHeader(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authorization bearer token required")
		return 0, false
	}
	claims := apimw.GetClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return 0, false
	}
	if claims.TokenType == auth.TokenTypeAPIKey {
		writeError(w, http.StatusForbidden, "api_key_not_allowed", "API keys cannot upload diagnostics")
		return 0, false
	}
	if claims.TokenType != auth.TokenTypeAccess {
		writeError(w, http.StatusForbidden, "forbidden", "Diagnostics require a user access token")
		return 0, false
	}
	if claims.UserID <= 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return 0, false
	}
	return claims.UserID, true
}

func hasBearerAuthorizationHeader(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	if header == "" {
		return false
	}
	parts := strings.SplitN(header, " ", 2)
	return len(parts) == 2 && strings.EqualFold(parts[0], "bearer") && strings.TrimSpace(parts[1]) != ""
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

func (h *DiagnosticsHandler) writeDiagnosticsMultipartError(ctx context.Context, userID int, w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	switch {
	case errors.As(err, &maxBytesErr), errors.Is(err, errDiagnosticsPartTooLarge):
		h.logRejected(ctx, userID, "too_large")
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Diagnostics upload is too large")
	default:
		h.logRejected(ctx, userID, "invalid_bundle")
		writeError(w, http.StatusBadRequest, "invalid_bundle", "Invalid diagnostics upload")
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

func writeDiagnosticsServiceError(w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	switch {
	case errors.As(err, &maxBytesErr), errors.Is(err, diagnostics.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Diagnostics upload is too large")
	case errors.Is(err, diagnostics.ErrDisabled):
		writeError(w, http.StatusForbidden, "disabled", "Diagnostics uploads are disabled")
	case errors.Is(err, diagnostics.ErrStorageUnavailable):
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "Diagnostics storage is not configured")
	case errors.Is(err, diagnostics.ErrQuotaExceeded):
		w.Header().Set("Retry-After", diagnosticsQuotaRetryAfter)
		writeError(w, http.StatusTooManyRequests, "quota_exceeded", "Diagnostics upload quota exceeded")
	case errors.Is(err, diagnostics.ErrUnsupportedSchema):
		writeError(w, http.StatusBadRequest, "unsupported_schema", "Diagnostics schema version is not supported")
	case errors.Is(err, diagnostics.ErrDestinationMismatch):
		writeError(w, http.StatusBadRequest, "destination_mismatch", "Diagnostics destination does not match this server")
	case errors.Is(err, diagnostics.ErrStaleConsent):
		writeError(w, http.StatusBadRequest, "stale_consent", "Diagnostics consent notice is stale")
	case errors.Is(err, diagnostics.ErrArchiveMismatch):
		writeError(w, http.StatusBadRequest, "archive_mismatch", "Diagnostics archive metadata does not match")
	case errors.Is(err, diagnostics.ErrProfileMismatch):
		writeError(w, http.StatusBadRequest, "profile_mismatch", "Diagnostics profile does not match the captured report")
	case errors.Is(err, diagnostics.ErrChildProfileForbidden):
		writeError(w, http.StatusForbidden, "child_profile_forbidden", "Diagnostics cannot be attributed to a child profile")
	case errors.Is(err, diagnostics.ErrInvalidBundle):
		writeError(w, http.StatusBadRequest, "invalid_bundle", "Invalid diagnostics bundle")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Diagnostics upload failed")
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
