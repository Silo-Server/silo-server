package chapterthumbs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/telemetry"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

const (
	chapterThumbnailExecutionSetting            = "playback.chapter_thumbnail_execution"
	chapterThumbnailExecutionLocal              = "local"
	chapterThumbnailExecutionPreferTranscode    = "prefer_transcode_nodes"
	chapterThumbnailExecutionTranscodeOnly      = "transcode_nodes_only"
	chapterThumbnailNodeCapacitySetting         = "playback.chapter_thumbnail_node_capacity"
	chapterThumbnailNodeUnavailableReason       = RemoteNodeUnavailableReason
	chapterThumbnailNodeCapacityExhaustedReason = "transcode_node_capacity_exhausted"
)

// RemoteNodeUnavailableReason is the reason a transcode node answers a chapter
// extraction it cannot run now (draining, re-probing its hardware, or unable to
// open the granted source). prefer_transcode_nodes answers it by extracting
// locally (IsInfrastructureRemoteFailure).
const RemoteNodeUnavailableReason = "transcode_node_unavailable"

// remoteExtractOverheadBuffer covers HTTP transport and node-side scheduling
// overhead on top of the extraction attempt deadlines.
const remoteExtractOverheadBuffer = 3 * time.Second

type RemoteExtractRequest struct {
	InputPath string `json:"input_path"`
	// SourceGrant is the core's source grant for InputPath, naming the node
	// the request is sent to.
	SourceGrant          string  `json:"source_grant,omitempty" required:"true"`
	SeekSeconds          float64 `json:"seek_seconds"`
	ToneMap              bool    `json:"tone_map"`
	AllowSoftwareToneMap bool    `json:"allow_software_tone_map"`
}

type RemoteExtractErrorResponse struct {
	Reason string `json:"reason"`
	Error  string `json:"error,omitempty"`
}

type remoteFrameExtractor interface {
	ExtractFrame(ctx context.Context, node *nodepool.Node, keys nodeauth.Keyring, req RemoteExtractRequest) ([]byte, string, error)
}

type httpRemoteFrameExtractor struct {
	client *http.Client
}

func (e *httpRemoteFrameExtractor) ExtractFrame(
	ctx context.Context,
	node *nodepool.Node,
	keys nodeauth.Keyring,
	req RemoteExtractRequest,
) ([]byte, string, error) {
	if node == nil {
		return nil, chapterThumbnailNodeUnavailableReason, fmt.Errorf("chapter thumbnail remote extract: missing node")
	}
	if keys == nil {
		return nil, chapterThumbnailNodeUnavailableReason, errors.New("chapter thumbnail remote extract: missing node keyring")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, reasonChapterExtractFailed, fmt.Errorf("chapter thumbnail remote extract: marshal request: %w", err)
	}

	timeout := remoteExtractTimeout(req.ToneMap, req.AllowSoftwareToneMap)
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(requestCtx, http.MethodPost, nodepool.NodeEndpoint(node.URL, "/chapter-thumbnails/extract"), bytes.NewReader(body))
	if err != nil {
		return nil, chapterThumbnailNodeUnavailableReason, fmt.Errorf("chapter thumbnail remote extract: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if err := nodeauth.SignTranscode(httpReq, node.URL, keys); err != nil {
		return nil, chapterThumbnailNodeUnavailableReason, fmt.Errorf("chapter thumbnail remote extract: sign request: %w", err)
	}

	client := e.client
	if client == nil {
		client = &http.Client{Timeout: timeout, CheckRedirect: nodepool.RefuseRedirects}
	}

	resp, err := telemetry.DoTrustedNode(client, httpReq, "chapter_extract")
	if err != nil {
		return nil, chapterThumbnailNodeUnavailableReason, fmt.Errorf("chapter thumbnail remote extract: request failed: %w", err)
	}
	defer resp.Body.Close()
	// One byte over the frame limit tells a frame that passes it from one
	// that fits.
	respBody := io.LimitReader(resp.Body, mediasample.MaxImageBytes+1)

	if resp.StatusCode != http.StatusOK {
		reason := chapterThumbnailNodeUnavailableReason
		message := fmt.Sprintf("node returned status %d", resp.StatusCode)
		var payload RemoteExtractErrorResponse
		if decodeErr := json.NewDecoder(respBody).Decode(&payload); decodeErr == nil {
			if payload.Reason != "" {
				reason = payload.Reason
			}
			if payload.Error != "" {
				message = payload.Error
			}
		}
		return nil, reason, fmt.Errorf("chapter thumbnail remote extract: %s", message)
	}

	data, err := io.ReadAll(respBody)
	if err != nil {
		return nil, chapterThumbnailNodeUnavailableReason, fmt.Errorf("chapter thumbnail remote extract: read response: %w", err)
	}
	if len(data) == 0 {
		return nil, reasonChapterExtractFailed, fmt.Errorf("chapter thumbnail remote extract: empty response")
	}
	// A node with the same limit never sends this, so the fault is the
	// node's: prefer_transcode_nodes extracts locally instead.
	if len(data) > mediasample.MaxImageBytes {
		return nil, chapterThumbnailNodeUnavailableReason, fmt.Errorf("chapter thumbnail remote extract: frame is over %d bytes", mediasample.MaxImageBytes)
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

// IsInfrastructureRemoteFailure reports whether a remote extraction's reason
// is the node's failure rather than the source's, which prefer_transcode_nodes
// answers by extracting locally.
func IsInfrastructureRemoteFailure(reason string) bool {
	switch reason {
	case chapterThumbnailNodeUnavailableReason, chapterThumbnailNodeCapacityExhaustedReason, reasonFFmpegProbeFailed:
		return true
	default:
		return false
	}
}
