// Package handlers provides HTTP handler functions for the Silo API.
package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/artworkstore"
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

// ArtworkStorageChecker is the interface used to check that the canonical
// artwork store is reachable, writable, and still the store this process was
// pinned to. *artworkstore.Handle satisfies it.
type ArtworkStorageChecker interface {
	Check(ctx context.Context) error
}

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

// ReadyHandler checks the health of PostgreSQL and S3. Artwork storage is
// reported as a degradable dependency: resilient delivery can still return a
// source fallback or placeholder while that backend recovers.
type ReadyHandler struct {
	pg      PGPinger
	s3      S3HealthChecker
	artwork ArtworkStorageChecker
}

// NewReadyHandler creates a ReadyHandler with the given PG and S3 dependencies.
// Either dependency may be nil: a nil PG pinger means PG is unavailable,
// and a nil S3 checker means S3 is not configured (treated as healthy).
func NewReadyHandler(pg PGPinger, s3 S3HealthChecker) *ReadyHandler {
	return &ReadyHandler{pg: pg, s3: s3}
}

// SetArtworkStorage wires the canonical artwork store into readiness. Nodes
// that do not own artwork storage leave it unset and are reported as healthy;
// where it is set, an unwritable store is reported without taking the API node
// out of service. Reachable pin mismatches are rejected earlier by store Open.
func (h *ReadyHandler) SetArtworkStorage(checker ArtworkStorageChecker) {
	if h != nil {
		h.artwork = checker
	}
}

// ServeHTTP checks PostgreSQL, S3, and artwork storage health and responds
// with the combined status. Artwork failure produces a 200 degraded response;
// core database dependencies still produce 503.
func (h *ReadyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	pgOK := h.checkPostgres(ctx)
	s3OK := h.checkS3(ctx)
	artworkOK := h.checkArtwork(ctx)

	status := readyStatus{
		Status: "ok",
	}

	if !pgOK || !s3OK {
		status.Status = "error"
		status.Postgres = new(pgOK)
		status.S3 = new(s3OK)
		status.Artwork = new(artworkOK)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(status)
		return
	}
	if !artworkOK {
		status.Status = string(artworkstore.HealthDegraded)
		status.Artwork = new(false)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(status)
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

// checkArtwork verifies the canonical artwork store. Returns true when this
// node owns no artwork storage. A failure is recoverable by resilient source
// fallback and is exposed in the response without failing node readiness.
func (h *ReadyHandler) checkArtwork(ctx context.Context) bool {
	if h.artwork == nil {
		return true
	}
	if err := h.artwork.Check(ctx); err != nil {
		slog.ErrorContext(ctx, "artwork storage readiness check failed", "component", "api", "error", err)
		return false
	}
	return true
}
