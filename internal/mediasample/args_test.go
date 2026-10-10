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
			want: "-hide_banner -nostdin -loglevel repeat+info -ss 117.25 -i /media/show/episode.mkv -t 33 " +
				"-vn -sn -dn -af silencedetect=noise=-50dB:duration=0.33 -f null -",
		},
		{
			name: "fingerprint and silence in one run",
			req: Request{
				Input:  "/media/a.mkv",
				Window: &Window{StartSeconds: 1200.5, DurationSeconds: 90},
				Audio:  &AudioOutput{Fingerprint: true, Silence: &SilenceParams{NoiseDB: -60, MinSeconds: 1}},
			},
			want: "-hide_banner -nostdin -loglevel repeat+info -ss 1200.5 -i /media/a.mkv -t 90 " +
				"-vn -sn -dn -af silencedetect=noise=-60dB:duration=1 -ac 2 -f chromaprint -fp_format raw -",
		},
		{
			name: "credits tail pass",
			req: Request{
				Input:   "/media/show/episode.mkv",
				Window:  &Window{StartSeconds: 2250, DurationSeconds: 450, KeyframesOnly: true},
				Audio:   &AudioOutput{Fingerprint: true, Silence: &SilenceParams{NoiseDB: -50, MinSeconds: 0.5}},
				Stats:   &StatsOutput{CropWidth: 0.9, CropHeight: 0.8, Width: 480, BlackThresholds: []int{20, 26, 32}},
				Threads: 1,
			},
			want: "-hide_banner -nostdin -loglevel repeat+info -threads 1 -filter_threads 1 -skip_frame:v nokey " +
				"-ss 2250 -i /media/show/episode.mkv " +
				"-t 450 -vn -sn -dn -af silencedetect=noise=-50dB:duration=0.5 -ac 2 -f chromaprint -fp_format raw - " +
				"-t 450 -map 0:V:0 -an -sn -dn -vf crop=trunc(iw*0.9/2)*2:trunc(ih*0.8/2)*2,scale=480:-2:flags=area,format=yuv420p," +
				"blackframe=amount=0:threshold=20,blackframe=amount=0:threshold=26,blackframe=amount=0:threshold=32,signalstats,metadata=print " +
				"-f null -",
		},
		{
			name: "stats only",
			req: Request{
				Input:  "/media/a.mkv",
				Window: &Window{StartSeconds: 10.0006, DurationSeconds: 5},
				Stats:  &StatsOutput{CropWidth: 1, CropHeight: 0.75, Width: 320},
			},
			want: "-hide_banner -nostdin -loglevel repeat+info -ss 10.001 -i /media/a.mkv " +
				"-t 5 -map 0:V:0 -an -sn -dn -vf crop=trunc(iw*1/2)*2:trunc(ih*0.75/2)*2,scale=320:-2:flags=area,format=yuv420p,signalstats,metadata=print " +
				"-f null -",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.req.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			args, stdin, err := buildArgs(tt.req, Attempt{}, hardwareDecode{}, 0)
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
	args, _, err := buildArgs(req, Attempt{}, hardwareDecode{}, 0)
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
