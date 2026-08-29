package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/buildinfo"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/nodemetrics"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const remoteNodeInventoryProbeTimeout = 5 * time.Second

// resourceSampler is the read side of the local host's resource sampler.
type resourceSampler interface {
	Snapshot() nodemetrics.Snapshot
}

// playbackSettings reports the playback configuration a local probe should run
// under. It is a function rather than three strings because these settings hot
// reload: frozen at construction, /admin/system/hw-accel would keep probing the
// backend and devices the process started with, and the Playback settings page
// would show an operator a verification result for the configuration they just
// replaced.
type playbackSettings func() (ffmpegPath, hwAccel, hwDevice string)

// SystemHandler serves read-only system inspection endpoints.
type SystemHandler struct {
	transcodePool *nodepool.TranscodePool
	jwtSecret     string
	playback      playbackSettings
	buildInfo     buildinfo.Info
	resources     resourceSampler
}

// NewSystemHandler creates a SystemHandler. playback supplies the current
// playback settings on each call, so a local probe verifies the backend and
// devices this host would transcode on right now.
func NewSystemHandler(transcodePool *nodepool.TranscodePool, jwtSecret string, playback playbackSettings) *SystemHandler {
	if playback == nil {
		playback = func() (string, string, string) { return "", "", "" }
	}
	return &SystemHandler{
		transcodePool: transcodePool,
		jwtSecret:     jwtSecret,
		playback:      playback,
		buildInfo:     buildinfo.Current(),
	}
}

// SetResourceSampler wires the local host's resource sampler. Without one,
// /admin/system/resources reports the host as unsampled rather than failing:
// the endpoint's answer is "what does this host look like right now", and
// "nothing is measuring it" is a valid answer to that.
func (h *SystemHandler) SetResourceSampler(sampler resourceSampler) {
	h.resources = sampler
}

// SystemResources is the local host's current resource sample.
type SystemResources struct {
	// Available is false on a host that cannot be sampled (non-Linux, or before
	// the first sample lands), in which case the two fields below are absent.
	Available bool                     `json:"available"`
	SampledAt string                   `json:"sampled_at,omitempty"`
	System    *nodemetrics.SystemStats `json:"system,omitempty"`
	GPU       []nodemetrics.GPUStats   `json:"gpu,omitempty"`
}

// HandleSystemResources handles GET /admin/system/resources.
//
// This is the API host's own sample — the counterpart to the per-node
// last_stats on /admin/nodes, which the Nodes page reads. The API host is not a
// registered stream node, so without this route the machine actually serving
// the request is the one machine an operator cannot see.
//
// It reads a snapshot the sampler already published, so it costs nothing and
// cannot hang, no matter what a mount or a GPU query is doing.
func (h *SystemHandler) HandleSystemResources(w http.ResponseWriter, _ *http.Request) {
	if h.resources == nil {
		writeJSON(w, http.StatusOK, SystemResources{})
		return
	}
	snapshot := h.resources.Snapshot()
	response := SystemResources{
		Available: snapshot.Available,
		System:    snapshot.System,
		GPU:       snapshot.GPU,
	}
	if !snapshot.SampledAt.IsZero() {
		response.SampledAt = snapshot.SampledAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, response)
}

// NodeHWAccel reports one transcode node's GPU inventory.
type NodeHWAccel struct {
	NodeURL             string                      `json:"node_url"`
	NodeName            string                      `json:"node_name,omitempty"`
	Resolved            string                      `json:"resolved,omitempty"`
	RenderDevices       []string                    `json:"render_devices,omitempty"`
	RenderDeviceDetails []playback.RenderDeviceInfo `json:"render_device_details,omitempty"`
	Error               string                      `json:"error,omitempty"`
}

// HWAccelInventory is the admin hw-accel response: the primary probe (flat,
// backward compatible with the previous single-probe response) plus every
// healthy node's inventory. playback.hw_device is one cluster-wide value, so
// the UI needs all node inventories to warn when they diverge (devices must
// exist at identical paths on every node for the setting to be safe).
type HWAccelInventory struct {
	playback.HWAccelInfo
	Nodes []NodeHWAccel `json:"nodes,omitempty"`
}

// HandleHWAccel handles GET /admin/system/hw-accel.
// With transcode nodes registered it probes every healthy node (the primary
// flat fields come from the first that responds, preserving the historical
// shape); with none it probes the local host.
func (h *SystemHandler) HandleHWAccel(w http.ResponseWriter, r *http.Request) {
	var healthy []*nodepool.Node
	if h.transcodePool != nil {
		for _, node := range h.transcodePool.Nodes() {
			if node != nil && node.Enabled && node.Healthy {
				healthy = append(healthy, node)
			}
		}
	}
	if len(healthy) == 0 {
		writeJSON(w, http.StatusOK, HWAccelInventory{HWAccelInfo: h.localHWAccel(w, r)})
		return
	}

	// The fan-out waits for its slowest node, whose budget can pass the API
	// listener's write timeout — the same reason the local walk extends the
	// deadline. Sized to the largest per-node budget, since the fetches run
	// concurrently.
	maxBudget := time.Duration(0)
	for _, node := range healthy {
		if budget := h.remoteInventoryTimeout(node); budget > maxBudget {
			maxBudget = budget
		}
	}
	extendWriteDeadlineBy(w, r, maxBudget+hwAccelWriteSlack)

	inventory := HWAccelInventory{Nodes: make([]NodeHWAccel, len(healthy))}
	infos := make([]playback.HWAccelInfo, len(healthy))
	errs := make([]error, len(healthy))
	var wg sync.WaitGroup
	for i, node := range healthy {
		wg.Add(1)
		go func() {
			defer wg.Done()
			infos[i], errs[i] = h.fetchRemoteHWAccel(r.Context(), node)
		}()
	}
	wg.Wait()

	for i, node := range healthy {
		entry := NodeHWAccel{NodeURL: node.URL, NodeName: node.Name}
		if errs[i] != nil {
			slog.WarnContext(r.Context(), "hw-accel: node probe failed", "component", "api",
				"node", logredact.SanitizeURL(node.URL), "error", errs[i])
			entry.Error = errs[i].Error()
		} else {
			entry.Resolved = infos[i].Resolved
			entry.RenderDevices = infos[i].RenderDevices
			entry.RenderDeviceDetails = infos[i].RenderDeviceDetails
		}
		inventory.Nodes[i] = entry
	}

	// Primary flat fields: the first node that answered; if none did, fall
	// back to a local probe (matching the historical fallback behavior).
	primaried := false
	for i := range healthy {
		if errs[i] == nil {
			inventory.HWAccelInfo = infos[i]
			primaried = true
			break
		}
	}
	if !primaried {
		inventory.HWAccelInfo = h.localHWAccel(w, r)
	}
	writeJSON(w, http.StatusOK, inventory)
}

// localHWAccel probes this host against its current playback settings.
//
// A zero-value handler answers with the ffmpeg on PATH and auto-detection
// rather than panicking: tests build one directly, and the accessor is wiring
// this method should not depend on having received.
func (h *SystemHandler) localHWAccel(w http.ResponseWriter, r *http.Request) playback.HWAccelInfo {
	var ffmpegPath, hwAccel, hwDevice string
	if h.playback != nil {
		ffmpegPath, hwAccel, hwDevice = h.playback()
	}
	extendHWAccelWriteDeadline(w, r, hwDevice)
	return playback.DetectHWAccelWithFFmpeg(hwAccel, ffmpegPath, hwDevice)
}

// hwAccelWriteSlack covers reading the node list and writing the JSON around the
// walk this route runs.
const hwAccelWriteSlack = 15 * time.Second

// extendHWAccelWriteDeadline lifts this connection's write deadline to cover a
// synchronous hardware walk.
//
// The walk is bounded by its own budget, which scales with the configured device
// set: eight Intel render devices draw five ffmpeg commands each at three
// seconds apiece, which is already past the API listener's 120-second write
// timeout. Without this the settings page loses its response while every probe
// is still inside its bound, and the operator sees a failed request for a probe
// that is about to succeed. The re-probe route extends its deadline for exactly
// the same reason.
func extendHWAccelWriteDeadline(w http.ResponseWriter, r *http.Request, hwDevice string) {
	extendWriteDeadlineBy(w, r, playback.HWAccelWalkTimeout(hwDevice)+hwAccelWriteSlack)
}

func extendWriteDeadlineBy(w http.ResponseWriter, r *http.Request, budget time.Duration) {
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(budget)); err != nil {
		// A ResponseWriter that cannot carry a deadline (a test recorder, a
		// wrapper that does not unwrap) is not a reason to refuse the probe.
		slog.WarnContext(r.Context(), "hw-accel write deadline not extended", "component", "api",
			"budget", budget, "error", err)
	}
}

// HandleBuildInfo handles GET /admin/system/build.
func (h *SystemHandler) HandleBuildInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.buildInfo)
}

func (h *SystemHandler) fetchRemoteHWAccel(ctx context.Context, node *nodepool.Node) (playback.HWAccelInfo, error) {
	requestCtx, cancel := context.WithTimeout(ctx, h.remoteInventoryTimeout(node))
	defer cancel()
	return fetchRemoteTranscodeCapabilities(requestCtx, node.URL, h.jwtSecret)
}

// remoteInventoryTimeout bounds one node's inventory fetch by that node's own
// cold probe budget — its stored report and its effective override, exactly
// how the playback and download paths price a cold read. A warm node answers
// from cache in milliseconds either way; what this sizes is the node whose
// caches were just invalidated (a widened device override is the common case),
// whose full walk legitimately takes past the old flat five seconds — cutting
// it off reported the node as failed on the Playback settings page while it
// was still inside its own advertised budget. The flat constant survives as
// the floor, so a node the cluster prices at nothing still gets a real chance
// to answer, and it is the whole bound only when nothing is known at all.
func (h *SystemHandler) remoteInventoryTimeout(node *nodepool.Node) time.Duration {
	var hwAccel, hwDevice string
	if h.playback != nil {
		_, hwAccel, hwDevice = h.playback()
	}
	return playback.ColdCapabilityRequestTimeout(
		node.StoredCapabilities(),
		node.EffectiveHWAccel(hwAccel),
		node.EffectiveHWDevice(hwDevice),
		remoteNodeInventoryProbeTimeout,
	)
}
