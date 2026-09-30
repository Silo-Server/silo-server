package trickplay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
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
	// maxRemoteResultBytes bounds a node's answer: at most maxChunkSheets
	// sheets of JPEG, base64-encoded.
	maxRemoteResultBytes = 256 << 20
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
	nodes    interface{ Nodes() []*nodepool.Node }
	settings SettingsReader
	client   *http.Client
	logger   *slog.Logger
}

// NewNodeExtractor returns an extractor that runs on nodes from pool, or on
// local.
func NewNodeExtractor(local Extractor, pool interface{ Nodes() []*nodepool.Node }, settings SettingsReader) *NodeExtractor {
	return &NodeExtractor{local: local, nodes: pool, settings: settings, client: &http.Client{}, logger: slog.Default().With("component", "trickplay")}
}

// Extract runs req where the execution setting says.
func (e *NodeExtractor) Extract(ctx context.Context, job *Job, req mediasample.Request) (mediasample.Result, error) {
	mode := readSetting(ctx, e.settings, ExecutionSetting)
	if mode != ExecutionPreferTranscodeNodes && mode != ExecutionTranscodeNodesOnly {
		return e.local.Extract(ctx, job, req)
	}
	secret := readSetting(ctx, e.settings, jwtSecretSetting)
	for _, node := range e.eligible() {
		if secret == "" {
			break
		}
		result, err := e.remote(ctx, node, secret, req)
		if err == nil {
			return result, nil
		}
		failure, fromNode := errors.AsType[*ExtractError](err)
		if fromNode && failure.Reason != NodeBusyReason && failure.Reason != NodeUnavailableReason {
			// The node ran the request and it failed: another node would too.
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

// eligible is the enabled, healthy nodes that make trickplay sheets, in
// random order, so the API servers do not all pick the same one.
func (e *NodeExtractor) eligible() []*nodepool.Node {
	if e.nodes == nil {
		return nil
	}
	var out []*nodepool.Node
	for _, node := range e.nodes.Nodes() {
		if node != nil && node.Enabled && node.Healthy && makesTrickplay(node.Capabilities) {
			out = append(out, node)
		}
	}
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
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
	return result, nil
}
