package mediasample

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// jellyfinFilters and stockFilters are the tone-map filters a Jellyfin FFmpeg
// and a stock FFmpeg list.
var (
	jellyfinFilters = []string{"zscale", "sidedata", "tonemapx", "tonemap"}
	stockFilters    = []string{"zscale", "sidedata", "tonemap"}
)

func hdrSheetsRequest(source tonemap.SourceKind, attempts ...Attempt) Request {
	req := sheetsRequest(secondsFrom(5, 10, 4), 2, 2)
	req.Sheets.ToneMap = &ToneMap{AllowSoftware: true, Source: source}
	req.Attempts = attempts
	return req
}

func fourFrames() *fakeSheets {
	return &fakeSheets{format: "matroska,webm", frames: []fakeSheetFrame{tagged(10, 5), tagged(20, 15), tagged(30, 25), tagged(40, 35)}}
}

// graphOf returns the -vf argument of a run.
func graphOf(args []string) string {
	if i := slices.Index(args, "-vf"); i >= 0 {
		return args[i+1]
	}
	return ""
}

func TestRunSheetsToneMapsDolbyVisionWithTheRPU(t *testing.T) {
	dv := tonemap.SoftwareFilter(tonemap.SourceDolbyVision, tonemap.SoftwareFilterBT2390)
	for _, accel := range []string{"", "vaapi", "qsv", "nvenc", "videotoolbox"} {
		var loads atomic.Int32
		fake := fourFrames()
		runner := Runner{FFmpegPath: "ffmpeg", HWAccel: accel, HWDevice: "/dev/dri/renderD128", Exec: fake.exec,
			Capabilities: listingCapabilities(&loads, nil, jellyfinFilters...)}
		attempts := []Attempt{{}}
		if accel != "" {
			attempts = []Attempt{{Hardware: true}, {}}
		}
		result, err := runner.Run(t.Context(), hdrSheetsRequest(tonemap.SourceDolbyVision, attempts...))
		if err != nil {
			t.Fatalf("%q: %v", accel, err)
		}
		graph := graphOf(fake.calls[1])
		if !strings.Contains(graph, "scale=24:16:flags=area,"+dv+",") || !strings.Contains(graph, "apply_dovi=1") {
			t.Errorf("%q: graph %s, want the scaled frames tone mapped with the RPU", accel, graph)
		}
		gpu := accel == "vaapi" || accel == "qsv" || accel == "nvenc"
		if gpu != strings.Contains(graph, "hwdownload,format=p010le,") {
			t.Errorf("%q: graph %s; GPU frames download as P010 and nothing else does", accel, graph)
		}
		if strings.Contains(graph, "tonemap_vaapi") || strings.Contains(graph, "tonemap_cuda") || strings.Contains(graph, "opencl") {
			t.Errorf("%q: graph %s tone maps on the GPU", accel, graph)
		}
		want := "software"
		if accel != "" {
			want = "hardware:" + accel
		}
		if result.Decoder != want || loads.Load() != 1 {
			t.Errorf("%q: decoder %s after %d capability loads", accel, result.Decoder, loads.Load())
		}
	}
}

func TestRunSheetsToneMapsHLGWithItsTransfer(t *testing.T) {
	var loads atomic.Int32
	fake := fourFrames()
	runner := Runner{HWAccel: "vaapi", HWDevice: "/dev/dri/renderD128", Exec: fake.exec, Capabilities: listingCapabilities(&loads, nil, stockFilters...)}
	result, err := runner.Run(t.Context(), hdrSheetsRequest(tonemap.SourceHLG, Attempt{Hardware: true}, Attempt{}))
	if err != nil {
		t.Fatal(err)
	}
	graph := graphOf(fake.calls[1])
	if result.Decoder != "hardware:vaapi" || !strings.Contains(graph, tonemap.SoftwareFilter(tonemap.SourceHLG, tonemap.SoftwareFilterHable)) ||
		!strings.Contains(graph, "color_trc=arib-std-b67") || strings.Contains(graph, "procamp_vaapi") {
		t.Fatalf("decoder %s, graph %s; want the HLG chain after a VAAPI decode", result.Decoder, graph)
	}
}

func TestRunSheetsDecodesOnCUDA(t *testing.T) {
	var loads atomic.Int32
	fake := fourFrames()
	runner := Runner{HWAccel: "nvenc", HWDevice: "1", Exec: fake.exec, Capabilities: listingCapabilities(&loads, nil, jellyfinFilters...)}
	result, err := runner.Run(t.Context(), hdrSheetsRequest(tonemap.SourcePQ, Attempt{Hardware: true}, Attempt{}))
	if err != nil {
		t.Fatal(err)
	}
	args := fake.calls[1]
	decode := []string{"-init_hw_device", "cuda=cu:1", "-filter_hw_device", "cu", "-hwaccel", "cuda", "-hwaccel_output_format", "cuda"}
	if result.Decoder != "hardware:nvenc" || !slices.Equal(args[4:12], decode) || !strings.Contains(graphOf(args), "scale_cuda=") {
		t.Fatalf("decoder %s, args %q; want CUDA decode on device 1", result.Decoder, args)
	}
	// Without a source signal there is no CUDA chain: software tone maps.
	fake = fourFrames()
	runner = Runner{HWAccel: "nvenc", Exec: fake.exec, Capabilities: listingCapabilities(&loads, nil, jellyfinFilters...)}
	result, err = runner.Run(t.Context(), hdrSheetsRequest("", Attempt{Hardware: true}, Attempt{}))
	if err != nil || result.Decoder != "software" || len(fake.calls) != 2 {
		t.Fatalf("decoder %s, runs %d, error %v; want software only", result.Decoder, len(fake.calls), err)
	}
}

// TestRunRefusesCUDAForStatsAndImages keeps the outputs whose chains assume
// VAAPI surfaces off CUDA: their hardware attempts fail as unsupported
// without starting ffmpeg.
func TestRunRefusesCUDAForStatsAndImages(t *testing.T) {
	for name, req := range map[string]Request{
		"stats":  creditsTailRequest(),
		"images": imageRequest(nil),
	} {
		var started atomic.Int32
		runner := Runner{HWAccel: "nvenc", Exec: func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error {
			started.Add(1)
			return nil
		}}
		req.Attempts = []Attempt{{Hardware: true}}
		_, err := runner.Run(t.Context(), req)
		if failure, ok := errors.AsType[*Error](err); !ok || failure.Reason != ReasonUnsupported || started.Load() != 0 {
			t.Errorf("%s: error %v after %d processes, want unsupported before ffmpeg", name, err, started.Load())
		}
	}
}

func TestRunSheetsWithoutASourceToneMap(t *testing.T) {
	tests := map[string]struct {
		filters []string
		loadErr error
		allow   bool
		want    Reason
	}{
		"Dolby Vision without tonemapx": {filters: stockFilters, allow: true, want: ReasonUnsupported},
		"no sidedata":                   {filters: []string{"zscale", "tonemapx"}, allow: true, want: ReasonUnsupported},
		"software not allowed":          {filters: jellyfinFilters, want: ReasonUnsupported},
		"listing failed":                {loadErr: errors.New("ffmpeg -filters timed out"), allow: true, want: ReasonCapabilities},
	}
	for name, tt := range tests {
		var loads atomic.Int32
		fake := fourFrames()
		runner := Runner{HWAccel: "vaapi", HWDevice: "/dev/dri/renderD128", Exec: fake.exec, Capabilities: listingCapabilities(&loads, tt.loadErr, tt.filters...)}
		req := hdrSheetsRequest(tonemap.SourceDolbyVision, Attempt{Hardware: true}, Attempt{})
		req.Sheets.ToneMap.AllowSoftware = tt.allow
		_, err := runner.Run(t.Context(), req)
		wantLoads := int32(1)
		if !tt.allow {
			wantLoads = 0
		}
		if failure, ok := errors.AsType[*Error](err); !ok || failure.Reason != tt.want || len(fake.calls) != 0 || loads.Load() != wantLoads {
			t.Errorf("%s: error %v after %d processes and %d loads, want %s before ffmpeg", name, err, len(fake.calls), loads.Load(), tt.want)
		}
	}
}

func TestSelectSourceToneMap(t *testing.T) {
	tests := []struct {
		kind    tonemap.SourceKind
		filters []string
		want    string
	}{
		{tonemap.SourcePQ, jellyfinFilters, tonemap.SoftwareFilter(tonemap.SourcePQ, tonemap.SoftwareFilterBT2390)},
		{tonemap.SourcePQ, stockFilters, tonemap.SoftwareFilter(tonemap.SourcePQ, tonemap.SoftwareFilterHable)},
		{tonemap.SourceDolbyVision, jellyfinFilters, tonemap.SoftwareFilter(tonemap.SourceDolbyVision, tonemap.SoftwareFilterBT2390)},
		{tonemap.SourceDolbyVision, stockFilters, ""},
		{tonemap.SourceSDRBT2020, []string{"zscale", "sidedata"}, tonemap.SoftwareFilter(tonemap.SourceSDRBT2020, "")},
	}
	for _, tt := range tests {
		var loads atomic.Int32
		runner := Runner{Capabilities: listingCapabilities(&loads, nil, tt.filters...)}
		got, failure := runner.selectSourceToneMap(t.Context(), tt.kind)
		if got != tt.want || (tt.want == "") != (failure != nil) {
			t.Errorf("%s with %v: chain %q, failure %v; want %q", tt.kind, tt.filters, got, failure, tt.want)
		}
	}
}

func TestToneMapSourceValidation(t *testing.T) {
	req := hdrSheetsRequest(tonemap.SourceHLG)
	if err := req.Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	encoded, err := json.Marshal(req)
	if err != nil || !strings.Contains(string(encoded), `"source":"hlg"`) {
		t.Fatalf("encoded %s, error %v", encoded, err)
	}
	var decoded Request
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.Sheets.ToneMap.Source != tonemap.SourceHLG {
		t.Fatalf("decoded %+v, error %v", decoded.Sheets.ToneMap, err)
	}
	req.Sheets.ToneMap.Source = "hdr12"
	if err := req.Validate(); err == nil {
		t.Error("unknown source: want a validation error")
	}
	image := imageRequest(&ToneMap{AllowSoftware: true, Source: tonemap.SourcePQ})
	if err := image.Validate(); err == nil {
		t.Error("image with a source: want a validation error")
	}
}

func TestSupportsHardwareSheets(t *testing.T) {
	for accel, want := range map[string]bool{"qsv": true, "vaapi": true, "videotoolbox": true, "nvenc": true, "none": false, "": false} {
		if got := SupportsHardwareSheets(accel); got != want {
			t.Errorf("SupportsHardwareSheets(%q) = %t, want %t", accel, got, want)
		}
	}
}

func TestHardwareDecoderRefusalIsUnsupported(t *testing.T) {
	for _, log := range []string{
		"[mpeg4 @ 0x5] Failed setup for format vaapi: hwaccel initialisation returned error.",
		"[h264 @ 0x5] Your platform doesn't support hardware accelerated H264 decoding.",
	} {
		failure := AttemptError{Reason: ReasonExit, Err: errors.New("exit status 218"), StderrTail: log}
		if got := failure.Cause(); got != ReasonUnsupported {
			t.Errorf("%q: cause %s, want unsupported", log, got)
		}
	}
}
