package playback

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Bundled fonts live in the container header. Extraction must not wait for
// the media tail, which may be slow or temporarily unavailable on a remote mount.
func TestDumpFontAttachmentsDoesNotReadMediaTail(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	dir := t.TempDir()
	want := []byte("attached-font-bytes")
	font := filepath.Join(dir, "font.ttf")
	if err := os.WriteFile(font, want, 0600); err != nil {
		t.Fatal(err)
	}
	subtitle := filepath.Join(dir, "late.srt")
	if err := os.WriteFile(subtitle, []byte("1\n00:00:59,000 --> 00:01:00,000\nLate subtitle\n"), 0600); err != nil {
		t.Fatal(err)
	}
	video := []string{"-f", "lavfi", "-i", "testsrc2=s=64x64:r=10:d=60"}
	audio := []string{"-f", "lavfi", "-i", "anullsrc=r=8000:cl=mono:d=60"}
	for _, tc := range []struct {
		name   string
		inputs []string
	}{
		{"video", video},
		{"audio", audio},
		{"delayed_audio", append(append([]string{}, video...), "-itsoffset", "59", "-f", "lavfi", "-i", "anullsrc=r=8000:cl=mono:d=1")},
		{"sparse_subtitle", append(append([]string{}, video...), "-i", subtitle)},
		{"subtitle_only", []string{"-i", subtitle}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			media := filepath.Join(t.TempDir(), "media.mkv")
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			args := append([]string{"-v", "error"}, tc.inputs...)
			args = append(args, "-t", "60", "-c:v", "ffv1", "-c:a", "pcm_s16le", "-c:s", "srt", "-threads", "1", "-attach", font,
				"-metadata:s:t", "mimetype=application/x-truetype-font", media)
			cmd := exec.CommandContext(ctx, ffmpeg, args...)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("media fixture: %v %s", err, out)
			}
			streams, err := probeFontAttachmentStreams(ctx, media, ffprobePathFromFFmpeg(ffmpeg))
			if err != nil || len(streams) != 1 {
				t.Fatalf("font probe: %v, %d streams", err, len(streams))
			}
			data, err := os.ReadFile(media)
			if err != nil {
				t.Fatal(err)
			}
			// Supply enough media for FFmpeg's initial stream probe, then leave
			// the second half unavailable until the requesting process closes.
			// Subtitle-only files are small, so serve those completely.
			prefix := len(data) / 2
			if tc.name == "subtitle_only" {
				prefix = len(data)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", fmt.Sprint(len(data)))
				w.Header().Set("Content-Type", "video/x-matroska")
				_, _ = w.Write(data[:prefix])
				w.(http.Flusher).Flush()
				if prefix < len(data) {
					<-r.Context().Done()
				}
			}))
			defer server.Close()
			extractCtx, extractCancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer extractCancel()
			fonts, err := dumpFontAttachments(extractCtx, server.URL+"/media.mkv", ffmpeg, streams, maxSubtitleFontBytes)
			if err != nil {
				t.Fatalf("font extraction waited for unavailable media tail: %v", err)
			}
			if len(fonts) != 1 || !bytes.Equal(fonts[0].Data, want) || !strings.HasSuffix(fonts[0].Name, ".ttf") {
				t.Fatalf("header extraction changed attached font: %+v", fonts)
			}
		})
	}
}
