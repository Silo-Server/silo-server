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
	entries map[string]TransformationSpecV3
}

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

// ProbeTransformationRegistryWithToneMapV3Result also reports incomplete
// command execution so capability endpoints do not advertise a misleading
// partial inventory after cancellation or a probe deadline.
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
	audioRecipeCtx, cancelAudioRecipe := context.WithTimeout(ctx, 3*time.Second)
	audioRecipeErr := exec.CommandContext(audioRecipeCtx, ffmpegPath,
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "anullsrc=r=8000:cl=5.1",
		"-frames:a", "1", "-af", stereoDownmixBoostFilterV3,
		"-f", "null", "-",
	).Run()
	audioRecipeContextErr := audioRecipeCtx.Err()
	cancelAudioRecipe()
	_, ffmpegErr := exec.LookPath(ffmpegPath)
	registry := NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: TransformationServerDV7HDR10V3, RecipeVersion: "1", Available: bytes.Contains(bsfs, []byte("dovi_rpu")), RequiredCapability: "ffmpeg_bsf:dovi_rpu", PromisedDynamicRange: DynamicRangeHDR10V3, ValidatedClaims: DV7ToHDR10ClaimsV3(), TerminalReason: TerminalDVConversionUnsupportedV3},
		{Name: TransformationAudioToAACV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, Available: ffmpegErr == nil && bytes.Contains(encoders, []byte(" aac ")) && audioRecipeErr == nil, RequiredCapability: "ffmpeg_encoder:aac+ffmpeg_filter_smoke:stereo_downmix_limiter_v3", ValidatedClaims: []string{ClaimAudioDecodeV3}, TerminalReason: TerminalAudioConversionUnsupportedV3},
		{Name: TransformationVideoToH264V3, RecipeVersion: TransformationVideoToH264RecipeVersionV3, Available: ffmpegErr == nil && h264EncoderAvailableV3(encoders), RequiredCapability: "ffmpeg_encoder:h264", PromisedDynamicRange: DynamicRangeSDRV3, ValidatedClaims: []string{ClaimH264DecodeV3}, TerminalReason: TerminalVideoConversionUnsupportedV3},
		{Name: TransformationHDRToSDRToneMapV3, RecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3, Available: len(toneMapCapabilities) > 0, RequiredCapability: "ffmpeg_filter:hdr_to_sdr_tonemap", PromisedDynamicRange: DynamicRangeSDRV3, ValidatedClaims: []string{ClaimHDRMetadataRemovedV3, ClaimSDRBT709OutputV3}, TerminalReason: TerminalHDRTranscodeUnsupportedV3},
	})
	return registry, errors.Join(bsfErr, encoderErr, bsfContextErr, encoderContextErr, audioRecipeProbeInfrastructureError(audioRecipeErr), audioRecipeContextErr)
}

// An ordinary non-zero FFmpeg exit means the installed filter graph is not a
// v3 executor; that is a capability result, not a failed inventory. Process
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

func h264EncoderAvailableV3(encoders []byte) bool {
	for _, encoder := range h264EncodersV3 {
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
