package mediasample

import (
	"context"
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
