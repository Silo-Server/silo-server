package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/buildinfo"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

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

// HandleHWAccel handles GET /admin/system/hw-accel.
// When transcode nodes are registered it delegates to the first healthy node.
// Otherwise it probes the local host.
func (h *SystemHandler) HandleHWAccel(w http.ResponseWriter, r *http.Request) {
	if h.transcodePool != nil {
		if node := h.transcodePool.Acquire(); node != nil {
			info, err := h.fetchRemoteHWAccel(r.Context(), node)
			if err == nil {
				writeJSON(w, http.StatusOK, info)
				return
			}
			slog.WarnContext(r.Context(), "hw-accel: remote node probe failed, falling back to local", "component", "api",
				"node", node.URL, "error", err)
		}
	}

	info := playback.DetectHWAccelWithFFmpeg(h.ffmpegPath)
	writeJSON(w, http.StatusOK, info)
}

// HandleBuildInfo handles GET /admin/system/build.
func (h *SystemHandler) HandleBuildInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.buildInfo)
}

func (h *SystemHandler) fetchRemoteHWAccel(ctx context.Context, node *nodepool.Node) (playback.HWAccelInfo, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, node.URL+"/hw-capabilities", nil)
	if err != nil {
		return playback.HWAccelInfo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+h.jwtSecret)

	resp, err := client.Do(req)
	if err != nil {
		return playback.HWAccelInfo{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return playback.HWAccelInfo{}, fmt.Errorf("node returned %d", resp.StatusCode)
	}

	var info playback.HWAccelInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return playback.HWAccelInfo{}, err
	}
	info.Source = "transcode_node"
	info.NodeURL = node.URL
	return info, nil
}
