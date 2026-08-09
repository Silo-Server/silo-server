package playback

import (
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// SourceDurationSecondsV3 reports a media file's runtime, or nil when it is
// unknown. models.MediaFile.Duration stores 0 for "probe failed", so a zero
// must never reach a client as a duration. This mirrors the legacy start
// response's fileDurationSeconds so both protocols answer identically.
func SourceDurationSecondsV3(file *models.MediaFile) *float64 {
	if file == nil || file.Duration <= 0 {
		return nil
	}
	duration := float64(file.Duration)
	return &duration
}

func SourceDescriptorFromFileV3(file *models.MediaFile, audioIndex int) SourceDescriptorV3 {
	if file == nil {
		return SourceDescriptorV3{DVEnhancementLayer: EnhancementUnknownV3}
	}
	audioOnly := file.IsAudioOnly()
	source := SourceDescriptorV3{
		MediaFileID:        file.ID,
		DurationSeconds:    SourceDurationSecondsV3(file),
		Container:          normalizeCodecV3(file.Container),
		VideoCodec:         normalizeCodecV3(file.CodecVideo),
		AudioCodec:         normalizeCodecV3(file.CodecAudio),
		AudioChannels:      file.AudioChannels,
		BitrateKbps:        normalizeBitrateKbpsV3(file.Bitrate),
		DVEnhancementLayer: EnhancementNoneV3,
	}
	if audioOnly {
		source.VideoCodec = ""
	}
	if !audioOnly && len(file.VideoTracks) > 0 {
		track := file.VideoTracks[0]
		source.VideoCodec = firstNonEmptyV3(normalizeCodecV3(track.Codec), source.VideoCodec)
		source.VideoProfile = strings.ToLower(strings.TrimSpace(track.Profile))
		source.VideoLevel = track.Level
		source.BitDepth = models.NormalizeVideoBitDepth(track.BitDepth, track.PixelFormat, track.Profile)
		source.ColorRange = normalizeColorRangeV3(track.ColorRange)
		source.Width = track.Width
		source.Height = track.Height
		source.FrameRate = parseFrameRateV3(track.FrameRate)
		if track.Bitrate > 0 {
			source.BitrateKbps = normalizeBitrateKbpsV3(track.Bitrate)
		}
		source.DynamicRange = normalizeDynamicRangeV3(track)
		source.HDR10Plus = track.HDR10Plus || strings.Contains(strings.ToLower(track.VideoRangeType), "hdr10+")
		source.DVProfile = track.DVProfile
		source.DVBLCompatID = track.DVBLCompatID
		source.VideoCopyUnsafe = videoCopyUnsafeFile(file)
		switch EnhancementLayerV3(strings.ToLower(track.DVEnhancementLayer)) {
		case EnhancementNoneV3, EnhancementMELV3, EnhancementFELV3, EnhancementUnknownV3:
			source.DVEnhancementLayer = EnhancementLayerV3(strings.ToLower(track.DVEnhancementLayer))
		case "":
			// Legacy rows predate the explicit enhancement-layer fields. A
			// Profile 7 DOVIWithEL label proves an EL exists but cannot prove
			// MEL versus FEL, so keep it unknown rather than misclassifying it
			// as a safe single-layer stream.
			legacyProfile7EL := track.DVProfile == 7 && strings.Contains(strings.ToLower(track.VideoRangeType), "withel")
			if track.DVELPresent || legacyProfile7EL {
				source.DVEnhancementLayer = EnhancementUnknownV3
			} else {
				source.DVEnhancementLayer = EnhancementNoneV3
			}
		default:
			source.DVEnhancementLayer = EnhancementUnknownV3
		}
	}
	if !audioOnly && (source.Width == 0 || source.Height == 0) {
		source.Width, source.Height = dimensionsFromResolutionV3(file.Resolution)
	}
	if audioIndex >= 0 && audioIndex < len(file.AudioTracks) {
		track := file.AudioTracks[audioIndex]
		source.AudioCodec = firstNonEmptyV3(normalizeCodecV3(track.Codec), source.AudioCodec)
		source.AudioChannels = track.Channels
		source.AudioLayout = normalizeLayoutV3(track.Layout)
	}
	if source.DynamicRange == "" {
		if file.HDR {
			source.DynamicRange = "hdr_unknown"
		} else {
			source.DynamicRange = DynamicRangeSDRV3
		}
	}
	return source
}

func normalizeColorRangeV3(value string) string {
	switch normalized := strings.ToLower(strings.TrimSpace(value)); normalized {
	case "tv", "pc", "unknown":
		return normalized
	default:
		return ""
	}
}

// EvidenceInsufficientForDirectV3 marks a direct/copy route that was blocked
// by the client's capability-evidence tier rather than by a negative device
// fact, so lower-tier clients get an actionable reason instead of a mystery
// transcode.
const EvidenceInsufficientForDirectV3 = "evidence_insufficient_for_direct"

// videoEligibleV3 reports whether the source's video stream is validated for
// a copy/direct route under the request's video evidence tier. The second
// result reports that the route was blocked by insufficient evidence for the
// tier — the client claims the codec in its flat lists but the tier's
// validation could not confirm the stream — rather than by device facts.
func videoEligibleV3(source SourceDescriptorV3, request StartRequestV3) (bool, bool) {
	if !routeVideoMetadataCompleteV3(source) {
		return false, false
	}
	flatClaims := containsFoldV3(request.Capabilities.CodecsVideo, source.VideoCodec) ||
		containsFoldV3(request.Capabilities.CodecsVideoHardware, source.VideoCodec)
	switch request.Capabilities.VideoEvidence {
	case EvidenceDeclaredV3:
		// Boolean support statements: copy routes are granted on a flat codec
		// match (container and dynamic range are gated separately by the
		// planner); there is no stricter validation to run.
		return flatClaims, false
	case EvidenceExactV3, EvidencePlatformAttestedV3:
		skipProfileLevel := request.Capabilities.VideoEvidence == EvidencePlatformAttestedV3
		matchedCodec := false
		for _, capability := range request.Capabilities.VideoDecode {
			if !strings.EqualFold(capability.Codec, source.VideoCodec) || !capability.Hardware {
				continue
			}
			matchedCodec = true
			if !skipProfileLevel {
				if len(capability.Profiles) > 0 && (source.VideoProfile == "" || !containsFoldV3(capability.Profiles, source.VideoProfile)) {
					continue
				}
				if len(capability.Levels) > 0 && (source.VideoLevel <= 0 || !containsAtLeastV3(capability.Levels, source.VideoLevel)) {
					continue
				}
			}
			if len(capability.BitDepths) > 0 && !containsIntV3(capability.BitDepths, source.BitDepth) {
				continue
			}
			if capability.MaxWidth > 0 && source.Width > capability.MaxWidth || capability.MaxHeight > 0 && source.Height > capability.MaxHeight || capability.MaxFrameRate > 0 && source.FrameRate > capability.MaxFrameRate || capability.MaxBitrateKbps > 0 && source.BitrateKbps > capability.MaxBitrateKbps {
				continue
			}
			return true, false
		}
		// A flat-list claim with no validating decode entry means the tier's
		// evidence could not confirm the stream, not that the device refused
		// it: report the insufficiency so the degradation is explainable.
		return false, flatClaims && !matchedCodec
	default:
		return false, false
	}
}

// routeVideoMetadataCompleteV3 covers the fields every validated route needs.
// Profile and level are direct-decode constraints, not prerequisites for a
// server transcode: ffprobe legitimately reports an unknown level for codecs
// such as VP9. Exact evidence still rejects a direct route when the client's
// capability entry constrains a profile or level the source does not expose.
func routeVideoMetadataCompleteV3(source SourceDescriptorV3) bool {
	return source.VideoCodec != "" &&
		source.BitDepth > 0 &&
		source.Width > 0 &&
		source.Height > 0 &&
		source.FrameRate > 0 &&
		source.BitrateKbps > 0
}

func outputRangeEligibleV3(source SourceDescriptorV3, request StartRequestV3) (bool, VideoClaimsV3) {
	hdr := request.ClientPlaybackContext.Output.HDRDetails
	if hdr == nil {
		hdr = request.Capabilities.HDRDetails
	}
	claims := VideoClaimsV3{}
	switch source.DynamicRange {
	case "", "sdr":
		return true, claims
	case "hdr10":
		claims.HDR10 = hdr != nil && hdr.HDR10
		return claims.HDR10, claims
	case "hdr_unknown":
		// Legacy rows only recorded a file-level HDR flag without per-track
		// range metadata. HDR10 is by far the most common static-HDR range, so
		// an HDR10-capable output treats the source as HDR10 instead of
		// refusing playback outright; the planner attaches a degradation
		// warning for these assumed-range plans.
		claims.HDR10 = hdr != nil && hdr.HDR10
		return claims.HDR10, claims
	case DynamicRangeHDR10PlusV3:
		claims.HDR10Plus = hdr != nil && hdr.HDR10Plus
		return claims.HDR10Plus, claims
	case DynamicRangeHLGV3:
		claims.HLG = hdr != nil && hdr.HLG
		return claims.HLG, claims
	case "dolby_vision":
		if source.DVProfile == 7 && source.DVEnhancementLayer == EnhancementUnknownV3 {
			claims.DolbyVisionReason = "profile_7_enhancement_layer_unknown"
			return false, claims
		}
		if hdr != nil && containsIntV3(hdr.DolbyVisionProfiles, source.DVProfile) {
			claims.DolbyVision = true
			claims.DolbyVisionReason = "native_profile_supported"
			return true, claims
		}
		claims.DolbyVisionReason = "native_profile_not_supported"
		return false, claims
	default:
		return false, claims
	}
}

func clientSupportsHDR10V3(request StartRequestV3) bool {
	hdr := request.ClientPlaybackContext.Output.HDRDetails
	if hdr == nil {
		hdr = request.Capabilities.HDRDetails
	}
	return hdr != nil && hdr.HDR10
}

func audioEligibilityV3(source SourceDescriptorV3, request StartRequestV3) (copyOK, passthrough bool, claim AudioClaimsV3) {
	claim.Codec = source.AudioCodec
	passthroughCaps := request.ClientPlaybackContext.Output.AudioPassthrough
	if passthroughCaps == nil {
		passthroughCaps = request.Capabilities.AudioPassthrough
	}
	// Passthrough claims require exact audio evidence: only a client that can
	// attest real sink layouts (Android audio HAL enumeration) may earn a
	// validated passthrough claim. platform_attested and declared decode
	// evidence still qualifies for copy routes below.
	if request.Capabilities.AudioEvidence == EvidenceExactV3 &&
		passthroughCaps != nil && containsFoldV3(passthroughCaps.PassthroughCodecs, source.AudioCodec) &&
		HasFeatureV3(request.ClientFeatures, FeatureLayoutPassthrough) {
		for _, entry := range passthroughCaps.Entries {
			if !strings.EqualFold(entry.Codec, source.AudioCodec) || len(entry.ChannelCounts) == 0 || len(entry.Layouts) == 0 ||
				!containsIntV3(entry.ChannelCounts, source.AudioChannels) || !containsFoldV3(entry.Layouts, source.AudioLayout) {
				continue
			}
			claim.Passthrough = true
			claim.AtmosPreserved = strings.Contains(strings.ToLower(source.AudioLayout), "joc") || strings.Contains(strings.ToLower(source.AudioLayout), "atmos")
			claim.Reason = "sink_passthrough_validated"
			return true, true, claim
		}
	}
	if containsFoldV3(request.Capabilities.CodecsAudio, source.AudioCodec) {
		claim.Reason = "client_decode_supported"
		return true, false, claim
	}
	if passthroughCaps != nil && containsFoldV3(passthroughCaps.PassthroughCodecs, source.AudioCodec) {
		claim.Reason = "passthrough_layout_unsupported"
	} else {
		claim.Reason = "audio_codec_unsupported"
	}
	return false, false, claim
}

func normalizeDynamicRangeV3(track models.VideoTrack) string {
	if track.DVProfile > 0 || strings.Contains(strings.ToLower(track.VideoRangeType), "dovi") || strings.Contains(strings.ToLower(track.DolbyVision), "dolby") {
		return DynamicRangeDolbyVisionV3
	}
	if track.HDR10Plus || strings.Contains(strings.ToLower(track.VideoRangeType), "hdr10+") {
		return DynamicRangeHDR10PlusV3
	}
	joined := strings.ToLower(strings.Join([]string{track.VideoRange, track.VideoRangeType, track.ColorTransfer}, " "))
	if strings.Contains(joined, "hlg") || strings.Contains(joined, "arib-std-b67") {
		return DynamicRangeHLGV3
	}
	if strings.Contains(joined, "hdr") || strings.Contains(joined, "smpte2084") || strings.Contains(joined, "pq") {
		return DynamicRangeHDR10V3
	}
	if joined == "  " || strings.TrimSpace(joined) == "" {
		return ""
	}
	return DynamicRangeSDRV3
}

func parseFrameRateV3(value string) float64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if parts := strings.Split(value, "/"); len(parts) == 2 {
		n, nErr := strconv.ParseFloat(parts[0], 64)
		d, dErr := strconv.ParseFloat(parts[1], 64)
		if nErr == nil && dErr == nil && d != 0 {
			return n / d
		}
	}
	v, _ := strconv.ParseFloat(value, 64)
	return v
}

func normalizeBitrateKbpsV3(value int) int {
	if value > 10_000_000 {
		return value / 1000
	}
	return value
}

func dimensionsFromResolutionV3(value string) (int, int) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "4320p", "8k":
		return 7680, 4320
	case "2160p", "4k", "uhd":
		return 3840, 2160
	case "1080p", "fhd":
		return 1920, 1080
	case "720p", "hd":
		return 1280, 720
	case "480p", "sd":
		return 854, 480
	default:
		return 0, 0
	}
}

func normalizeCodecV3(value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	switch v {
	case "h265", "h.265", "x265":
		return "hevc"
	case "h264", "h.264", "avc", "x264":
		return "h264"
	case "eac3", "e-ac-3", "ec-3":
		return "eac3"
	case "truehd", "mlp fba":
		return "truehd"
	case subtitleCodecPGSShort:
		return subtitleCodecPGSFFmpeg
	case subtitleCodecDVDShort, subtitleCodecVOBShort:
		return subtitleCodecDVDFFmpeg
	case subtitleCodecDVBShort:
		return subtitleCodecDVBFFmpeg
	default:
		return v
	}
}

func normalizeLayoutV3(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
func firstNonEmptyV3(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
func containsFoldV3(values []string, wanted string) bool {
	for _, v := range values {
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(wanted)) {
			return true
		}
	}
	return false
}
func containsIntV3(values []int, wanted int) bool {
	for _, v := range values {
		if v == wanted {
			return true
		}
	}
	return false
}
func containsAtLeastV3(values []int, wanted int) bool {
	for _, v := range values {
		if v >= wanted {
			return true
		}
	}
	return false
}
