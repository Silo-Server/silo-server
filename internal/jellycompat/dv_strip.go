package jellycompat

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// Jellyfin clients reject a Dolby Vision range type they cannot render by
// listing it in a VideoRangeType condition; Jellyfin Android TV does so for
// DOVIWithHDR10 when the user disables Dolby Vision profile 8 or the display
// lacks Dolby Vision. Jellyfin's server then copies the video and strips the
// Dolby Vision RPUs, handing the client the HDR10 base layer unchanged. The
// helpers below give jellycompat the same route: an HLS remux whose video copy
// runs the dovi_rpu strip, instead of a tone-mapped encode or no route at all.

// compatDVStripCandidate reports whether a version's primary video is Dolby
// Vision with an HDR10 base layer that survives the strip: every profile 7
// stream (the enhancement layer is dropped by stream mapping) and profile 8
// streams whose compatibility id identifies HDR10. It mirrors the native
// planner's canStripDolbyVisionToHDR10V3.
func compatDVStripCandidate(version catalog.FileVersion) bool {
	video := compatPrimaryVideoTrack(version)
	switch strings.ToLower(strings.TrimSpace(video.Codec)) {
	case compatVideoCodecHEVC, compatVideoCodecH265:
	default:
		return false
	}
	return video.DVProfile == 7 || video.DVProfile == 8 && video.DVBLCompatID == 1
}

// compatHDR10BaseVersion describes the stream a strip produces: the same
// file with the primary video's Dolby Vision signaling removed, so device
// profile conditions evaluate the HDR10 (or HDR10+) base layer the client
// will actually decode.
func compatHDR10BaseVersion(version catalog.FileVersion) catalog.FileVersion {
	if len(version.VideoTracks) == 0 {
		return version
	}
	tracks := append(version.VideoTracks[:0:0], version.VideoTracks...)
	video := tracks[0]
	video.VideoRangeType = compatRangeHDR10
	if video.HDR10Plus {
		video.VideoRangeType = compatRangeHDR10Plus
	}
	video.DolbyVision = ""
	video.DVProfile = 0
	video.DVLevel = 0
	video.DVBLCompatID = 0
	video.DVConfigPresent = false
	video.DVBLCompatIDPresent = false
	video.DVBLPresent = false
	video.DVRPUPresent = false
	tracks[0] = video
	version.VideoTracks = tracks
	version.HDR = true
	return version
}

// applyCompatDVStrip replaces source with an HDR10 strip remux when the client
// rejects the Dolby Vision stream as-is but accepts its HDR10 base layer and
// some executor the routing policy allows can run the strip. Sources the
// client can already play, direct stream, or copy are returned unchanged.
func (h *PlaybackHandler) applyCompatDVStrip(
	ctx context.Context,
	routeItemID, playSessionID string,
	source PlaybackMediaSource,
	profile DeviceProfile,
	req playbackInfoRequest,
	allow4KTranscode bool,
) PlaybackMediaSource {
	if source.SupportsDirectPlay || source.SupportsDirectStream || compatHLSCopiesVideo(source) ||
		!compatDVStripCandidate(source.Version) {
		return source
	}
	// Direct play would hand the client the original Dolby Vision bytes, so
	// evaluate the base layer only as a copy remux, tagged as the strip writes it.
	req.EnableDirectPlay = boolPtr(false)
	profile.hlsRemuxSampleEntry = playback.VideoSampleEntryHVC1
	// Keep the negotiated encoder available if a later subtitle selection
	// requires burning into the video instead of copying the HDR10 base layer.
	allowHEVCEncoding := compatSourceTargetVideoCodec(source) == compatVideoCodecHEVC
	stripped := h.buildPlaybackSource(routeItemID, playSessionID, compatHDR10BaseVersion(source.Version), profile, req, allow4KTranscode, allowHEVCEncoding)
	if !stripped.HLSRemux {
		return source
	}
	// A surround-to-stereo AAC remux also needs audio_to_aac v2 on the same
	// executor.
	if !h.compatDVStripExecutable(ctx, source.Version, compatHLSRecipeSourceAudioChannels(stripped)) {
		return source
	}
	// The negotiated source still names the original file: MediaStreams keep
	// describing its Dolby Vision video, as Jellyfin does for a transcode.
	stripped.Version = source.Version
	stripped.ETag = source.ETag
	stripped.SupportsDirectPlay = false
	stripped.SupportsDirectStream = false
	stripped.DOVIVariant = false
	stripped.DVStripToHDR10 = true
	return stripped
}

// compatDVStripExecutable reports whether this file's RPUs can be stripped and
// whether a route the playback policy allows has an executor with the
// dovi_rpu filter: the API host's FFmpeg or a pooled transcode node whose
// stored capability report advertises server_dv7_to_hdr10. Negotiation reads
// stored reports rather than asking nodes, so an unresponsive node cannot
// stall PlaybackInfo.
func (h *PlaybackHandler) compatDVStripExecutable(ctx context.Context, version catalog.FileVersion, sourceAudioChannels int) bool {
	compiled, err := noderouting.Candidates(noderouting.Request{
		Workload: noderouting.WorkloadRemux, Delivery: noderouting.DeliveryHLSRemux,
		Policy: h.playbackRoutingPolicy(), ProxyAllowed: h.JWTSecret != "",
	})
	if err != nil {
		slog.WarnContext(ctx, "compile Jellyfin-compatible Dolby Vision strip routes", "component", "jellycompat", "error", err)
		return false
	}
	apiAllowed, transcodeAllowed := false, false
	for _, shape := range compiled.Candidates {
		switch shape.Execution {
		case noderouting.ExecutionAPI:
			apiAllowed = true
		case noderouting.ExecutionTranscode:
			transcodeAllowed = true
		}
	}
	executor := apiAllowed && h.compatAPIHostCanStrip(ctx, sourceAudioChannels) ||
		transcodeAllowed && h.compatAnyTranscodeNodeCanStrip(sourceAudioChannels > 2)
	// The per-file RPU probe can read the source for seconds, so it runs only
	// once an executor exists.
	return executor && h.compatDVRPUStrippable(ctx, version.FilePath)
}

func (h *PlaybackHandler) compatDVRPUStrippable(ctx context.Context, filePath string) bool {
	if h.compatDVRPUProbe != nil {
		return h.compatDVRPUProbe(ctx, filePath)
	}
	return playback.DVRPUStrippable(ctx, h.FFmpegPath, filePath)
}

// compatAPIHostCanStrip reports whether the API host can run a strip remux:
// the dovi_rpu recipe, and audio_to_aac v2 as well when it downmixes surround
// audio.
func (h *PlaybackHandler) compatAPIHostCanStrip(ctx context.Context, sourceAudioChannels int) bool {
	return h.compatDVStripLocalAvailable(ctx) && h.requireLocalAudioDownmixCapability(ctx, sourceAudioChannels) == nil
}

// compatDVStripLocalAvailable reads the API host's cached transformation
// registry, the same probe that gates local audio_to_aac, for the dovi_rpu
// strip recipe.
func (h *PlaybackHandler) compatDVStripLocalAvailable(ctx context.Context) bool {
	if h.compatDVStripLocalProbe != nil {
		return h.compatDVStripLocalProbe()
	}
	registry, err := h.localAudioTransformationRegistry(ctx)
	return err == nil && registry.Available(playback.TransformationServerDV7HDR10V3)
}

// compatAnyTranscodeNodeCanStrip reports whether a routable pooled transcode
// node's stored capability report advertises the strip recipe, and audio_to_aac v2
// too when the remux downmixes surround audio.
func (h *PlaybackHandler) compatAnyTranscodeNodeCanStrip(requiresAudioBoost bool) bool {
	enumerator, canList := h.NodePlanner.(compatTranscodeNodeEnumerator)
	lookup, canLookup := h.NodePlanner.(compatTranscodeNodeLookup)
	if !canList || !canLookup {
		return false
	}
	for _, nodeURL := range enumerator.TranscodeNodeURLs() {
		// The URL list includes unhealthy nodes; only a node route selection
		// could pick right now makes the strip executable.
		node, ok := lookup.TranscodeNodeByURL(nodeURL)
		if !ok || !node.Enabled || !node.Healthy {
			continue
		}
		if info, ok := compatNodeReport(node); ok && compatSupportsDVStrip(info.Transformations) &&
			(!requiresAudioBoost || compatSupportsAudioBoost(info.Transformations)) {
			return true
		}
	}
	return false
}

// compatTranscodeNodeCanStrip checks the stored report of the node a remote
// start is about to use. Restarts and audio switches can reach a node without
// fresh route selection, so the start confirms the recipe itself; a node whose
// FFmpeg then rejects the filter fails the start and the caller retries
// elsewhere.
func (h *PlaybackHandler) compatTranscodeNodeCanStrip(nodeURL string) bool {
	lookup, ok := h.NodePlanner.(compatTranscodeNodeLookup)
	if !ok {
		return false
	}
	node, ok := lookup.TranscodeNodeByURL(strings.TrimRight(nodeURL, "/"))
	return ok && compatNodeCanStrip(node)
}

// compatNodeCanStrip reads a node's stored capability report, which the health
// sweep refetches whenever the node's reported hash changes.
func compatNodeCanStrip(node *nodepool.Node) bool {
	info, ok := compatNodeReport(node)
	return ok && compatSupportsDVStrip(info.Transformations)
}

// compatNodeReport decodes a node's stored capability report.
func compatNodeReport(node *nodepool.Node) (playback.HWAccelInfo, bool) {
	var info playback.HWAccelInfo
	raw := node.StoredCapabilities()
	if len(raw) == 0 || json.Unmarshal(raw, &info) != nil {
		return playback.HWAccelInfo{}, false
	}
	return info, true
}

func compatSupportsDVStrip(transformations []playback.TransformationV3) bool {
	for _, transformation := range transformations {
		if strings.EqualFold(strings.TrimSpace(transformation.Name), playback.TransformationServerDV7HDR10V3) &&
			strings.EqualFold(strings.TrimSpace(transformation.Executor), playback.ExecutorServerV3) &&
			strings.TrimSpace(transformation.RecipeVersion) == compatDVStripRecipeVersion {
			return true
		}
	}
	return false
}

// compatDVStripRecipeVersion pins the server_dv7_to_hdr10 recipe jellycompat
// runs, matching the version the native planner freezes into its plans.
const compatDVStripRecipeVersion = "1"

// compatDVStripRouting narrows HLS route selection for a strip remux to
// executors that can run it: transcode nodes whose stored report advertises
// the recipe, and the API host only when it can run the whole recipe,
// including a surround downmix. Node audio capability is already required by
// compatTranscodeEligibility.
func (h *PlaybackHandler) compatDVStripRouting(
	ctx context.Context,
	eligible func(*nodepool.Node) bool,
	excludedShapes map[string]struct{},
	sourceAudioChannels int,
) (func(*nodepool.Node) bool, map[string]struct{}) {
	baseEligible := eligible
	eligible = func(node *nodepool.Node) bool {
		return node != nil && compatNodeCanStrip(node) && (baseEligible == nil || baseEligible(node))
	}
	if !h.compatAPIHostCanStrip(ctx, sourceAudioChannels) {
		excluded := make(map[string]struct{}, len(excludedShapes)+1)
		for id := range excludedShapes {
			excluded[id] = struct{}{}
		}
		excluded[noderouting.ShapeHLSRemuxAPI] = struct{}{}
		excludedShapes = excluded
	}
	return eligible, excludedShapes
}

// compatCopyVideoRecipe returns the sample entry and bitstream filter for a
// copy-video HLS session: the HDR10 strip tags the base layer hvc1, and an
// unstripped Dolby Vision copy keeps its dvh1 entry.
func compatCopyVideoRecipe(source PlaybackMediaSource, dvProfile int) (sampleEntry, bitstreamFilter string) {
	if source.DVStripToHDR10 {
		return playback.VideoSampleEntryHVC1, playback.DV7ToHDR10BitstreamFilter
	}
	return playback.VideoSampleEntryForDVCopy(dvProfile), ""
}
