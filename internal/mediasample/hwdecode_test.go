package mediasample

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

// The stats chain the credits tail pass measures: software frames and
// downloaded VideoToolbox surfaces are cropped and then scaled, VAAPI surfaces
// are scaled and converted to NV12 on the GPU and then cropped.
const (
	softwareTailChain = "crop=trunc(iw*0.9/2)*2:trunc(ih*0.8/2)*2,scale=480:-2:flags=area,format=yuv420p," +
		"blackframe=amount=0:threshold=20,blackframe=amount=0:threshold=26,blackframe=amount=0:threshold=32,signalstats,metadata=print"
	gpuTailChain = "scale_vaapi=w=534:h=-2:format=nv12,hwdownload,format=nv12,crop=trunc(iw*0.9/2)*2:trunc(ih*0.8/2)*2,format=yuv420p," +
		"blackframe=amount=0:threshold=20,blackframe=amount=0:threshold=26,blackframe=amount=0:threshold=32,signalstats,metadata=print"
	vtTailChain      = "hwdownload,format=nv12," + softwareTailChain
	vt10BitTailChain = "hwdownload,format=p010le," + softwareTailChain
)

func TestBuildStatsGraphScalesVAAPISurfacesOnTheGPU(t *testing.T) {
	graph := buildStatsGraph(tailStats(), "vaapi", 10)
	if graph.filter != gpuTailChain {
		t.Fatalf("filter\n got %s\nwant %s", graph.filter, gpuTailChain)
	}
	// hwdownload and format=nv12 are two filters, so the measuring filters
	// sit two positions later than in the software chain.
	if !reflect.DeepEqual(graph.blackframes, []int{5, 6, 7}) || graph.metadata != 9 {
		t.Fatalf("instances: blackframe %v, metadata %d", graph.blackframes, graph.metadata)
	}
	for _, tt := range []struct {
		stats StatsOutput
		want  int
	}{
		{tailStats(), 534},
		{StatsOutput{CropWidth: 0.7, Width: 480}, 686},
		{StatsOutput{CropWidth: 1, Width: 320}, 320},
		{StatsOutput{CropWidth: 0.75, Width: 2}, 2},
	} {
		if got := gpuScaleWidth(tt.stats); got != tt.want {
			t.Errorf("gpuScaleWidth(%+v) = %d, want %d", tt.stats, got, tt.want)
		}
	}
}

// TestBuildStatsGraphDownloadsVideoToolboxSurfaces expects the download to
// name the surface format of the source's depth, since hwdownload cannot
// convert, and the software chain after it.
func TestBuildStatsGraphDownloadsVideoToolboxSurfaces(t *testing.T) {
	for depth, want := range map[int]string{0: vtTailChain, 8: vtTailChain, 10: vt10BitTailChain} {
		graph := buildStatsGraph(tailStats(), "videotoolbox", depth)
		if graph.filter != want {
			t.Errorf("%d bits: filter\n got %s\nwant %s", depth, graph.filter, want)
		}
		if !reflect.DeepEqual(graph.blackframes, []int{5, 6, 7}) || graph.metadata != 9 {
			t.Errorf("%d bits: instances: blackframe %v, metadata %d", depth, graph.blackframes, graph.metadata)
		}
	}
}

// creditsTailRequest is the episode credits tail pass: audio and keyframe
// statistics of one window.
func creditsTailRequest() Request {
	return Request{
		Input:    "/media/show/episode.mkv",
		Window:   &Window{StartSeconds: 2250, DurationSeconds: 450, KeyframesOnly: true},
		Audio:    &AudioOutput{Fingerprint: true, Silence: &SilenceParams{NoiseDB: -50, MinSeconds: 0.5}},
		Stats:    validStats(),
		Threads:  1,
		Attempts: []Attempt{{Hardware: true}, {}},
	}
}

// tenBit marks req's source as 10-bit.
func tenBit(req Request) Request {
	req.VideoBitDepth = 10
	return req
}

func TestBuildArgsDecodesStatsOnHardware(t *testing.T) {
	const (
		vaapi = "-init_hw_device vaapi=hw:/dev/dri/renderD128 -filter_hw_device hw -hwaccel vaapi -hwaccel_output_format vaapi "
		qsv   = "-init_hw_device vaapi=va:/dev/dri/renderD129,driver=iHD,kernel_driver=i915,vendor_id=0x8086 -init_hw_device qsv=qs@va -filter_hw_device va -hwaccel vaapi -hwaccel_output_format vaapi "
		vt    = "-hwaccel videotoolbox -hwaccel_output_format videotoolbox_vld "
	)
	windowArgs := func(decode, chain string) string {
		return "-hide_banner -nostdin -loglevel repeat+info -threads 1 -filter_threads 1 -skip_frame:v nokey " + decode +
			"-ss 2250 -i /media/show/episode.mkv " +
			"-t 450 -vn -sn -dn -af silencedetect=noise=-50dB:duration=0.5 -ac 2 -f chromaprint -fp_format raw - " +
			"-t 450 -map 0:V:0 -an -sn -dn -vf " + chain + " -f null -"
	}
	samplesArgs := func(decode, chain string) string {
		return "-hide_banner -nostdin -loglevel repeat+info -threads 1 -filter_threads 1 " + decode +
			"-skip_frame:v nokey -protocol_whitelist file,pipe -f concat -safe 0 -i pipe:0 " +
			"-map 0:V:0 -an -sn -dn -vf " + chain + " -f null -"
	}
	tests := []struct {
		name string
		req  Request
		hw   hardwareDecode
		want string
	}{
		{name: "window vaapi", req: creditsTailRequest(), hw: hardwareDecode{Accel: "vaapi", Device: "/dev/dri/renderD128"}, want: windowArgs(vaapi, gpuTailChain)},
		{name: "window qsv", req: creditsTailRequest(), hw: hardwareDecode{Accel: "qsv", Device: "/dev/dri/renderD129"}, want: windowArgs(qsv, gpuTailChain)},
		{name: "window videotoolbox", req: creditsTailRequest(), hw: hardwareDecode{Accel: "videotoolbox"}, want: windowArgs(vt, vtTailChain)},
		{name: "window videotoolbox 10-bit", req: tenBit(creditsTailRequest()), hw: hardwareDecode{Accel: "videotoolbox"}, want: windowArgs(vt, vt10BitTailChain)},
		{name: "samples vaapi", req: samplesRequest(5400, 5403), hw: hardwareDecode{Accel: "vaapi", Device: "/dev/dri/renderD128"}, want: samplesArgs(vaapi, gpuTailChain)},
		{name: "samples qsv", req: samplesRequest(5400, 5403), hw: hardwareDecode{Accel: "qsv", Device: "/dev/dri/renderD129"}, want: samplesArgs(qsv, gpuTailChain)},
		{name: "samples videotoolbox", req: samplesRequest(5400, 5403), hw: hardwareDecode{Accel: "videotoolbox"}, want: samplesArgs(vt, vtTailChain)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.req.Attempts = []Attempt{{Hardware: true}, {}}
			if err := tt.req.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			args, _, err := buildArgs(tt.req, Attempt{Hardware: true}, tt.hw, 0)
			if err != nil {
				t.Fatalf("buildArgs: %v", err)
			}
			if want := strings.Fields(tt.want); !reflect.DeepEqual(args, want) {
				t.Fatalf("args\n got %q\nwant %q", args, want)
			}
			// The software attempt of the same request is unchanged.
			software, _, err := buildArgs(tt.req, Attempt{}, tt.hw, 0)
			if err != nil {
				t.Fatal(err)
			}
			if joined := strings.Join(software, " "); strings.Contains(joined, "hwaccel") || !strings.Contains(joined, softwareTailChain) {
				t.Fatalf("software args %q", software)
			}
		})
	}
}

func TestBuildArgsRejectsHardwareItCannotRun(t *testing.T) {
	tests := []struct {
		name string
		req  Request
		hw   hardwareDecode
		want string
	}{
		{name: "vaapi without a device", req: creditsTailRequest(), hw: hardwareDecode{Accel: "vaapi"}, want: "vaapi requires a render device"},
		{name: "nvenc", req: creditsTailRequest(), hw: hardwareDecode{Accel: "nvenc"}, want: `does not support "nvenc"`},
		{name: "audio only", req: validRequest(), hw: hardwareDecode{Accel: "videotoolbox"}, want: "needs a video output"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := buildArgs(tt.req, Attempt{Hardware: true}, tt.hw, 0)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v, want containing %q", err, tt.want)
			}
		})
	}
}

// gpuStatsFrameLog is statsFrameLog for the GPU chain, whose filters sit two
// positions later.
func gpuStatsFrameLog(index string) string {
	return strings.NewReplacer("Parsed_blackframe_3", "Parsed_blackframe_5", "Parsed_blackframe_4", "Parsed_blackframe_6",
		"Parsed_blackframe_5", "Parsed_blackframe_7", "Parsed_metadata_7", "Parsed_metadata_9").Replace(statsFrameLog(index, "", ""))
}

// fakeStatsExec answers each process with the next of its results: a log
// written to stderr, and an error when fail is set.
type fakeStatsExec struct {
	results []fakeStatsResult
	calls   [][]string
}

type fakeStatsResult struct {
	log  string
	fail bool
}

func (f *fakeStatsExec) exec(_ context.Context, _ string, args []string, _ io.Reader, _, stderr io.Writer) error {
	f.calls = append(f.calls, args)
	result := f.results[len(f.calls)-1]
	_, _ = io.WriteString(stderr, result.log)
	if result.fail {
		return errors.New("exit status 1")
	}
	return nil
}

func statsOnlyRequest(attempts ...Attempt) Request {
	req := creditsTailRequest()
	req.Audio = nil
	req.Attempts = attempts
	return req
}

func TestRunStatsOnHardwareParsesTheGPUChain(t *testing.T) {
	fake := &fakeStatsExec{results: []fakeStatsResult{{log: gpuStatsFrameLog("0") + gpuStatsFrameLog("1")}}}
	runner := Runner{HWAccel: "vaapi", HWDevice: "/dev/dri/renderD128", Workload: processmetrics.Analysis, Exec: fake.exec}
	result, err := runner.Run(context.Background(), statsOnlyRequest(Attempt{Hardware: true}, Attempt{}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Decoder != "hardware:vaapi" || len(fake.calls) != 1 || len(result.Frames) != 2 {
		t.Fatalf("decoder %q after %d processes with %d frames, want hardware:vaapi after one with 2", result.Decoder, len(fake.calls), len(result.Frames))
	}
	if frame := result.Frames[1]; frame.Seconds != 2250.01 || frame.YMin != 1 || !reflect.DeepEqual(frame.PBlack, []uint8{90, 91, 92}) {
		t.Fatalf("frame %+v", frame)
	}
	if !strings.Contains(strings.Join(fake.calls[0], " "), "scale_vaapi=w=534:h=-2") {
		t.Fatalf("args %q, want the GPU chain", fake.calls[0])
	}
}

// TestRunStatsFallsBackFromHardware fails the hardware attempt the way a
// broken driver does, with a log that would otherwise read as a broken
// file, and expects the software attempt to produce the frames.
func TestRunStatsFallsBackFromHardware(t *testing.T) {
	driverFailure := "[AVHWDeviceContext @ 0x1] Failed to initialise VAAPI connection: -1 (unknown libva error).\n" + //nolint:misspell // ffmpeg's own message.
		"Device creation failed: -5.\n/media/show/episode.mkv: Invalid data found when processing input\n"
	fake := &fakeStatsExec{results: []fakeStatsResult{{log: driverFailure, fail: true}, {log: statsFrameLog("0", "", "") + statsFrameLog("1", "", "")}}}
	runner := Runner{HWAccel: "vaapi", HWDevice: "/dev/dri/renderD128", Workload: processmetrics.Analysis, Exec: fake.exec}
	result, err := runner.Run(context.Background(), statsOnlyRequest(Attempt{Hardware: true}, Attempt{}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Decoder != "software" || len(fake.calls) != 2 || len(result.Frames) != 2 {
		t.Fatalf("decoder %q after %d processes with %d frames, want software after 2 with 2", result.Decoder, len(fake.calls), len(result.Frames))
	}
	if strings.Contains(strings.Join(fake.calls[1], " "), "hwaccel") {
		t.Fatalf("software attempt args %q", fake.calls[1])
	}
}

func TestRunSamplesFallsBackFromHardware(t *testing.T) {
	req := samplesRequest(100, 103)
	req.Attempts = []Attempt{{Hardware: true}, {}}
	header := probeHeaderLog("matroska,webm", "0.000000")
	fake := &fakeStatsExec{results: []fakeStatsResult{
		{log: header}, {log: "Impossible to convert between the formats supported by the filter\n", fail: true},
		{log: header}, {log: statsFrameLog("0", "100", "") + statsFrameLog("1", "103", "")},
	}}
	runner := Runner{HWAccel: "qsv", HWDevice: "/dev/dri/renderD128", Workload: processmetrics.Analysis, Exec: fake.exec}
	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Decoder != "software" || len(fake.calls) != 4 || len(result.Frames) != 2 || result.Frames[1].Seconds != 103 {
		t.Fatalf("decoder %q after %d processes with frames %+v", result.Decoder, len(fake.calls), result.Frames)
	}
	if hw := strings.Join(fake.calls[1], " "); !strings.Contains(hw, "qsv=qs@va") || !strings.Contains(hw, "-f concat") {
		t.Fatalf("hardware attempt args %q", fake.calls[1])
	}
}

// TestRunStatsHardwareWithoutADeviceMovesOn runs VAAPI on a host without a
// render device: the hardware attempt fails as unsupported without starting
// ffmpeg, and the software attempt runs.
func TestRunStatsHardwareWithoutADeviceMovesOn(t *testing.T) {
	if playback.PickRenderDevice("") != "" {
		t.Skip("this host has a render device")
	}
	fake := &fakeStatsExec{results: []fakeStatsResult{{log: statsFrameLog("0", "", "")}}}
	runner := Runner{HWAccel: "vaapi", Workload: processmetrics.Analysis, Exec: fake.exec}
	result, err := runner.Run(context.Background(), statsOnlyRequest(Attempt{Hardware: true}, Attempt{}))
	if err != nil || result.Decoder != "software" || len(fake.calls) != 1 {
		t.Fatalf("decoder %q, error %v after %d processes; want software after one", result.Decoder, err, len(fake.calls))
	}

	fake = &fakeStatsExec{}
	runner.Exec = fake.exec
	_, err = runner.Run(context.Background(), statsOnlyRequest(Attempt{Hardware: true}))
	if Classify(err) != ReasonUnsupported || Classify(err).Permanent() || len(fake.calls) != 0 {
		t.Fatalf("error %v after %d processes, want unsupported before ffmpeg starts", err, len(fake.calls))
	}
}

// TestVideoToolboxStatsDecodeOnHardwareOrFail runs stats attempts on
// VideoToolbox. A stream it decodes, 8-bit H.264 or 10-bit HEVC given its
// depth, is measured on hardware. A stream it cannot decode, VP8, fails the
// hardware attempt rather than passing a quiet software decode off as
// hardware, so the software attempt after it runs.
func TestVideoToolboxStatsDecodeOnHardwareOrFail(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("VideoToolbox is macOS only")
	}
	ffmpeg, caps := realFFmpeg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	stats := Request{Window: &Window{DurationSeconds: 3, KeyframesOnly: true}, Stats: validStats()}
	if err := caps.Require(stats); err != nil {
		t.Skipf("ffmpeg cannot measure stats: %v", err)
	}
	clip := func(name string, encoder ...string) string {
		path := filepath.Join(t.TempDir(), name)
		args := append([]string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=10", "-t", "3"}, encoder...)
		if output, err := exec.CommandContext(ctx, ffmpeg, append(args, "-g", "5", path)...).CombinedOutput(); err != nil {
			t.Skipf("cannot encode %s: %v: %s", name, err, output)
		}
		return path
	}
	run := func(path string, depth int, attempts ...Attempt) (Result, error) {
		req := stats
		req.Input, req.VideoBitDepth, req.Attempts = path, depth, attempts
		return (Runner{FFmpegPath: ffmpeg, HWAccel: "videotoolbox"}).Run(ctx, req)
	}
	sample := func(path string, depth int) (Result, error) {
		req := Request{Input: path, Samples: &Samples{Seconds: []float64{0.5, 1.5, 2.5}}, Stats: validStats(), VideoBitDepth: depth, Attempts: []Attempt{{Hardware: true}}}
		return (Runner{FFmpegPath: ffmpeg, HWAccel: "videotoolbox"}).Run(ctx, req)
	}

	for _, tt := range []struct {
		name    string
		depth   int
		encoder []string
	}{
		{"h264.mkv", 8, []string{"-c:v", "libx264", "-pix_fmt", "yuv420p"}},
		{"hevc10.mkv", 10, []string{"-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le", "-x265-params", "log-level=error"}},
	} {
		path := clip(tt.name, tt.encoder...)
		result, err := run(path, tt.depth, Attempt{Hardware: true})
		if err != nil {
			t.Skipf("VideoToolbox cannot decode %s on this host: %v", tt.name, err)
		}
		if result.Decoder != "hardware:videotoolbox" || len(result.Frames) < 5 {
			t.Fatalf("%s: decoder %q with %d frames", tt.name, result.Decoder, len(result.Frames))
		}
		if result, err := sample(path, tt.depth); err != nil || result.Decoder != "hardware:videotoolbox" || len(result.Frames) != 3 {
			t.Fatalf("%s samples: decoder %q with %d frames, error %v", tt.name, result.Decoder, len(result.Frames), err)
		}
	}

	vp8 := clip("vp8.webm", "-c:v", "libvpx")
	if result, err := run(vp8, 8, Attempt{Hardware: true}); err == nil {
		t.Fatalf("VP8 decoded as %q with %d frames, want the hardware attempt to fail", result.Decoder, len(result.Frames))
	}
	result, err := run(vp8, 8, Attempt{Hardware: true}, Attempt{})
	if err != nil || result.Decoder != "software" || len(result.Frames) < 5 {
		t.Fatalf("VP8 with a software attempt: decoder %q with %d frames, error %v", result.Decoder, len(result.Frames), err)
	}
}
