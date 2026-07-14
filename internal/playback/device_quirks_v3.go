package playback

import "strings"

const (
	QuirkFireTVAFTKRTHigh10V3  = "android.fire_tv.aftkrt.h264_high10_l52_v1"
	QuirkFireTVAFTKRTEAC3HLSV3 = "android.fire_tv.aftkrt.eac3_7_1_hls_audio_adapt_v1"
	QuirkFireTVDV8HDR10PlusV3  = "android.fire_tv.dv8_hdr10plus_sei_v1"
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

func dv8HDR10PlusRuntimeCorrectionV3(source SourceDescriptorV3, request StartRequestV3, engine EngineV3) (*AppliedQuirkV3, bool) {
	if !deviceQuirkProtocolAvailableV3(request) || source.DVProfile != 8 || !source.HDR10Plus ||
		!isAmazonFireTVV3(request) || !engineSupportsFeatureV3(request, engine, ClientDV8HDR10PlusSanitizerV3) {
		return nil, false
	}
	quirk := AppliedQuirkV3{
		ID:               QuirkFireTVDV8HDR10PlusV3,
		RegistryRevision: DeviceQuirkRegistryRevisionV3,
		Action:           "client_runtime_correction",
		Reason:           "The native Fire TV Dolby Vision path requires HDR10+ dynamic-metadata SEI removal for hybrid Profile 8 samples.",
	}
	return &quirk, true
}

func applyCopiedVideoQuirksV3(plan *PlanV3, source SourceDescriptorV3, request StartRequestV3, high10 *AppliedQuirkV3) {
	if high10 != nil {
		appendAppliedQuirkV3(plan, *high10, "")
	}
	if quirk, ok := dv8HDR10PlusRuntimeCorrectionV3(source, request, plan.Engine); ok {
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
	// Like every other dual-location feature check, either advertisement
	// location proves the client speaks the quirk protocol.
	return HasFeatureV3(request.ClientFeatures, FeatureDeviceQuirksV3) ||
		HasFeatureV3(request.ClientPlaybackContext.Features, FeatureDeviceQuirksV3)
}

func engineSupportsFeatureV3(request StartRequestV3, engine EngineV3, feature string) bool {
	value, ok := request.ClientPlaybackContext.Engines[string(engine)]
	return ok && value.Enabled && value.SupportedOnDevice && HasFeatureV3(value.Features, feature)
}

func isAmazonModelV3(request StartRequestV3, model string) bool {
	device := request.ClientPlaybackContext.Device
	return strings.EqualFold(device.Model, model) &&
		(strings.EqualFold(device.Manufacturer, "Amazon") || strings.EqualFold(device.Brand, "Amazon"))
}

func isAmazonFireTVV3(request StartRequestV3) bool {
	device := request.ClientPlaybackContext.Device
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(device.Model)), "AFT") &&
		(strings.EqualFold(device.Manufacturer, "Amazon") || strings.EqualFold(device.Brand, "Amazon"))
}

func isHigh10ProfileV3(profile string) bool {
	normalized := strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.ToLower(profile))
	return normalized == "high10" || normalized == "high10intra"
}
