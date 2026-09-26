package mediasample

import (
	"reflect"
	"strings"
	"testing"
)

// The intro fingerprint cache depends on these exact argument lists; see
// docs/architecture/media-sampling.md before changing a snapshot.
func TestBuildArgsSnapshots(t *testing.T) {
	tests := []struct {
		name string
		req  Request
		want string
	}{
		{
			name: "intro fingerprint",
			req: Request{
				Input:   "/media/show/episode.mkv",
				Window:  &Window{StartSeconds: 0, DurationSeconds: 600},
				Audio:   &AudioOutput{Fingerprint: true},
				Threads: 1,
			},
			want: "-hide_banner -nostdin -loglevel warning -threads 1 -ss 0 -i /media/show/episode.mkv -t 600 " +
				"-vn -sn -dn -ac 2 -f chromaprint -fp_format raw -",
		},
		{
			name: "chapter silence",
			req: Request{
				Input:  "/media/show/episode.mkv",
				Window: &Window{StartSeconds: 117.25, DurationSeconds: 33.0004},
				Audio:  &AudioOutput{Silence: &SilenceParams{NoiseDB: -50, MinSeconds: 0.33}},
			},
			want: "-hide_banner -nostdin -loglevel info -ss 117.25 -i /media/show/episode.mkv -t 33 " +
				"-vn -sn -dn -af silencedetect=noise=-50dB:duration=0.33 -f null -",
		},
		{
			name: "fingerprint and silence in one run",
			req: Request{
				Input:  "/media/a.mkv",
				Window: &Window{StartSeconds: 1200.5, DurationSeconds: 90},
				Audio:  &AudioOutput{Fingerprint: true, Silence: &SilenceParams{NoiseDB: -60, MinSeconds: 1}},
			},
			want: "-hide_banner -nostdin -loglevel info -ss 1200.5 -i /media/a.mkv -t 90 " +
				"-vn -sn -dn -af silencedetect=noise=-60dB:duration=1 -ac 2 -f chromaprint -fp_format raw -",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.req.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			args, stdin, err := buildArgs(tt.req, Attempt{}, hardwareDecode{})
			if err != nil {
				t.Fatalf("buildArgs: %v", err)
			}
			if stdin != nil {
				t.Fatalf("stdin = %q, want none", stdin)
			}
			if want := strings.Fields(tt.want); !reflect.DeepEqual(args, want) {
				t.Fatalf("args\n got %q\nwant %q", args, want)
			}
		})
	}
}

func TestBuildArgsKeepsInputAsOneArgument(t *testing.T) {
	req := Request{
		Input:  "/media/My Show/S01E01 -t 5.mkv",
		Window: &Window{DurationSeconds: 10},
		Audio:  &AudioOutput{Fingerprint: true},
	}
	args, _, err := buildArgs(req, Attempt{}, hardwareDecode{})
	if err != nil {
		t.Fatal(err)
	}
	for i, arg := range args {
		if arg == "-i" {
			if args[i+1] != req.Input {
				t.Fatalf("input argument = %q, want %q", args[i+1], req.Input)
			}
			return
		}
	}
	t.Fatalf("no -i in %q", args)
}

func TestFormatSeconds(t *testing.T) {
	for seconds, want := range map[float64]string{0: "0", 600: "600", 0.33: "0.33", 117.25: "117.25", 33.0004: "33", 1.0006: "1.001"} {
		if got := formatSeconds(seconds); got != want {
			t.Errorf("formatSeconds(%v) = %q, want %q", seconds, got, want)
		}
	}
}
