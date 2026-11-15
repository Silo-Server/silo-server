package mediasample

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodesource/nodesourcetest"
	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

// A transcode node that reads its source through the core samples a relayed
// input (a loopback relay URL): chapter frames, trickplay sheets read through
// the relayed concat list, and the Matroska Cues a sheet plan starts from,
// read through the relay's reader. Each must give exactly what the same
// request gives on the file's path.
func TestRelayedInputSamplesLikeTheFileWithRealFFmpeg(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, caps := realFFmpeg(t)
	grays := []int{20, 60, 100, 140, 180, 220}
	var sources, inputs []string
	for i, gray := range grays {
		sources = append(sources, "-f", "lavfi", "-i", fmt.Sprintf("color=c=0x%02x%02x%02x:s=160x66:r=24:d=10", gray, gray, gray))
		inputs = append(inputs, fmt.Sprintf("[%d:v]", i))
	}
	dir := t.TempDir()
	clip := filepath.Join(dir, "clip.mkv")
	args := append([]string{"-hide_banner", "-loglevel", "error"}, sources...)
	args = append(args, "-filter_complex", strings.Join(inputs, "")+"concat=n=6:v=1:a=0,format=yuv420p[v]", "-map", "[v]",
		"-g", "48", "-keyint_min", "48", clip)
	if output, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
		t.Skipf("cannot generate the clip: %v: %s", err, output)
	}
	mp4 := filepath.Join(dir, "clip.mp4")
	if output, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-i", clip, "-c", "copy", mp4).CombinedOutput(); err != nil {
		t.Skipf("cannot remux the clip: %v: %s", err, output)
	}

	for _, input := range []string{clip, mp4} {
		core := nodesourcetest.NewCore(t, input)
		relayed := core.Open(t).Input()
		t.Run(filepath.Ext(input), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			run := func(req Request, workload processmetrics.Workload) Result {
				t.Helper()
				result, err := Runner{FFmpegPath: ffmpeg, Workload: workload}.Run(ctx, req)
				if err != nil {
					t.Fatalf("%s: %v", req.Input, err)
				}
				return result
			}
			// Chapter frame.
			image := Request{At: &At{Seconds: 25}, Images: &ImageOutput{Width: 80}, Threads: 1}
			local, remote := image, image
			local.Input, remote.Input = input, relayed
			got, want := run(remote, processmetrics.Thumbnail), run(local, processmetrics.Thumbnail)
			if len(got.Images) != 1 || len(want.Images) != 1 || !bytes.Equal(got.Images[0].JPEG, want.Images[0].JPEG) {
				t.Fatalf("chapter frame through the relay differs from the file's")
			}
			// Trickplay sheets, read through the list.
			sheets := Request{
				Samples: &Samples{Seconds: []float64{5, 15, 25, 35, 45, 55}},
				Sheets:  &SheetsOutput{TileWidth: 64, TileHeight: 26, Columns: 2, Rows: 2, Quality: 90},
				Threads: 1,
			}
			if err := caps.Require(sheets); err != nil {
				t.Skipf("ffmpeg cannot sample sheets: %v", err)
			}
			local, remote = sheets, sheets
			local.Input, remote.Input = input, relayed
			if err := caps.Require(remote); err != nil {
				t.Fatalf("this ffmpeg cannot read a relayed list: %v", err)
			}
			got, want = run(remote, processmetrics.Trickplay), run(local, processmetrics.Trickplay)
			if len(got.Sheets) != 2 || len(got.Sheets) != len(want.Sheets) || got.SheetFrames != want.SheetFrames {
				t.Fatalf("relayed sheets %d %+v, local %d %+v", len(got.Sheets), got.SheetFrames, len(want.Sheets), want.SheetFrames)
			}
			for i := range got.Sheets {
				if !bytes.Equal(got.Sheets[i].JPEG, want.Sheets[i].JPEG) {
					t.Fatalf("sheet %d through the relay differs from the file's", i)
				}
			}
		})
		if filepath.Ext(input) == ".mkv" {
			// The plan's keyframes come from the Cues, read through the
			// relay's reader rather than degrading to none.
			if got, want := matroskaKeyframes(t.Context(), relayed), matroskaKeyframes(t.Context(), input); len(want) == 0 ||
				fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("relayed Cues keyframes %v, file's %v", got, want)
			}
		}
		if core.Requests.Load() == 0 {
			t.Fatal("nothing was read through the core")
		}
	}
}
