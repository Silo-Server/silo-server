package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/buildinfo"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const remoteNodeInventoryProbeTimeout = 5 * time.Second

// SystemHandler serves read-only system inspection endpoints.
type SystemHandler struct {
	transcodePool *nodepool.TranscodePool
	jwtSecret     string
	ffmpegPath    string
	buildInfo     buildinfo.Info
}

// NewSystemHandler creates a SystemHandler.
func NewSystemHandler(transcodePool *nodepool.TranscodePool, jwtSecret string, ffmpegPath string) *SystemHandler {
	return &SystemHandler{
		transcodePool: transcodePool,
		jwtSecret:     jwtSecret,
		ffmpegPath:    ffmpegPath,
		buildInfo:     buildinfo.Current(),
	}
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
		writeJSON(w, http.StatusOK, HWAccelInventory{HWAccelInfo: playback.DetectHWAccelWithFFmpeg(h.ffmpegPath)})
		return
	}

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
		inventory.HWAccelInfo = playback.DetectHWAccelWithFFmpeg(h.ffmpegPath)
	}
	writeJSON(w, http.StatusOK, inventory)
}

// HandleBuildInfo handles GET /admin/system/build.
func (h *SystemHandler) HandleBuildInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.buildInfo)
}

func (h *SystemHandler) fetchRemoteHWAccel(ctx context.Context, node *nodepool.Node) (playback.HWAccelInfo, error) {
	// Inventory is an interactive admin request, so a stalled healthy node must
	// fail quickly and surface through the node entry's existing Error field.
	requestCtx, cancel := context.WithTimeout(ctx, remoteNodeInventoryProbeTimeout)
	defer cancel()
	return fetchRemoteTranscodeCapabilities(requestCtx, node.URL, h.jwtSecret)
}
