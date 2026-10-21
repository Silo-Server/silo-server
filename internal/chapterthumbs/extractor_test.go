package chapterthumbs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

func TestExtractFrameRetriesTransientSoftwareProbeFailure(t *testing.T) {
	probeCalls := 0
	resolver := capabilitiesFromListing(func(string) ([]byte, error) {
		probeCalls++
		if probeCalls == 1 {
			return []byte("temporary stderr"), errors.New("resource temporarily unavailable")
		}
		return []byte(" .S. zscale V->V\n .S. tonemap V->V\n"), nil
	})
	opts := FrameExtractOptions{
		InputPath:            "/media/movie.mkv",
		FFmpegPath:           "/test/ffmpeg",
		HWAccel:              hwAccelNone,
		ToneMap:              true,
		AllowSoftwareToneMap: true,
		loadCapabilities:     resolver,
		RunFunc: func(context.Context, string, []string) ([]byte, error) {
			return []byte("frame"), nil
		},
	}

	if _, reason, err := ExtractFrame(context.Background(), opts); err == nil || reason != reasonFFmpegProbeFailed {
		t.Fatalf("first ExtractFrame() reason = %q, error = %v, want retryable probe failure", reason, err)
	}
	data, reason, err := ExtractFrame(context.Background(), opts)
	if err != nil || reason != "" || string(data) != "frame" {
		t.Fatalf("second ExtractFrame() = %q, %q, %v", data, reason, err)
	}
	if probeCalls != 2 {
		t.Fatalf("probe calls = %d, want 2", probeCalls)
	}
}

func TestRemoteExtractTimeoutBudgetsOnlyAllowedAttempts(t *testing.T) {
	sdrExtractBudget := extractTimeoutForAttempt(true, false) + extractTimeoutForAttempt(false, false)
	if got := remoteExtractTimeout(false, false); got <= sdrExtractBudget {
		t.Fatalf("remoteExtractTimeout(SDR) = %s, want more than extract budget %s", got, sdrExtractBudget)
	}

	hardwareHDRBudget := extractTimeoutForAttempt(true, true)
	softwareHDRBudget := extractTimeoutForAttempt(false, true)
	if got := remoteExtractTimeout(true, false); got <= hardwareHDRBudget || got >= hardwareHDRBudget+softwareHDRBudget {
		t.Fatalf("remoteExtractTimeout(HDR hardware only) = %s, want hardware plus overhead without CPU budget", got)
	}

	hdrExtractBudget := hardwareHDRBudget + softwareHDRBudget
	if got := remoteExtractTimeout(true, true); got-hdrExtractBudget <= softwareToneMapProbeTimeout {
		t.Fatalf(
			"remoteExtractTimeout(HDR with CPU) overhead = %s, want more than probe budget %s",
			got-hdrExtractBudget,
			softwareToneMapProbeTimeout,
		)
	}
}

// capabilitiesFromListing loads capabilities from the ffmpeg -filters listing
// probe returns.
func capabilitiesFromListing(probe func(ffmpegPath string) ([]byte, error)) func(context.Context, string) (mediasample.Capabilities, error) {
	return func(_ context.Context, ffmpegPath string) (mediasample.Capabilities, error) {
		output, err := probe(ffmpegPath)
		if err != nil {
			return mediasample.Capabilities{}, err
		}
		return mediasample.FilterCapabilities(output), nil
	}
}

func capabilitiesWithFilters(t *testing.T, filters ...string) func(context.Context, string) (mediasample.Capabilities, error) {
	t.Helper()
	lines := make([]string, 0, len(filters))
	for _, filter := range filters {
		lines = append(lines, " .S. "+filter+" V->V")
	}
	output := strings.Join(lines, "\n")
	return capabilitiesFromListing(func(string) ([]byte, error) {
		return []byte(output), nil
	})
}

// TestExtractFrameKeepsLegacyReasonsAndFallback pins the persisted reasons
// and fallback rules chapter thumbnails had before they moved onto
// mediasample, where mediasample's own causes would differ.
func TestExtractFrameKeepsLegacyReasonsAndFallback(t *testing.T) {
	tests := []struct {
		name       string
		accel      string
		toneMap    bool
		results    []string
		wantReason string
		wantCalls  int
	}{
		{name: "unknown option is not a tone-map failure", accel: hwAccelNone, results: []string{"Unrecognized option 'x'.\nError splitting the argument list: Option not found"}, wantReason: reasonChapterExtractFailed, wantCalls: 1},
		{name: "missing decoder is not a tone-map failure", accel: hwAccelNone, results: []string{"Decoder not found"}, wantReason: reasonChapterExtractFailed, wantCalls: 1},
		{name: "missing filter is a tone-map failure", accel: hwAccelNone, results: []string{"[AVFilterGraph @ 0x1] No such filter: 'zscale'"}, wantReason: reasonToneMapUnsupported, wantCalls: 1},
		{name: "hardware tone-map error", accel: "vaapi", toneMap: true, results: []string{"[Parsed_tonemap_vaapi_2 @ 0x1] Failed to create processing pipeline.\nError reinitializing filters!"}, wantReason: reasonToneMapUnsupported, wantCalls: 1},
		{name: "hardware-only plan with a missing decoder", accel: "vaapi", toneMap: true, results: []string{"Decoder not found"}, wantReason: reasonChapterExtractFailed, wantCalls: 1},
		{name: "hardware without a stream falls back", accel: "vaapi", results: []string{"Output file #0 does not contain any stream", ""}, wantCalls: 2},
		{name: "hardware killed while logging invalid data stops", accel: "qsv", results: []string{"signal: killed (Invalid data found when processing input)"}, wantReason: reasonDecodeInvalidData, wantCalls: 1},
		{name: "sdr retry after invalid data", accel: "nvenc", results: []string{"Invalid data found when processing input", ""}, wantCalls: 2},
		{name: "sdr retry fails twice", accel: "nvenc", results: []string{"Invalid data found when processing input", "signal: killed"}, wantReason: reasonChapterExtractFailed, wantCalls: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			data, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
				InputPath:      "/media/movie.mkv",
				SeekSeconds:    42.5,
				HWAccel:        tt.accel,
				HWDevice:       "/dev/dri/renderD128",
				ToneMap:        tt.toneMap,
				resolveHWAccel: func(_ context.Context, accel, _, _ string) string { return accel },
				RunFunc: func(context.Context, string, []string) ([]byte, error) {
					calls++
					if message := tt.results[calls-1]; message != "" {
						return nil, errors.New(message)
					}
					return []byte("frame"), nil
				},
			})
			if reason != tt.wantReason || calls != tt.wantCalls {
				t.Fatalf("ExtractFrame() reason %q after %d calls (error %v), want %q after %d", reason, calls, err, tt.wantReason, tt.wantCalls)
			}
			if (tt.wantReason == "") != (err == nil && string(data) == "frame") {
				t.Fatalf("ExtractFrame() = %q, %v, want a frame only without a reason", data, err)
			}
		})
	}
}

func TestExtractReasonForAttemptTimeouts(t *testing.T) {
	timeout := mediasample.AttemptError{Reason: mediasample.ReasonTimeout, Err: context.DeadlineExceeded}
	plan := extractPlan{attempts: extractAttempts("vaapi", false, false), accel: "vaapi"}
	if got := plan.reason(plan.attempts[0], timeout, false); got != reasonHWTimeout {
		t.Fatalf("hardware attempt timeout reason = %q, want %q", got, reasonHWTimeout)
	}
	if got := plan.reason(plan.attempts[1], timeout, true); got != reasonCPUTimeout {
		t.Fatalf("software attempt timeout reason = %q, want %q", got, reasonCPUTimeout)
	}
	canceled := mediasample.AttemptError{Reason: mediasample.ReasonCanceled, Err: context.Canceled}
	if got := plan.reason(plan.attempts[0], canceled, false); got != reasonChapterExtractFailed {
		t.Fatalf("canceled attempt reason = %q, want %q", got, reasonChapterExtractFailed)
	}
}
