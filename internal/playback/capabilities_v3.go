package playback

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/tonemap"
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
		if track.DVLevel >= 1 && track.DVLevel <= 13 {
			source.DVLevel = track.DVLevel
		}
		source.DVBLCompatID = track.DVBLCompatID
		source.DVBaseLayerProven = track.DVConfigPresent && track.DVBLCompatIDPresent && track.DVBLPresent
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
			source.DynamicRange = DynamicRangeHDRUnknownV3
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

const h264BaselineProfileV3 = "baseline"

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
		softwareDecodeOptIn := HasFeatureV3(request.ClientFeatures, FeatureSoftwareVideoDecodeV3)
		matchedCodec := false
		for _, capability := range request.Capabilities.VideoDecode {
			if !strings.EqualFold(capability.Codec, source.VideoCodec) ||
				(!capability.Hardware && !softwareDecodeOptIn) {
				continue
			}
			matchedCodec = true
			skipProfileLevel := request.Capabilities.VideoEvidence == EvidencePlatformAttestedV3 && capability.Hardware
			if !skipProfileLevel {
				if len(capability.Profiles) > 0 && !videoProfileSupportedV3(source.VideoCodec, source.VideoProfile, capability.Profiles) {
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

// videoProfileSupportedV3 compares a source profile with the profiles an exact
// decoder capability reports. Most codecs retain strict case-insensitive
// equality. H.264 additionally treats Constrained Baseline as a restricted
// subset of Baseline, so a Baseline decoder validates a Constrained Baseline
// source; the reverse direction intentionally remains unsupported.
func videoProfileSupportedV3(codec string, sourceProfile string, decoderProfiles []string) bool {
	if strings.TrimSpace(sourceProfile) == "" {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(codec), "h264") {
		return containsFoldV3(decoderProfiles, sourceProfile)
	}

	source := canonicalH264ProfileV3(sourceProfile)
	if source == "" {
		return false
	}
	for _, decoderProfile := range decoderProfiles {
		decoder := canonicalH264ProfileV3(decoderProfile)
		if decoder == source || source == "constrainedbaseline" && decoder == h264BaselineProfileV3 {
			return true
		}
	}
	return false
}

// canonicalH264ProfileV3 removes presentation-only separators while retaining
// the profile identity. It is deliberately local to H.264 exact-evidence
// comparison; source descriptors keep the original normalized probe value.
func canonicalH264ProfileV3(profile string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r), r == '-', r == '_', r == '.', r == ':':
			return -1
		default:
			return unicode.ToLower(r)
		}
	}, profile)
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

// nativeOutputHDRV3 resolves the HDR facts a native HDR/DV presentation may be
// planned against. output.hdr_details is the authority. The device-level
// capability is a fallback only for clients that predate the output display
// evidence field: a client that reports the evidence tier has separated its
// decoder facts from its output facts, so a missing output value means "no
// native HDR output", and an unknown evidence tier fails closed the same way.
func nativeOutputHDRV3(request StartRequestV3) *HDRCapabilitiesV3 {
	output := request.ClientPlaybackContext.Output
	if output.Display != nil {
		if output.Display.HDREvidence != OutputHDREvidenceExactV3 || output.HDRDetails == nil {
			return nil
		}
		// Validation rejects a contradiction, but planning still narrows to
		// the safe intersection so an exact SDR panel can never authorize a
		// native range through hdr_details alone.
		panel := output.Display.HDRTypes
		if panel == nil {
			panel = &HDRCapabilitiesV3{}
		}
		narrowed := *output.HDRDetails
		narrowed.HDR10 = narrowed.HDR10 && panel.HDR10
		narrowed.HDR10Plus = narrowed.HDR10Plus && panel.HDR10Plus
		narrowed.HLG = narrowed.HLG && panel.HLG
		profiles := make([]int, 0, len(narrowed.DolbyVisionProfiles))
		for _, profile := range narrowed.DolbyVisionProfiles {
			if containsIntV3(panel.DolbyVisionProfiles, profile) {
				profiles = append(profiles, profile)
			}
		}
		narrowed.DolbyVisionProfiles = profiles
		if !narrowed.HDR10 {
			narrowed.HDR10MaxWidth, narrowed.HDR10MaxHeight, narrowed.HDR10MaxFrameRate, narrowed.HDR10MaxBitrateKbps = 0, 0, 0, 0
		} else {
			// A panel ceiling caps the decoder ceiling; zero on either side
			// means that side declared none.
			narrowed.HDR10MaxWidth = tighterBoundV3(narrowed.HDR10MaxWidth, panel.HDR10MaxWidth)
			narrowed.HDR10MaxHeight = tighterBoundV3(narrowed.HDR10MaxHeight, panel.HDR10MaxHeight)
			narrowed.HDR10MaxFrameRate = tighterBoundFloatV3(narrowed.HDR10MaxFrameRate, panel.HDR10MaxFrameRate)
			narrowed.HDR10MaxBitrateKbps = tighterBoundV3(narrowed.HDR10MaxBitrateKbps, panel.HDR10MaxBitrateKbps)
		}
		narrowed.DolbyVisionProfileLevels = intersectDolbyVisionProfileLevelsV3(profiles, narrowed.DolbyVisionProfileLevels, panel.DolbyVisionProfileLevels)
		return &narrowed
	}
	if output.HDRDetails != nil {
		return output.HDRDetails
	}
	return request.Capabilities.HDRDetails
}

// intersectDolbyVisionProfileLevelsV3 keeps one bounded record per surviving
// profile. When both sides bound a profile the tighter level wins and the
// compatibility-id sets intersect (an empty set on one side means "any");
// a record present on only one side is kept as-is so a panel-only bound is
// never dropped.
func intersectDolbyVisionProfileLevelsV3(profiles []int, output, panel []DolbyVisionProfileCapabilityV3) []DolbyVisionProfileCapabilityV3 {
	byProfile := make(map[int]DolbyVisionProfileCapabilityV3, len(output)+len(panel))
	for _, capability := range output {
		if containsIntV3(profiles, capability.Profile) {
			byProfile[capability.Profile] = capability
		}
	}
	for _, panelCapability := range panel {
		if !containsIntV3(profiles, panelCapability.Profile) {
			continue
		}
		existing, ok := byProfile[panelCapability.Profile]
		if !ok {
			byProfile[panelCapability.Profile] = panelCapability
			continue
		}
		if panelCapability.MaxLevel > 0 && (existing.MaxLevel == 0 || panelCapability.MaxLevel < existing.MaxLevel) {
			existing.MaxLevel = panelCapability.MaxLevel
		}
		switch {
		case len(panelCapability.BLCompatibilityIDs) == 0:
		case len(existing.BLCompatibilityIDs) == 0:
			existing.BLCompatibilityIDs = append([]int(nil), panelCapability.BLCompatibilityIDs...)
		default:
			ids := make([]int, 0, len(existing.BLCompatibilityIDs))
			for _, id := range existing.BLCompatibilityIDs {
				if containsIntV3(panelCapability.BLCompatibilityIDs, id) {
					ids = append(ids, id)
				}
			}
			if len(ids) == 0 {
				// Disjoint compatibility sets: nothing this profile can carry.
				ids = []int{-1}
			}
			existing.BLCompatibilityIDs = ids
		}
		byProfile[panelCapability.Profile] = existing
	}
	result := make([]DolbyVisionProfileCapabilityV3, 0, len(byProfile))
	for _, profile := range profiles {
		if capability, ok := byProfile[profile]; ok {
			result = append(result, capability)
		}
	}
	return result
}

// hdr10OutputFitsSourceV3 applies the resolved output's HDR10 ceilings to the
// source. The per-delivery hdr_details gate re-checks the same ceilings later,
// but a delivery that omits hdr_details would otherwise never see the panel's
// limits at all.
func hdr10OutputFitsSourceV3(hdr *HDRCapabilitiesV3, source SourceDescriptorV3) bool {
	if hdr == nil || !hdr.HDR10 {
		return false
	}
	return !(hdr.HDR10MaxWidth > 0 && source.Width > hdr.HDR10MaxWidth ||
		hdr.HDR10MaxHeight > 0 && source.Height > hdr.HDR10MaxHeight ||
		hdr.HDR10MaxFrameRate > 0 && source.FrameRate > hdr.HDR10MaxFrameRate ||
		hdr.HDR10MaxBitrateKbps > 0 && source.BitrateKbps > hdr.HDR10MaxBitrateKbps)
}

func outputRangeEligibleV3(source SourceDescriptorV3, request StartRequestV3) (bool, VideoClaimsV3) {
	hdr := nativeOutputHDRV3(request)
	claims := VideoClaimsV3{}
	switch source.DynamicRange {
	case "", "sdr":
		return true, claims
	case "hdr10":
		claims.HDR10 = hdr10OutputFitsSourceV3(hdr, source)
		return claims.HDR10, claims
	case DynamicRangeHDRUnknownV3:
		// Legacy rows only recorded a file-level HDR flag without per-track
		// range metadata. HDR10 is by far the most common static-HDR range, so
		// an HDR10-capable output treats the source as HDR10 instead of
		// refusing playback outright; the planner attaches a degradation
		// warning for these assumed-range plans.
		claims.HDR10 = hdr10OutputFitsSourceV3(hdr, source)
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
		if hdr != nil && hdrSupportsDolbyVisionSourceV3(*hdr, source) {
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

// clientManagesOriginalDynamicRangeV3 is intentionally delivery-scoped. It
// says the original-file executor can inspect the source and choose its own
// display presentation after delivery; it does not make the same HDR source
// safe for a server-produced progressive or HLS stream.
func clientManagesOriginalDynamicRangeV3(source SourceDescriptorV3, request StartRequestV3) bool {
	if source.DynamicRange == "" || source.DynamicRange == DynamicRangeSDRV3 {
		return false
	}
	delivery, ok := request.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
	return ok && delivery.Enabled && delivery.SupportedOnDevice &&
		containsFoldV3(delivery.ValidatedClaims, ClaimClientManagedDynamicRangeV3)
}

// clientSelectsOriginalAudioTrackV3 is delivery-scoped because original HTTP
// carries every source stream unchanged. A client that makes this claim maps
// selected_tracks.audio.index onto its probed source inventory; clients that
// do not keep the historical server-remux requirement for non-default audio.
func clientSelectsOriginalAudioTrackV3(request StartRequestV3) bool {
	delivery, ok := request.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
	return ok && delivery.Enabled && delivery.SupportedOnDevice &&
		containsFoldV3(delivery.ValidatedClaims, ClaimClientSelectedAudioTrackV3)
}

// clientSupportsHDR10V3 reports whether the resolved output can present
// [source] as HDR10, including any HDR10 ceilings an exact panel record
// narrowed onto the output. Every HDR10-producing route (native, server
// strip, client conversion, base layer) shares this so a delivery that omits
// its own hdr_details still sees the panel's limits.
func clientSupportsHDR10V3(request StartRequestV3, source SourceDescriptorV3) bool {
	return hdr10OutputFitsSourceV3(nativeOutputHDRV3(request), source)
}

func clientSupportsHLGV3(request StartRequestV3) bool {
	hdr := nativeOutputHDRV3(request)
	return hdr != nil && hdr.HLG
}

// clientDV8BaseLayerFallbackV3 reports whether the original_http executor may
// play a single-layer Dolby Vision Profile 8 source through its ordinary HEVC
// decoder, and which base range that presents. It is delivery-scoped like the
// other original_http claims and, unlike clientManagesOriginalDynamicRangeV3,
// the server keeps every other gate: an eligible profile and enhancement
// layer, a compatibility id whose base range is standards-defined, that range
// supported by the active output, and an HEVC decode entry that fits the
// source (checked by videoEligibleV3 on the caller's side).
func clientDV8BaseLayerFallbackV3(source SourceDescriptorV3, request StartRequestV3) (bool, string) {
	if source.DynamicRange != DynamicRangeDolbyVisionV3 || source.DVProfile != 8 ||
		source.DVEnhancementLayer != EnhancementNoneV3 || !source.DVBaseLayerProven {
		return false, ""
	}
	delivery, ok := request.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
	if !ok || !delivery.Enabled || !delivery.SupportedOnDevice ||
		!containsFoldV3(delivery.ValidatedClaims, ClaimClientDV8BaseLayerFallbackV3) {
		return false, ""
	}
	baseRange, ok := dolbyVisionBaseLayerRangeV3(source.DVBLCompatID)
	if !ok {
		return false, ""
	}
	switch baseRange {
	case DynamicRangeHDR10V3:
		if !clientSupportsHDR10V3(request, source) {
			return false, ""
		}
	case DynamicRangeHLGV3:
		if !clientSupportsHLGV3(request) {
			return false, ""
		}
	case DynamicRangeSDRV3:
	default:
		return false, ""
	}
	return true, baseRange
}

// dolbyVisionBaseLayerRangeV3 maps a Profile 8 base-layer compatibility id to
// the dynamic range an ordinary HEVC decoder presents, using only the
// standard Profile 8 pairings the tone-map path accepts without a source
// preflight: 1 is PQ (HDR10), 2 is BT.709 SDR, 4 is BT.2100 HLG. Id 6 is a
// Profile 7 pairing that some Profile 8 files carry, 5 (BT.2020 SDR) needs
// a gamut conversion, and 0, 3, and reserved ids fail closed.
func dolbyVisionBaseLayerRangeV3(compatID int) (string, bool) {
	switch compatID {
	case 1:
		return DynamicRangeHDR10V3, true
	case 4:
		return DynamicRangeHLGV3, true
	case 2:
		return DynamicRangeSDRV3, true
	default:
		return "", false
	}
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

// normalizeDynamicRangeV3 returns the protocol dynamic-range label for a track.
func normalizeDynamicRangeV3(track models.VideoTrack) string {
	return tonemap.DynamicRangeForVideoTrack(track)
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

func tighterBoundV3(a, b int) int {
	switch {
	case a == 0:
		return b
	case b == 0:
		return a
	case b < a:
		return b
	default:
		return a
	}
}

func tighterBoundFloatV3(a, b float64) float64 {
	switch {
	case a == 0:
		return b
	case b == 0:
		return a
	case b < a:
		return b
	default:
		return a
	}
}
