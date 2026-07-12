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
	if strings.TrimSpace(ffmpegPath) == "" {
		ffmpegPath = "ffmpeg"
	}
	bsfCtx, cancelBSF := context.WithTimeout(ctx, 3*time.Second)
	bsfs, _ := exec.CommandContext(bsfCtx, ffmpegPath, "-hide_banner", "-bsfs").Output()
	cancelBSF()
	encoderCtx, cancelEncoders := context.WithTimeout(ctx, 3*time.Second)
	encoders, _ := exec.CommandContext(encoderCtx, ffmpegPath, "-hide_banner", "-encoders").Output()
	cancelEncoders()
	_, ffmpegErr := exec.LookPath(ffmpegPath)
	return NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: "dv_metadata_strip_to_hdr10", RecipeVersion: "1", Available: bytes.Contains(bsfs, []byte("dovi_rpu")), RequiredCapability: "ffmpeg_bsf:dovi_rpu", PromisedDynamicRange: "hdr10", ValidatedClaims: []string{"dolby_vision_metadata_removed", "hdr10_base_layer_preserved"}, TerminalReason: "dv_conversion_unsupported"},
		{Name: "audio_to_aac", RecipeVersion: "1", Available: ffmpegErr == nil && bytes.Contains(encoders, []byte(" aac ")), RequiredCapability: "ffmpeg_encoder:aac", ValidatedClaims: []string{"media3_audio_decode"}, TerminalReason: "audio_conversion_unsupported"},
		{Name: "video_to_h264", RecipeVersion: "1", Available: ffmpegErr == nil && bytes.Contains(encoders, []byte("libx264")), RequiredCapability: "ffmpeg_encoder:libx264", PromisedDynamicRange: "sdr", ValidatedClaims: []string{"media3_h264_decode"}, TerminalReason: "video_conversion_unsupported"},
	})
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

func (r *TransformationRegistryV3) Advertised() []TransformationV3 {
	if r == nil {
		return nil
	}
	result := make([]TransformationV3, 0, len(r.entries))
	for _, spec := range r.entries {
		if spec.Available {
			result = append(result, TransformationV3{Name: spec.Name, ValidatedClaims: append([]string(nil), spec.ValidatedClaims...)})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}
