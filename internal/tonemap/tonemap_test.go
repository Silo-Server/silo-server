package tonemap

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestResolveSourceCoversDolbyVisionFallbackCompatibilityIDs verifies supported Dolby Vision bases are classified.
func TestResolveSourceCoversDolbyVisionFallbackCompatibilityIDs(t *testing.T) {
	tests := []struct {
		name          string
		profile       int
		compatibility int
		primaries     string
		transfer      string
		matrix        string
		want          SourceKind
		wantPreflight bool
	}{
		{name: "id 1 HDR10 PQ", profile: 8, compatibility: 1, primaries: "bt2020", transfer: "smpte2084", matrix: "bt2020nc", want: SourcePQ},
		{name: "id 2 BT709 SDR", profile: 8, compatibility: 2, primaries: "bt709", transfer: "bt709", matrix: "bt709", want: SourceSDRBT709},
		{name: "id 3 legacy HLG", profile: 6, compatibility: 3, primaries: "bt709", transfer: "arib-std-b67", matrix: "bt709", want: SourceHLGBT709, wantPreflight: true},
		{name: "id 4 BT2100 HLG", profile: 8, compatibility: 4, primaries: "bt2020", transfer: "arib-std-b67", matrix: "bt2020nc", want: SourceHLG},
		{name: "id 5 legacy BT2020 SDR", profile: 6, compatibility: 5, primaries: "bt2020", transfer: "bt709", matrix: "bt2020nc", want: SourceSDRBT2020, wantPreflight: true},
		{name: "id 6 UHD Bluray PQ", profile: 7, compatibility: 6, primaries: "bt2020", transfer: "smpte2084", matrix: "bt2020nc", want: SourcePQ},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := SourceMetadata{
				DynamicRange: DynamicRangeDolbyVision, DVProfile: tt.profile, DVBLCompatID: tt.compatibility,
				DVConfigPresent: true, DVBLCompatIDPresent: true, DVBLPresent: true, DVRPUPresent: true,
				ColorRange: "tv", ColorPrimaries: tt.primaries, ColorTransfer: tt.transfer, ColorSpace: tt.matrix,
			}
			got := ResolveSource(source)
			if got.Kind != tt.want || got.PreflightRequired != tt.wantPreflight {
				t.Fatalf("ResolveSource() = %#v, want kind %q preflight %t", got, tt.want, tt.wantPreflight)
			}
		})
	}
}

// TestResolveSourceClassifiesDolbyOnlyAndPreflightsAmbiguousMetadata verifies
// that RPU-only bases need an RPU executor and unsafe signals are rejected or checked.
func TestResolveSourceClassifiesDolbyOnlyAndPreflightsAmbiguousMetadata(t *testing.T) {
	base := SourceMetadata{
		DynamicRange: DynamicRangeDolbyVision, DVProfile: 7, DVBLCompatID: 6,
		DVConfigPresent: true, DVBLCompatIDPresent: true, DVBLPresent: true, DVRPUPresent: true,
		ColorRange: "tv", ColorPrimaries: "bt2020", ColorTransfer: "smpte2084", ColorSpace: "bt2020nc",
	}
	tests := []struct {
		name          string
		mutate        func(*SourceMetadata)
		want          SourceKind
		wantPreflight bool
	}{
		{name: "profile 5", mutate: func(source *SourceMetadata) { source.DVProfile = 5; source.DVBLCompatID = 0 }, want: SourceDolbyVision},
		{name: "profile 5 with untagged color", mutate: func(source *SourceMetadata) {
			source.DVProfile, source.DVBLCompatID = 5, 0
			source.ColorRange, source.ColorPrimaries, source.ColorTransfer, source.ColorSpace = "pc", "", "", ""
		}, want: SourceDolbyVision},
		{name: "profile 5 without RPU", mutate: func(source *SourceMetadata) {
			source.DVProfile = 5
			source.DVBLCompatID = 0
			source.DVRPUPresent = false
		}, want: ""},
		{name: "profile 5 legacy row", mutate: func(source *SourceMetadata) { source.DVProfile = 5; source.DVConfigPresent = false }, want: ""},
		{name: "explicit id 0", mutate: func(source *SourceMetadata) { source.DVBLCompatID = 0 }, want: SourceDolbyVision, wantPreflight: true},
		{name: "explicit id 0 without RPU", mutate: func(source *SourceMetadata) { source.DVBLCompatID = 0; source.DVRPUPresent = false }, want: ""},
		{name: "explicit id 0 with untagged transfer", mutate: func(source *SourceMetadata) { source.DVBLCompatID = 0; source.ColorTransfer = "unknown" }, want: ""},
		{name: "explicit id 0 with SDR transfer", mutate: func(source *SourceMetadata) { source.DVBLCompatID = 0; source.ColorTransfer = "bt709" }, want: ""},
		{name: "absent base layer", mutate: func(source *SourceMetadata) { source.DVBLPresent = false }, want: ""},
		{name: "legacy row lacks presence flags", mutate: func(source *SourceMetadata) { source.DVConfigPresent = false; source.DVBLCompatIDPresent = false }, want: ""},
		{name: "legacy row lacks config presence flag", mutate: func(source *SourceMetadata) { source.DVConfigPresent = false }, want: ""},
		{name: "legacy row lacks compatibility id presence flag", mutate: func(source *SourceMetadata) { source.DVBLCompatIDPresent = false }, want: ""},
		{name: "reserved id with PQ VUI", mutate: func(source *SourceMetadata) { source.DVBLCompatID = 7 }, want: SourcePQ, wantPreflight: true},
		{name: "contradictory transfer", mutate: func(source *SourceMetadata) { source.ColorTransfer = "arib-std-b67" }, want: SourcePQ, wantPreflight: true},
		{name: "missing color signaling", mutate: func(source *SourceMetadata) { source.ColorSpace = "" }, want: SourcePQ, wantPreflight: true},
		{name: "full-range PQ base", mutate: func(source *SourceMetadata) { source.ColorRange = "pc" }, want: SourcePQFullRange, wantPreflight: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := base
			tt.mutate(&source)
			got := ResolveSource(source)
			if got.Kind != tt.want || got.PreflightRequired != tt.wantPreflight {
				t.Fatalf("ResolveSource() = %#v, want kind %q preflight %t", got, tt.want, tt.wantPreflight)
			}
		})
	}
}

// TestResolveSourceClassifiesHLGByPrimaries verifies an HLG source tagged
// with BT.709 primaries and matrix converts as BT.709 without a preflight,
// while missing or mixed tags keep the BT.2020 kind and its preflight.
func TestResolveSourceClassifiesHLGByPrimaries(t *testing.T) {
	tests := []struct {
		name          string
		primaries     string
		matrix        string
		want          SourceKind
		wantPreflight bool
	}{
		{name: "BT.2020 primaries", primaries: "bt2020", matrix: "bt2020nc", want: SourceHLG},
		{name: "BT.709 primaries", primaries: "bt709", matrix: "bt709", want: SourceHLGBT709},
		{name: "BT.709 primaries with untagged matrix", primaries: "bt709", matrix: "", want: SourceHLG, wantPreflight: true},
		{name: "BT.709 primaries with BT.2020 matrix", primaries: "bt709", matrix: "bt2020nc", want: SourceHLG, wantPreflight: true},
		{name: "BT.709 primaries with BT.601 matrix", primaries: "bt709", matrix: "smpte170m", want: SourceHLG, wantPreflight: true},
		{name: "untagged primaries", primaries: "", matrix: "bt2020nc", want: SourceHLG, wantPreflight: true},
		{name: "unknown primaries", primaries: "unknown", matrix: "bt709", want: SourceHLG, wantPreflight: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveSource(SourceMetadata{
				DynamicRange: DynamicRangeHLG,
				ColorRange:   "tv", ColorPrimaries: tt.primaries, ColorTransfer: ColorTransferHLG, ColorSpace: tt.matrix,
			})
			if got.Kind != tt.want || got.PreflightRequired != tt.wantPreflight {
				t.Fatalf("ResolveSource() = %#v, want kind %q preflight %t", got, tt.want, tt.wantPreflight)
			}
		})
	}
}

// TestResolveSourceClassifiesPQByRange verifies an explicit full-range tag
// selects the full-range PQ kind, which decoded frames must then confirm,
// while limited and missing ranges keep the limited-range kind.
func TestResolveSourceClassifiesPQByRange(t *testing.T) {
	tests := []struct {
		name          string
		dynamicRange  string
		colorRange    string
		want          SourceKind
		wantPreflight bool
	}{
		{name: "HDR10 limited", dynamicRange: DynamicRangeHDR10, colorRange: "tv", want: SourcePQ},
		{name: "HDR10 full", dynamicRange: DynamicRangeHDR10, colorRange: "pc", want: SourcePQFullRange, wantPreflight: true},
		{name: "HDR10 jpeg", dynamicRange: DynamicRangeHDR10, colorRange: "jpeg", want: SourcePQFullRange, wantPreflight: true},
		{name: "HDR10 untagged range", dynamicRange: DynamicRangeHDR10, colorRange: "", want: SourcePQ, wantPreflight: true},
		{name: "HDR10+ full", dynamicRange: DynamicRangeHDR10Plus, colorRange: "pc", want: SourcePQFullRange, wantPreflight: true},
		{name: "HLG full stays rejected by preflight", dynamicRange: DynamicRangeHLG, colorRange: "pc", want: SourceHLG, wantPreflight: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transfer := ColorTransferPQ
			if tt.dynamicRange == DynamicRangeHLG {
				transfer = ColorTransferHLG
			}
			got := ResolveSource(SourceMetadata{
				DynamicRange: tt.dynamicRange,
				ColorRange:   tt.colorRange, ColorPrimaries: "bt2020", ColorTransfer: transfer, ColorSpace: "bt2020nc",
			})
			if got.Kind != tt.want || got.PreflightRequired != tt.wantPreflight {
				t.Fatalf("ResolveSource() = %#v, want kind %q preflight %t", got, tt.want, tt.wantPreflight)
			}
		})
	}
}

// TestSoftwareToneMapReadsFullRangePQAtItsOwnLevels encodes one PQ BT.2020
// picture at limited and at full range, classifies each from its stream tag,
// and requires both software graphs to convert them to the same SDR picture.
// So must a limited-range copy whose stream is tagged full range, as when a
// container's range contradicts the bitstream. A graph that reads samples at
// the wrong range stretches or flattens shadows and highlights.
func TestSoftwareToneMapReadsFullRangePQAtItsOwnLevels(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffmpegRun := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		output, err := exec.CommandContext(ctx, ffmpeg, append([]string{"-hide_banner", "-nostdin", "-y"}, args...)...).CombinedOutput()
		return string(output), err
	}
	filters, err := ffmpegRun("-filters")
	if err != nil {
		t.Skipf("ffmpeg filters unavailable: %v", err)
	}
	for _, name := range []string{"zscale", "sidedata", "psnr"} {
		if !FilterListingHas([]byte(filters), name) {
			t.Skipf("ffmpeg lacks %s", name)
		}
	}
	run := func(t *testing.T, args ...string) string {
		t.Helper()
		output, err := ffmpegRun(args...)
		if err != nil {
			t.Fatalf("ffmpeg %v: %v\n%s", args, err, output)
		}
		return output
	}
	dir := t.TempDir()
	// testsrc2's RGB values, scaled to PQ code values up to 0.75, give a
	// picture from black to about 1,000 nits, the peak the graphs map from,
	// with saturated chroma.
	sources := map[string]string{}
	for _, levels := range []struct{ ffmpeg, zimg string }{{"tv", "limited"}, {"pc", "full"}} {
		// Y4M carries the range into the decoded frames, as a bitstream does.
		path := filepath.Join(dir, "source-"+levels.ffmpeg+".y4m")
		run(t, "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=1,format=gbrp,lutrgb=r=val*0.75:g=val*0.75:b=val*0.75",
			"-vf", "zscale=min=gbr:rin=full:pin=bt2020:tin=smpte2084:m=bt2020nc:p=bt2020:t=smpte2084:r="+levels.zimg+",format=yuv420p10le",
			"-color_range", levels.ffmpeg, "-frames:v", "1", "-strict", "-1", path)
		sources[levels.ffmpeg] = path
	}
	cases := []struct {
		name, streamRange, source string
	}{
		{name: "limited range", streamRange: "tv", source: sources["tv"]},
		{name: "full range", streamRange: "pc", source: sources["pc"]},
		{name: "limited samples tagged full", streamRange: "pc", source: sources["tv"]},
	}
	psnr := regexp.MustCompile(`PSNR y:([0-9.]+|inf) u:([0-9.]+|inf) v:([0-9.]+|inf)`)
	for _, filter := range []string{SoftwareFilterBT2390, SoftwareFilterHable} {
		t.Run(filter, func(t *testing.T) {
			if !FilterListingHas([]byte(filters), filter) {
				t.Skipf("ffmpeg lacks %s", filter)
			}
			// Silo's graphs target Jellyfin FFmpeg; an older build may not
			// know a filter option they use.
			if output, err := ffmpegRun("-i", sources["tv"], "-vf", SoftwareFilter(SourcePQ, filter), "-f", "null", "-"); err != nil {
				t.Skipf("ffmpeg cannot run the %s graph: %v\n%s", filter, err, output)
			}
			converted := make([]string, len(cases))
			for i, tc := range cases {
				resolution := ResolveSource(SourceMetadata{
					DynamicRange: DynamicRangeHDR10,
					ColorRange:   tc.streamRange, ColorPrimaries: "bt2020", ColorTransfer: ColorTransferPQ, ColorSpace: "bt2020nc",
				})
				converted[i] = filepath.Join(dir, filter+"-"+strconv.Itoa(i)+".nut")
				run(t, "-i", tc.source, "-vf", SoftwareFilter(resolution.Kind, filter), "-frames:v", "1", "-c:v", "rawvideo", converted[i])
			}
			for i, tc := range cases[1:] {
				match := psnr.FindStringSubmatch(run(t, "-i", converted[0], "-i", converted[i+1], "-lavfi", "psnr", "-f", "null", "-"))
				if match == nil {
					t.Fatalf("%s: ffmpeg reported no PSNR", tc.name)
				}
				// With Jellyfin FFmpeg 8.1.3 the copies agree to above 53 dB
				// on every plane; reading full-range samples as limited range
				// drops luma below 40 dB.
				for p, plane := range []string{"y", "u", "v"} {
					value := match[p+1]
					if value == "inf" {
						continue
					}
					db, err := strconv.ParseFloat(value, 64)
					if err != nil {
						t.Fatalf("%s: parse %s plane PSNR %q: %v", tc.name, plane, value, err)
					}
					if db < 48 {
						t.Errorf("%s: %s plane PSNR = %.1f dB against the limited source, want at least 48", tc.name, plane, db)
					}
				}
			}
		})
	}
}

// TestProbeAdvertisesOnlySuccessfulSourceKinds verifies discovery reports only smoke-tested conversions.
func TestProbeAdvertisesOnlySuccessfulSourceKinds(t *testing.T) {
	filters := " .S. sidedata V->V\n .S. zscale V->V\n .S. tonemap V->V\n .S. tonemapx V->V\n .S. tonemap_opencl V->V\n .S. hwmap V->V\n .S. scale_vaapi V->V\n .S. overlay_opencl VV->V\n .S. scale_opencl V->V\n"
	runner := listingRunner(nil, filters, "libx264 h264_qsv", func(args string) ([]byte, error) {
		if strings.Contains(args, "libx264") || strings.Contains(args, "h264_qsv") && strings.Contains(args, "smpte2084") {
			return nil, nil
		}
		return nil, errors.New("unsupported fixture")
	})

	got := ProbeWithRunner(context.Background(), "/ffmpeg", "qsv", "/dev/dri/renderD128", runner)
	if !got.Supports(ModeSoftware, SourcePQ) || !got.Supports(ModeSoftware, SourceHLG) {
		t.Fatalf("software capabilities = %#v", got)
	}
	if !got.Supports(ModeHardware, SourcePQ) || got.Supports(ModeHardware, SourceHLG) || got.Supports(ModeHardware, SourceHLGBT709) {
		t.Fatalf("hardware capabilities = %#v", got)
	}
}

// TestProbeValidatesEveryConfiguredHardwareDevice verifies pooled devices share only common capabilities.
func TestProbeValidatesEveryConfiguredHardwareDevice(t *testing.T) {
	seenSecondDevice := false
	filters := " .S. tonemap_opencl V->V\n .S. hwmap V->V\n .S. scale_vaapi V->V\n .S. overlay_opencl VV->V\n .S. scale_opencl V->V\n"
	runner := listingRunner(nil, filters, "h264_qsv", func(args string) ([]byte, error) {
		if strings.Contains(args, "renderD129") {
			seenSecondDevice = true
			if strings.Contains(args, "arib-std-b67") {
				return nil, errors.New("HLG unsupported on second device")
			}
		}
		return nil, nil
	})

	got := ProbeWithRunner(context.Background(), "/ffmpeg", "qsv", "/dev/dri/renderD128,/dev/dri/renderD129", runner)
	if !seenSecondDevice {
		t.Fatal("second configured render device was not smoke-tested")
	}
	if !got.Supports(ModeHardware, SourcePQ) || got.Supports(ModeHardware, SourceHLG) || got.Supports(ModeHardware, SourceHLGBT709) {
		t.Fatalf("hardware capabilities = %#v, want PQ only", got)
	}
}

// TestSoftwareFiltersUseThePixelFormatExpectedByEachToneMapper guards the
// distinct input contracts of Jellyfin's tonemapx and FFmpeg's tonemap.
func TestSoftwareFiltersUseThePixelFormatExpectedByEachToneMapper(t *testing.T) {
	bt2390 := SoftwareFilter(SourcePQ, SoftwareFilterBT2390)
	for _, unwanted := range []string{"zscale=t=linear", "format=gbrpf32le"} {
		if strings.Contains(bt2390, unwanted) {
			t.Fatalf("BT.2390 filter %q unexpectedly contains %q", bt2390, unwanted)
		}
	}
	if !strings.Contains(bt2390, "tonemapx=tonemap=bt2390") {
		t.Fatalf("BT.2390 filter = %q, want tonemapx", bt2390)
	}

	hable := SoftwareFilter(SourcePQ, SoftwareFilterHable)
	for _, required := range []string{"zscale=t=linear", "format=gbrpf32le", "tonemap=hable"} {
		if !strings.Contains(hable, required) {
			t.Fatalf("Hable filter %q lacks %q", hable, required)
		}
	}
}

// TestIntelFilterScalesHDRBeforeOpenCL checks where IntelFilter places the resize:
// before the OpenCL tone mapper for HDR kinds, after color conversion for SDR
// bases, and nowhere when the height is 0.
func TestIntelFilterScalesHDRBeforeOpenCL(t *testing.T) {
	hdr := IntelFilter(SourcePQ, 720)
	resize := strings.Index(hdr, "scale_vaapi=w=-2:h=720:format=p010")
	if resize < 0 || resize > strings.Index(hdr, HardwareFilterOpenCL) {
		t.Fatalf("HDR Intel filter %q does not resize before tone mapping", hdr)
	}
	if sdr := IntelFilter(SourceSDRBT2020, 720); !strings.HasSuffix(sdr, ",scale_vaapi=w=-2:h=720:format=nv12") {
		t.Fatalf("SDR Intel filter %q does not resize after color conversion", sdr)
	}
	for _, kind := range []SourceKind{SourcePQ, SourceSDRBT2020} {
		if filter := IntelFilter(kind, 0); strings.Contains(filter, "scale_vaapi=w=") {
			t.Fatalf("unscaled %s Intel filter %q resizes", kind, filter)
		}
	}
}

// TestSoftwareAndIntelToneMapsPinTheSourcePeak keeps tonemapx on the source
// peak the Intel OpenCL graph uses. Without it tonemapx reads a Dolby Vision frame's
// peak from the RPU and darkens sources mastered above 1000 nits.
func TestSoftwareAndIntelToneMapsPinTheSourcePeak(t *testing.T) {
	for _, kind := range AllSourceKinds() {
		if IsSDRSource(kind) {
			continue
		}
		for name, filter := range map[string]string{
			"software": SoftwareFilter(kind, SoftwareFilterBT2390),
			"intel":    IntelFilter(kind, 0),
		} {
			options := strings.FieldsFunc(filter, func(r rune) bool { return r == ',' || r == ':' })
			if !slices.Contains(options, "peak=100") {
				t.Errorf("%s %s filter %q does not pin the source peak", name, kind, filter)
			}
		}
	}
}

// TestDolbyVisionGraphsApplyTheRPU pins RPU application to the Dolby Vision
// kind: standard bases keep their validated graphs, and only filters that can
// apply an RPU may carry the kind.
func TestDolbyVisionGraphsApplyTheRPU(t *testing.T) {
	for name, filter := range map[string]string{
		"software": SoftwareFilter(SourceDolbyVision, SoftwareFilterBT2390),
		"intel":    IntelFilter(SourceDolbyVision, 0),
		"cuda":     CUDAFilter(SourceDolbyVision),
	} {
		if !strings.Contains(filter, "apply_dovi=1") {
			t.Errorf("%s Dolby Vision filter %q does not apply the RPU", name, filter)
		}
	}
	for name, filter := range map[string]string{
		"software": SoftwareFilter(SourcePQ, SoftwareFilterBT2390),
		"qsv":      IntelFilter(SourcePQ, 0),
	} {
		if strings.Contains(filter, "apply_dovi") {
			t.Errorf("%s PQ filter %q changed its Dolby Vision handling", name, filter)
		}
	}
	if got := CUDAFilter(SourcePQ); !strings.Contains(got, "apply_dovi=0") {
		t.Errorf("CUDA PQ filter %q must keep Dolby Vision processing off", got)
	}
	for _, filter := range []string{SoftwareFilterBT2390, HardwareFilterOpenCL, HardwareFilterCUDA} {
		if !FilterAppliesDolbyVision(filter) {
			t.Errorf("FilterAppliesDolbyVision(%q) = false", filter)
		}
	}
	for _, filter := range []string{SoftwareFilterHable, HardwareFilterVAAPI, HardwareFilterVideoToolbox, ""} {
		if FilterAppliesDolbyVision(filter) {
			t.Errorf("FilterAppliesDolbyVision(%q) = true", filter)
		}
	}
	if !ValidSourceKind(SourceDolbyVision) || slices.Contains(StandardSourceKinds(), SourceDolbyVision) {
		t.Fatal("Dolby Vision must be a valid but non-standard source kind")
	}
}

func TestDynamicRangeForVideoTrackReadsStoredHDR10PlusRangeType(t *testing.T) {
	tests := []struct {
		rangeType string
		want      string
	}{
		{rangeType: "HDR10Plus", want: DynamicRangeHDR10Plus},
		{rangeType: "HDR10+", want: DynamicRangeHDR10Plus},
		{rangeType: "HDR10", want: DynamicRangeHDR10},
	}
	for _, tt := range tests {
		track := models.VideoTrack{Codec: "hevc", VideoRange: "HDR", VideoRangeType: tt.rangeType, ColorTransfer: "smpte2084"}
		if got := DynamicRangeForVideoTrack(track); got != tt.want {
			t.Errorf("DynamicRangeForVideoTrack(%q) = %q, want %q", tt.rangeType, got, tt.want)
		}
	}
}
