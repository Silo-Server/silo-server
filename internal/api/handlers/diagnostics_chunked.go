package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/diagnostics"
	"github.com/Silo-Server/silo-server/internal/uploads"
)

// Chunked diagnostics upload: a fallback path for bundles that a reverse proxy
// in front of Silo refuses as a single request (nginx's default
// client_max_body_size is 1 MiB; /diagnostics/status advertises bundles up to
// 10 MiB). The client:
//
//  1. POST   /diagnostics/reports/uploads                      {manifest, bundle_bytes}
//  2. PUT    /diagnostics/reports/uploads/{id}/chunks/{index}  raw bundle bytes, ≤ upload_chunk_bytes each
//  3. POST   /diagnostics/reports/uploads/{id}/complete        → same 201 as the single-shot upload
//     DELETE /diagnostics/reports/uploads/{id}                 best-effort abandon
//
// The assembled bundle goes through the exact same Ingest path as the
// single-shot endpoint, so every content check (manifest contract, archive
// sha/bytes/entries, quotas, profile attribution) applies identically. The
// manifest is validated only for size at init; Ingest judges it at complete.
//
// Sessions spool to local disk and expire after diagnosticsChunkSessionTTL of
// inactivity (chunk arrivals refresh the deadline). They do NOT hold the
// shared in-flight ingest slot while chunks stream in — only complete
// acquires it, for the ingest itself — so a client that dies mid-upload
// leaves nothing behind but spool bytes the TTL sweep reclaims, and never
// blocks its user's later uploads. Concurrency is bounded by one session per
// user (a new init replaces the previous session) plus a global session cap,
// both reserved atomically in init.
//
// Session state (ownership map + spool files) is process-local. That is fine
// for the integrated single-process server this feature targets; a
// multi-replica API deployment would need sticky routing for the few requests
// of one chunked upload, or a chunk PUT landing on another replica answers
// 404 and the client restarts from init.
const (
	// diagnosticsChunkSessionTTL bounds how long an in-progress chunked upload
	// may sit idle before its spooled bytes are reclaimed. Generous enough for
	// a slow uplink to push 10 MiB in 768 KiB chunks, small enough that
	// abandoned sessions don't hold disk for long.
	diagnosticsChunkSessionTTL = 15 * time.Minute
	// diagnosticsChunkSessionCap bounds concurrent chunked sessions across all
	// users, capping worst-case spool disk at cap × max_bundle_bytes
	// (≈ 160 MiB at defaults).
	diagnosticsChunkSessionCap = 16
)

type diagnosticsChunkInitRequest = DiagnosticsChunkInitRequest

type DiagnosticsChunkInitRequest struct {
	// Manifest is the same part-1 manifest the multipart endpoint takes,
	// embedded verbatim. Deferring all content validation to Ingest keeps one
	// authority for what a valid manifest is.
	Manifest json.RawMessage `json:"manifest"`
	// BundleBytes is the exact assembled bundle size the client will upload.
	// Declared up front so over-limit uploads fail at init, before any chunk
	// bytes are spent.
	BundleBytes int64 `json:"bundle_bytes"`
}

type diagnosticsChunkInitResponse = DiagnosticsChunkInitResponse

type DiagnosticsChunkInitResponse struct {
	UploadID    string `json:"upload_id"`
	ChunkBytes  int64  `json:"chunk_bytes"`
	TotalChunks int    `json:"total_chunks"`
	ExpiresAt   string `json:"expires_at"`
}

type DiagnosticsChunkStateResponse struct {
	ReceivedChunks int `json:"received_chunks"`
	TotalChunks    int `json:"total_chunks"`
}

// diagnosticsChunkSessions pairs the generic upload session manager with the
// per-session diagnostics state it does not track: the manifest captured at
// init and the owning user (sessions must not be readable or writable across
// accounts).
type diagnosticsChunkSessions struct {
	manager *uploads.Manager

	mu     sync.Mutex
	owners map[string]diagnosticsChunkOwner
	byUser map[int]string
	// reserved counts init calls that have claimed a cap slot but not yet
	// registered their created session in owners. Counting reservations and
	// registrations together makes the per-user + global-cap admission atomic:
	// concurrent inits cannot each pass the checks before any of them
	// registers.
	reserved map[int]struct{}
}

type diagnosticsChunkOwner struct {
	userID   int
	manifest []byte
}

func newDiagnosticsChunkSessions(spoolDir string) *diagnosticsChunkSessions {
	sessions := &diagnosticsChunkSessions{
		manager: uploads.NewManager(uploads.ManagerOptions{
			RootDir:      spoolDir,
			TTL:          diagnosticsChunkSessionTTL,
			MaxChunkSize: diagnostics.UploadChunkBytes,
			// MaxSize is enforced per-init against the live max_bundle_bytes
			// setting; the manager-level bound is just a hard backstop.
			MaxSize: 256 << 20,
		}),
		owners:   make(map[string]diagnosticsChunkOwner),
		byUser:   make(map[int]string),
		reserved: make(map[int]struct{}),
	}
	// A restart leaves the previous process's spool directories on disk with
	// no session map entry to ever expire them; reclaim them now.
	sessions.manager.ReclaimOrphanedDirs()
	return sessions
}

// startSweeper begins the timed expiry sweep. Split from the constructor so
// tests can run without background goroutines.
func (s *diagnosticsChunkSessions) startSweeper(stop <-chan struct{}) {
	s.manager.StartExpirySweeper(time.Minute, stop)
}

// owner returns the session owner entry when id belongs to userID.
func (s *diagnosticsChunkSessions) owner(id string, userID int) (diagnosticsChunkOwner, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, ok := s.owners[id]
	if !ok || owner.userID != userID {
		return diagnosticsChunkOwner{}, false
	}
	return owner, true
}

// reserve atomically claims the user's session slot and a global cap slot,
// evicting the user's own previous session first (its id is returned for the
// caller to cancel outside the lock — the caller cancels it whether or not
// admission succeeds, since the eviction already happened). A false capOK
// means the cap is full, or a concurrent init by the same user holds an
// unfinished reservation — that racing call must not create a second session.
//
// The cap counts live sessions, unfinished reservations, AND the manager's
// detached-writer sessions: a canceled session whose slow chunk writer is
// still draining holds real disk and a connection until the writer's read
// deadline, so a cancel-and-reinit loop must stall at the cap rather than
// stack unbounded live writers behind it.
func (s *diagnosticsChunkSessions) reserve(userID int) (previousID string, capOK bool) {
	detached := s.manager.DetachedWriterSessions()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, inFlight := s.reserved[userID]; inFlight {
		return "", false
	}
	if id, ok := s.byUser[userID]; ok {
		previousID = id
		delete(s.owners, id)
		delete(s.byUser, userID)
	}
	if len(s.owners)+len(s.reserved)+detached >= diagnosticsChunkSessionCap {
		return previousID, false
	}
	s.reserved[userID] = struct{}{}
	return previousID, true
}

// commit registers the created session under an existing reservation.
func (s *diagnosticsChunkSessions) commit(userID int, id string, manifest []byte) {
	s.mu.Lock()
	delete(s.reserved, userID)
	s.owners[id] = diagnosticsChunkOwner{userID: userID, manifest: manifest}
	s.byUser[userID] = id
	s.mu.Unlock()
}

// unreserve rolls back a reservation whose session creation failed.
func (s *diagnosticsChunkSessions) unreserve(userID int) {
	s.mu.Lock()
	delete(s.reserved, userID)
	s.mu.Unlock()
}

// drop removes the owner entry. Idempotent; the manager session is the
// caller's to cancel.
func (s *diagnosticsChunkSessions) drop(id string) {
	s.mu.Lock()
	owner, ok := s.owners[id]
	delete(s.owners, id)
	if ok && s.byUser[owner.userID] == id {
		delete(s.byUser, owner.userID)
	}
	s.mu.Unlock()
}

// sweepExpired drops owner entries whose manager session has expired or is
// gone, so the owner map (and the cap headroom it counts against) doesn't
// leak alongside the manager's own spool cleanup. Called from init, the only
// entry point that needs the freed headroom.
func (s *diagnosticsChunkSessions) sweepExpired() {
	s.mu.Lock()
	stale := make([]string, 0, len(s.owners))
	for id := range s.owners {
		if _, err := s.manager.Peek(id); errors.Is(err, uploads.ErrNotFound) || errors.Is(err, uploads.ErrExpired) {
			stale = append(stale, id)
		}
	}
	s.mu.Unlock()
	for _, id := range stale {
		s.drop(id)
	}
}

// HandleChunkedUploadInit handles POST /diagnostics/reports/uploads.
func (h *DiagnosticsHandler) HandleChunkedUploadInit(w http.ResponseWriter, r *http.Request) {
	userID, ok := diagnosticsUserID(w, r)
	if !ok {
		return
	}

	status, ok := h.diagnosticsUploadStatus(w, r, userID)
	if !ok {
		return
	}
	// Manifest cap plus a small envelope allowance keeps init requests tiny —
	// they must themselves fit under restrictive proxy body caps.
	r.Body = http.MaxBytesReader(w, r.Body, diagnostics.MaxManifestBytes+diagnosticsMultipartOverheadBytes)
	var req diagnosticsChunkInitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			h.logRejected(r.Context(), userID, diagnosticsTooLargeCode)
			writeError(w, http.StatusRequestEntityTooLarge, diagnosticsTooLargeCode, "Diagnostics manifest is too large")
			return
		}
		writeError(w, http.StatusBadRequest, diagnosticsInvalidBundleCode, "Invalid diagnostics upload init")
		return
	}

	result, err := h.initDiagnosticChunks(r.Context(), userID, req, status)
	if err != nil {
		writeDiagnosticsUploadFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

// InitDiagnosticChunks reserves a process-local upload session. A new init replaces
// this account's prior session; it is not safe to replay an uncertain init.
func (h *DiagnosticsHandler) InitDiagnosticChunks(ctx context.Context, userID int, req DiagnosticsChunkInitRequest) (DiagnosticsChunkInitResponse, error) {
	status, err := h.service.Status(ctx, userID)
	if err != nil {
		return DiagnosticsChunkInitResponse{}, &DiagnosticsUploadFailure{Status: 500, Code: diagnosticsInternalCode, Message: diagnosticsStatusFailureMessage}
	}
	return h.initDiagnosticChunks(ctx, userID, req, status)
}

func (h *DiagnosticsHandler) initDiagnosticChunks(ctx context.Context, userID int, req DiagnosticsChunkInitRequest, status diagnostics.Status) (DiagnosticsChunkInitResponse, error) {
	if status.Status != diagnostics.StatusAvailable {
		if status.Status == diagnostics.StatusDisabled {
			return DiagnosticsChunkInitResponse{}, &DiagnosticsUploadFailure{Status: 403, Code: diagnosticsDisabledCode, Message: diagnosticsDisabledMessage}
		}
		message := "Diagnostics storage is not available"
		if status.Status == diagnostics.StatusStorageUnavailable {
			message = diagnosticsStorageMessage
		}
		return DiagnosticsChunkInitResponse{}, &DiagnosticsUploadFailure{Status: 503, Code: errCodeStorageUnavailable, Message: message}
	}
	maxBundleBytes := status.MaxBundleBytes
	if maxBundleBytes <= 0 {
		maxBundleBytes = diagnostics.DefaultMaxBundleBytes
	}
	if len(req.Manifest) == 0 || int64(len(req.Manifest)) > diagnostics.MaxManifestBytes {
		h.logRejected(ctx, userID, diagnosticsTooLargeCode)
		return DiagnosticsChunkInitResponse{}, &DiagnosticsUploadFailure{Status: http.StatusRequestEntityTooLarge, Code: diagnosticsTooLargeCode, Message: "Diagnostics manifest is too large"}
	}
	if req.BundleBytes <= 0 {
		return DiagnosticsChunkInitResponse{}, &DiagnosticsUploadFailure{Status: http.StatusBadRequest, Code: diagnosticsInvalidBundleCode, Message: "bundle_bytes must be positive"}
	}
	if req.BundleBytes > maxBundleBytes {
		h.logRejected(ctx, userID, diagnosticsTooLargeCode)
		return DiagnosticsChunkInitResponse{}, &DiagnosticsUploadFailure{Status: http.StatusRequestEntityTooLarge, Code: diagnosticsTooLargeCode, Message: diagnosticsTooLargeMessage}
	}

	h.chunkSessions.sweepExpired()

	// Atomically evict the user's previous session (a new init replaces it,
	// so a client that died mid-upload can start over immediately) and claim
	// a cap slot. Cancel the evicted spool outside the lock either way.
	previousID, capOK := h.chunkSessions.reserve(userID)
	if previousID != "" {
		h.abortChunkedUpload(previousID)
	}
	if !capOK {
		h.logRejected(ctx, userID, diagnosticsBusyCode)
		return DiagnosticsChunkInitResponse{}, &DiagnosticsUploadFailure{Status: http.StatusServiceUnavailable, Code: diagnosticsBusyCode, Message: diagnosticsBusyMessage, RetryAfter: diagnosticsBusyRetryAfter}
	}

	session, err := h.chunkSessions.manager.Create(uploads.CreateRequest{
		Filename:  "bundle.tar.gz",
		SizeBytes: req.BundleBytes,
		ChunkSize: diagnostics.UploadChunkBytes,
	})
	if err != nil {
		h.chunkSessions.unreserve(userID)
		statusCode, message := uploadErrorResponse(err)
		return DiagnosticsChunkInitResponse{}, &DiagnosticsUploadFailure{Status: statusCode, Code: diagnosticsUploadErrorCode, Message: message}
	}
	h.chunkSessions.commit(userID, session.ID, bytes.Clone(req.Manifest))

	return DiagnosticsChunkInitResponse{
		UploadID:    session.ID,
		ChunkBytes:  session.ChunkSize,
		TotalChunks: session.TotalChunks,
		ExpiresAt:   session.ExpiresAt.UTC().Format(time.RFC3339),
	}, nil
}

// HandleChunkedUploadChunk handles PUT /diagnostics/reports/uploads/{upload_id}/chunks/{chunk_index}.
func (h *DiagnosticsHandler) HandleChunkedUploadChunk(w http.ResponseWriter, r *http.Request) {
	// The integrated server's 30s ReadTimeout kills a 768 KiB chunk arriving
	// below ~26 KiB/s — exactly the slow uplinks the chunked fallback exists
	// for. The write deadline needs lifting too: the 120s WriteTimeout starts
	// at request start, so on a sufficiently slow uplink the stored chunk's
	// JSON acknowledgement would miss it and the client would retry an
	// already-accepted chunk.
	h.extendDiagnosticsUploadDeadlines(w, r, true)

	userID, ok := diagnosticsUserID(w, r)
	if !ok {
		return
	}
	uploadID := chi.URLParam(r, "upload_id")
	chunkIndex, err := strconv.Atoi(chi.URLParam(r, "chunk_index"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid chunk index")
		return
	}

	result, err := h.PutDiagnosticChunk(w, r, userID, uploadID, chunkIndex)
	if err != nil {
		writeDiagnosticsUploadFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// PutDiagnosticChunk checks account ownership before reading bounded chunk bytes.
func (h *DiagnosticsHandler) PutDiagnosticChunk(w http.ResponseWriter, r *http.Request, userID int, uploadID string, chunkIndex int) (DiagnosticsChunkStateResponse, error) {
	if _, ok := h.chunkSessions.owner(uploadID, userID); !ok {
		return DiagnosticsChunkStateResponse{}, &DiagnosticsUploadFailure{Status: http.StatusNotFound, Code: diagnosticsUploadErrorCode, Message: diagnosticsSessionNotFoundMessage}
	}

	r.Body = http.MaxBytesReader(w, r.Body, diagnostics.UploadChunkBytes+1)
	defer r.Body.Close()

	session, err := h.chunkSessions.manager.PutChunk(r.Context(), uploadID, chunkIndex, r.Body, r.ContentLength)
	if err != nil {
		if errors.Is(err, uploads.ErrNotFound) || errors.Is(err, uploads.ErrExpired) {
			h.chunkSessions.drop(uploadID)
		}
		statusCode, message := uploadErrorResponse(err)
		return DiagnosticsChunkStateResponse{}, &DiagnosticsUploadFailure{Status: statusCode, Code: diagnosticsUploadErrorCode, Message: message}
	}
	return DiagnosticsChunkStateResponse{
		ReceivedChunks: session.ReceivedChunks,
		TotalChunks:    session.TotalChunks,
	}, nil
}

// HandleChunkedUploadComplete handles POST /diagnostics/reports/uploads/{upload_id}/complete.
func (h *DiagnosticsHandler) HandleChunkedUploadComplete(w http.ResponseWriter, r *http.Request) {
	// Ingest + object-store write can outlast the server's 120s WriteTimeout;
	// without the extension the stored report's 201 would be lost and the
	// client would retry an upload that already succeeded (see the single-shot
	// handler).
	h.extendDiagnosticsUploadDeadlines(w, r, true)

	userID, ok := diagnosticsUserID(w, r)
	if !ok {
		return
	}
	uploadID := chi.URLParam(r, "upload_id")
	var profileID *string
	if value := strings.TrimSpace(r.Header.Get("X-Profile-Id")); value != "" {
		profileID = new(value)
	}

	result, err := h.CompleteDiagnosticChunks(r.Context(), userID, uploadID, profileID)
	if err != nil {
		writeDiagnosticsUploadFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

// CompleteDiagnosticChunks consumes the local session before ingest. A lost
// completion reply is uncertain; there is no durable receipt replay.
func (h *DiagnosticsHandler) CompleteDiagnosticChunks(ctx context.Context, userID int, uploadID string, profileID *string) (diagnostics.IngestResult, error) {
	owner, ok := h.chunkSessions.owner(uploadID, userID)
	if !ok {
		return diagnostics.IngestResult{}, &DiagnosticsUploadFailure{Status: http.StatusNotFound, Code: diagnosticsUploadErrorCode, Message: diagnosticsSessionNotFoundMessage}
	}

	// Availability can have changed since init (admin toggle, storage loss);
	// re-check so a completed spool is not ingested into a disabled feature.
	// Only a definitive non-available answer discards the session — a
	// transient status load failure (500) keeps it so a retried complete can
	// succeed without re-uploading every chunk.
	status, statusErr := h.service.Status(ctx, userID)
	if statusErr != nil {
		return diagnostics.IngestResult{}, &DiagnosticsUploadFailure{Status: http.StatusInternalServerError, Code: diagnosticsInternalCode, Message: diagnosticsStatusFailureMessage}
	}
	if status.Status != diagnostics.StatusAvailable {
		h.abortChunkedUpload(uploadID)
		switch status.Status {
		case diagnostics.StatusDisabled:
			return diagnostics.IngestResult{}, &DiagnosticsUploadFailure{Status: http.StatusForbidden, Code: diagnosticsDisabledCode, Message: diagnosticsDisabledMessage}
		default:
			return diagnostics.IngestResult{}, &DiagnosticsUploadFailure{Status: http.StatusServiceUnavailable, Code: errCodeStorageUnavailable, Message: diagnosticsStorageMessage}
		}
	}

	// The ingest itself shares capacity with single-shot uploads. Check
	// before consuming the manager session so a busy answer leaves the
	// session intact for a client-side retry of complete.
	release, acquired := h.inflight.acquire(userID)
	if !acquired {
		h.logRejected(ctx, userID, diagnosticsBusyCode)
		return diagnostics.IngestResult{}, &DiagnosticsUploadFailure{Status: http.StatusServiceUnavailable, Code: diagnosticsBusyCode, Message: diagnosticsBusyMessage, RetryAfter: diagnosticsBusyRetryAfter}
	}
	defer release()

	upload, err := h.chunkSessions.manager.Complete(uploadID)
	if err != nil {
		if errors.Is(err, uploads.ErrNotFound) || errors.Is(err, uploads.ErrExpired) {
			h.chunkSessions.drop(uploadID)
		}
		statusCode, message := uploadErrorResponse(err)
		return diagnostics.IngestResult{}, &DiagnosticsUploadFailure{Status: statusCode, Code: diagnosticsUploadErrorCode, Message: message}
	}
	// The manager session is consumed; a failed ingest is retried by the
	// client from a fresh init, never by re-completing a spent session.
	defer h.chunkSessions.drop(uploadID)
	defer upload.Cleanup()

	bundle, err := os.Open(upload.Path)
	if err != nil {
		return diagnostics.IngestResult{}, &DiagnosticsUploadFailure{Status: http.StatusInternalServerError, Code: diagnosticsInternalCode, Message: diagnosticsUploadFailedMessage}
	}
	defer bundle.Close()

	result, err := h.service.Ingest(ctx, userID, profileID, owner.manifest, io.Reader(bundle))
	if err != nil {
		return diagnostics.IngestResult{}, diagnosticsServiceFailure(err)
	}
	return result, nil
}

// HandleChunkedUploadAbort handles DELETE /diagnostics/reports/uploads/{upload_id}.
func (h *DiagnosticsHandler) HandleChunkedUploadAbort(w http.ResponseWriter, r *http.Request) {
	userID, ok := diagnosticsUserID(w, r)
	if !ok {
		return
	}
	h.AbortDiagnosticChunks(userID, chi.URLParam(r, "upload_id"))
	w.WriteHeader(http.StatusNoContent)
}

// AbortDiagnosticChunks is an idempotent, account-bound best-effort local abort.
func (h *DiagnosticsHandler) AbortDiagnosticChunks(userID int, uploadID string) {
	if _, ok := h.chunkSessions.owner(uploadID, userID); !ok {
		return
	}
	h.abortChunkedUpload(uploadID)
}

func (h *DiagnosticsHandler) abortChunkedUpload(uploadID string) {
	if err := h.chunkSessions.manager.Cancel(uploadID); err != nil && !errors.Is(err, uploads.ErrNotFound) {
		h.diagnosticsLogger().Warn("diagnostics chunked upload cancel failed",
			"component", "diagnostics",
			"error", err,
		)
	}
	h.chunkSessions.drop(uploadID)
}

// diagnosticsUploadStatus loads the feature status and writes the
// disabled/storage-unavailable rejection when uploads cannot proceed,
// mirroring the single-shot endpoint's gate.
func (h *DiagnosticsHandler) diagnosticsUploadStatus(w http.ResponseWriter, r *http.Request, userID int) (diagnostics.Status, bool) {
	status, err := h.service.Status(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, diagnosticsInternalCode, diagnosticsStatusFailureMessage)
		return diagnostics.Status{}, false
	}
	switch status.Status {
	case diagnostics.StatusDisabled:
		writeError(w, http.StatusForbidden, diagnosticsDisabledCode, diagnosticsDisabledMessage)
		return diagnostics.Status{}, false
	case diagnostics.StatusStorageUnavailable:
		writeError(w, http.StatusServiceUnavailable, errCodeStorageUnavailable, diagnosticsStorageMessage)
		return diagnostics.Status{}, false
	case diagnostics.StatusAvailable:
		return status, true
	default:
		writeError(w, http.StatusServiceUnavailable, errCodeStorageUnavailable, "Diagnostics storage is not available")
		return diagnostics.Status{}, false
	}
}
