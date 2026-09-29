package jellycompat

import (
	"context"
	"maps"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// compatHEVCExecutable checks complete recipes on executors allowed by policy.
// Stored node reports keep negotiation independent of slow capability requests.
func (h *PlaybackHandler) compatHEVCExecutable(ctx context.Context, source PlaybackMediaSource) bool {
	compiled, err := noderouting.Candidates(noderouting.Request{
		Workload: noderouting.WorkloadVideoTranscode, Delivery: noderouting.DeliveryHLSVideo,
		Policy: h.playbackRoutingPolicy(), ProxyAllowed: h.JWTSecret != "",
	})
	if err != nil {
		return false
	}
	toneMapPolicy := h.compatHEVCToneMapPolicy(ctx, source)
	for _, shape := range compiled.Candidates {
		switch shape.Execution {
		case noderouting.ExecutionAPI:
			if h.compatLocalHEVCSupportsSource(ctx, source, toneMapPolicy) {
				return true
			}
		case noderouting.ExecutionTranscode:
			enumerator, canList := h.NodePlanner.(compatTranscodeNodeEnumerator)
			lookup, canLookup := h.NodePlanner.(compatTranscodeNodeLookup)
			if !canList || !canLookup {
				continue
			}
			for _, nodeURL := range enumerator.TranscodeNodeURLs() {
				node, ok := lookup.TranscodeNodeByURL(nodeURL)
				if ok && node != nil && node.Enabled && node.Healthy && compatHEVCNodeSupportsSource(node, source, toneMapPolicy) {
					return true
				}
			}
		}
	}
	return false
}

func (h *PlaybackHandler) compatLocalHEVCSupportsSource(ctx context.Context, source PlaybackMediaSource, policy tonemap.Policy) bool {
	registry, err := h.localAudioTransformationRegistry(ctx)
	if err != nil || !registry.Available(playback.TransformationVideoToHEVCV3) ||
		(compatHLSRecipeSourceAudioChannels(source) > 0 && !compatSupportsAudioBoost(registry.Advertised())) {
		return false
	}
	if !compatVersionRequiresToneMap(source.Version) {
		return true
	}
	capabilities, err := h.localToneMapCapabilities(ctx)
	return err == nil && compatHEVCToneMapSupported(source, capabilities, policy)
}

func compatHEVCNodeSupportsSource(node *nodepool.Node, source PlaybackMediaSource, policy tonemap.Policy) bool {
	if node == nil {
		return false
	}
	info, ok := compatNodeReport(node)
	return ok && compatSupportsHEVCEncoding(info.Transformations) &&
		(compatHLSRecipeSourceAudioChannels(source) == 0 || compatSupportsAudioBoost(info.Transformations)) &&
		compatHEVCToneMapSupported(source, info.ToneMapCapabilities, policy)
}

func compatHEVCToneMapSupported(source PlaybackMediaSource, capabilities tonemap.Capabilities, policy tonemap.Policy) bool {
	if !compatVersionRequiresToneMap(source.Version) {
		return true
	}
	_, err := resolveCompatToneMapRecipeWithPolicy(&models.MediaFile{
		ID: source.FileID, HDR: source.Version.HDR, VideoTracks: source.Version.VideoTracks,
	}, capabilities, policy)
	return err == nil
}

// compatHEVCRouting preserves the frozen codec while excluding executors that
// cannot run it, including the API fallback when only workers support HEVC.
func (h *PlaybackHandler) compatHEVCRouting(
	ctx context.Context,
	source PlaybackMediaSource,
	eligible func(*nodepool.Node) bool,
	excludedShapes map[string]struct{},
) (func(*nodepool.Node) bool, map[string]struct{}) {
	toneMapPolicy := h.compatHEVCToneMapPolicy(ctx, source)
	baseEligible := eligible
	eligible = func(node *nodepool.Node) bool {
		return compatHEVCNodeSupportsSource(node, source, toneMapPolicy) && (baseEligible == nil || baseEligible(node))
	}
	if !h.compatLocalHEVCSupportsSource(ctx, source, toneMapPolicy) {
		excludedShapes = maps.Clone(excludedShapes)
		if excludedShapes == nil {
			excludedShapes = make(map[string]struct{})
		}
		excludedShapes["hls_video_api"] = struct{}{}
	}
	return eligible, excludedShapes
}

// Read policy once before the pool invokes eligibility under its lock.
func (h *PlaybackHandler) compatHEVCToneMapPolicy(ctx context.Context, source PlaybackMediaSource) tonemap.Policy {
	if !compatVersionRequiresToneMap(source.Version) {
		return tonemap.PolicyNone
	}
	return h.toneMapPolicy(ctx)
}
