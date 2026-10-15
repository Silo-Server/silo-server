package tonemap

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
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

// TestPolicyKeepsHardwareAndSoftwareIndependent verifies each executor can be enabled separately.
func TestPolicyKeepsHardwareAndSoftwareIndependent(t *testing.T) {
	tests := []struct {
		hardware bool
		software bool
		want     Policy
	}{
		{want: PolicyNone},
		{hardware: true, want: PolicyHardwareOnly},
		{software: true, want: PolicySoftwareOnly},
		{hardware: true, software: true, want: PolicyHardwareThenSoftware},
	}
	for _, tt := range tests {
		if got := NewPolicy(tt.hardware, tt.software); got != tt.want {
			t.Fatalf("NewPolicy(%t, %t) = %q, want %q", tt.hardware, tt.software, got, tt.want)
		}
	}
}

// TestProbeAdvertisesOnlySuccessfulSourceKinds verifies discovery reports only smoke-tested conversions.
func TestProbeAdvertisesOnlySuccessfulSourceKinds(t *testing.T) {
	runner := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case slices.Contains(args, "-filters"):
			return []byte(" .S. sidedata V->V\n .S. zscale V->V\n .S. tonemap V->V\n .S. tonemapx V->V\n .S. tonemap_opencl V->V\n .S. hwmap V->V\n .S. scale_vaapi V->V\n .S. overlay_opencl VV->V\n .S. scale_opencl V->V\n"), nil
		case slices.Contains(args, "-encoders"):
			return []byte("libx264 h264_qsv"), nil
		case strings.Contains(joined, "libx264"):
			return nil, nil
		case strings.Contains(joined, "h264_qsv") && strings.Contains(joined, "smpte2084"):
			return nil, nil
		default:
			return nil, errors.New("unsupported fixture")
		}
	}

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
	runner := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case slices.Contains(args, "-filters"):
			return []byte(" .S. tonemap_opencl V->V\n .S. hwmap V->V\n .S. scale_vaapi V->V\n .S. overlay_opencl VV->V\n .S. scale_opencl V->V\n"), nil
		case slices.Contains(args, "-encoders"):
			return []byte("h264_qsv"), nil
		case strings.Contains(joined, "renderD129"):
			seenSecondDevice = true
			if strings.Contains(joined, "arib-std-b67") {
				return nil, errors.New("HLG unsupported on second device")
			}
			return nil, nil
		default:
			return nil, nil
		}
	}

	got := ProbeWithRunner(context.Background(), "/ffmpeg", "qsv", "/dev/dri/renderD128,/dev/dri/renderD129", runner)
	if !seenSecondDevice {
		t.Fatal("second configured render device was not smoke-tested")
	}
	if !got.Supports(ModeHardware, SourcePQ) || got.Supports(ModeHardware, SourceHLG) || got.Supports(ModeHardware, SourceHLGBT709) {
		t.Fatalf("hardware capabilities = %#v, want PQ only", got)
	}
}

// TestFiltersDeclareBT709Output verifies tone-map graphs explicitly produce SDR color metadata.
func TestFiltersDeclareBT709Output(t *testing.T) {
	for name, filter := range map[string]string{
		"software pq":  SoftwareFilter(SourcePQ, "tonemapx"),
		"software hlg": SoftwareFilter(SourceHLG, "tonemap"),
		"qsv":          QSVFilter(SourcePQ, 0),
		"vaapi":        VAAPIFilter(SourcePQ),
		"cuda":         CUDAFilter(SourcePQ),
		"software dv":  SoftwareFilter(SourceDolbyVision, SoftwareFilterBT2390),
		"qsv dv":       QSVFilter(SourceDolbyVision, 0),
		"cuda dv":      CUDAFilter(SourceDolbyVision),
	} {
		t.Run(name, func(t *testing.T) {
			for _, token := range []string{"bt709", "format="} {
				if !strings.Contains(filter, token) {
					t.Fatalf("filter %q lacks %q", filter, token)
				}
			}
		})
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

// TestQSVFilterScalesHDRBeforeOpenCL checks where QSVFilter places the resize:
// before the OpenCL tone mapper for HDR kinds, after color conversion for SDR
// bases, and nowhere when the height is 0.
func TestQSVFilterScalesHDRBeforeOpenCL(t *testing.T) {
	hdr := QSVFilter(SourcePQ, 720)
	resize := strings.Index(hdr, "scale_vaapi=w=-2:h=720:format=p010")
	if resize < 0 || resize > strings.Index(hdr, HardwareFilterOpenCL) {
		t.Fatalf("HDR QSV filter %q does not resize before tone mapping", hdr)
	}
	if sdr := QSVFilter(SourceSDRBT2020, 720); !strings.HasSuffix(sdr, ",scale_vaapi=w=-2:h=720:format=nv12") {
		t.Fatalf("SDR QSV filter %q does not resize after color conversion", sdr)
	}
	for _, kind := range []SourceKind{SourcePQ, SourceSDRBT2020} {
		if filter := QSVFilter(kind, 0); strings.Contains(filter, "scale_vaapi=w=") {
			t.Fatalf("unscaled %s QSV filter %q resizes", kind, filter)
		}
	}
}

// TestSoftwareAndQSVToneMapsPinTheSourcePeak keeps tonemapx on the source
// peak the QSV graph uses. Without it tonemapx reads a Dolby Vision frame's
// peak from the RPU and darkens sources mastered above 1000 nits.
func TestSoftwareAndQSVToneMapsPinTheSourcePeak(t *testing.T) {
	for _, kind := range AllSourceKinds() {
		if IsSDRSource(kind) {
			continue
		}
		for name, filter := range map[string]string{
			"software": SoftwareFilter(kind, SoftwareFilterBT2390),
			"qsv":      QSVFilter(kind, 0),
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
		"qsv":      QSVFilter(SourceDolbyVision, 0),
		"cuda":     CUDAFilter(SourceDolbyVision),
	} {
		if !strings.Contains(filter, "apply_dovi=1") {
			t.Errorf("%s Dolby Vision filter %q does not apply the RPU", name, filter)
		}
	}
	for name, filter := range map[string]string{
		"software": SoftwareFilter(SourcePQ, SoftwareFilterBT2390),
		"qsv":      QSVFilter(SourcePQ, 0),
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
