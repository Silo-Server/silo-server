package chapterthumbs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

const (
	chapterThumbnailExecutionSetting            = "playback.chapter_thumbnail_execution"
	chapterThumbnailExecutionLocal              = "local"
	chapterThumbnailExecutionPreferTranscode    = "prefer_transcode_nodes"
	chapterThumbnailExecutionTranscodeOnly      = "transcode_nodes_only"
	chapterThumbnailNodeCapacitySetting         = "playback.chapter_thumbnail_node_capacity"
	chapterThumbnailNodeUnavailableReason       = "transcode_node_unavailable"
	chapterThumbnailNodeCapacityExhaustedReason = "transcode_node_capacity_exhausted"
	authJWTSecretSetting                        = "auth.jwt_secret"
)

// remoteExtractOverheadBuffer covers HTTP transport and node-side scheduling
// overhead on top of the extraction attempt deadlines.
const remoteExtractOverheadBuffer = 3 * time.Second

type RemoteExtractRequest struct {
	InputPath            string  `json:"input_path"`
	SeekSeconds          float64 `json:"seek_seconds"`
	ToneMap              bool    `json:"tone_map"`
	AllowSoftwareToneMap bool    `json:"allow_software_tone_map"`
}

type RemoteExtractErrorResponse struct {
	Reason string `json:"reason"`
	Error  string `json:"error,omitempty"`
}

type remoteFrameExtractor interface {
	ExtractFrame(ctx context.Context, node *nodepool.Node, jwtSecret string, req RemoteExtractRequest) ([]byte, string, error)
}

type httpRemoteFrameExtractor struct {
	client *http.Client
}

func (e *httpRemoteFrameExtractor) ExtractFrame(
	ctx context.Context,
	node *nodepool.Node,
	jwtSecret string,
	req RemoteExtractRequest,
) ([]byte, string, error) {
	if node == nil {
		return nil, chapterThumbnailNodeUnavailableReason, fmt.Errorf("chapter thumbnail remote extract: missing node")
	}
	if strings.TrimSpace(jwtSecret) == "" {
		return nil, chapterThumbnailNodeUnavailableReason, fmt.Errorf("chapter thumbnail remote extract: missing jwt secret")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, reasonChapterExtractFailed, fmt.Errorf("chapter thumbnail remote extract: marshal request: %w", err)
	}

	timeout := remoteExtractTimeout(req.ToneMap, req.AllowSoftwareToneMap)
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(requestCtx, http.MethodPost, node.URL+"/chapter-thumbnails/extract", bytes.NewReader(body))
	if err != nil {
		return nil, chapterThumbnailNodeUnavailableReason, fmt.Errorf("chapter thumbnail remote extract: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+jwtSecret)

	client := e.client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, chapterThumbnailNodeUnavailableReason, fmt.Errorf("chapter thumbnail remote extract: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		reason := chapterThumbnailNodeUnavailableReason
		message := fmt.Sprintf("node returned status %d", resp.StatusCode)
		var payload RemoteExtractErrorResponse
		if decodeErr := json.NewDecoder(resp.Body).Decode(&payload); decodeErr == nil {
			if payload.Reason != "" {
				reason = payload.Reason
			}
			if payload.Error != "" {
				message = payload.Error
			}
		}
		return nil, reason, fmt.Errorf("chapter thumbnail remote extract: %s", message)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, chapterThumbnailNodeUnavailableReason, fmt.Errorf("chapter thumbnail remote extract: read response: %w", err)
	}
	if len(data) == 0 {
		return nil, reasonChapterExtractFailed, fmt.Errorf("chapter thumbnail remote extract: empty response")
	}
	return data, "", nil
}

func remoteExtractTimeout(toneMap bool, allowSoftwareToneMap bool) time.Duration {
	timeout := extractTimeoutForAttempt(true, toneMap) + remoteExtractOverheadBuffer
	if !toneMap || allowSoftwareToneMap {
		timeout += extractTimeoutForAttempt(false, toneMap)
	}
	if toneMap && allowSoftwareToneMap {
		timeout += softwareToneMapProbeTimeout
	}
	return timeout
}

func isInfrastructureRemoteFailure(reason string) bool {
	switch reason {
	case chapterThumbnailNodeUnavailableReason, chapterThumbnailNodeCapacityExhaustedReason, reasonFFmpegProbeFailed:
		return true
	default:
		return false
	}
}
