package trickplay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/telemetry"
)

// Reasons a transcode node gives for not running a request, which move the
// work to another node rather than failing it.
const (
	NodeBusyReason        = "node_busy"
	NodeUnavailableReason = "node_unavailable"
)

// jwtSecretSetting authenticates the API server to its transcode nodes.
const jwtSecretSetting = "auth.jwt_secret"

const (
	// remoteOverhead covers the transfer and the node's own start on top of
	// a request's attempt timeouts.
	remoteOverhead = 30 * time.Second
	// Generation caps chunks at 64 million pixels, including the initial
	// geometry sample. This also fits the base64 form of uncompressible JPEGs.
	maxRemoteResultBytes = 512 << 20
)

// ExtractError is a node's failure to make sheets. Permanent names a cause in
// the file itself (invalid data, no video stream), which marks it unusable.
type ExtractError struct {
	Reason    string `json:"reason"`
	Permanent bool   `json:"permanent,omitempty"`
	Message   string `json:"error,omitempty"`
}

func (e *ExtractError) Error() string {
	return fmt.Sprintf("transcode node %s: %s", e.Reason, e.Message)
}

// NodeExtractor makes sheets on transcode nodes when ExecutionSetting asks
// for it, and on this server otherwise. prefer_transcode_nodes falls back to
// this server when no node can take the work; transcode_nodes_only gives the
// work back to the queue instead.
type NodeExtractor struct {
	local    Extractor
	planner  NodeWorkPlanner
	settings SettingsReader
	client   *http.Client
	logger   *slog.Logger
}

// NodeWorkPlanner reserves transcode capacity shared with playback and
// prepared downloads. The caller releases it when the remote request ends.
type NodeWorkPlanner interface {
	ReserveTranscodeWorkWith(string, func(*nodepool.Node) bool) (*nodepool.Node, func())
}

// NewNodeExtractor returns an extractor that reserves nodes through planner,
// or runs locally when the execution setting permits it.
func NewNodeExtractor(local Extractor, planner NodeWorkPlanner, settings SettingsReader) *NodeExtractor {
	return &NodeExtractor{local: local, planner: planner, settings: settings, client: &http.Client{}, logger: slog.Default().With("component", "trickplay")}
}

// Extract runs req where the execution setting says.
func (e *NodeExtractor) Extract(ctx context.Context, job *Job, req mediasample.Request) (mediasample.Result, error) {
	mode := ""
	if e.settings != nil {
		value, err := e.settings.Get(ctx, ExecutionSetting)
		if err != nil {
			return mediasample.Result{}, fmt.Errorf("%w: %s: %w", errSettingsUnreadable, ExecutionSetting, err)
		}
		mode = strings.TrimSpace(value)
	}
	if mode != ExecutionPreferTranscodeNodes && mode != ExecutionTranscodeNodesOnly {
		return e.local.Extract(ctx, job, req)
	}
	secret := readSetting(ctx, e.settings, jwtSecretSetting)
	tried := make(map[string]bool)
	for secret != "" && e.planner != nil {
		node, release := e.planner.ReserveTranscodeWorkWith("trickplay", func(candidate *nodepool.Node) bool {
			return trickplayNodeEligible(candidate) && !tried[candidate.URL]
		})
		if node == nil {
			break
		}
		tried[node.URL] = true
		result, err := e.remote(ctx, node, secret, req)
		release()
		if err == nil {
			return result, nil
		}
		failure, fromNode := errors.AsType[*ExtractError](err)
		if fromNode && failure.Permanent {
			// Only an input-specific cause is shared by every node.
			return mediasample.Result{}, err
		}
		if ctx.Err() != nil {
			return mediasample.Result{}, ctx.Err()
		}
		e.logger.DebugContext(ctx, "trickplay node could not take the work", "node", node.Name, "error", err)
	}
	if mode == ExecutionPreferTranscodeNodes {
		return e.local.Extract(ctx, job, req)
	}
	return mediasample.Result{}, errNoNode
}

// trickplayNodeEligible checks the node's current capability report. The
// planner applies the capacity and provisional reservation checks.
func trickplayNodeEligible(node *nodepool.Node) bool {
	if node == nil || !node.Enabled || !node.Healthy || !makesTrickplay(node.Capabilities) {
		return false
	}
	advertised := node.AdvertisedCapabilitiesHash
	return advertised == nil || (*advertised != "" && node.CapabilitiesHash != nil && *advertised == *node.CapabilitiesHash)
}

// makesTrickplay reports whether a node's capability report advertises the
// trickplay endpoint.
func makesTrickplay(report []byte) bool {
	var capabilities struct {
		TransportFeatures []string `json:"transport_features"`
	}
	if len(report) == 0 || json.Unmarshal(report, &capabilities) != nil {
		return false
	}
	return slices.Contains(capabilities.TransportFeatures, playback.TransportFeatureTrickplayExtractV1)
}

// remote runs req on node. The node decides whether its hardware can decode,
// so the plan always offers a hardware attempt first.
func (e *NodeExtractor) remote(ctx context.Context, node *nodepool.Node, secret string, req mediasample.Request) (mediasample.Result, error) {
	req.Attempts = AttemptPlan(true, len(req.Samples.Seconds))
	timeout := remoteOverhead
	for _, attempt := range req.Attempts {
		timeout += time.Duration(attempt.TimeoutSeconds * float64(time.Second))
	}
	body, err := json.Marshal(req)
	if err != nil {
		return mediasample.Result{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(requestCtx, http.MethodPost, nodepool.NodeEndpoint(node.URL, "/trickplay/extract"), bytes.NewReader(body))
	if err != nil {
		return mediasample.Result{}, &ExtractError{Reason: NodeUnavailableReason, Message: err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+secret)
	resp, err := telemetry.DoTrustedNode(e.client, httpReq, "trickplay_extract")
	if err != nil {
		// A node that dies mid-request is unavailable, not a failure of the file.
		return mediasample.Result{}, &ExtractError{Reason: NodeUnavailableReason, Message: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()
	limited := io.LimitReader(resp.Body, maxRemoteResultBytes)
	if resp.StatusCode != http.StatusOK {
		failure := &ExtractError{Reason: NodeUnavailableReason, Message: fmt.Sprintf("node answered %d", resp.StatusCode)}
		var payload ExtractError
		if json.NewDecoder(limited).Decode(&payload) == nil && strings.TrimSpace(payload.Reason) != "" {
			failure = &payload
		}
		if resp.StatusCode != http.StatusUnprocessableEntity && failure.Reason != NodeBusyReason {
			failure.Reason = NodeUnavailableReason
		}
		return mediasample.Result{}, failure
	}
	var result mediasample.Result
	if err := json.NewDecoder(limited).Decode(&result); err != nil {
		return mediasample.Result{}, &ExtractError{Reason: NodeUnavailableReason, Message: "read the node's sheets: " + err.Error()}
	}
	if req.Sheets.UseInputAspect && result.SheetTileHeight <= 0 {
		// An earlier worker can advertise the endpoint while ignoring the
		// geometry option. Its answer cannot describe a reliable manifest.
		return mediasample.Result{}, &ExtractError{Reason: NodeUnavailableReason, Message: "node did not report the decoded tile height"}
	}
	return result, nil
}
