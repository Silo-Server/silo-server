package proxy

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// reprobeCapabilitiesResponse mirrors the transcode node's re-probe answer, so
// an operator action does not have to know which node type it is talking to.
type reprobeCapabilitiesResponse struct {
	// Resolved is the backend this proxy would now use.
	Resolved string `json:"resolved"`
	// CapabilityHash identifies the snapshot this re-probe published.
	CapabilityHash string `json:"capability_hash"`
}

// handleReprobeCapabilities discards this proxy's cached probe verdicts and
// rebuilds the capability snapshot against the live ffmpeg binary.
//
// A proxy runs ffmpeg for remux and Dolby Vision RPU strips, so an operator who
// swaps the binary under a running proxy needs a way to say so now instead of
// waiting out the 15-minute snapshot tick. It is deliberately *not* a hardware
// re-verification: a proxy never executes a hardware transcode, so its report
// carries no acceleration inventory to re-check — see
// buildCapabilitySnapshotLocked. The rebuild is therefore cheap, and one that
// does not finish still keeps the previously published hash.
//
// Unlike the transcode node this does not refuse while *jobs* are running. That
// guard is about encoder sessions: a proxy's jobs are remuxes and RPU strips,
// which hold no encoder slot for a probe's smoke encode to lose a race against.
func (s *Server) handleReprobeCapabilities(w http.ResponseWriter, r *http.Request) {
	// Held across the invalidation and the rebuild together, so a scheduled
	// snapshot cannot interleave with them and publish a hash for a half-cleared
	// cache.
	s.capabilityBuildMu.Lock()
	defer s.capabilityBuildMu.Unlock()

	// The mutex is not enough on its own: a probe outlives its caller by design
	// — the singleflights run on background contexts so a canceled request
	// cannot kill work another request is waiting on — so a capability request
	// abandoned mid-probe releases this mutex while ffmpeg is still running.
	// Invalidating then would start a second matrix beside the first. A proxy no
	// longer launches the hardware walk that made this expensive, so in practice
	// the count is zero; the gate stays because it is the shared contract with
	// the transcode node's route and costs one comparison.
	if busy := s.probesInFlight(); busy > 0 {
		slog.InfoContext(r.Context(), "proxy capability re-probe refused while probes are still running",
			"component", "proxy", "probes_in_flight", busy)
		http.Error(w, fmt.Sprintf(
			"node is running %d probe(s); starting another beside them would report working hardware as failed. Retry shortly.",
			busy), http.StatusConflict)
		return
	}

	playback.InvalidateHWProbeCache()
	tonemap.InvalidateProbeCache()
	// The resource sampler retires nvidia-smi after repeated failure, and a
	// driver that was broken at start is exactly what an operator reaches for
	// this route after. A proxy samples the same GPU a transcode node does — it
	// reports utilization on /health even though it never transcodes — so
	// without this nudge it keeps reporting no GPU until the breaker's own retry
	// interval.
	s.metrics.RetrySources()

	// buildCapabilitySnapshotLocked owns the probe deadline, so a re-probe can
	// never cost more than a cold capability fetch already may.
	info, err := s.buildCapabilitySnapshotLocked(r.Context())
	if err != nil {
		slog.WarnContext(r.Context(), "proxy capability re-probe incomplete", "component", "proxy", "error", err)
		http.Error(w, "capability probe unavailable", http.StatusServiceUnavailable)
		return
	}
	previous := s.storedCapabilityHash()
	s.storeCapabilityHash(info.CapabilityHash)
	slog.InfoContext(r.Context(), "proxy capabilities re-probed", "component", "proxy",
		"previous_hash", previous, "hash", info.CapabilityHash, "resolved", info.Resolved)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(reprobeCapabilitiesResponse{
		Resolved:       info.Resolved,
		CapabilityHash: info.CapabilityHash,
	}); err != nil {
		slog.WarnContext(r.Context(), "encode proxy re-probe result", "component", "proxy", "error", err)
	}
}

// probesInFlight counts the hardware and tone-map probes this process has
// claimed the encoder for. It is a method so a test can drive the refusal
// without reaching into either package's unexported singleflight.
func (s *Server) probesInFlight() int {
	if s.countProbesInFlight != nil {
		return s.countProbesInFlight()
	}
	return playback.HWProbesInFlight() + tonemap.ProbesInFlight()
}
