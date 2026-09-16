// Package handlers provides HTTP handler functions for the Silo API.
package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// PGPinger is the interface used to check PostgreSQL connectivity.
// *pgxpool.Pool satisfies this interface.
type PGPinger interface {
	Ping(ctx context.Context) error
}

// S3HealthChecker is the interface used to check S3 bucket accessibility.
// *s3client.Client satisfies this interface.
type S3HealthChecker interface {
	HeadBucket(ctx context.Context, bucket string) error
	Bucket() string
}

// ArtworkHealthChecker is the backend-neutral artwork storage probe.
type ArtworkHealthChecker interface {
	Probe(ctx context.Context) error
}

// healthStatus represents the JSON response for the health endpoint.
//
// ServerName and ServerID identify this Silo instance. They are
// populated from server configuration and are stable across restarts,
// which allows clients (notably the iOS/tvOS multi-server picker) to
// display a friendly name and detect the same server reached via
// different URLs.
type healthStatus struct {
	Status     string `json:"status"`
	ServerName string `json:"server_name,omitempty"`
	ServerID   string `json:"server_id,omitempty"`
}

// Readiness status values. The v1 shape is frozen; "degraded" is additive.
const (
	readyStatusOK       = "ok"
	readyStatusError    = "error"
	readyStatusDegraded = "degraded"
)

// readyStatus represents the JSON response for the readiness endpoint.
type readyStatus struct {
	Status   string `json:"status"`
	Postgres *bool  `json:"postgres,omitempty"`
	S3       *bool  `json:"s3,omitempty"`
	Artwork  *bool  `json:"artwork,omitempty"`
}

// HealthHandler responds to liveness probes and advertises the server's
// identity. Identity fields (ServerName, ServerID) are injected at
// construction time and reused for every request.
type HealthHandler struct {
	serverName string
	serverID   string
}

// NewHealthHandler creates a HealthHandler with the given identity
// fields. Empty strings are allowed: the corresponding JSON fields are
// omitted from the response.
func NewHealthHandler(serverName, serverID string) *HealthHandler {
	return &HealthHandler{
		serverName: serverName,
		serverID:   serverID,
	}
}

// ServeHTTP responds with 200 OK and a JSON body indicating the service
// is alive, along with server identity fields. This endpoint does not
// check any dependencies.
func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(healthStatus{
		Status:     "ok",
		ServerName: h.serverName,
		ServerID:   h.serverID,
	})
}

// ReadyHandler checks the health of PostgreSQL and the storage dependencies.
// Only PostgreSQL gates readiness; storage health is informational.
type ReadyHandler struct {
	pg             PGPinger
	s3             S3HealthChecker
	artwork        ArtworkHealthChecker
	artworkMu      sync.Mutex
	artworkChecked time.Time
	artworkOK      bool
}

// NewReadyHandler creates a ReadyHandler with the given PG and S3 dependencies.
// Either dependency may be nil: a nil PG pinger means PG is unavailable,
// and a nil S3 checker means S3 is not configured (treated as healthy).
func NewReadyHandler(pg PGPinger, s3 S3HealthChecker, artwork ...ArtworkHealthChecker) *ReadyHandler {
	var checker ArtworkHealthChecker
	if len(artwork) > 0 {
		checker = artwork[0]
	}
	return &ReadyHandler{pg: pg, s3: s3, artwork: checker}
}

// ServeHTTP checks PostgreSQL and storage health. It returns 503 when
// PostgreSQL is unreachable and 200 otherwise, with "degraded" in the body when
// a configured storage backend failed its probe.
func (h *ReadyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	pgOK := h.checkPostgres(ctx)
	s3OK := h.checkS3(ctx)
	artworkOK := h.checkArtwork(ctx)

	// Readiness answers "can this node serve requests", which the database
	// decides. Object and artwork storage are reported so operators can see an
	// outage, but a missing poster must not pull a node out of rotation: the
	// API keeps working, artwork routes answer 503 or fall back on their own,
	// and readiness follows storage recovery without a restart.
	//
	// The v1 body shape is frozen: a healthy answer carries only status, and
	// any other answer carries every dependency boolean, with an unconfigured
	// dependency reporting true. "degraded" is additive to that contract.
	status := readyStatus{Status: readyStatusOK}
	if !pgOK || !s3OK || !artworkOK {
		status.Postgres = new(pgOK)
		status.S3 = new(s3OK)
		status.Artwork = new(artworkOK)
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case !pgOK:
		status.Status = readyStatusError
		w.WriteHeader(http.StatusServiceUnavailable)
	case !s3OK || !artworkOK:
		status.Status = readyStatusDegraded
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusOK)
	}
	_ = json.NewEncoder(w).Encode(status)
}

func (h *ReadyHandler) checkArtwork(ctx context.Context) bool {
	if h.artwork == nil {
		return true
	}
	h.artworkMu.Lock()
	defer h.artworkMu.Unlock()
	if !h.artworkChecked.IsZero() && time.Since(h.artworkChecked) < 30*time.Second {
		return h.artworkOK
	}
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	h.artworkOK = h.artwork.Probe(probeCtx) == nil
	h.artworkChecked = time.Now()
	return h.artworkOK
}

// checkPostgres pings the PG pool. Returns false if the pool is nil or
// the ping fails.
func (h *ReadyHandler) checkPostgres(ctx context.Context) bool {
	if h.pg == nil {
		return false
	}
	return h.pg.Ping(ctx) == nil
}

// checkS3 performs a HeadBucket call on the S3 client. Returns true if
// no S3 client is configured (S3 is optional). Returns false if the
// HeadBucket call fails.
func (h *ReadyHandler) checkS3(ctx context.Context) bool {
	if h.s3 == nil {
		return true
	}
	return h.s3.HeadBucket(ctx, h.s3.Bucket()) == nil
}
