package playback

import "strings"

const (
	QuirkFireTVAFTKRTHigh10V3       = "android.fire_tv.aftkrt.h264_high10_l52_v1"
	QuirkFireTVAFTKRTEAC3HLSV3      = "android.fire_tv.aftkrt.eac3_7_1_hls_audio_adapt_v1"
	QuirkFireTVDV8HDR10PlusV3       = "android.fire_tv.dv8_hdr10plus_sei_v1"
	QuirkMediaTekDV8HDR10PlusV3     = "android.mediatek.dv8_hdr10plus_sei_v1"
	QuirkFirefoxMatroskaAACTimingV3 = "web.firefox.matroska_aac_timestamps_v1"
	legacyAndroidMedia3HLSBuildV3   = "15"

	// QuirkAppleAVPlayerMKVContainerV3 corrects a container capability
	// mis-advertisement present in Silo Apple clients: they advertise "mkv" as a
	// supported container, but AVPlayer cannot play raw Matroska streams over HTTP
	// progressive. Stripping "mkv" from the effective container list causes the
	// planner to fall through to a remux route (fMP4 or HLS), which AVPlayer can
	// play. The correction is detected via the "apple_execution_plan_v1" validated
	// claim that all Silo AVPlayer clients include in their original_http delivery.
	QuirkAppleAVPlayerMKVContainerV3 = "apple.avplayer.mkv_container_correction_v1"
)

func high10DecodeOverrideV3(source SourceDescriptorV3, request StartRequestV3) (*AppliedQuirkV3, bool) {
	if !deviceQuirkProtocolAvailableV3(request) || !isAmazonModelV3(request, "AFTKRT") ||
		!strings.EqualFold(source.VideoCodec, "h264") || !isHigh10ProfileV3(source.VideoProfile) ||
		source.BitDepth != 10 || source.VideoLevel <= 0 || source.VideoLevel > 52 ||
		source.Width <= 0 || source.Height <= 0 || source.Width > 1920 || source.Height > 1080 {
		return nil, false
	}
	for _, capability := range request.Capabilities.VideoDecode {
		if !capability.Hardware || !strings.EqualFold(capability.Codec, "h264") ||
			capability.MaxWidth > 0 && source.Width > capability.MaxWidth ||
			capability.MaxHeight > 0 && source.Height > capability.MaxHeight ||
			capability.MaxFrameRate > 0 && source.FrameRate > capability.MaxFrameRate ||
			capability.MaxBitrateKbps > 0 && source.BitrateKbps > capability.MaxBitrateKbps {
			continue
		}
		quirk := AppliedQuirkV3{
			ID:               QuirkFireTVAFTKRTHigh10V3,
			RegistryRevision: DeviceQuirkRegistryRevisionV3,
			Action:           "positive_decode_override",
			Reason:           "Exact AFTKRT evidence supports hardware H.264 High 10 through level 5.2 at 1080p.",
		}
		return &quirk, true
	}
	return nil, false
}

func hlsEAC3AudioCorrectionV3(source SourceDescriptorV3, request StartRequestV3) (*AppliedQuirkV3, bool) {
	if !deviceQuirkProtocolAvailableV3(request) || !isAmazonModelV3(request, "AFTKRT") ||
		!strings.EqualFold(source.AudioCodec, "eac3") || source.AudioChannels != 8 {
		return nil, false
	}
	quirk := AppliedQuirkV3{
		ID:               QuirkFireTVAFTKRTEAC3HLSV3,
		RegistryRevision: DeviceQuirkRegistryRevisionV3,
		Action:           "audio_only_transcode",
		Reason:           "AFTKRT cannot reliably consume eight-channel E-AC-3 from an HLS MPEG-TS route.",
	}
	return &quirk, true
}

// usesFirstPartyAndroidMedia3HLSV3 identifies the exact legacy Silo Android
// build that predates native_hls_playback_v1 but already reports its build and
// opts into the device-quirks contract. Current clients advertise the native
// HLS feature on the delivery directly.
func usesFirstPartyAndroidMedia3HLSV3(request StartRequestV3) bool {
	return deviceQuirkProtocolAvailableV3(request) &&
		strings.EqualFold(strings.TrimSpace(request.ClientPlaybackContext.Device.Platform), "android") &&
		strings.TrimSpace(request.ClientPlaybackContext.AppBuild) == legacyAndroidMedia3HLSBuildV3
}

// dv8HDR10PlusRuntimeCorrectionV3 asks the client to strip HDR10+ SEI from
// hybrid Dolby Vision Profile 8 samples on devices whose native Dolby Vision
// decoder switches the output to HDR10+ when both metadata types are present.
// Fire TV is matched by model because Fire OS does not report its SoC; other
// MediaTek devices are matched by the SoC the client reports.
func dv8HDR10PlusRuntimeCorrectionV3(source SourceDescriptorV3, request StartRequestV3, deliveryClass string) (*AppliedQuirkV3, bool) {
	if !deviceQuirkProtocolAvailableV3(request) || source.DVProfile != 8 || !source.HDR10Plus ||
		!deliverySupportsFeatureV3(request, deliveryClass, ClientDV8HDR10PlusSanitizerV3) {
		return nil, false
	}
	quirk := AppliedQuirkV3{
		RegistryRevision: DeviceQuirkRegistryRevisionV3,
		Action:           "client_runtime_correction",
	}
	switch {
	case isAmazonFireTVV3(request):
		quirk.ID = QuirkFireTVDV8HDR10PlusV3
		quirk.Reason = "The native Fire TV Dolby Vision path requires HDR10+ dynamic-metadata SEI removal for hybrid Profile 8 samples."
	case isMediaTekSoCV3(request):
		quirk.ID = QuirkMediaTekDV8HDR10PlusV3
		quirk.Reason = "The native MediaTek Dolby Vision path outputs HDR10+ instead of Dolby Vision unless HDR10+ dynamic-metadata SEI is removed from hybrid Profile 8 samples."
	default:
		return nil, false
	}
	return &quirk, true
}

// firefoxMatroskaAACTimingQuirkV3 prevents millisecond-rounded Matroska AAC
// packet timestamps from being copied into MP4/fMP4. Firefox treats those
// sub-frame gaps as missing audio and inserts silence, which is heard as
// crackling. Other clients keep codec-copy remuxing, and Firefox direct play
// remains available when its native container claim is valid.
func firefoxMatroskaAACTimingQuirkV3(source SourceDescriptorV3, request StartRequestV3) (*AppliedQuirkV3, bool) {
	container := strings.ToLower(strings.TrimSpace(source.Container))
	if !isFirefoxWebV3(request) || (container != containerMKVV3 && container != containerMatroskaV3) ||
		!strings.EqualFold(strings.TrimSpace(source.AudioCodec), audioCodecAACV3) {
		return nil, false
	}
	quirk := AppliedQuirkV3{
		ID:               QuirkFirefoxMatroskaAACTimingV3,
		RegistryRevision: DeviceQuirkRegistryRevisionV3,
		Action:           "audio_only_transcode",
		Reason:           "Firefox requires Matroska AAC timestamps to be normalized before MP4 or HLS packaging.",
	}
	return &quirk, true
}

// firefoxMacOSHEVCResumeLeadingPictureDropV3 reports whether a progressive
// HEVC remux for this client should drop open-GOP leading pictures when it
// starts past zero. A seeked stream copy begins at the keyframe before the
// requested position, and the RASL pictures that precede that CRA in
// presentation order reference frames the copy never sent. macOS Firefox
// rejects the stream at that boundary instead of skipping them, and it also
// shows nothing from a stream that starts on a CRA at all, so the remux leads
// the copy with the source's first frame (see remuxIDRPrimer).
func firefoxMacOSHEVCResumeLeadingPictureDropV3(source SourceDescriptorV3, request StartRequestV3) bool {
	if !isFirefoxWebV3(request) || normalizeCodecV3(source.VideoCodec) != transcodeCodecHEVC {
		return false
	}
	userAgent := strings.ToLower(request.ClientPlaybackContext.Device.PlatformDetails["user_agent"])
	return strings.Contains(userAgent, "macintosh") || strings.Contains(userAgent, "mac os x")
}

func isFirefoxWebV3(request StartRequestV3) bool {
	device := request.ClientPlaybackContext.Device
	if !strings.EqualFold(device.Platform, "web") {
		return false
	}
	userAgent := strings.ToLower(strings.TrimSpace(device.PlatformDetails["user_agent"]))
	return strings.Contains(userAgent, "firefox/") && !strings.Contains(userAgent, "seamonkey/")
}

func applyCopiedVideoQuirksV3(plan *PlanV3, source SourceDescriptorV3, request StartRequestV3, high10 *AppliedQuirkV3) {
	if high10 != nil {
		appendAppliedQuirkV3(plan, *high10, "")
	}
	if quirk, ok := dv8HDR10PlusRuntimeCorrectionV3(source, request, DeliveryClassV3(plan.Delivery)); ok {
		appendAppliedQuirkV3(plan, *quirk, ClientDV8HDR10PlusSanitizerV3)
	}
}

func appendAppliedQuirkV3(plan *PlanV3, quirk AppliedQuirkV3, runtimeCorrection string) {
	for _, existing := range plan.AppliedQuirks {
		if existing.ID == quirk.ID && existing.RegistryRevision == quirk.RegistryRevision {
			return
		}
	}
	plan.AppliedQuirks = append(plan.AppliedQuirks, quirk)
	if runtimeCorrection != "" && !containsFoldV3(plan.RuntimeCorrections, runtimeCorrection) {
		plan.RuntimeCorrections = append(plan.RuntimeCorrections, runtimeCorrection)
	}
}

func deviceQuirkProtocolAvailableV3(request StartRequestV3) bool {
	return HasFeatureV3(request.ClientFeatures, FeatureDeviceQuirksV3)
}

func deliverySupportsFeatureV3(request StartRequestV3, deliveryClass string, feature string) bool {
	value, ok := request.ClientPlaybackContext.Deliveries[deliveryClass]
	return ok && value.Enabled && value.SupportedOnDevice && HasFeatureV3(value.Features, feature)
}

func isAmazonModelV3(request StartRequestV3, model string) bool {
	device := request.ClientPlaybackContext.Device
	return strings.EqualFold(device.Model, model) && strings.EqualFold(device.Manufacturer, "Amazon")
}

func isAmazonFireTVV3(request StartRequestV3) bool {
	device := request.ClientPlaybackContext.Device
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(device.Model)), "AFT") &&
		strings.EqualFold(device.Manufacturer, "Amazon")
}

// isMediaTekSoCV3 reads the SoC vendor Android reports from API 31 onward
// (Build.SOC_MANUFACTURER, for example "Mediatek").
func isMediaTekSoCV3(request StartRequestV3) bool {
	soc := request.ClientPlaybackContext.Device.PlatformDetails["soc_manufacturer"]
	return strings.EqualFold(strings.TrimSpace(soc), "mediatek")
}

func isHigh10ProfileV3(profile string) bool {
	normalized := strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.ToLower(profile))
	return normalized == "high10" || normalized == "high10intra"
}

// appleAVPlayerMKVContainerFallback detects the known capability
// mis-advertisement in Silo Apple clients where "mkv" is listed as a supported
// container even though AVPlayer cannot play raw Matroska over HTTP progressive.
// When it fires, the caller must strip "mkv" from the effective container list so
// the planner selects a remux route instead. The quirk is recorded on the plan for
// observability and client-side diagnostics.
//
// Detection uses the "apple_execution_plan_v1" validated claim in the client's
// original_http delivery capability, which every Silo AVPlayer build includes and
// no other platform includes.
func appleAVPlayerMKVContainerFallback(source SourceDescriptorV3, request StartRequestV3) (*AppliedQuirkV3, bool) {
	if !isAppleAVPlayerClientV3(request) {
		return nil, false
	}
	if !strings.EqualFold(strings.TrimSpace(source.Container), "mkv") &&
		!strings.EqualFold(strings.TrimSpace(source.Container), "matroska") {
		return nil, false
	}
	if !containsFoldV3(request.Capabilities.Containers, "mkv") &&
		!containsFoldV3(request.Capabilities.Containers, "matroska") {
		return nil, false // already not claiming mkv; nothing to correct
	}
	quirk := AppliedQuirkV3{
		ID:               QuirkAppleAVPlayerMKVContainerV3,
		RegistryRevision: DeviceQuirkRegistryRevisionV3,
		Action:           "container_claim_correction",
		Reason:           "AVPlayer cannot play raw Matroska over HTTP progressive; mkv/matroska removed from effective container list to select a remux route.",
	}
	return &quirk, true
}

// isAppleAVPlayerClientV3 reports whether the request originates from a Silo
// AVPlayer-based client (Apple TV, iPhone, iPad, Mac). The signal is the
// "apple_execution_plan_v1" validated claim that every Silo Apple build includes
// in its original_http delivery capability advertisement.
func isAppleAVPlayerClientV3(request StartRequestV3) bool {
	delivery, ok := request.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
	if !ok || !delivery.Enabled {
		return false
	}
	return containsFoldV3(delivery.ValidatedClaims, "apple_execution_plan_v1")
}

// filterContainersV3 returns a new slice with all entries matching remove
// removed (case-insensitive, ignoring surrounding whitespace).
func filterContainersV3(containers []string, remove ...string) []string {
	result := make([]string, 0, len(containers))
	for _, c := range containers {
		trimmed := strings.TrimSpace(c)
		excluded := false
		for _, r := range remove {
			if strings.EqualFold(trimmed, r) {
				excluded = true
				break
			}
		}
		if !excluded {
			result = append(result, c)
		}
	}
	return result
}
