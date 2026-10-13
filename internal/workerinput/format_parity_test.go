package workerinput

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCataloguedFLVThroughAdmittedInput(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is unavailable")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is unavailable")
	}
	fixture := filepath.Join(t.TempDir(), "supported.flv")
	if out, err := exec.CommandContext(t.Context(), ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=size=64x36:rate=2", "-t", "1", "-c:v", "flv1", "-f", "flv", fixture).CombinedOutput(); err != nil {
		t.Fatalf("generate FLV: %s: %v", out, err)
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"revision"`)
		http.ServeContent(w, r, "supported.flv", time.Time{}, bytes.NewReader(data))
	}))
	defer origin.Close()
	adapter, err := New(Options{LocalRoot: t.TempDir(), Origins: []string{origin.URL}, AllowHTTP: true, Allowed: func(string, uint64) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adapter.Close() }()
	handle, err := adapter.Open(t.Context(), Source{ID: "media", Revision: "r", URL: origin.URL + "/source", Grant: "g", ETag: `"revision"`, Size: int64(len(data)), WorkID: "work", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	ctx := WithContext(t.Context(), adapter)
	for _, tc := range []struct {
		name string
		tool string
		args func(string) []string
	}{
		{"decode", ffmpeg, func(input string) []string {
			return []string{"-v", "error", "-i", input, "-map", "0:v:0", "-f", "framemd5", "-"}
		}},
		{"probe", ffprobe, func(input string) []string {
			return []string{"-v", "error", "-count_frames", "-show_entries", "stream=codec_name,width,height,nb_read_frames", "-of", "json", input}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local, err := exec.CommandContext(t.Context(), tc.tool, tc.args(fixture)...).CombinedOutput()
			if err != nil {
				t.Fatalf("local: %s: %v", local, err)
			}
			remote, err := CommandContext(ctx, tc.tool, tc.args(handle.URL())...).CombinedOutput()
			if err != nil {
				t.Fatalf("admitted HTTP: %s: %v", remote, err)
			}
			if !bytes.Equal(local, remote) {
				t.Fatalf("FLV %s differs: local=%s admitted=%s", tc.name, local, remote)
			}
		})
	}
}
