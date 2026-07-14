package playback

import (
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

func SourceDescriptorFromFileV3(file *models.MediaFile, audioIndex int) SourceDescriptorV3 {
	if file == nil {
		return SourceDescriptorV3{DVEnhancementLayer: EnhancementUnknownV3}
	}
	source := SourceDescriptorV3{
		MediaFileID:        file.ID,
		Container:          normalizeCodecV3(file.Container),
		VideoCodec:         normalizeCodecV3(file.CodecVideo),
		AudioCodec:         normalizeCodecV3(file.CodecAudio),
		AudioChannels:      file.AudioChannels,
		BitrateKbps:        normalizeBitrateKbpsV3(file.Bitrate),
		DVEnhancementLayer: EnhancementNoneV3,
	}
	if len(file.VideoTracks) > 0 {
		track := file.VideoTracks[0]
		source.VideoCodec = firstNonEmptyV3(normalizeCodecV3(track.Codec), source.VideoCodec)
		source.VideoProfile = strings.ToLower(strings.TrimSpace(track.Profile))
		source.VideoLevel = track.Level
		source.BitDepth = models.NormalizeVideoBitDepth(track.BitDepth, track.PixelFormat, track.Profile)
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
	if source.Width == 0 || source.Height == 0 {
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
			source.DynamicRange = "sdr"
		}
	}
	return source
}

func detailedVideoEligibleV3(source SourceDescriptorV3, request StartRequestV3) bool {
	if !HasFeatureV3(request.ClientFeatures, FeatureDetailedDecodeV3) && !HasFeatureV3(request.ClientPlaybackContext.Features, FeatureDetailedDecodeV3) {
		return false
	}
	if !detailedVideoEvidenceCompleteV3(source) {
		return false
	}
	for _, capability := range request.Capabilities.VideoDecode {
		if !strings.EqualFold(capability.Codec, source.VideoCodec) || !capability.Hardware {
			continue
		}
		if len(capability.Profiles) > 0 && !containsFoldV3(capability.Profiles, source.VideoProfile) {
			continue
		}
		if len(capability.Levels) > 0 && !containsAtLeastV3(capability.Levels, source.VideoLevel) {
			continue
		}
		if len(capability.BitDepths) > 0 && !containsIntV3(capability.BitDepths, source.BitDepth) {
			continue
		}
		if capability.MaxWidth > 0 && source.Width > capability.MaxWidth || capability.MaxHeight > 0 && source.Height > capability.MaxHeight || capability.MaxFrameRate > 0 && source.FrameRate > capability.MaxFrameRate || capability.MaxBitrateKbps > 0 && source.BitrateKbps > capability.MaxBitrateKbps {
			continue
		}
		return true
	}
	return false
}

func detailedVideoEvidenceCompleteV3(source SourceDescriptorV3) bool {
	return source.VideoCodec != "" &&
		source.VideoProfile != "" &&
		source.VideoLevel > 0 &&
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
	case "hdr10_plus":
		claims.HDR10Plus = hdr != nil && hdr.HDR10Plus
		return claims.HDR10Plus, claims
	case "hlg":
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
	if passthroughCaps != nil && containsFoldV3(passthroughCaps.PassthroughCodecs, source.AudioCodec) &&
		(HasFeatureV3(request.ClientFeatures, FeatureLayoutPassthrough) || HasFeatureV3(request.ClientPlaybackContext.Features, FeatureLayoutPassthrough)) {
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
		return "dolby_vision"
	}
	if track.HDR10Plus || strings.Contains(strings.ToLower(track.VideoRangeType), "hdr10+") {
		return "hdr10_plus"
	}
	joined := strings.ToLower(strings.Join([]string{track.VideoRange, track.VideoRangeType, track.ColorTransfer}, " "))
	if strings.Contains(joined, "hlg") || strings.Contains(joined, "arib-std-b67") {
		return "hlg"
	}
	if strings.Contains(joined, "hdr") || strings.Contains(joined, "smpte2084") || strings.Contains(joined, "pq") {
		return "hdr10"
	}
	if joined == "  " || strings.TrimSpace(joined) == "" {
		return ""
	}
	return "sdr"
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
