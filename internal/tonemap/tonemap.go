// Package tonemap owns HDR-to-SDR source classification and FFmpeg execution
// capability probing shared by playback transcodes and other media workers.
package tonemap

import (
	"bytes"
	"cmp"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	SoftwareFilterBT2390       = "tonemapx"
	SoftwareFilterHable        = "tonemap"
	HardwareFilterOpenCL       = "tonemap_opencl"
	HardwareFilterVAAPI        = "tonemap_vaapi"
	HardwareFilterCUDA         = "tonemap_cuda"
	HardwareFilterVideoToolbox = "scale_vt"

	BackendSoftware     = "software"
	BackendQSV          = "qsv"
	BackendVAAPI        = "vaapi"
	BackendNVENC        = "nvenc"
	BackendVideoToolbox = "videotoolbox"

	DynamicRangeHDRUnknown  = "hdr_unknown"
	DynamicRangeSDR         = "sdr"
	DynamicRangeDolbyVision = "dolby_vision"
	DynamicRangeHDR10Plus   = "hdr10_plus"
	DynamicRangeHLG         = "hlg"
	DynamicRangeHDR10       = "hdr10"
	ColorTransferHLG        = "arib-std-b67"
	ColorTransferPQ         = "smpte2084"

	colorBT709             = "bt709"
	colorBT2020            = "bt2020"
	colorBT2020NC          = "bt2020nc"
	colorUnknown           = "unknown"
	colorUnspecified       = "unspecified"
	defaultDRIRenderDevice = "/dev/dri/renderD128"
	ffmpegHideBannerArg    = "-hide_banner"
	ffmpegLogLevelArg      = "-loglevel"
	ffmpegErrorLogLevel    = "error"
	codecHEVC              = "hevc"
)

// Mode identifies the class of executor used to convert an HDR source to SDR.
type Mode string

const (
	ModeHardware Mode = "hardware"
	ModeSoftware Mode = BackendSoftware
)

// Policy records which executor modes an administrator permits.
type Policy string

const (
	PolicyNone                 Policy = "none"
	PolicyHardwareOnly         Policy = "hardware_only"
	PolicySoftwareOnly         Policy = "software_only"
	PolicyHardwareThenSoftware Policy = "hardware_then_software"
)

// NewPolicy combines the independent hardware and software settings into a
// stable value that can be frozen in a playback or download recipe.
func NewPolicy(hardwareEnabled, softwareEnabled bool) Policy {
	switch {
	case hardwareEnabled && softwareEnabled:
		return PolicyHardwareThenSoftware
	case hardwareEnabled:
		return PolicyHardwareOnly
	case softwareEnabled:
		return PolicySoftwareOnly
	default:
		return PolicyNone
	}
}

// Allows reports whether the policy permits the requested executor mode.
func (p Policy) Allows(mode Mode) bool {
	switch p {
	case PolicyHardwareThenSoftware:
		return mode == ModeHardware || mode == ModeSoftware
	case PolicyHardwareOnly:
		return mode == ModeHardware
	case PolicySoftwareOnly:
		return mode == ModeSoftware
	default:
		return false
	}
}

// NVENCSoftwareFallbackPixelFormat preserves the decoded source depth when
// CUDA frames must be downloaded for a software color conversion.
func NVENCSoftwareFallbackPixelFormat(sourceVideoBitDepth int) string {
	if sourceVideoBitDepth > 8 {
		return "p010le"
	}
	return "nv12"
}

// SourceKind describes the transfer function and color primaries of the base
// signal that an executor must convert.
type SourceKind string

const (
	SourcePQ        SourceKind = "pq"
	SourceHLG       SourceKind = DynamicRangeHLG
	SourceHLGBT709  SourceKind = "hlg_bt709"
	SourceSDRBT709  SourceKind = "sdr_bt709"
	SourceSDRBT2020 SourceKind = "sdr_bt2020"
)

// SourceMetadata contains the catalog facts used to resolve an HDR base signal
// without trusting a single dynamic-range label in isolation.
type SourceMetadata struct {
	DynamicRange        string
	DVProfile           int
	DVBLCompatID        int
	DVConfigPresent     bool
	DVBLCompatIDPresent bool
	DVBLPresent         bool
	DVRPUPresent        bool
	ColorRange          string
	ColorPrimaries      string
	ColorTransfer       string
	ColorSpace          string
}

// SourceResolution is the safe execution classification for one HDR source.
// A non-empty Kind with PreflightRequired is a candidate, not permission to
// execute: the selected node must validate decoded frames before publishing
// output. An empty Kind is a hard rejection.
type SourceResolution struct {
	Kind              SourceKind
	PreflightRequired bool
}

// MetadataForFile extracts tone-map classification facts from a media file's
// primary video track, retaining an HDR fallback for legacy catalog rows.
func MetadataForFile(file *models.MediaFile) SourceMetadata {
	if file == nil {
		return SourceMetadata{}
	}
	if len(file.VideoTracks) == 0 {
		if file.HDR {
			return SourceMetadata{DynamicRange: DynamicRangeHDRUnknown}
		}
		return SourceMetadata{DynamicRange: DynamicRangeSDR}
	}
	track := file.VideoTracks[0]
	dynamicRange := DynamicRangeForVideoTrack(track)
	if dynamicRange == "" && file.HDR {
		dynamicRange = DynamicRangeHDRUnknown
	}
	return SourceMetadata{
		DynamicRange:        dynamicRange,
		DVProfile:           track.DVProfile,
		DVBLCompatID:        track.DVBLCompatID,
		DVConfigPresent:     track.DVConfigPresent,
		DVBLCompatIDPresent: track.DVBLCompatIDPresent,
		DVBLPresent:         track.DVBLPresent,
		DVRPUPresent:        track.DVRPUPresent,
		ColorRange:          track.ColorRange,
		ColorPrimaries:      track.ColorPrimaries,
		ColorTransfer:       track.ColorTransfer,
		ColorSpace:          track.ColorSpace,
	}
}

// NeedsToneMap preserves the broad chapter-thumbnail policy gate: thumbnails
// may attempt best-effort conversion for incomplete legacy HDR metadata, while
// full video transcodes require ResolveSource to return a safe base or a
// candidate that passes executor preflight.
func NeedsToneMap(file *models.MediaFile) bool {
	if file == nil {
		return false
	}
	if file.HDR {
		return true
	}
	for _, track := range file.VideoTracks {
		if strings.TrimSpace(track.DolbyVision) != "" {
			return true
		}
	}
	return false
}

// DynamicRangeForVideoTrack derives a normalized dynamic-range label from the
// scanner's Dolby Vision, HDR10+, transfer, and range metadata.
func DynamicRangeForVideoTrack(track models.VideoTrack) string {
	if track.DVProfile > 0 || strings.Contains(strings.ToLower(track.VideoRangeType), "dovi") || strings.Contains(strings.ToLower(track.DolbyVision), "dolby") {
		return DynamicRangeDolbyVision
	}
	if track.HDR10Plus || strings.Contains(strings.ToLower(track.VideoRangeType), "hdr10+") {
		return DynamicRangeHDR10Plus
	}
	joined := strings.ToLower(strings.Join([]string{track.VideoRange, track.VideoRangeType, track.ColorTransfer}, " "))
	if strings.Contains(joined, DynamicRangeHLG) || strings.Contains(joined, ColorTransferHLG) {
		return DynamicRangeHLG
	}
	if strings.Contains(joined, "hdr") || strings.Contains(joined, ColorTransferPQ) || strings.Contains(joined, "pq") {
		return DynamicRangeHDR10
	}
	if strings.TrimSpace(joined) == "" {
		return ""
	}
	return DynamicRangeSDR
}

// ResolveSource determines the base signal an executor may use and whether the
// classification must be confirmed by decoding representative frames.
func ResolveSource(source SourceMetadata) SourceResolution {
	dynamicRange := strings.ToLower(strings.TrimSpace(source.DynamicRange))
	var candidate SourceKind
	preflight := false
	switch dynamicRange {
	case DynamicRangeHDR10, DynamicRangeHDR10Plus:
		candidate = SourcePQ
	case DynamicRangeHLG:
		candidate = SourceHLG
	case DynamicRangeDolbyVision:
		// Profile 5 and compatibility id 0 carry a Dolby-proprietary base
		// signal. Legacy rows omit the presence facts, so their default zero
		// values cannot establish a safe base layer either.
		if source.DVProfile == 5 || !source.DVConfigPresent || !source.DVBLCompatIDPresent || source.DVBLCompatID == 0 {
			return SourceResolution{}
		}
		if !source.DVBLPresent {
			return SourceResolution{}
		}
		candidate = sourceKindForCompatibilityID(source.DVBLCompatID)
		if candidate == "" {
			candidate = sourceKindFromColorMetadata(source)
			preflight = candidate != ""
		} else if !standardProfileCompatibility(source.DVProfile, source.DVBLCompatID) {
			preflight = true
		}
	case DynamicRangeHDRUnknown:
		candidate = sourceKindFromColorMetadata(source)
		preflight = candidate != ""
	default:
		return SourceResolution{}
	}
	if candidate == "" {
		return SourceResolution{}
	}
	complete, compatible := sourceMetadataCompatibility(candidate, source)
	if !compatible || !complete {
		preflight = true
	}
	return SourceResolution{Kind: candidate, PreflightRequired: preflight}
}

// ClassifySource returns only classifications that are safe without executor
// preflight; ambiguous or unsupported sources return an empty kind.
func ClassifySource(source SourceMetadata) SourceKind {
	resolution := ResolveSource(source)
	if resolution.PreflightRequired {
		return ""
	}
	return resolution.Kind
}

// sourceKindForCompatibilityID maps standardized Dolby Vision base-layer
// compatibility identifiers to their underlying transfer and primaries.
func sourceKindForCompatibilityID(id int) SourceKind {
	switch id {
	case 1, 6:
		return SourcePQ
	case 2:
		return SourceSDRBT709
	case 3:
		return SourceHLGBT709
	case 4:
		return SourceHLG
	case 5:
		return SourceSDRBT2020
	}
	return ""
}

// standardProfileCompatibility reports whether a Dolby Vision profile and
// compatibility identifier form a standardized pairing that needs no probe.
func standardProfileCompatibility(profile, compatibilityID int) bool {
	switch profile {
	case 4:
		return compatibilityID == 2
	case 7:
		return compatibilityID == 6
	case 8:
		return compatibilityID == 1 || compatibilityID == 2 || compatibilityID == 4
	case 9:
		return compatibilityID == 2
	case 10:
		return compatibilityID == 1 || compatibilityID == 2 || compatibilityID == 4
	case 0:
		return false
	default:
		// Retired profiles and legacy compatibility ids are accepted only
		// after source preflight, not from their catalog tags alone.
		return false
	}
}

// sourceKindFromColorMetadata infers a candidate base signal from explicit
// transfer and primaries metadata when Dolby Vision signaling is incomplete.
func sourceKindFromColorMetadata(source SourceMetadata) SourceKind {
	transfer := normalizeColorValue(source.ColorTransfer)
	primaries := normalizeColorValue(source.ColorPrimaries)
	switch {
	case transferIsPQ(transfer):
		return SourcePQ
	case transferIsHLG(transfer) && colorIsBT709(primaries):
		return SourceHLGBT709
	case transferIsHLG(transfer):
		return SourceHLG
	case transferIsSDR(transfer) && colorIsBT2020(primaries):
		return SourceSDRBT2020
	case transferIsSDR(transfer) && colorIsBT709(primaries):
		return SourceSDRBT709
	}
	return ""
}

// sourceMetadataCompatibility checks whether the available color fields agree
// with a candidate source kind and separately reports whether all fields exist.
func sourceMetadataCompatibility(kind SourceKind, source SourceMetadata) (complete, compatible bool) {
	values := []string{source.ColorRange, source.ColorPrimaries, source.ColorTransfer, source.ColorSpace}
	complete = true
	for _, value := range values {
		if normalizeColorValue(value) == "" || normalizeColorValue(value) == colorUnknown || normalizeColorValue(value) == colorUnspecified {
			complete = false
		}
	}
	rangeValue := normalizeColorValue(source.ColorRange)
	if rangeValue != "" && rangeValue != colorUnknown && rangeValue != colorUnspecified && !rangeIsLimited(rangeValue) {
		return complete, false
	}
	primaries := normalizeColorValue(source.ColorPrimaries)
	transfer := normalizeColorValue(source.ColorTransfer)
	matrix := normalizeColorValue(source.ColorSpace)
	return complete,
		colorValueMatchesPrimaries(kind, primaries) &&
			colorValueMatchesTransfer(kind, transfer) &&
			colorValueMatchesMatrix(kind, matrix)
}

// normalizeColorValue canonicalizes scanner color fields for comparison.
func normalizeColorValue(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// colorValueMatchesPrimaries accepts missing metadata for preflight while
// rejecting explicit primaries that contradict the candidate source kind.
func colorValueMatchesPrimaries(kind SourceKind, value string) bool {
	if value == "" || value == colorUnknown || value == colorUnspecified {
		return true
	}
	if kind == SourceHLGBT709 || kind == SourceSDRBT709 {
		return colorIsBT709(value)
	}
	return colorIsBT2020(value)
}

// colorValueMatchesTransfer accepts missing metadata for preflight while
// rejecting an explicit transfer function that contradicts the source kind.
func colorValueMatchesTransfer(kind SourceKind, value string) bool {
	if value == "" || value == colorUnknown || value == colorUnspecified {
		return true
	}
	switch kind {
	case SourcePQ:
		return transferIsPQ(value)
	case SourceHLG, SourceHLGBT709:
		return transferIsHLG(value)
	case SourceSDRBT709, SourceSDRBT2020:
		return transferIsSDR(value)
	default:
		return false
	}
}

// colorValueMatchesMatrix accepts missing metadata for preflight while
// validating explicit BT.709 and BT.2020 matrix coefficients.
func colorValueMatchesMatrix(kind SourceKind, value string) bool {
	if value == "" || value == colorUnknown || value == colorUnspecified {
		return true
	}
	if kind == SourceSDRBT709 {
		return colorIsBT709(value)
	}
	if kind == SourceHLGBT709 {
		return colorIsBT709(value) || colorIsBT2020(value)
	}
	return colorIsBT2020(value)
}

// transferIsPQ recognizes FFmpeg's names for the SMPTE ST 2084 transfer.
func transferIsPQ(value string) bool {
	return strings.Contains(value, ColorTransferPQ) || value == "pq"
}

// transferIsHLG recognizes FFmpeg's names for the ARIB STD-B67 transfer.
func transferIsHLG(value string) bool {
	return strings.Contains(value, ColorTransferHLG) || value == DynamicRangeHLG
}

// transferIsSDR recognizes the SDR transfer functions accepted as Dolby
// Vision compatible base layers.
func transferIsSDR(value string) bool {
	return value == colorBT709 || value == "bt1886" || value == "bt470bg" || value == "gamma28"
}

// colorIsBT709 reports whether a normalized color field names BT.709.
func colorIsBT709(value string) bool {
	return strings.Contains(value, colorBT709)
}

// colorIsBT2020 reports whether a normalized color field names BT.2020.
func colorIsBT2020(value string) bool {
	return strings.Contains(value, colorBT2020)
}

// rangeIsLimited recognizes FFmpeg's names for limited-range video levels.
func rangeIsLimited(value string) bool {
	return value == "tv" || value == "mpeg" || value == "limited"
}

// SourceKindFor maps already trusted dynamic-range metadata to a base signal.
// Call ResolveSource when the completeness of the metadata is not guaranteed.
func SourceKindFor(dynamicRange string, dvBLCompatID int) SourceKind {
	switch strings.ToLower(strings.TrimSpace(dynamicRange)) {
	case DynamicRangeHDR10, DynamicRangeHDR10Plus:
		return SourcePQ
	case DynamicRangeHLG:
		return SourceHLG
	case DynamicRangeDolbyVision:
		return sourceKindForCompatibilityID(dvBLCompatID)
	}
	return ""
}

// SourceTransfer returns the FFmpeg transfer characteristic for a source kind.
func SourceTransfer(kind SourceKind) string {
	if kind == SourceHLG || kind == SourceHLGBT709 {
		return ColorTransferHLG
	}
	if kind == SourceSDRBT709 || kind == SourceSDRBT2020 {
		return colorBT709
	}
	return ColorTransferPQ
}

// SourcePrimaries returns the FFmpeg color primaries for a source kind.
func SourcePrimaries(kind SourceKind) string {
	if kind == SourceHLGBT709 || kind == SourceSDRBT709 {
		return colorBT709
	}
	return colorBT2020
}

// SourceMatrix returns the FFmpeg matrix coefficients for a source kind.
func SourceMatrix(kind SourceKind) string {
	if kind == SourceHLGBT709 || kind == SourceSDRBT709 {
		return colorBT709
	}
	return colorBT2020NC
}

// IsSDRSource reports whether the Dolby Vision compatible base is already SDR
// and therefore needs gamut conversion but no luminance tone mapping.
func IsSDRSource(kind SourceKind) bool {
	return kind == SourceSDRBT709 || kind == SourceSDRBT2020
}

// AllSourceKinds returns every base signal supported by capability probes.
func AllSourceKinds() []SourceKind {
	return []SourceKind{SourcePQ, SourceHLG, SourceHLGBT709, SourceSDRBT709, SourceSDRBT2020}
}

// ValidSourceKind reports whether kind is a supported, serializable base signal.
func ValidSourceKind(kind SourceKind) bool {
	for _, candidate := range AllSourceKinds() {
		if candidate == kind {
			return true
		}
	}
	return false
}

// Capability records one validated executor and the source kinds its smoke
// tests successfully converted.
type Capability struct {
	Mode        Mode         `json:"mode"`
	Backend     string       `json:"backend"`
	Filter      string       `json:"filter"`
	SourceKinds []SourceKind `json:"source_kinds"`
}

// Capabilities is the validated executor inventory for one server or node.
type Capabilities []Capability

// lookup finds an executor of the requested mode that supports the source kind.
func (c Capabilities) lookup(mode Mode, kind SourceKind) (Capability, bool) {
	for _, capability := range c {
		if capability.Mode == mode && slices.Contains(capability.SourceKinds, kind) {
			return capability, true
		}
	}
	return Capability{}, false
}

// Supports reports whether a validated executor can process the source kind.
func (c Capabilities) Supports(mode Mode, kind SourceKind) bool {
	_, ok := c.lookup(mode, kind)
	return ok
}

// FilterFor returns the probed FFmpeg filter for a compatible executor.
func (c Capabilities) FilterFor(mode Mode, kind SourceKind) string {
	if capability, ok := c.lookup(mode, kind); ok {
		return capability.Filter
	}
	return ""
}

// BackendFor returns the hardware or software backend for a compatible executor.
func (c Capabilities) BackendFor(mode Mode, kind SourceKind) string {
	if capability, ok := c.lookup(mode, kind); ok {
		return capability.Backend
	}
	return ""
}

// SupportsPolicy reports whether at least one allowed executor supports kind.
func (c Capabilities) SupportsPolicy(policy Policy, kind SourceKind) bool {
	return policy.Allows(ModeHardware) && c.Supports(ModeHardware, kind) ||
		policy.Allows(ModeSoftware) && c.Supports(ModeSoftware, kind)
}

// PreferredMode chooses a validated hardware executor before software when the
// policy permits both, and returns an empty mode when neither is usable.
func (c Capabilities) PreferredMode(policy Policy, kind SourceKind) Mode {
	if policy.Allows(ModeHardware) && c.Supports(ModeHardware, kind) {
		return ModeHardware
	}
	if policy.Allows(ModeSoftware) && c.Supports(ModeSoftware, kind) {
		return ModeSoftware
	}
	return ""
}

// SourceParameters builds the FFmpeg setparams stage that declares the input
// base signal before conversion.
func SourceParameters(kind SourceKind) string {
	return "setparams=range=tv:color_primaries=" + SourcePrimaries(kind) + ":color_trc=" + SourceTransfer(kind) + ":colorspace=" + SourceMatrix(kind)
}

// SoftwareFilter builds the complete software conversion graph for a source
// kind using the filter selected during capability probing.
func SoftwareFilter(kind SourceKind, filterName string) string {
	if IsSDRSource(kind) {
		return SourceParameters(kind) +
			",zscale=p=bt709:t=bt709:m=bt709:r=tv,format=yuv420p," + HDRMetadataRemovalFilter()
	}
	if filterName == SoftwareFilterBT2390 {
		return SourceParameters(kind) +
			",tonemapx=tonemap=bt2390" +
			",zscale=p=bt709:t=bt709:m=bt709:r=tv,format=yuv420p," + HDRMetadataRemovalFilter()
	}
	return SourceParameters(kind) +
		",zscale=t=linear:npl=100,format=gbrpf32le,tonemap=hable" +
		",zscale=p=bt709:t=bt709:m=bt709:r=tv,format=yuv420p," + HDRMetadataRemovalFilter()
}

// SelectSoftwareFilter inspects an FFmpeg -filters listing and returns the
// preferred software tone-map filter. It is intentionally only a listing
// probe; playback capability advertising additionally runs an encode smoke
// test, while chapter thumbnails preserve their existing best-effort policy.
func SelectSoftwareFilter(output []byte) (filter string, hasZScale bool) {
	hasZScale = FilterListingHas(output, "zscale")
	if !hasZScale {
		return "", false
	}
	if FilterListingHas(output, SoftwareFilterBT2390) {
		return SoftwareFilterBT2390, true
	}
	if FilterListingHas(output, SoftwareFilterHable) {
		return SoftwareFilterHable, true
	}
	return "", true
}

// FilterListingHas reports whether an FFmpeg -filters listing contains an exact
// filter name rather than an incidental textual match.
func FilterListingHas(output []byte, name string) bool {
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		fields := bytes.Fields(line)
		if len(fields) >= 3 && bytes.Contains(fields[2], []byte("->")) && strings.EqualFold(string(fields[1]), name) {
			return true
		}
	}
	return false
}

// VAAPIFilter builds the VAAPI conversion graph for the source kind.
func VAAPIFilter(kind SourceKind) string {
	if IsSDRSource(kind) {
		return SourceParameters(kind) + "," + vaapiSDRFilter()
	}
	return SourceParameters(kind) + "," + vaapiToneMapFilter()
}

// QSVFilter builds the Intel HDR-to-SDR conversion graph. Intel's VAAPI/VPP
// tone mapper can severely crush midtones on otherwise valid PQ sources, so
// HDR frames take Jellyfin FFmpeg's GPU OpenCL path and return to the VAAPI
// surface family before the existing QSV encoder interop. SDR base layers only
// need color conversion and retain the cheaper VAAPI path.
func QSVFilter(kind SourceKind) string {
	if IsSDRSource(kind) {
		return VAAPIFilter(kind)
	}
	return SourceParameters(kind) +
		",scale_vaapi=format=p010" +
		",hwmap=derive_device=opencl:mode=read" +
		"," + openCLToneMapFilter() +
		",hwmap=derive_device=vaapi:mode=write:reverse=1:extra_hw_frames=16,format=vaapi"
}

// openCLToneMapFilter matches Jellyfin's BT.2390 GPU recipe. A fixed 100-nit
// SDR peak keeps output stable across sources while preserving PQ midtones.
func openCLToneMapFilter() string {
	return HardwareFilterOpenCL + "=format=nv12:p=bt709:t=bt709:m=bt709:tonemap=bt2390:peak=100:desat=0"
}

// vaapiToneMapFilter returns the HDR luminance and color conversion stage.
func vaapiToneMapFilter() string {
	return HardwareFilterVAAPI + "=format=nv12:p=bt709:t=bt709:m=bt709"
}

// vaapiSDRFilter returns the color-only conversion used by SDR base layers.
func vaapiSDRFilter() string {
	return "scale_vaapi=format=nv12:out_color_primaries=bt709:out_color_transfer=bt709:out_color_matrix=bt709:out_range=tv"
}

// CUDAFilter returns the CUDA HDR-to-SDR conversion stage with Dolby Vision
// enhancement processing disabled so the validated base layer is used.
func CUDAFilter() string {
	return HardwareFilterCUDA + "=tonemap=bt2390:format=nv12:p=bt709:t=bt709:m=bt709:r=tv:apply_dovi=0"
}

// VideoToolboxFilter builds Apple's HDR-to-SDR pixel-transfer conversion.
// VTPixelTransferSession performs tone mapping when HDR source attachments are
// converted to the BT.709 destination properties. The output surface retains
// the decoded bit depth, so callers must download it with
// VideoToolboxDownloadFilter before an 8-bit H.264 encode.
func VideoToolboxFilter(width, height string) string {
	width = cmp.Or(strings.TrimSpace(width), "iw")
	height = cmp.Or(strings.TrimSpace(height), "ih")
	return HardwareFilterVideoToolbox + "=w=" + width + ":h=" + height + ":color_matrix=bt709:color_primaries=bt709:color_transfer=bt709"
}

// VideoToolboxDownloadFilter moves the converted IOSurface to system memory
// and normalizes it to the 8-bit NV12 format accepted by the H.264 encoder and
// CPU subtitle filters. scale_vt preserves its input surface depth.
func VideoToolboxDownloadFilter(sourceVideoBitDepth int) string {
	format := videoToolboxSurfacePixelFormat(sourceVideoBitDepth)
	filter := "hwdownload,format=" + format
	if format == "p010le" {
		filter += ",format=nv12"
	}
	return filter
}

// VideoToolboxUploadFilter gives software-decoded frames the complete source
// signal attachments that VTPixelTransferSession consumes before uploading
// them to an IOSurface.
func VideoToolboxUploadFilter(kind SourceKind, sourceVideoBitDepth int) string {
	return SourceParameters(kind) + ",format=" + videoToolboxSurfacePixelFormat(sourceVideoBitDepth) + ",hwupload"
}

func videoToolboxSurfacePixelFormat(sourceVideoBitDepth int) string {
	if sourceVideoBitDepth > 0 && sourceVideoBitDepth <= 8 {
		return "nv12"
	}
	return "p010le"
}

// QSVInteropFilter normalizes the VAAPI tone-map surface before deriving the
// QSV encoder surface. The extra scale_vaapi stage is required by Intel's
// driver for real decoded HEVC frames even when tonemap_vaapi already emitted
// NV12; omitting it leaves FFmpeg trying an unsupported software auto-scale.
func QSVInteropFilter() string {
	return "scale_vaapi=format=nv12,hwmap=derive_device=qsv:mode=read+write,format=qsv"
}

// qsvVAAPIInitDevice builds the Intel-specific VAAPI device declaration used
// to derive a QSV encoding device.
func qsvVAAPIInitDevice(device string) string {
	return "vaapi=va:" + device + ",driver=iHD,kernel_driver=i915,vendor_id=0x8086"
}

// initHWDeviceFlag is FFmpeg's hardware-device declaration flag, shared by
// every init chain built here.
const initHWDeviceFlag = "-init_hw_device"

// QSVInitDeviceArgs declares the Intel VAAPI display and derives the QSV
// device from it. Every QSV command line in the server — transcode, encoder
// warmup, capability probes, tone-map smoke tests, chapter thumbnails — must
// initialize hardware through this chain, so a driver constraint is fixed in
// one place.
func QSVInitDeviceArgs(device string) []string {
	return []string{initHWDeviceFlag, qsvVAAPIInitDevice(device), initHWDeviceFlag, "qsv=qs@va"}
}

// VAAPIInitDeviceArgs declares one VAAPI device under the alias the caller's
// filter graph and encoder reference.
func VAAPIInitDeviceArgs(alias, device string) []string {
	return []string{initHWDeviceFlag, "vaapi=" + alias + ":" + device}
}

// HDRMetadataRemovalFilter removes side data that would otherwise incorrectly
// label the converted SDR frames as HDR or Dolby Vision.
func HDRMetadataRemovalFilter() string {
	return strings.Join([]string{
		"sidedata=mode=delete:type=MASTERING_DISPLAY_METADATA",
		"sidedata=mode=delete:type=CONTENT_LIGHT_LEVEL",
		"sidedata=mode=delete:type=DYNAMIC_HDR_PLUS",
		"sidedata=mode=delete:type=DOVI_RPU_BUFFER",
		"sidedata=mode=delete:type=DOVI_METADATA",
	}, ",")
}
