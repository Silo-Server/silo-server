package jellycompat

import (
	"context"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// Some clients, Roku among them, list AC-3 or MP3 but not AAC in their video
// transcoding profiles. PlaybackInfo then negotiates one of those audio
// encodes instead (see DeviceProfile.alternateAudioOutputs), but only while an
// executor that the routing policy allows advertises its recipe. Older
// binaries do not, and would encode AAC from the same recipe.

// compatAudioEncodeRecipe names the transformation that encodes codec, for
// each non-AAC codec compat can encode.
func compatAudioEncodeRecipe(codec string) (name, version string, ok bool) {
	switch codec {
	case compatAudioCodecAC3:
		return playback.TransformationAudioToAC3V3, playback.TransformationAudioToAC3RecipeVersionV3, true
	case compatAudioCodecMP3:
		return playback.TransformationAudioToMP3V3, playback.TransformationAudioToMP3RecipeVersionV3, true
	}
	return "", "", false
}

// compatSourceEncodedAudio returns the non-AAC codec source's HLS bytes
// encode, or "" when they encode AAC or copy the audio.
func compatSourceEncodedAudio(source PlaybackMediaSource) string {
	if !compatHLSTranscodesAudio(source) {
		return ""
	}
	if codec := compatSourceTargetAudioCodec(source); codec != compatTargetAudioCodec {
		return codec
	}
	return ""
}

func compatSupportsAudioEncode(transformations []playback.TransformationV3, codec string) bool {
	name, version, ok := compatAudioEncodeRecipe(codec)
	if !ok {
		return false
	}
	for _, transformation := range transformations {
		if strings.EqualFold(strings.TrimSpace(transformation.Name), name) &&
			strings.EqualFold(strings.TrimSpace(transformation.Executor), playback.ExecutorServerV3) &&
			strings.TrimSpace(transformation.RecipeVersion) == version {
			return true
		}
	}
	return false
}

func (h *PlaybackHandler) compatLocalAudioEncodeAvailable(ctx context.Context, codec string) bool {
	name, _, ok := compatAudioEncodeRecipe(codec)
	if !ok {
		return false
	}
	registry, err := h.localAudioTransformationRegistry(ctx)
	return err == nil && registry.Available(name)
}

func compatNodeSupportsAudioEncode(node *nodepool.Node, codec string) bool {
	if node == nil {
		return false
	}
	info, ok := compatNodeReport(node)
	return ok && compatSupportsAudioEncode(info.Transformations, codec)
}

// compatAudioEncodeExecutable reports whether some executor the routing policy
// allows for an HLS remux or video encode can encode codec. Stored node
// reports keep negotiation independent of slow capability requests.
func (h *PlaybackHandler) compatAudioEncodeExecutable(ctx context.Context, codec string) bool {
	localChecked, localAvailable := false, false
	for _, request := range []noderouting.Request{
		{Workload: noderouting.WorkloadRemux, Delivery: noderouting.DeliveryHLSRemux},
		{Workload: noderouting.WorkloadVideoTranscode, Delivery: noderouting.DeliveryHLSVideo},
	} {
		request.Policy, request.ProxyAllowed = h.playbackRoutingPolicy(), h.JWTSecret != ""
		compiled, err := noderouting.Candidates(request)
		if err != nil {
			continue
		}
		for _, shape := range compiled.Candidates {
			switch shape.Execution {
			case noderouting.ExecutionAPI:
				if !localChecked {
					localChecked, localAvailable = true, h.compatLocalAudioEncodeAvailable(ctx, codec)
				}
				if localAvailable {
					return true
				}
			case noderouting.ExecutionTranscode:
				if h.compatAnyTranscodeNode(func(node *nodepool.Node) bool { return compatNodeSupportsAudioEncode(node, codec) }) {
					return true
				}
			}
		}
	}
	return false
}

// compatAnyTranscodeNode reports whether a pooled transcode node that takes
// new work satisfies supports.
func (h *PlaybackHandler) compatAnyTranscodeNode(supports func(*nodepool.Node) bool) bool {
	enumerator, canList := h.NodePlanner.(compatTranscodeNodeEnumerator)
	lookup, canLookup := h.NodePlanner.(compatTranscodeNodeLookup)
	if !canList || !canLookup {
		return false
	}
	for _, nodeURL := range enumerator.TranscodeNodeURLs() {
		if node, ok := lookup.TranscodeNodeByURL(nodeURL); ok && node.TakesNewWork() && supports(node) {
			return true
		}
	}
	return false
}

// compatAudioEncodeRouting keeps a non-AAC audio recipe on executors that
// advertise it, excluding the API host's shape when its FFmpeg cannot.
func (h *PlaybackHandler) compatAudioEncodeRouting(
	ctx context.Context,
	codec string,
	videoTranscode bool,
	eligible func(*nodepool.Node) bool,
	excludedShapes map[string]struct{},
) (func(*nodepool.Node) bool, map[string]struct{}) {
	baseEligible := eligible
	eligible = func(node *nodepool.Node) bool {
		return compatNodeSupportsAudioEncode(node, codec) && (baseEligible == nil || baseEligible(node))
	}
	if !h.compatLocalAudioEncodeAvailable(ctx, codec) {
		excluded := make(map[string]struct{}, len(excludedShapes)+1)
		for id := range excludedShapes {
			excluded[id] = struct{}{}
		}
		if videoTranscode {
			excluded["hls_video_api"] = struct{}{}
		} else {
			excluded[noderouting.ShapeHLSRemuxAPI] = struct{}{}
		}
		excludedShapes = excluded
	}
	return eligible, excludedShapes
}

// requireLocalAudioEncodeCapability refuses to start a non-AAC audio recipe on
// an API host whose FFmpeg does not advertise it.
func (h *PlaybackHandler) requireLocalAudioEncodeCapability(ctx context.Context, source PlaybackMediaSource) error {
	codec := compatSourceEncodedAudio(source)
	if codec == "" || h.compatLocalAudioEncodeAvailable(ctx, codec) {
		return nil
	}
	name, version, _ := compatAudioEncodeRecipe(codec)
	return fmt.Errorf("%w: local FFmpeg does not support %s recipe %s", errAudioDownmixCapabilityUnavailable, name, version)
}
