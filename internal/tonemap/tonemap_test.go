package tonemap

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// TestSourceKindFor verifies dynamic-range and compatibility mappings.
func TestSourceKindFor(t *testing.T) {
	tests := []struct {
		name         string
		dynamicRange string
		dvCompatID   int
		want         SourceKind
	}{
		{name: "hdr10", dynamicRange: "hdr10", want: SourcePQ},
		{name: "hdr10 plus", dynamicRange: "hdr10_plus", want: SourcePQ},
		{name: "hlg", dynamicRange: "hlg", want: SourceHLG},
		{name: "dv hdr10 base", dynamicRange: "dolby_vision", dvCompatID: 1, want: SourcePQ},
		{name: "dv hlg base", dynamicRange: "dolby_vision", dvCompatID: 4, want: SourceHLG},
		{name: "dv unknown base", dynamicRange: "dolby_vision", want: ""},
		{name: "dv bt709 sdr base", dynamicRange: "dolby_vision", dvCompatID: 2, want: SourceSDRBT709},
		{name: "dv legacy hlg base", dynamicRange: "dolby_vision", dvCompatID: 3, want: SourceHLGBT709},
		{name: "dv bt2020 sdr base", dynamicRange: "dolby_vision", dvCompatID: 5, want: SourceSDRBT2020},
		{name: "dv uhd bluray hdr base", dynamicRange: "dolby_vision", dvCompatID: 6, want: SourcePQ},
		{name: "unknown hdr", dynamicRange: "hdr_unknown", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SourceKindFor(tt.dynamicRange, tt.dvCompatID); got != tt.want {
				t.Fatalf("SourceKindFor() = %q, want %q", got, tt.want)
			}
		})
	}
}

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

// TestResolveSourceRejectsDolbyOnlyAndPreflightsAmbiguousMetadata verifies unsafe signals are rejected or checked.
func TestResolveSourceRejectsDolbyOnlyAndPreflightsAmbiguousMetadata(t *testing.T) {
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
		{name: "profile 5", mutate: func(source *SourceMetadata) { source.DVProfile = 5 }, want: ""},
		{name: "explicit id 0", mutate: func(source *SourceMetadata) { source.DVBLCompatID = 0 }, want: ""},
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
			if tt.wantPreflight && ClassifySource(source) != "" {
				t.Fatal("ClassifySource accepted an ambiguous source without preflight")
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
			return []byte(" .S. sidedata V->V\n .S. zscale V->V\n .S. tonemap V->V\n .S. tonemapx V->V\n .S. tonemap_opencl V->V\n .S. hwmap V->V\n .S. scale_vaapi V->V\n"), nil
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
			return []byte(" .S. tonemap_opencl V->V\n .S. hwmap V->V\n .S. scale_vaapi V->V\n"), nil
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
		"qsv":          QSVFilter(SourcePQ),
		"vaapi":        VAAPIFilter(SourcePQ),
		"cuda":         CUDAFilter(),
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
