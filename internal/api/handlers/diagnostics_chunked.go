package handlers

import (
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

type diagnosticsChunkInitRequest struct {
	// Manifest is the same part-1 manifest the multipart endpoint takes,
	// embedded verbatim. Deferring all content validation to Ingest keeps one
	// authority for what a valid manifest is.
	Manifest json.RawMessage `json:"manifest"`
	// BundleBytes is the exact assembled bundle size the client will upload.
	// Declared up front so over-limit uploads fail at init, before any chunk
	// bytes are spent.
	BundleBytes int64 `json:"bundle_bytes"`
}

type diagnosticsChunkInitResponse struct {
	UploadID    string `json:"upload_id"`
	ChunkBytes  int64  `json:"chunk_bytes"`
	TotalChunks int    `json:"total_chunks"`
	ExpiresAt   string `json:"expires_at"`
}

type diagnosticsChunkStateResponse struct {
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
	maxBundleBytes := status.MaxBundleBytes
	if maxBundleBytes <= 0 {
		maxBundleBytes = diagnostics.DefaultMaxBundleBytes
	}

	// Manifest cap plus a small envelope allowance keeps init requests tiny —
	// they must themselves fit under restrictive proxy body caps.
	r.Body = http.MaxBytesReader(w, r.Body, diagnostics.MaxManifestBytes+diagnosticsMultipartOverheadBytes)
	var req diagnosticsChunkInitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			h.logRejected(r.Context(), userID, "too_large")
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Diagnostics manifest is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_bundle", "Invalid diagnostics upload init")
		return
	}
	if len(req.Manifest) == 0 || int64(len(req.Manifest)) > diagnostics.MaxManifestBytes {
		h.logRejected(r.Context(), userID, "too_large")
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Diagnostics manifest is too large")
		return
	}
	if req.BundleBytes <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_bundle", "bundle_bytes must be positive")
		return
	}
	if req.BundleBytes > maxBundleBytes {
		h.logRejected(r.Context(), userID, "too_large")
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Diagnostics upload is too large")
		return
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
		h.logRejected(r.Context(), userID, "busy")
		w.Header().Set("Retry-After", diagnosticsBusyRetryAfter)
		writeError(w, http.StatusServiceUnavailable, "busy", "Diagnostics upload capacity is busy")
		return
	}

	session, err := h.chunkSessions.manager.Create(uploads.CreateRequest{
		Filename:  "bundle.tar.gz",
		SizeBytes: req.BundleBytes,
		ChunkSize: diagnostics.UploadChunkBytes,
	})
	if err != nil {
		h.chunkSessions.unreserve(userID)
		statusCode, message := uploadErrorResponse(err)
		writeError(w, statusCode, "upload_error", message)
		return
	}
	h.chunkSessions.commit(userID, session.ID, append([]byte(nil), req.Manifest...))

	writeJSON(w, http.StatusCreated, diagnosticsChunkInitResponse{
		UploadID:    session.ID,
		ChunkBytes:  session.ChunkSize,
		TotalChunks: session.TotalChunks,
		ExpiresAt:   session.ExpiresAt.UTC().Format(time.RFC3339),
	})
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
	if _, ok := h.chunkSessions.owner(uploadID, userID); !ok {
		writeError(w, http.StatusNotFound, "upload_error", "Upload session not found")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, diagnostics.UploadChunkBytes+1)
	defer r.Body.Close()

	session, err := h.chunkSessions.manager.PutChunk(r.Context(), uploadID, chunkIndex, r.Body, r.ContentLength)
	if err != nil {
		if errors.Is(err, uploads.ErrNotFound) || errors.Is(err, uploads.ErrExpired) {
			h.chunkSessions.drop(uploadID)
		}
		statusCode, message := uploadErrorResponse(err)
		writeError(w, statusCode, "upload_error", message)
		return
	}
	writeJSON(w, http.StatusOK, diagnosticsChunkStateResponse{
		ReceivedChunks: session.ReceivedChunks,
		TotalChunks:    session.TotalChunks,
	})
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
	owner, ok := h.chunkSessions.owner(uploadID, userID)
	if !ok {
		writeError(w, http.StatusNotFound, "upload_error", "Upload session not found")
		return
	}

	// Availability can have changed since init (admin toggle, storage loss);
	// re-check so a completed spool is not ingested into a disabled feature.
	// Only a definitive non-available answer discards the session — a
	// transient status load failure (500) keeps it so a retried complete can
	// succeed without re-uploading every chunk.
	status, statusErr := h.service.Status(r.Context(), userID)
	if statusErr != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load diagnostics status")
		return
	}
	if status.Status != diagnostics.StatusAvailable {
		h.abortChunkedUpload(uploadID)
		switch status.Status {
		case diagnostics.StatusDisabled:
			writeError(w, http.StatusForbidden, "disabled", "Diagnostics uploads are disabled")
		default:
			writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "Diagnostics storage is not configured")
		}
		return
	}

	// The ingest itself shares capacity with single-shot uploads. Check
	// before consuming the manager session so a busy answer leaves the
	// session intact for a client-side retry of complete.
	release, acquired := h.inflight.acquire(userID)
	if !acquired {
		h.logRejected(r.Context(), userID, "busy")
		w.Header().Set("Retry-After", diagnosticsBusyRetryAfter)
		writeError(w, http.StatusServiceUnavailable, "busy", "Diagnostics upload capacity is busy")
		return
	}
	defer release()

	upload, err := h.chunkSessions.manager.Complete(uploadID)
	if err != nil {
		if errors.Is(err, uploads.ErrNotFound) || errors.Is(err, uploads.ErrExpired) {
			h.chunkSessions.drop(uploadID)
		}
		statusCode, message := uploadErrorResponse(err)
		writeError(w, statusCode, "upload_error", message)
		return
	}
	// The manager session is consumed; a failed ingest is retried by the
	// client from a fresh init, never by re-completing a spent session.
	defer h.chunkSessions.drop(uploadID)
	defer upload.Cleanup()

	bundle, err := os.Open(upload.Path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Diagnostics upload failed")
		return
	}
	defer bundle.Close()

	profileID := strings.TrimSpace(r.Header.Get("X-Profile-Id"))
	var profileIDPtr *string
	if profileID != "" {
		profileIDPtr = &profileID
	}

	result, err := h.service.Ingest(r.Context(), userID, profileIDPtr, owner.manifest, io.Reader(bundle))
	if err != nil {
		writeDiagnosticsServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

// HandleChunkedUploadAbort handles DELETE /diagnostics/reports/uploads/{upload_id}.
func (h *DiagnosticsHandler) HandleChunkedUploadAbort(w http.ResponseWriter, r *http.Request) {
	userID, ok := diagnosticsUserID(w, r)
	if !ok {
		return
	}
	uploadID := chi.URLParam(r, "upload_id")
	if _, ok := h.chunkSessions.owner(uploadID, userID); !ok {
		// Unknown or foreign session: abort is a best-effort courtesy, and
		// idempotent success leaks nothing about other users' session ids.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h.abortChunkedUpload(uploadID)
	w.WriteHeader(http.StatusNoContent)
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
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load diagnostics status")
		return diagnostics.Status{}, false
	}
	switch status.Status {
	case diagnostics.StatusDisabled:
		writeError(w, http.StatusForbidden, "disabled", "Diagnostics uploads are disabled")
		return diagnostics.Status{}, false
	case diagnostics.StatusStorageUnavailable:
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "Diagnostics storage is not configured")
		return diagnostics.Status{}, false
	case diagnostics.StatusAvailable:
		return status, true
	default:
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "Diagnostics storage is not available")
		return diagnostics.Status{}, false
	}
}
