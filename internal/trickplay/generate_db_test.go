package trickplay

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

// TestGenerateEndToEndDB runs the whole path with a real ffmpeg, database,
// and filesystem store: a library opts in, reconcile queues its file, the
// service generates and publishes, and the published manifest's sheets show
// the clip's scenes in order.
func TestGenerateEndToEndDB(t *testing.T) {
	f := newFixture(t)
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	// Four 10 s gray scenes at 2.4:1, a keyframe every 2 s.
	grays := []int{30, 90, 150, 210}
	var args, inputs []string
	for i, gray := range grays {
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("color=c=0x%02x%02x%02x:s=960x400:r=24:d=10", gray, gray, gray))
		inputs = append(inputs, fmt.Sprintf("[%d:v]", i))
	}
	clip := t.TempDir() + "/clip.mkv"
	args = append([]string{"-hide_banner", "-loglevel", "error"}, args...)
	args = append(args, "-filter_complex", strings.Join(inputs, "")+"concat=n=4:v=1:a=0,format=yuv420p[v]", "-map", "[v]", "-g", "48", clip)
	if output, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
		t.Skipf("cannot generate the clip: %v: %s", err, output)
	}

	folder := f.library(t, "movies", true)
	file := f.file(t, folder, "e2e")
	f.exec(t, `UPDATE public.media_files SET file_path = $2, duration = 40, container = 'matroska',
		video_tracks = '[{"codec":"h264","width":960,"height":400,"aspect_ratio":"12:5","bit_depth":8}]'::jsonb WHERE id = $1`, file, clip)
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings := fakeSettings{WidthSetting: "160", IntervalSetting: "10", hwAccelSetting: "none", ffmpegPathSetting: ffmpeg}
	s := NewService(f.pool, store, settings, NewLocalExtractor(settings), "node-e2e")
	if _, ran, err := s.Reconcile(t.Context()); err != nil || !ran {
		t.Fatalf("reconcile: %t %v", ran, err)
	}
	job, err := s.queue.(*Repository).ClaimFile(t.Context(), file, s.owner, leaseDuration)
	if err != nil || job == nil {
		t.Fatalf("claim: %v %v", job, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	s.process(ctx, job)

	manifests, err := f.repo.Manifests(t.Context(), []int{file}, store.Identity())
	if err != nil {
		t.Fatal(err)
	}
	m, ok := manifests[file]
	if !ok {
		row, _ := f.row(t, file)
		t.Fatalf("nothing published: %+v", row)
	}
	if m.Width != 160 || m.Height != 66 || m.TileColumns != 10 || m.TileRows != 8 || m.ThumbnailCount != 4 || m.SheetCount != 1 || m.IntervalMS != 10000 {
		t.Fatalf("manifest %+v", m)
	}
	reader, _, err := store.Get(t.Context(), m.SheetKey(0))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(reader)
	_ = reader.Close()
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if size := img.Bounds().Size(); size != image.Pt(1600, 528) {
		t.Fatalf("sheet is %v, want the full 10x8 grid", size)
	}
	ycc := img.(*image.YCbCr)
	for i, gray := range grays {
		x, y := i*160+80, 33
		if got := int(ycc.Y[ycc.YOffset(x, y)]); got < gray-4 || got > gray+4 {
			t.Errorf("thumbnail %d luma %d, want %d", i, got, gray)
		}
	}
}
