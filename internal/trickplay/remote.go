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
	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/telemetry"
)

// Reasons a transcode node gives for not running a request, which move the
// work to another node rather than failing it.
const (
	NodeBusyReason        = "node_busy"
	NodeUnavailableReason = "node_unavailable"
	// InputUnreadableReason is a node whose ffmpeg cannot open an input that
	// passed its path check, such as a file its user may not read. It moves
	// the work on like the reasons above: that run fails as it opens the
	// input, so a retry a minute later costs little.
	InputUnreadableReason = "input_unreadable"
)

// jwtSecretSetting authenticates the API server to its transcode nodes.
const jwtSecretSetting = "auth.jwt_secret"

const (
	// remoteOverhead covers the transfer and the node's own start on top of
	// a request's attempt timeouts.
	remoteOverhead = 30 * time.Second
	// Generation caps chunks at 32 million pixels, including the initial
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
// work back to the queue instead, or fails it when a node ran the work and no
// node made the sheets.
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
	// ran is the last failure of a node that ran the work, as opposed to one
	// that was busy, unreachable, died mid-request, could not list its
	// capabilities, or could not read the file. A node that skips the work
	// is not waited for: it may refuse this file on every pass while staying
	// healthy.
	var ran error
	// skipped is the last answer of a node that did not run the work, kept
	// so the queue row says why, such as a node that may not read the file.
	var skipped error
	// lacking is the last answer of a node whose ffmpeg lacks something the
	// run needs. It counts against the file, as when this server's own ffmpeg
	// lacks it, unless a node was busy and may have it once free. A node that
	// refuses or cannot read the file does so on every pass, so it does not
	// hold the file on the one-minute retry.
	var lacking error
	busy := false
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
		if fromNode && failure.Reason == string(mediasample.ReasonUnsupported) {
			lacking = fmt.Errorf("no transcode node can make the sheets; %s: %w", node.Name, err)
		} else if nodeFault(err) {
			skipped = fmt.Errorf("%s: %w", node.Name, err)
			busy = busy || (fromNode && failure.Reason == NodeBusyReason)
		} else {
			ran = fmt.Errorf("no transcode node made the sheets; last ran on %s: %w", node.Name, err)
		}
		e.logger.DebugContext(ctx, "trickplay node could not take the work", "node", node.Name, "error", err)
	}
	if mode == ExecutionPreferTranscodeNodes {
		return e.local.Extract(ctx, job, req)
	}
	if ran != nil {
		// A node ran the work and no node made the sheets: count a failure,
		// so the file backs off as it would on this server.
		return mediasample.Result{}, ran
	}
	if lacking != nil && !busy {
		return mediasample.Result{}, lacking
	}
	if skipped != nil {
		return mediasample.Result{}, fmt.Errorf("%w; last answer from %w", errNoNode, skipped)
	}
	return mediasample.Result{}, errNoNode
}

// nodeFault reports whether a remote failure belongs to the node rather than
// to the file, so it says nothing about whether the file can yield sheets.
// unsupported names something the node's ffmpeg or host lacks, such as a
// tone-map filter, which another node may have; Extract counts it only when
// no node that answered had it.
func nodeFault(err error) bool {
	failure, ok := errors.AsType[*ExtractError](err)
	if !ok {
		return true
	}
	switch failure.Reason {
	case NodeBusyReason, NodeUnavailableReason, InputUnreadableReason,
		string(mediasample.ReasonUnsupported), string(mediasample.ReasonCapabilities):
		return true
	}
	return false
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
// trickplay endpoint at the version this server's requests need: one that
// reads the tone-map source signal, which an older node would ignore and
// convert with the best-effort chains.
func makesTrickplay(report []byte) bool {
	var capabilities struct {
		TransportFeatures []string `json:"transport_features"`
	}
	if len(report) == 0 || json.Unmarshal(report, &capabilities) != nil {
		return false
	}
	return slices.Contains(capabilities.TransportFeatures, playback.TransportFeatureTrickplayExtractV2)
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
	if err := nodeauth.Sign(httpReq, node.URL, secret); err != nil {
		return mediasample.Result{}, &ExtractError{Reason: NodeUnavailableReason, Message: err.Error()}
	}
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
