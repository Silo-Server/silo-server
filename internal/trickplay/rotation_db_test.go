package trickplay

import (
	"bytes"
	"image"
	"image/jpeg"
	"io"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/mediaprobe"
)

func TestGenerateRotatedManifestEndToEndDB(t *testing.T) {
	f := newFixture(t)
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	base, clip := filepath.Join(dir, "base.mp4"), filepath.Join(dir, "portrait.mp4")
	run := func(args ...string) {
		t.Helper()
		if output, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
			t.Fatalf("generate rotated clip: %v: %s", err, output)
		}
	}
	run("-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i",
		"color=c=black:s=320x180:r=24:d=6,drawbox=x=110:y=40:w=100:h=100:color=white:t=fill",
		"-c:v", "libx264", "-g", "24", base)
	run("-hide_banner", "-loglevel", "error", "-display_rotation", "90", "-i", base, "-c", "copy", clip)
	track, err := mediaprobe.ProbePrimaryVideoTrack(t.Context(), "ffprobe", clip)
	if err != nil {
		t.Fatal(err)
	}
	// Existing catalog tracks describe the coded landscape geometry. The
	// execution-time probe must still account for the display matrix.
	if track.AspectRatio != "16:9" {
		t.Fatalf("fixture coded aspect %q", track.AspectRatio)
	}
	folder := f.library(t, "movies", true)
	file := f.file(t, folder, "rotated")
	f.exec(t, `UPDATE public.media_files SET file_path = $2, duration = 6, container = 'mp4',
		video_tracks = '[{"codec":"h264","width":320,"height":180,"aspect_ratio":"16:9","bit_depth":8}]'::jsonb WHERE id = $1`, file, clip)
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings := fakeSettings{WidthSetting: "160", IntervalSetting: "10", hwAccelSetting: "none", ffmpegPathSetting: ffmpeg}
	s := NewService(f.pool, store, settings, NewLocalExtractor(settings), "rotated-test")
	if _, ran, err := s.Reconcile(t.Context()); err != nil || !ran {
		t.Fatalf("reconcile: %v, %v", ran, err)
	}
	job, err := f.repo.ClaimFile(t.Context(), file, s.owner, leaseDuration)
	if err != nil || job == nil {
		t.Fatalf("claim: %v, %v", job, err)
	}
	s.process(t.Context(), job)
	reader := NewReader(f.pool, store, fakeURLs{})
	manifest, ok, err := reader.SignedManifest(t.Context(), file)
	if err != nil || !ok || manifest.Width != 160 || manifest.Height != 284 || len(manifest.SheetURLs) != 1 {
		t.Fatalf("signed rotated manifest = %+v, %v, %v", manifest, ok, err)
	}
	body, _, err := store.Get(t.Context(), manifest.SheetKey(0))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Size() != image.Pt(manifest.Width*manifest.TileColumns, manifest.Height*manifest.TileRows) {
		t.Fatalf("sheet dimensions %v disagree with manifest %+v", img.Bounds().Size(), manifest)
	}
	minX, minY, maxX, maxY := manifest.Width, manifest.Height, -1, -1
	for y := range manifest.Height {
		for x := range manifest.Width {
			r, g, b, _ := img.At(x, y).RGBA()
			if r > 50000 && g > 50000 && b > 50000 {
				minX, minY, maxX, maxY = min(minX, x), min(minY, y), max(maxX, x), max(maxY, y)
			}
		}
	}
	w, h := maxX-minX+1, maxY-minY+1
	if w < 80 || h < 80 || w-h < -2 || w-h > 2 {
		t.Fatalf("source square became %dx%d in the preview", w, h)
	}
}
