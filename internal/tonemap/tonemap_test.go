package tonemap

import (
	"context"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
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
