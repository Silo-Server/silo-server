package mediasample

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
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
