package playback

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

type PlannerSettingsV3 struct {
	TranscodeEnabled  bool
	Allow4KTranscode  bool
	AllowHEVCEncoding bool
	// PreferLowerVersionTranscode lets a lower-resolution version serve the
	// qualities it reaches when 4K transcoding is allowed too; with 4K
	// transcoding off the lower version serves them regardless.
	PreferLowerVersionTranscode bool
	HardwareToneMapEnabled      bool
	SoftwareToneMapEnabled      bool
	// ViewerTranscodeDisabled reflects the viewer's account policy. Session
	// admission enforces it; the planner only uses it to stop advertising
	// quality rungs the viewer cannot start.
	ViewerTranscodeDisabled bool
}

const (
	TerminalMessage4KTranscodeDisabledV3   = "A lower-resolution source is required because 4K transcoding is disabled."
	terminalMessageLowerVersionServesV3    = "A lower-resolution version serves this quality."
	containerMP4V3                         = "mp4"
	containerMKVV3                         = "mkv"
	containerHLSV3                         = "hls"
	mimeVideoMP4V3                         = "video/mp4"
	degradationAudioConvertedV3            = "audio_converted"
	audioCodecAACV3                        = "aac"
	codecCopyV3                            = "copy"
	serverAudioAdaptationReasonV3          = "server_audio_adaptation"
	decisionReasonAudioAdaptationV3        = "audio_adaptation"
	hlsAudioAdaptationReasonV3             = "hls_audio_adaptation"
	decisionReasonContainerNormalizationV3 = "container_normalization"
	audioLayoutMonoV3                      = "mono"
	audioLayoutStereoV3                    = "stereo"
	audioLayoutSurround51V3                = "5.1"
)

type PlannerInputV3 struct {
	Request StartRequestV3
	// ServerBitrateCapKbps is an administrator-owned, per-attempt ceiling.
	// Unlike a client's bandwidth preference it must fail closed.
	ServerBitrateCapKbps int
	RequestedFile        *models.MediaFile
	EffectiveFile        *models.MediaFile
	// LowerVersion names the version that serves lower qualities when the
	// requested version is 4K and 4K transcoding is disabled. Nil when the
	// item has no such version or the caller did not look for one.
	LowerVersion    *LowerVersionV3
	AudioTrackIndex int
	Settings        PlannerSettingsV3
	// Registry holds the transformations the local binary can execute.
	Registry *TransformationRegistryV3
	// ProgressiveRemuxRegistry, HLSRemuxRegistry, and HLSVideoRegistry report
	// what policy-eligible executors of each delivery can run. They are lazy
	// because building one can touch pooled nodes; the transport re-validates
	// the selected executor. HLSRegistry is the shared fallback for callers
	// without workload-specific HLS registries.
	ProgressiveRemuxRegistry func() *TransformationRegistryV3
	HLSRemuxRegistry         func() *TransformationRegistryV3
	HLSVideoRegistry         func() *TransformationRegistryV3
	HLSRegistry              func() *TransformationRegistryV3
	// ToneMapCapabilities and HLSToneMapCapabilities mirror Registry and
	// HLSVideoRegistry for executor variants of hdr_to_sdr_tonemap. The public
	// plan names one stable transformation; these internal capabilities select
	// a validated hardware or software implementation without exposing that
	// deployment policy to clients.
	ToneMapCapabilities    tonemap.Capabilities
	HLSToneMapCapabilities func() tonemap.Capabilities
	// DVRPUStrippable reports whether this source survives the Dolby Vision
	// RPU strip, which no capability probe can answer. Nil means it does. Lazy
	// because it runs ffmpeg.
	DVRPUStrippable func() bool
	// SubtitleRenditionExecutable reports whether some policy-eligible HLS
	// video executor can emit an HLS subtitle rendition. Nil means none can.
	SubtitleRenditionExecutable func() bool
	// RemuxSubtitleRenditionExecutable is SubtitleRenditionExecutable for an
	// HLS copy remux, whose executors are chosen under the remux routing
	// policy and must advertise TransportFeatureHLSRemuxSubtitleRenditionV1.
	RemuxSubtitleRenditionExecutable func() bool
	// OnDemandSubtitleRenditionsExecutable and its remux twin report whether
	// some policy-eligible executor of the delivery serves on-demand
	// renditions (TransportFeatureHLSOnDemandSubtitleRenditionsV1). Nil means
	// none can.
	OnDemandSubtitleRenditionsExecutable      func() bool
	RemuxOnDemandSubtitleRenditionsExecutable func() bool
	// CueIndexedSubtitleTracks answers which of EffectiveFile's subtitle
	// tracks, by container track number, the source's index can read, and in
	// which file version (CueIndexedSubtitleTracks). Nil, or a nil answer,
	// means none: the plan keeps the single pipeline rendition. Lazy because
	// it reads the file.
	CueIndexedSubtitleTracks func() *CueIndexedSubtitles
	Now                      time.Time
	AttemptedKeys            []string
	AdditionalSubtitles      []SubtitleInventoryEntryV3
	// SubtitleTrackFacts answers what the server learned about embedded tracks
	// by reading them, for the native_embedded limitations a client declares.
	// Nil means nothing is known, which never rules a track out.
	SubtitleTrackFacts SubtitleTrackFactsV3
	// LocalNetwork reports that the request came from the server's local
	// network (streamlocation.Local). False is treated as remote.
	LocalNetwork bool
}

// SourceExecutionMetadataV3 is the immutable source probe snapshot used to
// reopen a frozen playback recipe without consuming later catalog drift.
type SourceExecutionMetadataV3 struct {
	VideoCodec                 string
	VideoProfile               string
	VideoBitDepth              int
	SoftwareVideoDecode        bool
	DurationSeconds            float64
	ToneMapSourceKind          tonemap.SourceKind
	ToneMapPreflightRequired   bool
	ToneMapSourceRevision      tonemap.SourceRevision
	ToneMapDVConfigPresent     bool
	ToneMapDVBLCompatIDPresent bool
	ToneMapDVBLPresent         bool
	ToneMapDVRPUPresent        bool
}

// dvRPUStrippable resolves the per-source strip verdict, defaulting to true
// when no probe is wired in.
func (input PlannerInputV3) dvRPUStrippable() bool {
	return input.DVRPUStrippable == nil || input.DVRPUStrippable()
}

// hlsRegistry is the widened local and node registry when provided,
// otherwise the local one.
func (input PlannerInputV3) hlsRegistry() *TransformationRegistryV3 {
	if input.HLSRegistry != nil {
		if widened := input.HLSRegistry(); widened != nil {
			return widened
		}
	}
	return input.Registry
}

func (input PlannerInputV3) progressiveRemuxRegistry() *TransformationRegistryV3 {
	if input.ProgressiveRemuxRegistry != nil {
		if registry := input.ProgressiveRemuxRegistry(); registry != nil {
			return registry
		}
	}
	return input.Registry
}

func (input PlannerInputV3) hlsRemuxRegistry() *TransformationRegistryV3 {
	if input.HLSRemuxRegistry != nil {
		return input.HLSRemuxRegistry()
	}
	return input.hlsRegistry()
}

func (input PlannerInputV3) hlsVideoRegistry() *TransformationRegistryV3 {
	if input.HLSVideoRegistry != nil {
		return input.HLSVideoRegistry()
	}
	return input.hlsRegistry()
}

type PlannerResultV3 struct {
	Plan           *PlanV3
	Terminal       *TerminalV3
	PlayMethod     PlayMethod
	TranscodeAudio bool
	// RemuxResumeLeadingPictureDrop asks a progressive remux that starts past
	// zero to drop the open-GOP leading pictures macOS Firefox rejects. It is
	// best effort: an executor whose FFmpeg lacks the filter serves the plain
	// copy, so it never narrows where the route may run.
	RemuxResumeLeadingPictureDrop bool
	TargetVideoCodec              string
	TargetAudioCodec              string
	// SourceAudioChannels freezes the selected input track's channel count for
	// source-sensitive encode recipes such as multichannel-to-stereo downmixing.
	SourceAudioChannels int
	// TargetAudioChannels caps the transcode's re-encoded channel count;
	// 0 keeps the historical stereo downmix.
	TargetAudioChannels         int
	TargetAudioBitrateKbps      int
	TargetResolution            string
	TargetBitrateKbps           int
	SubtitleTrackIndex          int
	SubtitleTransportTrackIndex int
	SubtitleBurnIn              bool
	SubtitleCodec               string
	// SubtitleRendition emits the selected subtitle as the HLS stream's
	// subtitle rendition (Plan.Subtitle.HLSRendition): a transcode's, or an
	// HLS copy remux's.
	SubtitleRendition bool
	// SubtitleRenditionTracks, with SubtitleRendition, lists every embedded
	// text track the stream publishes as an on-demand rendition
	// (TranscodeOpts.SubtitleRenditionTracks); the inventory rows they come
	// from carry hls_rendition.
	SubtitleRenditionTracks []SubtitleRenditionTrack
	// SubtitleRenditionSource is the source version SubtitleRenditionTracks
	// were chosen from.
	SubtitleRenditionSource SubtitleRenditionSource
	// DownloadedSubtitleID comes from the same inventory snapshot used for
	// planning. Freezing must not re-list a mutable ordinal inventory after the
	// route has already been accepted.
	DownloadedSubtitleID int
	// FrozenSourceMetadata is set only when a durable executable recipe is
	// thawed for a seek reanchor. Transport construction must then use this
	// captured source snapshot instead of a freshly probed media row.
	FrozenSourceMetadata     *SourceExecutionMetadataV3
	ToneMapPolicy            tonemap.Policy
	ToneMapMode              tonemap.Mode
	ToneMapSourceKind        tonemap.SourceKind
	ToneMapRecipeVersion     string
	ToneMapPreflightRequired bool
	ToneMapSourceRevision    tonemap.SourceRevision
	// SettingsDependentRefusal marks a Terminal that a playback setting decided
	// (4K transcoding, tone mapping). A caller that could not load the settings
	// planned without them, so it reports such a refusal as temporary.
	SettingsDependentRefusal bool
}

// hlsToneMapCapabilities resolves the lazy pooled inventory when present and
// otherwise uses the eagerly supplied local capability set.
func (input PlannerInputV3) hlsToneMapCapabilities() tonemap.Capabilities {
	if input.HLSToneMapCapabilities != nil {
		if capabilities := input.HLSToneMapCapabilities(); capabilities != nil {
			return capabilities
		}
	}
	return input.ToneMapCapabilities
}

// applyServerBitrateCapV3 folds the administrator's bitrate cap into the
// request's own cap and reports whether the source must be encoded to fit it.
func applyServerBitrateCapV3(input *PlannerInputV3, file *models.MediaFile, source SourceDescriptorV3) bool {
	if input.ServerBitrateCapKbps <= 0 {
		return false
	}
	if requestCapKbps := optionalValueV3(input.Request.BandwidthCapKbps); requestCapKbps == 0 || input.ServerBitrateCapKbps < requestCapKbps {
		input.Request.BandwidthCapKbps = &input.ServerBitrateCapKbps
	}
	// The video track rate excludes audio and container overhead. Only a
	// known total rate can establish that a source-preserving route fits.
	capKbps := optionalValueV3(input.Request.BandwidthCapKbps)
	totalBitrateKbps := normalizeBitrateKbpsV3(file.Bitrate)
	return totalBitrateKbps <= 0 || totalBitrateKbps > capKbps || source.BitrateKbps > capKbps
}

// Native HLS consumers use hvc1/dvh1 byte recipes. Web MediaSource clients
// keep the server's existing FFmpeg-default labeling unless they explicitly
// advertise the delivery-scoped native-HLS feature.
func hlsVideoSampleEntryV3(source SourceDescriptorV3, request StartRequestV3, dvStrip bool) string {
	if !strings.EqualFold(source.VideoCodec, "hevc") {
		return ""
	}
	if !deliverySupportsFeatureV3(request, DeliveryClassHLSV3, ClientNativeHLSPlaybackV3) &&
		!usesFirstPartyAndroidMedia3HLSV3(request) {
		return ""
	}
	if !dvStrip && (source.DVProfile == 5 || source.DVProfile == 8) {
		return VideoSampleEntryDVH1
	}
	return VideoSampleEntryHVC1
}

// availableQualitiesV3 publishes the server ladder rungs a client could
// request for this source through a quality_change replan. The source rung is
// always present; transcode rungs are listed below the source resolution class,
// and at the same class when they reduce bitrate, only when the cheap transcode
// gates pass. Registry availability is deliberately not consulted: it can
// trigger lazy node-capability fetches, which source-preserving starts must
// never pay for, and a rung whose toolchain is missing degrades to a retryable
// terminal at replan time. A 4K version that may not be transcoded publishes
// the qualities its lower-resolution version serves instead.
func availableQualitiesV3(input PlannerInputV3, source SourceDescriptorV3) []AvailableQualityV3 {
	if lowerVersionServesQualitiesV3(input) {
		return lowerVersionQualitiesV3(input)
	}
	return append([]AvailableQualityV3{originalQualityEntryV3(source)}, transcodeRungsV3(input, input.EffectiveFile, source)...)
}

// activeQualityV3 names the AvailableQualities entry plan delivers, so a
// client checks the row the server chose instead of re-deriving it from its
// request. A rung the request named stays active while the plan keeps its
// resolution (an encode a cap lowered, or a source that already fits it):
// that row is the viewer's pick. Otherwise, as for a rung a decoder held to a
// smaller class, the delivery names its entry: an encode the rung of its own
// resolution class and bitrate (a plain 1080p a cap held to 720p at 2000 kbps
// is 720p Medium), and a lower version played unchanged its
// plain resolution entry ("720p"). A source played unchanged, or an encode
// between two rungs, names none; auto already says the source plays.
func activeQualityV3(request StartRequestV3, plan PlanV3) string {
	encodes := plan.Delivery == DeliveryTranscodeHLSV3
	playsLowerVersion := !encodes && plan.EffectiveMediaFileID != plan.RequestedMediaFileID
	class := ladderClassForSize(optionalValueV3(plan.EffectiveRecipe.Width), optionalValueV3(plan.EffectiveRecipe.Height))
	bitrate := optionalValueV3(plan.EffectiveRecipe.BitrateKbps)
	delivered := func(quality AvailableQualityV3) bool {
		if _, rung := ladderRungForLabelV3(quality.Label); rung {
			return encodes && class > 0 && quality.Height == class && quality.BitrateKbps == bitrate
		}
		return quality.Label != QualityOriginalV3 && playsLowerVersion && quality.Height == plan.Source.Height
	}
	requested, _ := NormalizeQualityV3(request.QualityPreference)
	for _, quality := range plan.AvailableQualities {
		if quality.Label != requested {
			continue
		}
		if rung, ok := ladderRungForLabelV3(quality.Label); ok {
			// A decoder bound fits the frame in a smaller class's box. The
			// rung's own frame never fits one, but keeping the source aspect
			// can class it higher: 1728x720 from a 2.4:1 film is 1080p.
			if !encodes || class == 0 || class >= rung.Height {
				return quality.Label
			}
		} else if delivered(quality) {
			return quality.Label
		}
	}
	for _, quality := range plan.AvailableQualities {
		if delivered(quality) {
			return quality.Label
		}
	}
	return ""
}

// originalQualityEntryV3 is the menu entry that plays source unchanged.
func originalQualityEntryV3(source SourceDescriptorV3) AvailableQualityV3 {
	return AvailableQualityV3{
		Label:           QualityOriginalV3,
		Height:          source.Height,
		BitrateKbps:     source.BitrateKbps,
		PreservesSource: true,
	}
}

// LowerVersionV3 describes the version of an item that serves lower
// qualities when the requested version is 4K and 4K transcoding is disabled.
type LowerVersionV3 struct {
	// Requested is the version the viewer asked for. The menu's original
	// entry stays this version even while the lower version plays.
	Requested SourceDescriptorV3
	// Lower is the first non-4K version in the handler's fallback order, the
	// one a lower quality moves playback to.
	Lower SourceDescriptorV3
}

// lowerVersionServesQualitiesV3 reports whether lower qualities come from
// another version. A lower quality on the 4K source is then refused with
// no_alternate_version, and the handler's alternate-version fallback plays
// the lower version: unchanged when the quality fits it, transcoded when the
// quality is lower still. Without HLS or transcoding the 4K source is refused
// for a reason the fallback does not act on, so the menu stays as it was.
func lowerVersionServesQualitiesV3(input PlannerInputV3) bool {
	return input.LowerVersion != nil && (!input.Settings.Allow4KTranscode || input.Settings.PreferLowerVersionTranscode) &&
		input.Settings.TranscodeEnabled && deliveryAvailableV3(input.Request, DeliveryClassHLSV3)
}

// lowerVersionTakesTranscodeV3 reports whether a transcode of a 4K source to
// quality, which 4K transcoding allows, belongs to its lower version instead:
// the lower version serves lower qualities and reaches quality's resolution
// class. A quality above the lower version's class still transcodes the 4K
// source.
func lowerVersionTakesTranscodeV3(input PlannerInputV3, source SourceDescriptorV3, quality QualityResultV3) bool {
	if !input.Settings.Allow4KTranscode || !lowerVersionServesQualitiesV3(input) || !is4KSourceV3(input.EffectiveFile, source) {
		return false
	}
	class := quality.Class
	if class == 0 {
		class = ladderClassForSize(quality.Width, quality.Height)
	}
	return class > 0 && class <= sourceLadderHeightV3(input.LowerVersion.Lower)
}

// lowerVersionQualitiesV3 is the menu of a 4K version whose lower qualities
// come from its lower-resolution version: original, the 4K rungs above the
// lower version's class when 4K transcoding is allowed, the lower version at
// its resolution class (for example "1080p"), which plays it unchanged, and
// the transcode rungs below that version.
func lowerVersionQualitiesV3(input PlannerInputV3) []AvailableQualityV3 {
	lower := input.LowerVersion.Lower
	qualities := []AvailableQualityV3{originalQualityEntryV3(input.LowerVersion.Requested)}
	if input.Settings.Allow4KTranscode {
		lowerClass := sourceLadderHeightV3(lower)
		for _, quality := range transcodeRungsV3(input, nil, input.LowerVersion.Requested) {
			if quality.Height > lowerClass {
				qualities = append(qualities, quality)
			}
		}
	}
	if label, ok := lowerVersionLabelV3(input.LowerVersion.Requested, lower); ok {
		qualities = append(qualities, AvailableQualityV3{
			Label:       label,
			DisplayName: label,
			Height:      lower.Height,
			BitrateKbps: lower.BitrateKbps,
		})
	}
	return append(qualities, transcodeRungsV3(input, nil, lower)...)
}

// lowerVersionLabelV3 names the plain resolution preference that plays the
// lower version unchanged: the planner preserves a source no taller than the
// preference's height. Only the 1080p and 720p classes qualify. "480p" is also
// a fixed transcode rung, and a source taller than its class, such as
// 2560x1440 in the 1080p class, would be scaled down. The requested version
// must be taller than the class, or the preference would preserve it (a
// 3840x1080 source) instead of moving playback to the lower version.
func lowerVersionLabelV3(requested, lower SourceDescriptorV3) (string, bool) {
	class := sourceLadderHeightV3(lower)
	if (class != 1080 && class != 720) || lower.Height <= 0 || lower.Height > class || requested.Height <= class {
		return "", false
	}
	return strconv.Itoa(class) + "p", true
}

// transcodeRungsV3 lists the fixed rungs a transcode of source can serve:
// none when the policy forbids transcoding it. HDR rungs are advertised from
// the configured policy alone, keeping source-preserving HDR planning lazy:
// selecting one performs the executor capability lookup during the replan;
// building the menu never probes local or pooled executors.
func transcodeRungsV3(input PlannerInputV3, file *models.MediaFile, source SourceDescriptorV3) []AvailableQualityV3 {
	if source.Height <= 0 {
		// Fixed rungs must sit strictly below a known source height; unknown
		// probe metadata cannot prove that any advertised rung avoids upscaling.
		return nil
	}
	if !deliveryAvailableV3(input.Request, DeliveryClassHLSV3) || !input.Settings.TranscodeEnabled || input.Settings.ViewerTranscodeDisabled {
		return nil
	}
	if is4KSourceV3(file, source) && !input.Settings.Allow4KTranscode {
		return nil
	}
	if source.DynamicRange != "" && source.DynamicRange != DynamicRangeSDRV3 &&
		tonemap.NewPolicy(input.Settings.HardwareToneMapEnabled, input.Settings.SoftwareToneMapEnabled) == tonemap.PolicyNone {
		return nil
	}
	var qualities []AvailableQualityV3
	for _, rung := range ladderRungsV3 {
		if !ladderRungPublishableV3(rung, source) {
			continue
		}
		qualities = append(qualities, AvailableQualityV3{
			Label:       rung.Label,
			DisplayName: rung.DisplayName,
			Height:      rung.Height,
			BitrateKbps: rung.BitrateKbps,
		})
	}
	return qualities
}

// audioAvailableQualitiesV3 is the audio-only menu: quality rungs are a video
// concept, so the only entry is the source itself.
func audioAvailableQualitiesV3(source SourceDescriptorV3) []AvailableQualityV3 {
	return []AvailableQualityV3{{Label: QualityOriginalV3, BitrateKbps: source.BitrateKbps, PreservesSource: true}}
}

const (
	// decisionReasonBandwidthCapV3 marks a plan whose recipe was constrained by
	// the request's bandwidth cap rather than by decode capability.
	decisionReasonBandwidthCapV3 = "quality_bandwidth_cap"

	// decisionReasonClientManagedDynamicRangeV3 marks an original-file plan
	// whose executor owns source-to-output dynamic-range presentation.
	decisionReasonClientManagedDynamicRangeV3 = "client_managed_dynamic_range"

	// decisionReasonClientDV8BaseLayerV3 marks an original-file plan for a
	// Dolby Vision Profile 8 source that the client plays through an ordinary
	// HEVC decoder as its compatible base layer. The recipe's dynamic_range is
	// that base range, not dolby_vision.
	decisionReasonClientDV8BaseLayerV3 = "client_dv8_base_layer"
)

func stereoDownmixSourceChannelsV3(sourceChannels, targetChannels int, transcodeAudio bool) int {
	if !transcodeAudio || sourceChannels <= 2 || (targetChannels != 0 && targetChannels != 2) {
		return 0
	}
	return sourceChannels
}

func audioOnlyAACOutputChannelsV3(request StartRequestV3, source SourceDescriptorV3) int {
	return aacOutputChannelsV3(request, DeliveryClassProgressiveV3, source.AudioChannels, false)
}

// aacOutputChannelsV3 picks an encoder-supported AAC layout that does not
// exceed the active delivery's ceiling. FFmpeg's planned AAC recipes support
// mono, stereo, and 5.1; an intermediate ceiling therefore falls back from
// 5.1 to stereo rather than advertising an output the encoder never creates.
func aacOutputChannelsV3(request StartRequestV3, deliveryClass string, sourceChannels int, preserveSurround bool) int {
	channels := 2
	if sourceChannels == 1 {
		channels = 1
	} else if preserveSurround && sourceChannels >= 6 {
		channels = 6
	}
	ceiling := deliveryChannelCeilingV3(request, deliveryClass, audioCodecAACV3)
	if ceiling <= 0 || channels <= ceiling {
		return channels
	}
	if ceiling == 1 {
		return 1
	}
	return 2
}

func audioLayoutForChannelsV3(channels int) string {
	switch channels {
	case 1:
		return audioLayoutMonoV3
	case 6:
		return audioLayoutSurround51V3
	default:
		return audioLayoutStereoV3
	}
}

func audioOnlyAACBitrateKbpsV3(bandwidthCapKbps int) int {
	const defaultAACBitrateKbps = 192
	if bandwidthCapKbps > 0 && bandwidthCapKbps < defaultAACBitrateKbps {
		return bandwidthCapKbps
	}
	return defaultAACBitrateKbps
}

func applyAudioOnlyAACConversionV3(plan *PlanV3, targetChannels, targetBitrateKbps int, bandwidthCapExceeded bool) {
	layout := audioLayoutStereoV3
	warning := "The selected audio track is converted to AAC stereo."
	if targetChannels == 1 {
		layout = audioLayoutMonoV3
		warning = "The selected audio track is converted to AAC mono."
	}
	plan.EffectiveRecipe.AudioCodec = "aac"
	plan.EffectiveRecipe.AudioChannels = intPointerV3(targetChannels)
	plan.EffectiveRecipe.AudioLayout = layout
	plan.EffectiveRecipe.BitrateKbps = intPointerV3(targetBitrateKbps)
	plan.Claims.Audio = AudioClaimsV3{Codec: "aac", Reason: "server_audio_adaptation"}
	plan.Transformations = append(plan.Transformations, TransformationV3{Name: TransformationAudioToAACV3, Executor: ExecutorServerV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, ValidatedClaims: []string{ClaimAudioDecodeV3}})
	plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{Code: degradationAudioConvertedV3, Message: warning})
	plan.DecisionReason = "audio_adaptation"
	if bandwidthCapExceeded {
		plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{Code: "bandwidth_cap_applied", Message: "Delivery quality is limited by the configured bandwidth cap."})
		plan.DecisionReason = decisionReasonBandwidthCapV3
	}
}

// h264BoundedQualityV3 fits a transcode target to the client's attested
// H.264 decoders. An encode that no decoder takes, scaled or a same-size
// codec conversion, steps down the ladder until its fitted size does,
// stopping at the smallest class since H.264 is the universal HLS output. A
// decoder takes a class only when its bitrate limit earns that class on the
// ladder, so a 1080p decoder limited to 3 Mbps gets 720p, as downloads do.
// The bitrate then stays within the limit of a decoder that takes the output.
func h264BoundedQualityV3(request StartRequestV3, quality QualityResultV3, source SourceDescriptorV3) QualityResultV3 {
	decoders := attestedH264DecodersV3(request, source.FrameRate)
	if len(decoders) == 0 {
		return quality
	}
	// The encode keeps the source's frame rate, so a decoder must take it too.
	takes := func(decoder VideoDecodeCapabilityV3, width, height int) bool {
		return decoderTakesFrameV3(decoder, width, height, source.FrameRate, true)
	}
	anyTakes := func(width, height, class int) bool {
		for _, decoder := range decoders {
			if takes(decoder, width, height) &&
				(decoder.MaxBitrateKbps <= 0 || LadderClassForBitrate(decoder.MaxBitrateKbps, source.FrameRate, transcodeCodecH264) >= class) {
				return true
			}
		}
		return false
	}
	outputClass := ladderClassForSize(quality.Width, quality.Height)
	if quality.PreservesSource {
		// A source-size conversion keeps the class its height earned on the
		// preserving route: a 2560x1080 film is 1080p here, not 2160p.
		outputClass = ladderClassForSize(0, quality.Height)
	}
	if quality.Height > 0 && !anyTakes(quality.Width, quality.Height, outputClass) {
		classes := ladderClassesFrom(ladderClassForSize(quality.Width, quality.Height))
		for i, class := range classes {
			width, height := FitLadderBox(source.Width, source.Height, class.Height)
			width, height = encodedFrame(source.Width, source.Height, width, height)
			if height == 0 {
				height = class.Height
			}
			if width == 0 {
				width = class.Width
			}
			if anyTakes(width, height, class.Height) || i == len(classes)-1 {
				quality.Label, quality.Width, quality.Height, quality.Class = heightLabel(height), width, height, class.Height
				quality.BitrateKbps = minPositiveV3(quality.BitrateKbps, ladderClassBitrateKbpsV3(class.Height))
				quality.PreservesSource = false
				break
			}
		}
	}
	limit := 0
	for _, decoder := range decoders {
		if !takes(decoder, quality.Width, quality.Height) {
			continue
		}
		if decoder.MaxBitrateKbps <= 0 {
			return quality
		}
		limit = max(limit, decoder.MaxBitrateKbps)
	}
	if limit > 0 && (quality.BitrateKbps <= 0 || quality.BitrateKbps > limit) {
		quality.BitrateKbps = limit
	}
	return quality
}

// attestedH264DecodersV3 lists the 8-bit H.264 decoders strict caps attest:
// the hardware ones when any takes the source's frame rate, else the opted-in
// software ones that do, else every attested one, hardware first. It is
// empty without detailed evidence, when H.264 stays unbounded.
func attestedH264DecodersV3(request StartRequestV3, frameRate float64) []VideoDecodeCapabilityV3 {
	caps := request.Capabilities
	if caps.VideoEvidence != EvidenceExactV3 && caps.VideoEvidence != EvidencePlatformAttestedV3 {
		return nil
	}
	softwareOptIn := HasFeatureV3(request.ClientFeatures, FeatureSoftwareVideoDecodeV3)
	var fallback []VideoDecodeCapabilityV3
	for _, hardware := range []bool{true, false} {
		var decoders []VideoDecodeCapabilityV3
		usable := false
		for _, decoder := range caps.VideoDecode {
			if decoder.Hardware != hardware || (!hardware && !softwareOptIn) || !strings.EqualFold(decoder.Codec, transcodeCodecH264) ||
				(len(decoder.BitDepths) > 0 && !containsIntV3(decoder.BitDepths, 8)) {
				continue
			}
			decoders = append(decoders, decoder)
			usable = usable || decoderTakesFrameRateV3(decoder, frameRate)
		}
		if usable {
			return decoders
		}
		if fallback == nil {
			fallback = decoders
		}
	}
	return fallback
}

func videoTransformationForTargetV3(codec string) TransformationV3 {
	if codec == transcodeCodecHEVC {
		return TransformationV3{Name: TransformationVideoToHEVCV3, Executor: ExecutorServerV3, RecipeVersion: TransformationVideoToHEVCRecipeVersionV3, ValidatedClaims: []string{ClaimHEVCDecodeV3}}
	}
	return TransformationV3{Name: TransformationVideoToH264V3, Executor: ExecutorServerV3, RecipeVersion: TransformationVideoToH264RecipeVersionV3, ValidatedClaims: []string{ClaimH264DecodeV3}}
}

const hevcMainProfileV3 = "main"

// hlsHEVCOutputSupportedV3 requires the selected HLS executor to name HEVC
// explicitly and verifies its detailed decoder can handle this server's Main
// 8-bit SDR output at the chosen dimensions and bitrate. Flat codec lists are
// insufficient here: they can describe source copy support without proving
// the fMP4 transcode decoder that will receive this recipe.
func hlsHEVCOutputSupportedV3(request StartRequestV3, quality QualityResultV3, source SourceDescriptorV3) bool {
	delivery, ok := request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
	if !ok || !delivery.Enabled || !delivery.SupportedOnDevice || !containsFoldV3(delivery.VideoCodecs, transcodeCodecHEVC) {
		return false
	}
	output := source
	output.VideoCodec = transcodeCodecHEVC
	output.VideoProfile = hevcMainProfileV3
	output.BitDepth = 8
	output.Width = quality.Width
	output.Height = quality.Height
	output.BitrateKbps = quality.BitrateKbps
	output.DynamicRange = DynamicRangeSDRV3
	// The FFmpeg HEVC recipe does not currently pin -level:v, so it must not
	// claim a level to an exact decoder with a bounded level list. A level-bound
	// client stays on H.264 until the target level is selected, frozen, and
	// enforced as part of the executable HEVC recipe.
	for _, decoder := range request.Capabilities.VideoDecode {
		if strings.EqualFold(decoder.Codec, transcodeCodecHEVC) && len(decoder.Levels) > 0 {
			return false
		}
	}
	ok, _ = videoEligibleV3(output, request)
	return ok
}

// hlsSubtitleRenditionEligibleV3 reports whether a server_transcode_hls or
// server_remux_hls plan carries its selected subtitle as an HLS subtitle
// rendition instead of a sidecar. The track must be an embedded text track the
// policy already shows as text, the client must have negotiated the feature
// (a remux also needs FeatureHLSRemuxSubtitleRenditionsV3, checked by its caller),
// an executor must be able to emit it (executable, the delivery's own check),
// and the media must fit the bounded playlist the rendition's segments follow.
// A rendition is WebVTT, so ASS keeps its sidecar when the client requires
// authored fidelity.
func hlsSubtitleRenditionEligibleV3(input PlannerInputV3, subtitle SubtitlePolicyResultV3, executable func() bool) bool {
	if !HasFeatureV3(input.Request.ClientFeatures, FeatureHLSSubtitleRenditionsV3) ||
		executable == nil || input.EffectiveFile == nil {
		return false
	}
	if subtitle.RequiresBurn || subtitle.Source != SubtitleSourceEmbeddedV3 || subtitle.SelectedIndex < 0 || subtitle.TransportIndex < 0 ||
		subtitle.Decision.Embedded != nil || !SubtitleRenditionCodecSupported(subtitle.Codec) {
		return false
	}
	// The pipeline rendition is a WebVTT output of the stream's own FFmpeg,
	// which can't demux an index-only track; on-demand renditions read it
	// through the index instead.
	if tracks := input.EffectiveFile.SubtitleTracks; subtitle.TransportIndex >= len(tracks) || tracks[subtitle.TransportIndex].IndexOnly {
		return false
	}
	if subtitle.Decision.Mode != SubtitleRenderV3 && subtitle.Decision.Mode != SubtitleConvertV3 ||
		IsASS(subtitle.Codec) && input.Request.SubtitleFidelityPreference == SubtitleFidelityPreserveV3 {
		return false
	}
	return SubtitleRenditionExecutable(float64(input.EffectiveFile.Duration), DefaultSegmentDuration) && executable()
}

// hlsRenditionsV3 is the subtitle renditions an HLS plan's stream carries:
// whether any, and the tracks it reads on demand, from which source version.
type hlsRenditionsV3 struct {
	carried bool
	tracks  []SubtitleRenditionTrack
	source  SubtitleRenditionSource
}

// maxOnDemandSubtitleRenditions bounds the on-demand renditions one plan
// lists, and with them its multivariant playlist and recipe. Real files
// rarely carry more text tracks; the rest keep their sidecar.
const maxOnDemandSubtitleRenditions = 64

// onDemandRenditionV3 is an inventory row an HLS plan lists as an on-demand
// rendition: its position in the plan's inventory and the track.
type onDemandRenditionV3 struct {
	row   int
	track SubtitleRenditionTrack
}

// hlsOnDemandSubtitleRenditionsV3 returns the embedded text tracks an HLS plan
// lists as on-demand renditions, in inventory order: every one whose codec
// WebVTT can carry (ASS too, unless the client requires authored fidelity,
// when it keeps its sidecar) and whose packets the source's index finds. Nil
// keeps the plan on the single pipeline rendition: the client or every
// executor lacks on-demand renditions, a burned-in subtitle changes the
// video, the media outruns the bounded playlist, no track qualifies, or the
// selected track would be a rendition but its packets can't be found
// through the index. It also returns the source version the index described.
func hlsOnDemandSubtitleRenditionsV3(input PlannerInputV3, plan PlanV3, subtitle SubtitlePolicyResultV3, remux bool) ([]onDemandRenditionV3, SubtitleRenditionSource) {
	features := input.Request.ClientFeatures
	if !HasFeatureV3(features, FeatureHLSSubtitleRenditionsV3) || !HasFeatureV3(features, FeatureHLSSubtitleRenditionsAllV3) ||
		remux && !HasFeatureV3(features, FeatureHLSRemuxSubtitleRenditionsV3) {
		return nil, SubtitleRenditionSource{}
	}
	executable := input.OnDemandSubtitleRenditionsExecutable
	if remux {
		executable = input.RemuxOnDemandSubtitleRenditionsExecutable
	}
	file := input.EffectiveFile
	if executable == nil || input.CueIndexedSubtitleTracks == nil || file == nil || subtitle.RequiresBurn ||
		!SubtitleRenditionExecutable(float64(file.Duration), DefaultSegmentDuration) {
		return nil, SubtitleRenditionSource{}
	}
	candidates := onDemandRenditionCandidatesV3(input, plan, subtitle, file)
	if len(candidates) == 0 || !executable() {
		return nil, SubtitleRenditionSource{}
	}
	indexed := input.CueIndexedSubtitleTracks()
	if indexed == nil {
		return nil, SubtitleRenditionSource{}
	}
	renditions := candidates[:0]
	selectedListed := false
	for _, candidate := range candidates {
		if indexed.Tracks[candidate.track.TrackNumber] {
			renditions = append(renditions, candidate)
			selectedListed = selectedListed || candidate.track.Default
		}
	}
	if len(renditions) == 0 || !selectedListed && selectedTextRenditionV3(subtitle, input.Request) {
		return nil, SubtitleRenditionSource{}
	}
	renditions = firstOnDemandRenditionsV3(renditions, maxOnDemandSubtitleRenditions)
	labels, languages := make([]string, len(renditions)), make([]string, len(renditions))
	for i, rendition := range renditions {
		labels[i], languages[i] = plan.Subtitle.Inventory[rendition.row].Label, rendition.track.Language
	}
	for i, name := range SubtitleRenditionNames(labels, languages) {
		renditions[i].track.Name = name
	}
	return renditions, indexed.Source
}

// firstOnDemandRenditionsV3 keeps the first limit renditions, in inventory
// order, with the selected one taking the last place when it comes later.
func firstOnDemandRenditionsV3(renditions []onDemandRenditionV3, limit int) []onDemandRenditionV3 {
	if len(renditions) <= limit {
		return renditions
	}
	if i := slices.IndexFunc(renditions[limit:], func(r onDemandRenditionV3) bool { return r.track.Default }); i >= 0 {
		renditions[limit-1] = renditions[limit+i]
	}
	return renditions[:limit]
}

// onDemandRenditionCandidatesV3 returns the plan's embedded inventory rows a
// WebVTT rendition can carry, with a known container track number, before
// the index is consulted. A track number repeated by a malformed file names
// only its first track.
func onDemandRenditionCandidatesV3(input PlannerInputV3, plan PlanV3, subtitle SubtitlePolicyResultV3, file *models.MediaFile) []onDemandRenditionV3 {
	preserveASS := input.Request.SubtitleFidelityPreference == SubtitleFidelityPreserveV3
	var candidates []onDemandRenditionV3
	seen := map[uint64]bool{}
	for row, item := range plan.Subtitle.Inventory {
		ordinal := item.CombinedIndex - len(file.ExternalSubtitles)
		if item.Source != SubtitleSourceEmbeddedV3 || ordinal < 0 || ordinal >= len(file.SubtitleTracks) {
			continue
		}
		track := file.SubtitleTracks[ordinal]
		number, err := strconv.ParseUint(track.ContainerTrackID, 10, 64)
		if err != nil || number == 0 || seen[number] || !SubtitleRenditionCodecSupported(track.Codec) || IsASS(track.Codec) && preserveASS {
			continue
		}
		seen[number] = true
		candidates = append(candidates, onDemandRenditionV3{row: row, track: SubtitleRenditionTrack{
			StreamIndex: track.Index, TrackNumber: number, Codec: track.Codec, Language: item.Language, Forced: item.Forced,
			Default: subtitle.Source == SubtitleSourceEmbeddedV3 && subtitle.SelectedIndex == item.CombinedIndex,
		}})
	}
	return candidates
}

// selectedTextRenditionV3 reports whether the plan's selected subtitle is one
// an HLS rendition would carry: an embedded text track shown as text.
func selectedTextRenditionV3(subtitle SubtitlePolicyResultV3, request StartRequestV3) bool {
	return subtitle.Source == SubtitleSourceEmbeddedV3 && subtitle.SelectedIndex >= 0 && subtitle.TransportIndex >= 0 &&
		subtitle.Decision.Embedded == nil && SubtitleRenditionCodecSupported(subtitle.Codec) &&
		(subtitle.Decision.Mode == SubtitleRenderV3 || subtitle.Decision.Mode == SubtitleConvertV3) &&
		(!IsASS(subtitle.Codec) || request.SubtitleFidelityPreference != SubtitleFidelityPreserveV3)
}

// applyHLSOnDemandSubtitleRenditionsV3 marks each rendition's inventory row
// with its place in the group and, when the selected track is one of them,
// moves the plan's subtitle into the stream as the pipeline rendition does.
// It returns the tracks for the recipe.
func applyHLSOnDemandSubtitleRenditionsV3(plan *PlanV3, renditions []onDemandRenditionV3) []SubtitleRenditionTrack {
	// The planner's candidate plans share one inventory; mark a copy.
	inventory := slices.Clone(plan.Subtitle.Inventory)
	tracks := make([]SubtitleRenditionTrack, len(renditions))
	for i, rendition := range renditions {
		tracks[i] = rendition.track
		inventory[rendition.row].HLSRendition = &SubtitleInventoryHLSRenditionV3{
			GroupID: SubtitleRenditionGroupID, Name: rendition.track.Name, Index: i + 1,
		}
		if rendition.track.Default {
			applyHLSSubtitleRenditionV3(plan)
			plan.Subtitle.HLSRendition.Name, plan.Subtitle.HLSRendition.Index = rendition.track.Name, i+1
		}
	}
	// Off, or a sidecar, keeps the plan's own subtitle decision.
	plan.Subtitle.Inventory = inventory
	return tracks
}

// applyHLSSubtitleRenditionV3 moves the plan's selected subtitle into its HLS
// presentation. The inventory keeps every sidecar URL, so a later selection
// of another track still has one.
func applyHLSSubtitleRenditionV3(plan *PlanV3) {
	plan.Subtitle.Artifact = nil
	plan.Subtitle.HLSRendition = &HLSSubtitleRenditionV3{
		GroupID:  SubtitleRenditionGroupID,
		Name:     SubtitleRenditionName,
		Format:   SubtitleFormatVTTV3,
		MIMEType: SubtitleMIMEVTTV3,
	}
	plan.Claims.Subtitles = SubtitleClaimsV3{Reason: "server_hls_subtitle_rendition"}
}

// applySubtitleDecisionV3 changes the delivery-specific subtitle policy without
// discarding the source inventory already frozen onto the base plan. Adapted
// routes still address the same combined ordinal space as original_http; only
// the rendering decision and claims vary by delivery capability.
func applySubtitleDecisionV3(plan *PlanV3, decision SubtitleDecisionV3) {
	if plan == nil {
		return
	}
	inventory := plan.Subtitle.Inventory
	plan.Subtitle = decision
	plan.Subtitle.Inventory = inventory
}

func canStripDolbyVisionToHDR10V3(source SourceDescriptorV3, request StartRequestV3, registry *TransformationRegistryV3) bool {
	if source.DynamicRange != DynamicRangeDolbyVisionV3 || !clientSupportsHDR10V3(request, source) || registry == nil || !registry.Available(TransformationServerDV7HDR10V3) {
		return false
	}
	// Profile 7 always carries an HDR10-viewable base layer. Profile 8 is
	// safe only when the DOVI compatibility id explicitly identifies HDR10.
	return source.DVProfile == 7 || source.DVProfile == 8 && source.DVBLCompatID == 1
}

// canServerConvertDV7ToDV81V3 reports whether an HLS copy can carry this
// Profile 7 source as Profile 8.1 to a client that takes it. Only a
// single-track source qualifies: its DOVI record says the RPUs and the
// enhancement layer travel in the base-layer track, which is the track a
// remux copies. The conversion rewrites fMP4 only, so a client that needs
// MPEG-TS copies (hls_copy_mpegts_v1) does not get it. The HLS delivery's
// own HDR details must take the 8.1 output too, as deliverySupportsPlanV3
// later requires; otherwise this would lift the planner's Dolby Vision gates
// for a plan that can never be accepted. The registry is consulted last
// because it may fetch node capabilities.
func canServerConvertDV7ToDV81V3(source SourceDescriptorV3, file *models.MediaFile, request StartRequestV3, registry func() *TransformationRegistryV3) bool {
	if source.DynamicRange != DynamicRangeDolbyVisionV3 || source.DVProfile != 7 || source.VideoCodec != transcodeCodecHEVC ||
		file == nil || len(file.VideoTracks) == 0 {
		return false
	}
	if track := file.VideoTracks[0]; !track.DVConfigPresent || !track.DVRPUPresent || !track.DVELPresent || !track.DVBLPresent {
		return false
	}
	if deliverySupportsFeatureV3(request, DeliveryClassHLSV3, FeatureHLSCopyMPEGTSV3) {
		return false
	}
	source.DVBLCompatID = 1
	if !clientSupportsDVProfileV3(request, source, 8) {
		return false
	}
	if hls := request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]; hls.HDRDetails != nil {
		source.DVProfile = 8
		if !hdrSupportsDolbyVisionSourceV3(*hls.HDRDetails, source) {
			return false
		}
	}
	r := registry()
	return r != nil && r.Available(TransformationServerDV7DV81V3)
}

func canClientTransformDV7ToDV81V3(source SourceDescriptorV3, request StartRequestV3) bool {
	if source.DynamicRange != DynamicRangeDolbyVisionV3 || source.DVProfile != 7 ||
		!clientTransformationAvailableV3(request, ClientDV7ToDV81V3, ClientDVTransformVersionV3) {
		return false
	}
	// The registered conversion recipe produces Profile 8.1.
	source.DVBLCompatID = 1
	return clientSupportsDVProfileV3(request, source, 8)
}

func canClientTransformDV7ToHDR10V3(source SourceDescriptorV3, request StartRequestV3) bool {
	return source.DynamicRange == DynamicRangeDolbyVisionV3 && source.DVProfile == 7 && clientSupportsHDR10V3(request, source) &&
		clientTransformationAvailableV3(request, ClientDV7ToHDR10V3, ClientDVTransformVersionV3)
}

func clientSupportsDVProfileV3(request StartRequestV3, source SourceDescriptorV3, profile int) bool {
	hdr := nativeOutputHDRV3(request)
	source.DVProfile = profile
	return hdr != nil && hdrSupportsDolbyVisionSourceV3(*hdr, source)
}

func clientTransformationAvailableV3(request StartRequestV3, name, version string) bool {
	if !HasFeatureV3(request.ClientFeatures, FeatureClientVideoTransforms) {
		return false
	}
	delivery, ok := request.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
	if !ok || !delivery.Enabled || !delivery.SupportedOnDevice {
		return false
	}
	for _, transformation := range delivery.Transformations {
		if transformation.Executor == ExecutorClientV3 && transformation.Name == name && transformation.RecipeVersion == version {
			return true
		}
	}
	return false
}

// Is4KMediaFileV3 reports whether a catalog file is recorded as 4K or higher.
// Scanners and imports write the resolution label in several spellings, and the
// stored primary video track can carry dimensions that disagree with that label,
// so callers use both facts to stay aligned with the planner's 4K policy.
func Is4KMediaFileV3(file *models.MediaFile) bool {
	if file == nil {
		return false
	}
	labelWidth, labelHeight := dimensionsFromResolutionV3(file.Resolution)
	if labelWidth >= 3840 || labelHeight >= 2160 {
		return true
	}
	return len(file.VideoTracks) > 0 && (file.VideoTracks[0].Width >= 3840 || file.VideoTracks[0].Height >= 2160)
}

func is4KSourceV3(file *models.MediaFile, source SourceDescriptorV3) bool {
	return Is4KMediaFileV3(file) || source.Width >= 3840 || source.Height >= 2160
}

type QualityResultV3 struct {
	Label             string
	Width             int
	Height            int
	BitrateKbps       int
	PreservesSource   bool
	RequiresTranscode bool
	// ExplicitRung marks a user-selected fixed rung, as opposed to an
	// automatic reduction from device limits, bandwidth evidence, or caps.
	ExplicitRung bool
	// Class is the resolution class a reduction encodes at, which its frame
	// alone does not give: a 2.67:1 film's 720p High rung and its 1080p class
	// fitted to the box are both 1920x720. 0 when the quality keeps the
	// source's frame.
	Class int
	// BudgetKbps is the share of a bandwidth estimate an automatic encode's
	// video and audio fit within together; 0 when no estimate bounds it.
	BudgetKbps int
	Reason     string
	Warnings   []DegradationWarningV3
}

// ResolveQualityPolicyV3 selects the delivery quality for a plan.
//
// "auto" is best-effort direct play: it preserves the source unless the
// device's maximum resolution rules it out or the source's bitrate exceeds the
// budget of a measured bandwidth estimate. A source that fits the budget is sent
// as-is at any resolution; only one the link cannot carry drops to the class
// the estimate earns. Without an estimate auto assumes the link can carry the
// source; metered is ignored, since clients apply their own cellular quality.
// A fixed rung or plain resolution never reads the estimate.
//
// bandwidth_cap_kbps is a hard delivery ceiling honored in every quality mode:
// under auto a source whose bitrate exceeds it is re-encoded at the tallest
// class whose encode bitrate fits within the cap, a plain resolution keeps its
// class down to that resolution's lowest menu rung, and a fixed rung whose
// ladder bitrate exceeds it is lowered. A source that fits under the cap is
// still sent as-is.
func ResolveQualityPolicyV3(request StartRequestV3, source SourceDescriptorV3) QualityResultV3 {
	quality, changed := NormalizeQualityV3(request.QualityPreference)
	var warnings []DegradationWarningV3
	if changed {
		warnings = append(warnings, DegradationWarningV3{Code: "quality_preference_normalized", Message: "Unknown quality preference was normalized to auto."})
	}
	capKbps := optionalValueV3(request.BandwidthCapKbps)
	// The cap is a hard ceiling, so a video source whose bitrate is unknown
	// cannot be shown to fit it, as with the server cap.
	capExceededBySource := capKbps > 0 && (source.BitrateKbps > capKbps || source.BitrateKbps <= 0 && source.VideoCodec != "")
	if rung, ok := ladderRungForLabelV3(quality); ok {
		return compoundRungQualityResultV3(rung, source, capKbps, warnings)
	}
	targetHeight := source.Height
	reason := "quality_auto_source"
	explicitRung := false
	capApplied := false
	// budgetKbps is the share of a bandwidth estimate automatic quality plans
	// for when the source exceeds it; the encode never targets more.
	budgetKbps := 0
	overEstimate := false
	// classLimit is the tallest class the preference, device, estimate or cap
	// allows, before the source's own height bounds targetHeight; 0 is no
	// limit. It keeps a cropped source in the class it earned.
	var classLimit int
	if quality != QualityAutoV3 {
		targetHeight, _ = strconv.Atoi(strings.TrimSuffix(quality, "p"))
		classLimit = targetHeight
		reason = "quality_fixed_rung"
		explicitRung = true
	} else {
		maxHeight := resolutionHeightV3(request.Capabilities.MaxResolution)
		if maxHeight > 0 && (targetHeight == 0 || maxHeight < targetHeight) {
			targetHeight = maxHeight
			reason = "quality_device_limit"
		}
		classLimit = maxHeight
	}
	if quality == QualityAutoV3 {
		earned, budget, overSource := estimateLimitV3(optionalValueV3(request.BandwidthEstimateKbps), targetHeight, classLimit, source)
		// A source the link can carry plays as-is. An encode the link forces,
		// or one the device already requires, stays within the estimate.
		deviceEncode := source.Height > 0 && targetHeight < source.Height
		if budget > 0 && (overSource || deviceEncode) {
			targetHeight = minPositiveV3(targetHeight, earned)
			classLimit = minPositiveV3(classLimit, earned)
			budgetKbps = budget
		}
		if overSource {
			overEstimate = true
			reason = "quality_bandwidth_limit"
		}
	}
	if targetHeight <= 0 {
		targetHeight = 1080
	}
	if source.Height > 0 && targetHeight > source.Height {
		targetHeight = source.Height
	}
	// The cap also constrains the rung chosen above: a rung that would
	// preserve the source is forced down when the source bitrate exceeds the
	// cap, and a transcode rung whose ladder bitrate exceeds the cap drops to
	// the cap's rung. Auto takes the tallest class whose class bitrate fits
	// the cap; a plain resolution keeps its class while the cap reaches that
	// resolution's lowest menu rung, as the explicit rung does, so 1080p under
	// a 3000 kbps cap is 1080p Low rather than 720p.
	if capKbps > 0 {
		wouldPreserve := source.Height > 0 && targetHeight >= source.Height
		if (wouldPreserve && capExceededBySource) || (!wouldPreserve && ladderClassBitrateKbpsV3(targetHeight) > capKbps) {
			capApplied = true
			// The class limit applies even when the source is already shorter
			// than the cap's class, so a cropped source wider than that class
			// still fits its box.
			capHeight := capLadderClassV3(capKbps)
			if quality != QualityAutoV3 {
				capHeight = capRungClassV3(streamClassV3(targetHeight, classLimit, source), capKbps)
			}
			targetHeight = min(targetHeight, capHeight)
			classLimit = minPositiveV3(classLimit, capHeight)
		}
	}
	if capApplied {
		reason = decisionReasonBandwidthCapV3
		warnings = append(warnings, DegradationWarningV3{Code: "bandwidth_cap_applied", Message: "Delivery quality is limited by the configured bandwidth cap."})
	}
	if source.Height > 0 && targetHeight >= source.Height && !capApplied && !overEstimate {
		label, width, height := sourceFrameV3(source)
		return QualityResultV3{
			Label:           label,
			Width:           width,
			Height:          height,
			BitrateKbps:     source.BitrateKbps,
			PreservesSource: true,
			ExplicitRung:    explicitRung,
			Reason:          reason,
			Warnings:        warnings,
		}
	}
	// Snap to a ladder class and fit the source into that class's box, so a
	// scope film at the 1080p class is 1920x800. The label is the exact height
	// the encoder scales to.
	class := ladderClassesFrom(streamClassV3(targetHeight, classLimit, source))[0]
	width, effectiveHeight := FitLadderBox(source.Width, source.Height, class.Height)
	if effectiveHeight == 0 {
		effectiveHeight = class.Height
	}
	if width == 0 {
		width = class.Width
	}
	// A fit that keeps an odd source frame still encodes an even one.
	width, effectiveHeight = encodedFrame(source.Width, source.Height, width, effectiveHeight)
	label := heightLabel(effectiveHeight)
	bitrate := ladderClassBitrateKbpsV3(class.Height)
	if budgetKbps > 0 {
		// An encode under an estimate uses the budget, up to what the
		// class's best menu rung spends. A class is earned at its floor,
		// which can sit below the class bitrate; the budget keeps the
		// headroom the class choice assumed.
		bitrate = min(budgetKbps, autoClassCeilingKbpsV3(class.Height))
	}
	if source.BitrateKbps > 0 {
		// A smaller frame never needs more bits than the whole source used,
		// counted as H.264, the output the planner starts from.
		sourceEquivalent := int(float64(source.BitrateKbps) / codecEfficiency(source.VideoCodec))
		bitrate = min(bitrate, max(sourceEquivalent, 1))
	}
	if capKbps > 0 && bitrate > capKbps {
		// The ladder has no rung below 480p, so a cap under the lowest rung's
		// bitrate is honored by lowering the encode target directly: the cap
		// is a hard delivery ceiling, never advisory.
		bitrate = capKbps
	}
	result := QualityResultV3{Label: label, Width: width, Height: effectiveHeight, Class: class.Height, BitrateKbps: bitrate, PreservesSource: !capApplied && !overEstimate && source.Height > 0 && effectiveHeight >= source.Height, ExplicitRung: explicitRung, BudgetKbps: budgetKbps, Reason: reason, Warnings: warnings}
	result.RequiresTranscode = !result.PreservesSource
	return result
}

// estimateLimitV3 reads a measured bandwidth estimate for automatic quality:
// the ladder class it earns, the 80% budget an encode stays within, and
// whether the source's bitrate exceeds that budget. A source of unknown
// bitrate counts as needing the full bitrate of the class it would be sent at.
// Without an estimate it returns zeros.
func estimateLimitV3(estimateKbps, targetHeight, classLimit int, source SourceDescriptorV3) (earned, budget int, overSource bool) {
	if estimateKbps <= 0 {
		return 0, 0, false
	}
	sourceKbps := source.BitrateKbps
	if sourceKbps <= 0 {
		sourceKbps = ladderClassBitrateKbpsV3(streamClassV3(minPositiveV3(targetHeight, source.Height), classLimit, source))
	}
	budget = StreamBudgetKbpsV3(estimateKbps)
	return streamLadderClassV3(estimateKbps, source), budget, sourceKbps > budget
}

// compoundRungQualityResultV3 resolves one explicit menu step. Its resolution
// class never changes under a bandwidth cap; only the bitrate is clamped. For
// cinema-aspect sources (for example 3840x1540 UHD), a same-class rung keeps
// the probed dimensions instead of upscaling to the class's nominal height.
func compoundRungQualityResultV3(rung ladderRungV3, source SourceDescriptorV3, capKbps int, warnings []DegradationWarningV3) QualityResultV3 {
	sourceClassHeight := sourceLadderHeightV3(source)
	height := rung.Height
	sameResolutionClass := sourceClassHeight == rung.Height
	if source.Height > 0 && (sameResolutionClass || source.Height < height) {
		height = source.Height
	}
	bitrate := rung.BitrateKbps
	if source.BitrateKbps > 0 && source.BitrateKbps < bitrate {
		bitrate = source.BitrateKbps
	}
	capApplied := capKbps > 0 && bitrate > capKbps
	if capApplied {
		bitrate = capKbps
	}
	reason := "quality_fixed_rung"
	if capApplied {
		reason = decisionReasonBandwidthCapV3
		warnings = append(warnings, DegradationWarningV3{Code: "bandwidth_cap_applied", Message: "Delivery quality is limited by the configured bandwidth cap."})
	}
	fitsRung := sourceClassHeight > 0 && sourceClassHeight <= rung.Height && source.BitrateKbps > 0 && source.BitrateKbps <= rung.BitrateKbps
	if fitsRung && !capApplied {
		label, width, height := sourceFrameV3(source)
		return QualityResultV3{
			Label:           label,
			Width:           width,
			Height:          height,
			BitrateKbps:     source.BitrateKbps,
			PreservesSource: true,
			ExplicitRung:    true,
			Reason:          reason,
			Warnings:        warnings,
		}
	}
	width := 0
	if source.Width > 0 && source.Height > 0 {
		width = source.Width * height / source.Height
		width -= width % 2
	}
	if width == 0 {
		width, _ = dimensionsFromResolutionV3(resolutionLabelV3(height))
	}
	// Keep the clamped height exact. The transcoder scales to exactly this
	// height, so a source crop is preserved even on a lower-class rung.
	targetLabel := strconv.Itoa(height) + "p"
	return QualityResultV3{
		Label:             targetLabel,
		Width:             width,
		Height:            height,
		Class:             rung.Height,
		BitrateKbps:       bitrate,
		RequiresTranscode: true,
		ExplicitRung:      true,
		Reason:            reason,
		Warnings:          warnings,
	}
}

// originalQualityResultV3 keeps the source's frame. Its label is the exact
// source height: a class label would scale a 3840x1600 source to 1080 lines
// whenever the original route still needs a video transcode.
func originalQualityResultV3(source SourceDescriptorV3) QualityResultV3 {
	label, width, height := sourceFrameV3(source)
	return QualityResultV3{Label: label, Width: width, Height: height, BitrateKbps: source.BitrateKbps, PreservesSource: true, Reason: "quality_original"}
}

// streamClassV3 is the ladder class an automatic encode snaps into for a
// target height already bounded by the source. A target below the source is
// a downscale into the class at or below it. A target that keeps the source
// height uses the class the source's frame belongs to by both dimensions,
// within limit (0 for none), so a cropped 1920x800 film under a 1080p limit
// re-encodes at its own size in the 1080p class rather than dropping to 720p.
func streamClassV3(targetHeight, limit int, source SourceDescriptorV3) int {
	class := ladderClassesFrom(targetHeight)[0].Height
	if source.Height > 0 && targetHeight >= source.Height {
		if own := ladderClassForSize(source.Width, source.Height); own > 0 {
			class = own
			if limit > 0 {
				class = min(class, ladderClassesFrom(limit)[0].Height)
			}
		}
	}
	return class
}

// sourceFrameV3 is the frame a route that keeps the source size encodes when
// it still converts (a codec change): the source frame with an odd height
// rounded down to the even one 4:2:0 output needs, the width scale=-2 then
// gives, and that height as the label. An unknown height has no label.
func sourceFrameV3(source SourceDescriptorV3) (label string, width, height int) {
	if source.Height <= 0 {
		return "", source.Width, source.Height
	}
	width, height = encodedFrame(source.Width, source.Height, source.Width, source.Height)
	return heightLabel(height), width, height
}

// HLSCopyFitsMPEGTS reports whether HLS can carry a copied video and audio
// codec in MPEG-TS segments. An empty audio codec checks the video alone.
func HLSCopyFitsMPEGTS(videoCodec, audioCodec string) bool {
	switch normalizeCodecV3(videoCodec) {
	case transcodeCodecH264, transcodeCodecHEVC:
		return audioCodec == "" || hlsNativeAudioCodecV3(audioCodec)
	}
	return false
}

// hlsNativeAudioCodecV3 reports whether an audio codec can be stream-copied
// into an HLS delivery. The allowlist follows the HLS authoring spec (AAC,
// AC-3, E-AC-3, MP3); everything else — DTS, TrueHD, PCM, Opus — must be
// converted even when the client can decode it in a progressive container.
func hlsNativeAudioCodecV3(codec string) bool {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "aac", "ac3", "eac3", "mp3":
		return true
	}
	return false
}

type resolvedToneMapRecipeV3 struct {
	policy      tonemap.Policy
	mode        tonemap.Mode
	resolution  tonemap.SourceResolution
	revision    tonemap.SourceRevision
	hlsRegistry *TransformationRegistryV3
	ok          bool
}

func resolveToneMapRecipeV3(input PlannerInputV3, source SourceDescriptorV3, hlsRegistry *TransformationRegistryV3) resolvedToneMapRecipeV3 {
	policy := tonemap.NewPolicy(input.Settings.HardwareToneMapEnabled, input.Settings.SoftwareToneMapEnabled)
	file := input.EffectiveFile
	if file == nil {
		file = input.RequestedFile
	}
	resolution := toneMapSourceResolutionV3(file, source)
	revision := tonemap.RevisionForFile(file)
	recipe := resolvedToneMapRecipeV3{policy: policy, resolution: resolution, revision: revision}
	if policy == tonemap.PolicyNone || resolution.Kind == "" {
		return recipe
	}
	if hlsRegistry == nil {
		hlsRegistry = input.hlsVideoRegistry()
	}
	recipe.hlsRegistry = hlsRegistry
	if hlsRegistry == nil || !hlsRegistry.Available(TransformationHDRToSDRToneMapV3) {
		return recipe
	}
	recipe.mode = input.hlsToneMapCapabilities().PreferredMode(policy, resolution.Kind)
	recipe.ok = recipe.mode != ""
	return recipe
}

// toneMapSourceResolutionV3 combines protocol source facts with the scanner's
// richer primary-track metadata before resolving a safe base signal.
func toneMapSourceResolutionV3(file *models.MediaFile, source SourceDescriptorV3) tonemap.SourceResolution {
	metadata := tonemap.SourceMetadata{
		DynamicRange: source.DynamicRange,
		DVProfile:    source.DVProfile,
		DVBLCompatID: source.DVBLCompatID,
	}
	if file != nil && len(file.VideoTracks) > 0 {
		track := file.VideoTracks[0]
		metadata.DVConfigPresent = track.DVConfigPresent
		metadata.DVBLCompatIDPresent = track.DVBLCompatIDPresent
		metadata.DVBLPresent = track.DVBLPresent
		metadata.DVRPUPresent = track.DVRPUPresent
		metadata.ColorRange = track.ColorRange
		metadata.ColorPrimaries = track.ColorPrimaries
		metadata.ColorTransfer = track.ColorTransfer
		metadata.ColorSpace = track.ColorSpace
	}
	return tonemap.ResolveSource(metadata)
}

func recipeFromSourceV3(source SourceDescriptorV3) EffectiveRecipeV3 {
	return EffectiveRecipeV3{VideoCodec: source.VideoCodec, AudioCodec: source.AudioCodec, Width: intPointerV3(source.Width), Height: intPointerV3(source.Height), FrameRate: floatPointerV3(source.FrameRate), BitrateKbps: intPointerV3(source.BitrateKbps), DynamicRange: source.DynamicRange, AudioChannels: intPointerV3(source.AudioChannels), AudioLayout: source.AudioLayout}
}

func selectedTracksForPlanV3(file *models.MediaFile, audioIndex int, subtitle SubtitlePolicyResultV3) SelectedTracksV3 {
	selected := SelectedTracksV3{}
	if file != nil && audioIndex >= 0 && audioIndex < len(file.AudioTracks) {
		index := audioIndex
		selected.Audio = &TrackIdentityV3{ID: TrackIDV3(file.ID, "audio", audioIndex), Index: &index}
	}
	if file != nil && subtitle.SelectedIndex >= 0 {
		index := subtitle.SelectedIndex
		selected.Subtitle = &TrackIdentityV3{ID: TrackIDV3(file.ID, "subtitle", index), Index: &index}
	}
	return selected
}

// audioSelectionUsesContainerDefaultV3 reports whether an untouched source
// stream can realize the selected audio track without client-side selection.
// Clients that explicitly claim client_selected_audio_track_v1 on
// original_http may select another stream after probing the complete source.
func audioSelectionUsesContainerDefaultV3(file *models.MediaFile, audioIndex int) bool {
	if file == nil || len(file.AudioTracks) == 0 {
		return true
	}
	return playedAudioIndexV3(file, audioIndex) == playedAudioIndexV3(file, -1)
}

// playedAudioIndexV3 is the audio ordinal a plan selecting audioIndex plays:
// that track when the file has it, otherwise the container default (the first
// default-flagged track, else the first). It is -1 for a file without audio.
func playedAudioIndexV3(file *models.MediaFile, audioIndex int) int {
	if file == nil || len(file.AudioTracks) == 0 {
		return -1
	}
	if audioIndex >= 0 && audioIndex < len(file.AudioTracks) {
		return audioIndex
	}
	for index, track := range file.AudioTracks {
		if track.Default {
			return index
		}
	}
	return 0
}

func finalizePlanIdentityV3(plan *PlanV3, attemptID string, outputContextID string) {
	plan.PlanID = DeterministicPlanIDV3(attemptID, plan.RequestedMediaFileID, plan.EffectiveMediaFileID, *plan)
	plan.PlanAttemptKey = PlanAttemptKeyV3(*plan, outputContextID, nil)
}

// planAttemptedV3 compares FNV-hex attempt keys exactly after trimming
// whitespace; the keys are case-sensitive hashes, not free-form labels.
func planAttemptedV3(plan PlanV3, outputContextID string, attempted []string) bool {
	wanted := PlanAttemptKeyV3(plan, outputContextID, nil)
	for _, key := range attempted {
		if strings.TrimSpace(key) == wanted {
			return true
		}
	}
	return false
}

func terminalPlannerResultV3(reason, message string, retryable bool) PlannerResultV3 {
	return PlannerResultV3{Terminal: &TerminalV3{Reason: reason, Message: message, Retryable: retryable}, SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1}
}
func subtitlePolicyNameV3(f SubtitleFidelityV3) string {
	if f == SubtitleFidelityPreserveV3 {
		return "require_authored_fidelity"
	}
	return "allow_simplified_rendering"
}
func floatOrZeroV3(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}
func intPointerV3(v int) *int {
	if v <= 0 {
		return nil
	}
	value := v
	return &value
}
func floatPointerV3(v float64) *float64 {
	if v <= 0 {
		return nil
	}
	value := v
	return &value
}
func optionalValueV3(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}
func resolutionHeightV3(v string) int {
	value, _ := strconv.Atoi(strings.TrimSuffix(strings.ToLower(v), "p"))
	if strings.EqualFold(v, "4k") {
		return 2160
	}
	return value
}
func resolutionLabelV3(h int) string {
	switch {
	case h >= 2160:
		return "2160p"
	case h >= 1080:
		return "1080p"
	case h >= 720:
		return "720p"
	default:
		return "480p"
	}
}

// streamLadderClassV3 is the ladder class a streaming bandwidth budget
// earns for this source. Automatic quality keeps a fifth of the measured
// bandwidth as headroom and plans H.264, the output every route can decode.
func streamLadderClassV3(kbps int, source SourceDescriptorV3) int {
	return LadderClassForBitrate(StreamBudgetKbpsV3(kbps), source.FrameRate, transcodeCodecH264)
}

// capLadderClassV3 is the tallest ladder class whose encode bitrate fits
// within a bandwidth cap, or the lowest class when none does. A cap is a
// ceiling the viewer chose, not a measured link, so unlike an estimate it keeps
// no headroom: a 6000 kbps cap earns 1080p at 6000 kbps. The encode bitrate
// does not depend on frame rate, so neither does the class a cap earns.
func capLadderClassV3(capKbps int) int {
	for _, class := range bitrateLadder {
		if ladderClassBitrateKbpsV3(class.Height) <= capKbps {
			return class.Height
		}
	}
	return bitrateLadder[len(bitrateLadder)-1].Height
}

// StreamBudgetKbpsV3 is the part of a bandwidth figure automatic quality
// plans to use.
func StreamBudgetKbpsV3(kbps int) int {
	return int(float64(kbps) * 0.8)
}
func minPositiveV3(a, b int) int {
	if a <= 0 {
		return b
	}
	if b <= 0 || a < b {
		return a
	}
	return b
}

// ladderClassBitrateKbpsV3 is the encode bitrate automatic quality and the
// plain resolution preferences use for a ladder class; a target between
// classes takes the class below it. Explicit menu steps use ladderRungsV3.
func ladderClassBitrateKbpsV3(height int) int {
	switch ladderClassesFrom(height)[0].Height {
	case 2160:
		return 20_000
	case 1080:
		return 6_000
	case 720:
		return 2_000
	case 540:
		return 1_800
	default:
		return 1_500
	}
}

// autoClassCeilingKbpsV3 is the most an automatic encode at a ladder class
// spends when its budget allows: the bitrate of the class's highest menu
// rung, or the class bitrate when no rung has the class's height.
func autoClassCeilingKbpsV3(height int) int {
	class := ladderClassesFrom(height)[0].Height
	ceiling := ladderClassBitrateKbpsV3(class)
	for _, rung := range ladderRungsV3 {
		if rung.Height == class {
			ceiling = max(ceiling, rung.BitrateKbps)
		}
	}
	return ceiling
}

type ladderRungV3 struct {
	Label       string
	DisplayName string
	Height      int
	BitrateKbps int
}

// ladderRungsV3 is the canonical menu ladder in descending resolution and
// bitrate order. The medium values retain the existing plain-rung bitrates;
// their compound labels make the bitrate constraint explicit at same height.
var ladderRungsV3 = []ladderRungV3{
	{Label: QualityRung2160pHighV3, DisplayName: "4K High", Height: 2160, BitrateKbps: 40_000},
	{Label: QualityRung2160pMediumV3, DisplayName: "4K Medium", Height: 2160, BitrateKbps: 20_000},
	{Label: QualityRung2160pLowV3, DisplayName: "4K Low", Height: 2160, BitrateKbps: 10_000},
	{Label: QualityRung1080pHighV3, DisplayName: "1080p High", Height: 1080, BitrateKbps: 10_000},
	{Label: QualityRung1080pMediumV3, DisplayName: "1080p Medium", Height: 1080, BitrateKbps: 6_000},
	{Label: QualityRung1080pLowV3, DisplayName: "1080p Low", Height: 1080, BitrateKbps: 3_000},
	{Label: QualityRung720pHighV3, DisplayName: "720p High", Height: 720, BitrateKbps: 4_000},
	{Label: QualityRung720pMediumV3, DisplayName: "720p Medium", Height: 720, BitrateKbps: 2_000},
	{Label: QualityRung720pLowV3, DisplayName: "720p Low", Height: 720, BitrateKbps: 1_500},
	{Label: "480p", DisplayName: "480p", Height: 480, BitrateKbps: 1_500},
	// The rungs below 1.5 Mbps let a client that lowers quality on Auto, which
	// needs a lower menu entry before it asks, keep going until the stream fits
	// a link of about 500 kbps.
	{Label: QualityRung480pLowV3, DisplayName: "480p Low", Height: 480, BitrateKbps: 800},
	{Label: QualityRung360pV3, DisplayName: "360p", Height: 360, BitrateKbps: 300},
}

// capRungClassV3 is the class a plain resolution preference encodes at under
// a bandwidth cap: class itself while the cap reaches the lowest menu rung at
// or below it, otherwise the tallest lower resolution whose lowest rung fits,
// and the bottom rung's resolution when none does. A class without rungs of
// its own, such as 540p, is judged by the rung below it.
func capRungClassV3(class, capKbps int) int {
	kept := class
	for i, rung := range ladderRungsV3 {
		lowestOfResolution := i+1 == len(ladderRungsV3) || ladderRungsV3[i+1].Height != rung.Height
		if rung.Height > class || !lowestOfResolution {
			continue
		}
		if rung.BitrateKbps <= capKbps {
			return kept
		}
		if i+1 < len(ladderRungsV3) {
			kept = ladderRungsV3[i+1].Height
		} else {
			kept = rung.Height
		}
	}
	return kept
}

func ladderRungForLabelV3(label string) (ladderRungV3, bool) {
	normalized := strings.ToLower(strings.TrimSpace(label))
	for _, rung := range ladderRungsV3 {
		if rung.Label == normalized {
			return rung, true
		}
	}
	return ladderRungV3{}, false
}

// sourceLadderHeightV3 classifies a source by the smallest class whose bounds
// contain both dimensions, the scanner's buckets for a file's resolution label
// (scanner.mapResolution). Cropped and cinema-aspect encodes therefore land in
// the class the catalog shows: 1918x872 is 1080p and 3840x1540 is 2160p. An 8K
// source is 4320p, above every rung, so its 4K rungs scale down to 2160 lines.
func sourceLadderHeightV3(source SourceDescriptorV3) int {
	switch {
	case source.Width <= 0 && source.Height <= 0:
		return 0
	case source.Width <= 854 && source.Height <= 480:
		return 480
	case source.Width <= 1280 && source.Height <= 962:
		return 720
	case source.Width <= 2560 && source.Height <= 1440:
		return 1080
	case source.Width <= 4096 && source.Height <= 3072:
		return 2160
	case source.Width <= 8192 && source.Height <= 6144:
		return 4320
	default:
		return 2160
	}
}

// ladderRungPublishableV3 avoids upscaling and pointless same-resolution
// re-encodes. Every lower resolution-class rung is useful; a same-class rung
// appears only when its bitrate is a real reduction from the source.
func ladderRungPublishableV3(rung ladderRungV3, source SourceDescriptorV3) bool {
	sourceHeight := sourceLadderHeightV3(source)
	if sourceHeight <= 0 {
		return false
	}
	if rung.Height < sourceHeight {
		return true
	}
	if rung.Height > sourceHeight || rung.Label == "480p" {
		return false
	}
	return source.BitrateKbps > 0 && rung.BitrateKbps < source.BitrateKbps
}

func SortedTransformationNamesV3(values []TransformationV3) []string {
	result := make([]string, 0, len(values))
	for _, v := range values {
		result = append(result, v.Name)
	}
	sort.Strings(result)
	return result
}

func deliveryAvailableV3(request StartRequestV3, deliveryClass string) bool {
	capability, ok := request.ClientPlaybackContext.Deliveries[deliveryClass]
	if !ok {
		return false
	}
	return capability.Enabled && capability.SupportedOnDevice
}

// cloneRemuxPlanCandidateV3 keeps progressive and HLS candidate mutations
// independent while preserving the common source and dynamic-range recipe.
func cloneRemuxPlanCandidateV3(plan PlanV3) PlanV3 {
	plan.Transformations = append([]TransformationV3{}, plan.Transformations...)
	plan.AppliedQuirks = append([]AppliedQuirkV3{}, plan.AppliedQuirks...)
	plan.RuntimeCorrections = append([]string{}, plan.RuntimeCorrections...)
	plan.DegradationWarnings = append([]DegradationWarningV3{}, plan.DegradationWarnings...)
	return plan
}

// deliverySupportsAudioClaimV3 narrows a device-wide audio claim to the active
// delivery when the client supplies scoped decode or passthrough lists. Empty
// scoped lists retain the legacy fallback because older clients use them to
// mean "unspecified."
func deliverySupportsAudioClaimV3(request StartRequestV3, deliveryClass, codec string, claim AudioClaimsV3, fallback bool) bool {
	capability, ok := request.ClientPlaybackContext.Deliveries[deliveryClass]
	if !ok || !capability.Enabled || !capability.SupportedOnDevice {
		return false
	}
	hasAudioConstraints := len(capability.AudioDecodeCodecs) > 0 || len(capability.AudioPassthroughCodecs) > 0
	if !hasAudioConstraints {
		return fallback
	}
	supportedCodecs := capability.AudioDecodeCodecs
	if claim.Passthrough {
		supportedCodecs = capability.AudioPassthroughCodecs
	}
	return containsFoldV3(supportedCodecs, codec)
}

// deliveryExceedsMaxChannelsV3 reports whether an audio stream in codec with
// the given channel count exceeds the delivery's ceiling for that codec.
// Planning and the finished-plan check in deliverySupportsPlanV3 share it.
func deliveryExceedsMaxChannelsV3(request StartRequestV3, deliveryClass, codec string, channels int) bool {
	ceiling := deliveryChannelCeilingV3(request, deliveryClass, codec)
	return ceiling > 0 && channels > ceiling
}

// deliveryChannelCeilingV3 is the lower of the delivery's max_channels and its
// max_channels_by_codec entry for codec, ignoring a value of zero or less. Zero
// means no ceiling.
func deliveryChannelCeilingV3(request StartRequestV3, deliveryClass, codec string) int {
	capability, ok := request.ClientPlaybackContext.Deliveries[deliveryClass]
	if !ok {
		return 0
	}
	ceiling := 0
	if capability.MaxChannels != nil && *capability.MaxChannels > 0 {
		ceiling = *capability.MaxChannels
	}
	if byCodec := capability.MaxChannelsByCodec[normalizeCodecV3(codec)]; byCodec > 0 && (ceiling == 0 || byCodec < ceiling) {
		ceiling = byCodec
	}
	return ceiling
}

// audioRemuxFitsServerCapV3 reports whether a video-copy remux that converts
// the selected audio to AAC stays inside the administrator's bitrate cap, or
// the client's bandwidth cap when that is lower: the planner has already folded
// the two into Request.BandwidthCapKbps. The file's total bitrate already
// counts the audio track the AAC output replaces, so adding the full AAC rate
// is an upper bound. The source descriptor's rate
// can be the video track's alone and would undercount. An unknown total cannot
// be bounded and needs the budgeted transcode.
func audioRemuxFitsServerCapV3(input PlannerInputV3, file *models.MediaFile, aacChannels int) bool {
	if input.ServerBitrateCapKbps <= 0 {
		return true
	}
	totalBitrateKbps := 0
	if file != nil {
		totalBitrateKbps = normalizeBitrateKbpsV3(file.Bitrate)
	}
	if totalBitrateKbps <= 0 {
		return false
	}
	_, aacKbps := ResolveAACOutputV3(aacChannels, 0)
	return totalBitrateKbps+aacKbps <= optionalValueV3(input.Request.BandwidthCapKbps)
}

// deliverySupportsPlanV3 applies the capability limits scoped to the delivery
// class after a concrete recipe has been built. Empty lists preserve clients
// that only advertise class availability; non-empty lists are authoritative
// subsets of the top-level device capabilities.
func deliverySupportsPlanV3(request StartRequestV3, deliveryClass string, plan PlanV3) bool {
	capability, ok := request.ClientPlaybackContext.Deliveries[deliveryClass]
	if !ok || !capability.Enabled || !capability.SupportedOnDevice {
		return false
	}
	if len(capability.Containers) > 0 && !containsFoldV3(capability.Containers, plan.Stream.Container) {
		return false
	}
	if codec := strings.TrimSpace(plan.EffectiveRecipe.VideoCodec); codec != "" && len(capability.VideoCodecs) > 0 && !containsFoldV3(capability.VideoCodecs, codec) {
		return false
	}
	if codec := strings.TrimSpace(plan.EffectiveRecipe.AudioCodec); codec != "" {
		hasAudioConstraints := len(capability.AudioDecodeCodecs) > 0 || len(capability.AudioPassthroughCodecs) > 0
		if hasAudioConstraints {
			supportedCodecs := capability.AudioDecodeCodecs
			if plan.Claims.Audio.Passthrough {
				supportedCodecs = capability.AudioPassthroughCodecs
			}
			if !containsFoldV3(supportedCodecs, codec) {
				return false
			}
		}
	}
	if plan.EffectiveRecipe.AudioChannels != nil && deliveryExceedsMaxChannelsV3(request, deliveryClass, plan.EffectiveRecipe.AudioCodec, *plan.EffectiveRecipe.AudioChannels) {
		return false
	}
	clientManagedOriginalRange := deliveryClass == DeliveryClassOriginalHTTPV3 &&
		len(plan.Transformations) == 0 &&
		containsFoldV3(capability.ValidatedClaims, ClaimClientManagedDynamicRangeV3)
	if capability.HDRDetails != nil && !hdrDetailsSupportPlanV3(*capability.HDRDetails, plan) && !clientManagedOriginalRange {
		return false
	}
	return true
}

func hdrDetailsSupportPlanV3(hdr HDRCapabilitiesV3, plan PlanV3) bool {
	switch plan.EffectiveRecipe.DynamicRange {
	case "", DynamicRangeSDRV3:
		return true
	case DynamicRangeHDR10V3, DynamicRangeHDRUnknownV3:
		return hdr.HDR10 && hdr10LimitsSupportPlanV3(hdr, plan)
	case DynamicRangeHDR10PlusV3:
		// A plan that claims HDR10+ needs a delivery that carries it. An HDR10
		// output shows HDR10+ as its HDR10 signal, and the HDR10 ceilings bound
		// an HDR10+ output as well.
		if plan.Claims.Video.HDR10Plus && !hdr.HDR10Plus {
			return false
		}
		return (hdr.HDR10Plus || hdr.HDR10) && hdr10LimitsSupportPlanV3(hdr, plan)
	case DynamicRangeHLGV3:
		return hdr.HLG
	case DynamicRangeDolbyVisionV3:
		candidate := plan.Source
		for _, transformation := range plan.Transformations {
			if transformation.Name == ClientDV7ToDV81V3 || transformation.Name == TransformationServerDV7DV81V3 {
				candidate.DVProfile = 8
				candidate.DVBLCompatID = 1
				break
			}
		}
		return hdrSupportsDolbyVisionSourceV3(hdr, candidate)
	default:
		return false
	}
}

func hdr10LimitsSupportPlanV3(hdr HDRCapabilitiesV3, plan PlanV3) bool {
	return !(hdr.HDR10MaxWidth > 0 && (plan.EffectiveRecipe.Width == nil || *plan.EffectiveRecipe.Width > hdr.HDR10MaxWidth) ||
		hdr.HDR10MaxHeight > 0 && (plan.EffectiveRecipe.Height == nil || *plan.EffectiveRecipe.Height > hdr.HDR10MaxHeight) ||
		hdr.HDR10MaxFrameRate > 0 && (plan.EffectiveRecipe.FrameRate == nil || *plan.EffectiveRecipe.FrameRate > hdr.HDR10MaxFrameRate) ||
		hdr.HDR10MaxBitrateKbps > 0 && (plan.EffectiveRecipe.BitrateKbps == nil || *plan.EffectiveRecipe.BitrateKbps > hdr.HDR10MaxBitrateKbps))
}

func hdrSupportsDolbyVisionSourceV3(hdr HDRCapabilitiesV3, source SourceDescriptorV3) bool {
	if !containsIntV3(hdr.DolbyVisionProfiles, source.DVProfile) {
		return false
	}
	var matchedCapability DolbyVisionProfileCapabilityV3
	foundCapability := false
	for _, capability := range hdr.DolbyVisionProfileLevels {
		if capability.Profile == source.DVProfile {
			matchedCapability = capability
			foundCapability = true
			break
		}
	}
	if !foundCapability {
		// Existing clients predate level-bounded Dolby Vision claims. Once a
		// client sends any bounds, every advertised profile must have one.
		return len(hdr.DolbyVisionProfileLevels) == 0
	}
	if len(matchedCapability.BLCompatibilityIDs) > 0 &&
		!containsIntV3(matchedCapability.BLCompatibilityIDs, source.DVBLCompatID) {
		return false
	}
	if source.DVLevel > 0 {
		return source.DVLevel <= matchedCapability.MaxLevel
	}
	return dolbyVisionSourceFitsLevelV3(source, matchedCapability.MaxLevel)
}

func dolbyVisionSourceFitsLevelV3(source SourceDescriptorV3, maxLevel int) bool {
	// Dolby Vision Version 2.0 defines levels by maximum pixel rate, decoded
	// width, and high-tier bitrate. Use those physical bounds for legacy rows
	// scanned before Silo persisted ffprobe's exact dv_level.
	type levelLimit struct {
		pixelRate   float64
		width       int
		bitrateKbps int
	}
	limits := map[int]levelLimit{
		1: {22_118_400, 1280, 50_000}, 2: {27_648_000, 1280, 50_000},
		3: {49_766_400, 1920, 70_000}, 4: {62_208_000, 2560, 70_000},
		5: {124_416_000, 3840, 70_000}, 6: {199_065_600, 3840, 130_000},
		7: {248_832_000, 3840, 130_000}, 8: {398_131_200, 3840, 130_000},
		9: {497_664_000, 3840, 130_000}, 10: {995_328_000, 3840, 240_000},
		11: {995_328_000, 7680, 240_000}, 12: {1_990_656_000, 7680, 480_000},
		13: {3_981_312_000, 7680, 800_000},
	}
	limit, ok := limits[maxLevel]
	if !ok || source.Width <= 0 || source.Height <= 0 || source.FrameRate <= 0 || source.BitrateKbps <= 0 {
		return false
	}
	return source.Width <= limit.width &&
		float64(source.Width*source.Height)*source.FrameRate <= limit.pixelRate &&
		source.BitrateKbps <= limit.bitrateKbps
}
func ExplainPlannerResultV3(result PlannerResultV3) string {
	if result.Plan != nil {
		return fmt.Sprintf("%s:%s", result.Plan.Delivery, result.Plan.DecisionReason)
	}
	if result.Terminal != nil {
		return "terminal:" + result.Terminal.Reason
	}
	return "invalid"
}
