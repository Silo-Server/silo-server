package playback

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

type TransformationSpecV3 struct {
	Name                 string
	RecipeVersion        string
	Available            bool
	RequiredCapability   string
	PromisedDynamicRange string
	ValidatedClaims      []string
	TerminalReason       string
}

type TransformationRegistryV3 struct {
	entries      map[string]TransformationSpecV3
	refreshAfter time.Time
}

var errHEVCEncoderUnavailableV3 = errors.New("libx265 encoder unavailable")

const transformationProbeRetryDelayV3 = 15 * time.Second

// ProbeTransformationRegistryV3 builds a registry without optional tone-map executors.
func ProbeTransformationRegistryV3(ctx context.Context, ffmpegPath string) *TransformationRegistryV3 {
	return ProbeTransformationRegistryWithToneMapV3(ctx, ffmpegPath, nil)
}

// ProbeTransformationRegistryWithToneMapV3 builds the server transformation
// registry and advertises HDR-to-SDR only when a smoke-tested executor exists.
func ProbeTransformationRegistryWithToneMapV3(ctx context.Context, ffmpegPath string, toneMapCapabilities tonemap.Capabilities) *TransformationRegistryV3 {
	registry, _ := ProbeTransformationRegistryWithToneMapV3Result(ctx, ffmpegPath, toneMapCapabilities)
	return registry
}

// ProbeTransformationRegistryWithToneMapV3Result reports incomplete baseline
// probing and caller cancellation. An incomplete optional HEVC probe leaves
// baseline capabilities usable and marks the registry for a later refresh.
func ProbeTransformationRegistryWithToneMapV3Result(ctx context.Context, ffmpegPath string, toneMapCapabilities tonemap.Capabilities) (*TransformationRegistryV3, error) {
	// Resolve exactly like the execution paths (remux and transcode) so every
	// capability advertised here holds for the binary that later runs.
	ffmpegPath = ResolveFFmpegPath(ffmpegPath)
	bsfCtx, cancelBSF := context.WithTimeout(ctx, 3*time.Second)
	bsfs, bsfErr := exec.CommandContext(bsfCtx, ffmpegPath, "-hide_banner", "-bsfs").Output()
	bsfContextErr := bsfCtx.Err()
	cancelBSF()
	encoderCtx, cancelEncoders := context.WithTimeout(ctx, 3*time.Second)
	encoders, encoderErr := exec.CommandContext(encoderCtx, ffmpegPath, "-hide_banner", "-encoders").Output()
	encoderContextErr := encoderCtx.Err()
	cancelEncoders()
	normalizeRecipeErr, normalizeRecipeContextErr := probeAudioRecipeFilterV3(ctx, ffmpegPath, "stereo", aacTimestampNormalizeFilterV3)
	downmixRecipeErr, downmixRecipeContextErr := probeAudioRecipeFilterV3(ctx, ffmpegPath, "5.1", stereoDownmixBoostFilterV3)
	hevcRecipeErr, hevcRecipeContextErr := probeHEVCRecipeV3(ctx, ffmpegPath, encoders)
	_, ffmpegErr := exec.LookPath(ffmpegPath)
	registry := NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: TransformationServerDV7HDR10V3, RecipeVersion: TransformationServerDV7HDR10RecipeVersionV3, Available: bytes.Contains(bsfs, []byte("dovi_rpu")) && bytes.Contains(bsfs, []byte("filter_units")), RequiredCapability: "ffmpeg_bsf:dovi_rpu+filter_units", PromisedDynamicRange: DynamicRangeHDR10V3, ValidatedClaims: DV7ToHDR10ClaimsV3(), TerminalReason: TerminalDVConversionUnsupportedV3},
		{Name: TransformationAudioToAACV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, Available: ffmpegErr == nil && bytes.Contains(encoders, []byte(" aac ")) && normalizeRecipeErr == nil && downmixRecipeErr == nil, RequiredCapability: "ffmpeg_encoder:aac+ffmpeg_filter_smoke:timestamp_normalization_and_stereo_downmix_v4", ValidatedClaims: []string{ClaimAudioDecodeV3}, TerminalReason: TerminalAudioConversionUnsupportedV3},
		{Name: TransformationVideoToH264V3, RecipeVersion: TransformationVideoToH264RecipeVersionV3, Available: ffmpegErr == nil && h264EncoderAvailableV3(encoders), RequiredCapability: "ffmpeg_encoder:h264", PromisedDynamicRange: DynamicRangeSDRV3, ValidatedClaims: []string{ClaimH264DecodeV3}, TerminalReason: TerminalVideoConversionUnsupportedV3},
		{Name: TransformationVideoToHEVCV3, RecipeVersion: TransformationVideoToHEVCRecipeVersionV3, Available: ffmpegErr == nil && hevcEncoderAvailableV3(encoders) && hevcRecipeErr == nil, RequiredCapability: "ffmpeg_encoder:hevc+ffmpeg_encode_smoke:main_8bit_sdr", PromisedDynamicRange: DynamicRangeSDRV3, ValidatedClaims: []string{ClaimHEVCDecodeV3}, TerminalReason: TerminalVideoConversionUnsupportedV3},
		{Name: TransformationHDRToSDRToneMapV3, RecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3, Available: len(toneMapCapabilities) > 0, RequiredCapability: "ffmpeg_filter:hdr_to_sdr_tonemap", PromisedDynamicRange: DynamicRangeSDRV3, ValidatedClaims: []string{ClaimHDRMetadataRemovedV3, ClaimSDRBT709OutputV3}, TerminalReason: TerminalHDRTranscodeUnsupportedV3},
	})
	if hevcRecipeContextErr != nil ||
		(!errors.Is(hevcRecipeErr, errHEVCEncoderUnavailableV3) && audioRecipeProbeInfrastructureError(hevcRecipeErr) != nil) {
		// Keep baseline capabilities usable, but retry incomplete optional
		// probing after a short delay instead of caching HEVC absence forever.
		registry.refreshAfter = time.Now().Add(transformationProbeRetryDelayV3)
	}
	return registry, errors.Join(
		bsfErr,
		encoderErr,
		bsfContextErr,
		encoderContextErr,
		audioRecipeProbeInfrastructureError(normalizeRecipeErr),
		normalizeRecipeContextErr,
		audioRecipeProbeInfrastructureError(downmixRecipeErr),
		downmixRecipeContextErr,
		// Optional HEVC failure, including its own smoke deadline, only
		// removes HEVC. A canceled inventory caller still invalidates the
		// snapshot rather than publishing an incomplete capability report.
		ctx.Err(),
	)
}

// probeAudioRecipeFilterV3 smoke-tests one filter branch used by the frozen
// AAC recipe and distinguishes unsupported graphs from probe infrastructure
// failures at the registry boundary.
func probeAudioRecipeFilterV3(ctx context.Context, ffmpegPath, channelLayout, filter string) (error, error) {
	probeCtx, cancelProbe := context.WithTimeout(ctx, 3*time.Second)
	probeErr := exec.CommandContext(probeCtx, ffmpegPath,
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "anullsrc=r=8000:cl="+channelLayout,
		"-frames:a", "1", "-af", filter,
		"-f", "null", "-",
	).Run()
	probeContextErr := probeCtx.Err()
	cancelProbe()
	return probeErr, probeContextErr
}

// probeHEVCRecipeV3 verifies a concrete Main 8-bit SDR encode. libx265 is
// the software fallback selected by appendVideoArgs, so this proves a server
// can complete the advertised transformation even when a hardware backend is
// temporarily unavailable. Hardware-specific readiness remains validated by
// the transcode backend probe before that backend is selected.
func probeHEVCRecipeV3(ctx context.Context, ffmpegPath string, encoders []byte) (error, error) {
	if !bytes.Contains(encoders, []byte("libx265")) {
		return errHEVCEncoderUnavailableV3, nil
	}
	probeCtx, cancelProbe := context.WithTimeout(ctx, 3*time.Second)
	probeErr := exec.CommandContext(probeCtx, ffmpegPath, hevcSoftwareSmokeArgsV3()...).Run()
	probeContextErr := probeCtx.Err()
	cancelProbe()
	return probeErr, probeContextErr
}

func hevcSoftwareSmokeArgsV3() []string {
	return []string{
		ffmpegFlagHideBanner, ffmpegLogLevelArg, ffmpegLogLevelError,
		"-f", "lavfi", "-i", "testsrc2=size=64x64:rate=1",
		"-frames:v", "1", "-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "pools=1:frame-threads=1", "-pix_fmt", pixelFormatYUV420P,
		"-f", "null", "-",
	}
}

// An ordinary non-zero FFmpeg exit means the installed recipe is not a v3
// executor; that is a capability result, not a failed inventory. Process
// startup failures and caller cancellation still make the registry uncacheable.
func audioRecipeProbeInfrastructureError(err error) error {
	var exitErr *exec.ExitError
	if err == nil || errors.As(err, &exitErr) {
		return nil
	}
	return err
}

// h264EncodersV3 lists every H.264 encoder the transcode pipeline can select
// (see buildTranscodeArgs' hardware ladder in transcode.go); any one of them
// satisfies the video_to_h264 transformation.
var h264EncodersV3 = []string{"libx264", "h264_qsv", "h264_vaapi", "h264_nvenc", "h264_videotoolbox"}

const (
	hevcSoftwareEncoderV3     = "libx265"
	hevcQSVEncoderV3          = "hevc_qsv"
	hevcVAAPIEncoderV3        = "hevc_vaapi"
	hevcNVENCEncoderV3        = "hevc_nvenc"
	hevcVideoToolboxEncoderV3 = "hevc_videotoolbox"
)

// hevcEncodersV3 lists every HEVC encoder the transcode pipeline can select.
// The actual FFmpeg invocation still resolves its configured hardware backend;
// this registry only advertises a target when that backend family exists.
var hevcEncodersV3 = []string{hevcSoftwareEncoderV3, hevcQSVEncoderV3, hevcVAAPIEncoderV3, hevcNVENCEncoderV3, hevcVideoToolboxEncoderV3}

func h264EncoderAvailableV3(encoders []byte) bool {
	for _, encoder := range h264EncodersV3 {
		if bytes.Contains(encoders, []byte(encoder)) {
			return true
		}
	}
	return false
}

func hevcEncoderAvailableV3(encoders []byte) bool {
	for _, encoder := range hevcEncodersV3 {
		if bytes.Contains(encoders, []byte(encoder)) {
			return true
		}
	}
	return false
}

func NewTransformationRegistryV3(specs []TransformationSpecV3) *TransformationRegistryV3 {
	r := &TransformationRegistryV3{entries: make(map[string]TransformationSpecV3, len(specs))}
	for _, spec := range specs {
		if spec.Name != "" {
			r.entries[spec.Name] = spec
		}
	}
	return r
}

func (r *TransformationRegistryV3) Available(name string) bool {
	if r == nil {
		return false
	}
	spec, ok := r.entries[name]
	return ok && spec.Available
}

// NeedsRefresh lets capability caches retry an incomplete optional probe while
// retaining complete positive and negative inventories for their usual lifetime.
func (r *TransformationRegistryV3) NeedsRefresh(now time.Time) bool {
	return r == nil || !r.refreshAfter.IsZero() && !now.Before(r.refreshAfter)
}

// WithAdvertised returns a registry whose known specs are additionally marked
// available when a pooled transcode node advertises the same server-executed
// transformation at the same recipe version. Advertisements never introduce
// new specs: the planner only selects transformations this server defines,
// and pinning versions to the local spec guarantees a plan built from the
// widened registry passes the per-node advertisement validation at transport
// time. Returns the receiver unchanged when nothing new becomes available.
func (r *TransformationRegistryV3) WithAdvertised(advertised []TransformationV3) *TransformationRegistryV3 {
	if r == nil || len(advertised) == 0 {
		return r
	}
	specs := make([]TransformationSpecV3, 0, len(r.entries))
	changed := false
	for _, spec := range r.entries {
		if !spec.Available {
			for _, remote := range advertised {
				if strings.EqualFold(strings.TrimSpace(remote.Name), spec.Name) &&
					strings.TrimSpace(remote.RecipeVersion) == spec.RecipeVersion &&
					strings.EqualFold(strings.TrimSpace(remote.Executor), "server") {
					spec.Available = true
					changed = true
					break
				}
			}
		}
		specs = append(specs, spec)
	}
	if !changed {
		return r
	}
	return NewTransformationRegistryV3(specs)
}

// OnlyAdvertised returns a registry whose availability comes exclusively from
// matching pooled-node advertisements. It preserves the local registry's
// transformation definitions and recipe pins, but deliberately discards local
// availability for routes whose policy forbids execution on the API host.
func (r *TransformationRegistryV3) OnlyAdvertised(advertised []TransformationV3) *TransformationRegistryV3 {
	if r == nil {
		return nil
	}
	specs := make([]TransformationSpecV3, 0, len(r.entries))
	for _, spec := range r.entries {
		spec.Available = false
		for _, remote := range advertised {
			if strings.EqualFold(strings.TrimSpace(remote.Name), spec.Name) &&
				strings.TrimSpace(remote.RecipeVersion) == spec.RecipeVersion &&
				strings.EqualFold(strings.TrimSpace(remote.Executor), "server") {
				spec.Available = true
				break
			}
		}
		specs = append(specs, spec)
	}
	return NewTransformationRegistryV3(specs)
}

func (r *TransformationRegistryV3) Advertised() []TransformationV3 {
	if r == nil {
		return nil
	}
	result := make([]TransformationV3, 0, len(r.entries))
	for _, spec := range r.entries {
		if spec.Available {
			result = append(result, TransformationV3{Name: spec.Name, Executor: ExecutorServerV3, RecipeVersion: spec.RecipeVersion, ValidatedClaims: append([]string(nil), spec.ValidatedClaims...)})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}
