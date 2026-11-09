package transcodenode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/telemetry"
	"github.com/Silo-Server/silo-server/internal/workerprotocol"
)

// deliveryProgressPath lists how far each HLS session on the node has been
// fetched by its client and produced by ffmpeg, so the API server can show it
// on the admin dashboard for sessions it hands to this node.
const deliveryProgressPath = "/transcode/delivery-progress"

// maxDeliveryProgressResponseBytes bounds the listing the API reads: a few
// hundred bytes per session leaves room for thousands of sessions.
const maxDeliveryProgressResponseBytes = 4 << 20

// SessionDeliveryProgress is one HLS session's delivery progress in media
// seconds. The fetched pair is present together or not at all.
type SessionDeliveryProgress struct {
	FetchedStartSeconds    *float64 `json:"fetched_start_seconds,omitempty" doc:"Start of the run of segments the client most recently finished downloading."`
	FetchedEndSeconds      *float64 `json:"fetched_end_seconds,omitempty" doc:"End of that run."`
	ProducedThroughSeconds *float64 `json:"produced_through_seconds,omitempty" doc:"End of the last segment the current ffmpeg generation wrote."`
}

// DeliveryProgressResponse is the node's delivery progress, keyed by the
// transport session id the API started each session with.
type DeliveryProgressResponse struct {
	Sessions map[string]SessionDeliveryProgress `json:"sessions"`
}

// ProtocolDeliveryProgress describes the node's delivery progress read.
func ProtocolDeliveryProgress(schemas huma.Registry) workerprotocol.Operation {
	op := workerprotocol.JSONRead[DeliveryProgressResponse](schemas, "transcode_node", deliveryProgressPath, "(*internal/transcodenode.Server).handleDeliveryProgress", 401)
	op.Description = "Report each HLS session's fetched segment run and produced head for the admin dashboard. Observation only; no playback decision reads it."
	return op
}

func (s *Server) handleDeliveryProgress(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	sessions := make(map[string]*playback.TranscodeSession, len(s.sessions))
	for id, ts := range s.sessions {
		sessions[id] = ts
	}
	s.mu.RUnlock()

	now := time.Now()
	out := DeliveryProgressResponse{Sessions: make(map[string]SessionDeliveryProgress, len(sessions))}
	for id, ts := range sessions {
		if ts == nil {
			continue
		}
		out.Sessions[id] = sessionDeliveryProgress(ts.DeliveryProgress(now))
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		slog.WarnContext(r.Context(), "write delivery progress", "component", "transcodenode", "error", err)
	}
}

func sessionDeliveryProgress(p playback.HLSDeliveryProgress) SessionDeliveryProgress {
	var out SessionDeliveryProgress
	if p.FetchedOK {
		out.FetchedStartSeconds, out.FetchedEndSeconds = new(p.Fetched.Start), new(p.Fetched.End)
	}
	if p.ProducedOK {
		out.ProducedThroughSeconds = new(p.ProducedThrough)
	}
	return out
}

// HLS converts the wire form back to the playback type.
func (p SessionDeliveryProgress) HLS() playback.HLSDeliveryProgress {
	var out playback.HLSDeliveryProgress
	if p.FetchedStartSeconds != nil && p.FetchedEndSeconds != nil {
		out.Fetched = playback.MediaSpan{Start: *p.FetchedStartSeconds, End: *p.FetchedEndSeconds}
		out.FetchedOK = true
	}
	if p.ProducedThroughSeconds != nil {
		out.ProducedThrough, out.ProducedOK = *p.ProducedThroughSeconds, true
	}
	return out
}

// FetchDeliveryProgress reads the delivery progress of every HLS session on
// the node at nodeURL. A node that predates the read reports no sessions and
// no error: its router matches the path to DELETE /transcode/{session_id} and
// answers 405 before authentication, or 404 where nothing matches.
func FetchDeliveryProgress(ctx context.Context, client *http.Client, nodeURL, jwtSecret string) (map[string]playback.HLSDeliveryProgress, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(nodeURL, "/")+deliveryProgressPath, nil)
	if err != nil {
		return nil, logredact.SanitizeURLError(err)
	}
	if err := nodeauth.Sign(request, nodeURL, jwtSecret); err != nil {
		return nil, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	response, err := telemetry.DoTrustedNode(client, request, "delivery_progress")
	if err != nil {
		return nil, logredact.SanitizeURLError(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, nil
	}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("node delivery progress returned status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxDeliveryProgressResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDeliveryProgressResponseBytes {
		return nil, fmt.Errorf("node delivery progress exceeds %d bytes", maxDeliveryProgressResponseBytes)
	}
	var listing DeliveryProgressResponse
	if err := json.Unmarshal(data, &listing); err != nil {
		return nil, err
	}
	out := make(map[string]playback.HLSDeliveryProgress, len(listing.Sessions))
	for id, p := range listing.Sessions {
		out[id] = p.HLS()
	}
	return out, nil
}
