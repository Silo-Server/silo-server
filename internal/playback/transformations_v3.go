package playback

import (
	"bytes"
	"context"
	"os/exec"
	"sort"
	"strings"
	"time"
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

func ProbeTransformationRegistryV3(ctx context.Context, ffmpegPath string) *TransformationRegistryV3 {
	// Resolve exactly like the execution paths (remux and transcode) so every
	// capability advertised here holds for the binary that later runs.
	ffmpegPath = ResolveFFmpegPath(ffmpegPath)
	bsfCtx, cancelBSF := context.WithTimeout(ctx, 3*time.Second)
	bsfs, bsfErr := exec.CommandContext(bsfCtx, ffmpegPath, "-hide_banner", "-bsfs").Output()
	cancelBSF()
	noiseRecipeAvailable := bsfErr == nil && bitstreamFilterAvailableV3(bsfs, "noise") &&
		supportsHEVCResumeLeadingPictureRecipeV3(ctx, ffmpegPath)
	encoderCtx, cancelEncoders := context.WithTimeout(ctx, 3*time.Second)
	encoders, _ := exec.CommandContext(encoderCtx, ffmpegPath, "-hide_banner", "-encoders").Output()
	cancelEncoders()
	_, ffmpegErr := exec.LookPath(ffmpegPath)
	return NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: TransformationServerDV7HDR10V3, RecipeVersion: "1", Available: bytes.Contains(bsfs, []byte("dovi_rpu")), RequiredCapability: "ffmpeg_bsf:dovi_rpu", PromisedDynamicRange: DynamicRangeHDR10V3, ValidatedClaims: DV7ToHDR10ClaimsV3(), TerminalReason: TerminalDVConversionUnsupportedV3},
		{Name: TransformationServerHEVCResumeLeadingPictureDropV3, RecipeVersion: TransformationServerHEVCResumeLeadingPictureDropVersionV3, Available: noiseRecipeAvailable, RequiredCapability: "ffmpeg_bsf:noise.drop", ValidatedClaims: []string{ClaimResumeLeadingPicturesRemovedV3}},
		{Name: TransformationAudioToAACV3, RecipeVersion: "1", Available: ffmpegErr == nil && bytes.Contains(encoders, []byte(" aac ")), RequiredCapability: "ffmpeg_encoder:aac", ValidatedClaims: []string{ClaimAudioDecodeV3}, TerminalReason: TerminalAudioConversionUnsupportedV3},
		{Name: TransformationVideoToH264V3, RecipeVersion: TransformationVideoToH264RecipeVersionV3, Available: ffmpegErr == nil && h264EncoderAvailableV3(encoders), RequiredCapability: "ffmpeg_encoder:h264", PromisedDynamicRange: DynamicRangeSDRV3, ValidatedClaims: []string{ClaimH264DecodeV3}, TerminalReason: TerminalVideoConversionUnsupportedV3},
	})
}

func bitstreamFilterAvailableV3(listing []byte, name string) bool {
	for _, field := range strings.Fields(string(listing)) {
		if field == name {
			return true
		}
	}
	return false
}

// supportsHEVCResumeLeadingPictureRecipeV3 distinguishes the expression-based
// noise BSF from older builds that expose only the integer dropamount option.
// The drop option was introduced with the startpts and key expression variables
// used by this recipe. A bounded live probe also catches a replaced/downgraded
// executable before a remux commits response headers.
func supportsHEVCResumeLeadingPictureRecipeV3(ctx context.Context, ffmpegPath string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	help, err := exec.CommandContext(probeCtx, ffmpegPath, "-hide_banner", "-h", "bsf=noise").CombinedOutput()
	return err == nil && bitstreamFilterAvailableV3(help, "-drop")
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

func (r *TransformationRegistryV3) AvailableRecipe(name, recipeVersion string) bool {
	if r == nil {
		return false
	}
	spec, ok := r.entries[name]
	return ok && spec.Available && spec.RecipeVersion == recipeVersion
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
