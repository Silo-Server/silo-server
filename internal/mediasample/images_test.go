package mediasample

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

func imageRequest(toneMap *ToneMap, attempts ...Attempt) Request {
	return Request{
		Input:    "/media/a.mkv",
		At:       &At{Seconds: 42.5},
		Images:   &ImageOutput{ToneMap: toneMap},
		Attempts: attempts,
	}
}

// listingCapabilities returns a capability loader that serves the filters
// named and counts its calls.
func listingCapabilities(calls *atomic.Int32, err error, filters ...string) func(context.Context, string) (Capabilities, error) {
	var listing strings.Builder
	for _, filter := range filters {
		listing.WriteString(" .S. " + filter + " V->V\n")
	}
	return func(context.Context, string) (Capabilities, error) {
		calls.Add(1)
		if err != nil {
			return Capabilities{}, err
		}
		return FilterCapabilities([]byte(listing.String())), nil
	}
}

func TestImageRequestRoundTripsThroughJSON(t *testing.T) {
	req := imageRequest(&ToneMap{AllowSoftware: true}, Attempt{Hardware: true, TimeoutSeconds: 20}, Attempt{TimeoutSeconds: 25})
	req.Images.Width = 320
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Request
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, req) {
		t.Fatalf("decoded %+v, want %+v (json %s)", decoded, req, data)
	}
}

func TestBuildImageArgs(t *testing.T) {
	tail := []string{"-frames:v", "1", "-f", "image2pipe", "-vcodec", "mjpeg", "-"}
	head := []string{"-hide_banner", "-loglevel", "error"}
	seek := []string{"-ss", "42.500", "-i", "/media/a.mkv"}
	join := func(parts ...[]string) []string {
		var args []string
		for _, part := range parts {
			args = append(args, part...)
		}
		return args
	}
	tests := []struct {
		name     string
		width    int
		threads  int
		toneMap  bool
		hardware bool
		hw       hardwareDecode
		software string
		want     []string
	}{
		{name: "software", want: join(head, seek, tail)},
		{name: "software tone map", toneMap: true, software: softwareToneMapHable, want: join(head, seek, []string{"-vf", softwareToneMapHable}, tail)},
		{name: "scaled", width: 320, want: join(head, seek, []string{"-vf", "scale=320:-2"}, tail)},
		{name: "threads", threads: 2, want: join(head, []string{"-threads", "2"}, seek, tail)},
		{
			name: "vaapi scaled", width: 320, hardware: true, hw: hardwareDecode{Accel: "vaapi", Device: "/dev/dri/renderD128"},
			want: join(head, []string{"-init_hw_device", "vaapi=hw:/dev/dri/renderD128", "-filter_hw_device", "hw", "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi"},
				seek, []string{"-vf", "hwdownload,format=nv12,scale=320:-2"}, tail),
		},
		{
			name: "videotoolbox tone map", toneMap: true, hardware: true, hw: hardwareDecode{Accel: "videotoolbox"}, software: softwareToneMapBT2390,
			want: join(head, []string{"-hwaccel", "videotoolbox"}, seek, []string{"-vf", softwareToneMapBT2390}, tail),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var toneMap *ToneMap
			if tt.toneMap {
				toneMap = &ToneMap{AllowSoftware: true}
			}
			req := imageRequest(toneMap)
			req.Images.Width = tt.width
			req.Threads = tt.threads
			got, err := buildImageArgs(req, Attempt{Hardware: tt.hardware}, tt.hw, tt.software)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("args\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestBuildImageArgsRejectsWhatCannotRun(t *testing.T) {
	tests := []struct {
		name     string
		toneMap  bool
		hardware bool
		hw       hardwareDecode
		want     string
	}{
		{name: "vaapi without a device", hardware: true, hw: hardwareDecode{Accel: "vaapi"}, want: "vaapi requires a render device"},
		{name: "qsv without a device", hardware: true, hw: hardwareDecode{Accel: "qsv"}, want: "qsv requires a render device"},
		{name: "nvenc", hardware: true, hw: hardwareDecode{Accel: "nvenc"}, want: `does not support "nvenc"`},
		{name: "software tone map without a chain", toneMap: true, want: "requires a software tone-map filter"},
		{name: "videotoolbox tone map without a chain", toneMap: true, hardware: true, hw: hardwareDecode{Accel: "videotoolbox"}, want: "requires a software tone-map filter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var toneMap *ToneMap
			if tt.toneMap {
				toneMap = &ToneMap{AllowSoftware: true}
			}
			_, err := buildImageArgs(imageRequest(toneMap), Attempt{Hardware: tt.hardware}, tt.hw, "")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestSupportsHardwareDecode(t *testing.T) {
	for accel, want := range map[string]bool{"qsv": true, "vaapi": true, "videotoolbox": true, "nvenc": false, "none": false, "": false} {
		if got := SupportsHardwareDecode(accel); got != want {
			t.Errorf("SupportsHardwareDecode(%q) = %t, want %t", accel, got, want)
		}
	}
}

func TestSelectSoftwareToneMap(t *testing.T) {
	tests := []struct {
		name       string
		filters    []string
		loadErr    error
		wantFilter string
		wantReason Reason
		wantError  string
	}{
		{name: "prefers tonemapx bt2390", filters: []string{"zscale", "tonemap", "tonemapx"}, wantFilter: softwareToneMapBT2390},
		{name: "falls back to standard hable", filters: []string{"zscale", "tonemap"}, wantFilter: softwareToneMapHable},
		{name: "requires zscale", filters: []string{"tonemapx"}, wantReason: ReasonUnsupported, wantError: "lacks the required zscale filter"},
		{name: "requires a tone map filter", filters: []string{"zscale"}, wantReason: ReasonUnsupported, wantError: "lacks the required tonemapx or tonemap filter"},
		{name: "listing failure", loadErr: errors.New("ffmpeg filter listing failed: exit status 1"), wantReason: ReasonCapabilities, wantError: "exit status 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			runner := Runner{FFmpegPath: "/test/ffmpeg", Capabilities: listingCapabilities(&calls, tt.loadErr, tt.filters...)}
			filter, failure := runner.selectSoftwareToneMap(context.Background())
			if tt.wantError != "" {
				if failure == nil || failure.Reason != tt.wantReason || !strings.Contains(failure.Err.Error(), tt.wantError) {
					t.Fatalf("failure %+v, want %s containing %q", failure, tt.wantReason, tt.wantError)
				}
				return
			}
			if failure != nil || filter != tt.wantFilter {
				t.Fatalf("filter %q failure %+v, want %q", filter, failure, tt.wantFilter)
			}
		})
	}
}

// fakeImage answers image attempts from a list of results, one per process,
// and records each process's arguments.
type fakeImage struct {
	results []error
	calls   [][]string
}

func (f *fakeImage) exec(_ context.Context, _ string, args []string, _ io.Reader, stdout, stderr io.Writer) error {
	f.calls = append(f.calls, args)
	err := f.results[len(f.calls)-1]
	if err != nil {
		_, _ = io.WriteString(stderr, err.Error())
		return err
	}
	_, _ = stdout.Write([]byte("\xff\xd8jpeg"))
	return nil
}

func TestRunImageFallsBackFromHardware(t *testing.T) {
	var loads atomic.Int32
	fake := &fakeImage{results: []error{errors.New("exit status 1"), nil}}
	runner := Runner{FFmpegPath: "ffmpeg", HWAccel: "vaapi", HWDevice: "/dev/dri/renderD128", Workload: processmetrics.Thumbnail,
		Exec: fake.exec, Capabilities: listingCapabilities(&loads, nil, "zscale", "tonemap")}
	req := imageRequest(&ToneMap{AllowSoftware: true}, Attempt{Hardware: true, TimeoutSeconds: 20}, Attempt{TimeoutSeconds: 25})

	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Images) != 1 || result.Images[0].Seconds != 42.5 || !bytes.Equal(result.Images[0].JPEG, []byte("\xff\xd8jpeg")) {
		t.Fatalf("images %+v, want the software attempt's frame at 42.5 s", result.Images)
	}
	if result.Decoder != "software" || len(fake.calls) != 2 || loads.Load() != 1 {
		t.Fatalf("decoder %q after %d processes and %d capability loads, want software after 2 and 1", result.Decoder, len(fake.calls), loads.Load())
	}
	if !strings.Contains(strings.Join(fake.calls[0], " "), "tonemap_vaapi") || !strings.Contains(strings.Join(fake.calls[1], " "), softwareToneMapHable) {
		t.Fatalf("processes %q, want the VAAPI tone map and then the software one", fake.calls)
	}
}

func TestRunImageHardwareSuccessLoadsNoCapabilities(t *testing.T) {
	var loads atomic.Int32
	fake := &fakeImage{results: []error{nil}}
	runner := Runner{HWAccel: "vaapi", HWDevice: "/dev/dri/renderD128", Exec: fake.exec, Capabilities: listingCapabilities(&loads, nil, "zscale", "tonemap")}
	result, err := runner.Run(context.Background(), imageRequest(&ToneMap{AllowSoftware: true}, Attempt{Hardware: true}, Attempt{}))
	if err != nil || result.Decoder != "hardware:vaapi" || loads.Load() != 0 {
		t.Fatalf("decoder %q, error %v, %d capability loads; want hardware:vaapi, nil, 0", result.Decoder, err, loads.Load())
	}
}

func TestRunFallbackDecidesWhetherToMoveOn(t *testing.T) {
	invalid := errors.New("[h264 @ 0x1] Invalid NAL unit size (1234 > 99).")
	fake := &fakeImage{results: []error{invalid, nil}}
	runner := Runner{HWAccel: "vaapi", HWDevice: "/dev/dri/renderD128", Exec: fake.exec}
	if _, err := runner.Run(context.Background(), imageRequest(nil, Attempt{Hardware: true}, Attempt{})); err != nil || len(fake.calls) != 2 {
		t.Fatalf("without Fallback: error %v after %d processes, want the software attempt's frame", err, len(fake.calls))
	}

	fake = &fakeImage{results: []error{invalid, nil}}
	var seen []AttemptError
	runner.Exec = fake.exec
	runner.Fallback = func(attempt Attempt, failure AttemptError) bool {
		seen = append(seen, failure)
		return !attempt.Hardware || failure.Cause() != ReasonInvalidData
	}
	_, err := runner.Run(context.Background(), imageRequest(nil, Attempt{Hardware: true}, Attempt{}))
	if Classify(err) != ReasonInvalidData || len(fake.calls) != 1 || len(seen) != 1 || seen[0].Decoder != "hardware:vaapi" {
		t.Fatalf("error %v after %d processes and %d Fallback calls, want invalid data after one", err, len(fake.calls), len(seen))
	}
}

func TestRunImageRefusesDisallowedSoftwareToneMap(t *testing.T) {
	var loads atomic.Int32
	fake := &fakeImage{}
	for _, accel := range []string{"none", "videotoolbox"} {
		runner := Runner{HWAccel: accel, Exec: fake.exec, Capabilities: listingCapabilities(&loads, nil, "zscale", "tonemap")}
		req := imageRequest(&ToneMap{}, Attempt{Hardware: SupportsHardwareDecode(accel)})
		_, err := runner.Run(context.Background(), req)
		if Classify(err) != ReasonUnsupported || !strings.Contains(err.Error(), "software HDR tone mapping is disabled") {
			t.Fatalf("%s: error %v, want software tone mapping refused", accel, err)
		}
	}
	if len(fake.calls) != 0 || loads.Load() != 0 {
		t.Fatalf("%d processes and %d capability loads, want none", len(fake.calls), loads.Load())
	}
}

func TestRunImageResolvesToneMapOncePerRun(t *testing.T) {
	var loads atomic.Int32
	fake := &fakeImage{}
	runner := Runner{HWAccel: "videotoolbox", Exec: fake.exec, Capabilities: listingCapabilities(&loads, nil, "zscale")}
	_, err := runner.Run(context.Background(), imageRequest(&ToneMap{AllowSoftware: true}, Attempt{Hardware: true}, Attempt{}))
	var runErr *Error
	if !errors.As(err, &runErr) || len(runErr.Attempts) != 2 || Classify(err) != ReasonUnsupported {
		t.Fatalf("error %v, want both attempts refused as unsupported", err)
	}
	if loads.Load() != 1 || len(fake.calls) != 0 {
		t.Fatalf("%d capability loads and %d processes, want 1 and 0", loads.Load(), len(fake.calls))
	}
}

func TestRunImageWithoutOutputFails(t *testing.T) {
	runner := Runner{Exec: func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error { return nil }}
	_, err := runner.Run(context.Background(), imageRequest(nil))
	var runErr *Error
	if !errors.As(err, &runErr) || runErr.Reason != ReasonEmpty {
		t.Fatalf("error %v, want an empty-output failure", err)
	}
}

func TestRunReplacedExecFailures(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want Reason
	}{
		{err: errors.New("exit status 1"), want: ReasonExit},
		{err: context.DeadlineExceeded, want: ReasonTimeout},
	} {
		runner := Runner{Exec: func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error { return tt.err }}
		_, err := runner.Run(context.Background(), imageRequest(nil))
		var runErr *Error
		if !errors.As(err, &runErr) || runErr.Reason != tt.want {
			t.Fatalf("exec error %v: run error %v, want %s", tt.err, err, tt.want)
		}
	}
}
