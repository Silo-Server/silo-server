package mediasample

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workerinput"
	"github.com/Silo-Server/silo-server/internal/workersource"
)

// registeredSamplingInput exercises the actual private byte adapter and core
// range authority. Only that core authority opens the fixture during network
// sampling; this is a process-level integration test, not a mount isolation test.
func registeredSamplingInput(t *testing.T, path string) (context.Context, string, *atomic.Int64) {
	t.Helper()
	server := httptest.NewUnstartedServer(nil)
	var requests atomic.Int64
	origin := "http://" + server.Listener.Addr().String()
	authority, err := workersource.New(workersource.Options{
		Store: workersource.NewMemoryStore(), Origin: origin,
		Active: func(_ context.Context, node int, work string, generation uint64) bool {
			return node == 7 && work == "sampling" && generation == 1
		},
		PathApprover: func(_ context.Context, input, kind string) error {
			if input != path || kind != "media" {
				return workersource.ErrDenied
			}
			return nil
		},
		VerifyRequest: func(r *http.Request, _ int) error {
			if r.Header.Get("X-Test-Proof") != "node-proof" {
				return workersource.ErrDenied
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		authority.Handler().ServeHTTP(w, r)
	})
	server.Start()
	t.Cleanup(server.Close)
	source, err := authority.Acquire(t.Context(), 7, "sampling", 1, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := workerinput.New(workerinput.Options{
		Origins: []string{origin}, AllowHTTP: true,
		Allowed: func(work string, generation uint64) bool { return work == "sampling" && generation == 1 },
		Sign:    func(r *http.Request) error { r.Header.Set("X-Test-Proof", "node-proof"); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	ctx := workerinput.WithContext(t.Context(), adapter)
	handle, err := adapter.Open(ctx, workerinput.Source{
		ID: source.Ref.ID, Revision: source.Ref.Revision, URL: source.URL, Grant: source.Grant,
		ETag: source.ETag, Size: source.Size, WorkID: source.WorkID, Generation: source.Generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, handle.URL(), &requests
}

func samplingFixture(t *testing.T, ffmpeg, container string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture."+container)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=s=160x90:r=24:d=5",
		"-an", "-c:v", "libx264", "-threads", "1", "-preset", "ultrafast", "-g", "24", "-keyint_min", "24", "-sc_threshold", "0", "-bf", "2", "-output_ts_offset", "11.4", path}
	if output, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("generate sampling fixture: %v: %s", err, output)
	}
	return path
}

func TestRegisteredRemoteSamplesAndSheetsMatchLocalFrames(t *testing.T) {
	ffmpeg, _ := realFFmpeg(t)
	for _, container := range []string{"mkv", "ts"} {
		t.Run(container, func(t *testing.T) {
			path := samplingFixture(t, ffmpeg, container)
			ctx, input, requests := registeredSamplingInput(t, path)
			for _, sheets := range []bool{false, true} {
				t.Run(map[bool]string{false: "stats", true: "sheets"}[sheets], func(t *testing.T) {
					req := Request{Input: path, Samples: &Samples{Seconds: []float64{0.5, 1.5, 2.5, 3.5}}, Threads: 1}
					if sheets {
						req.Sheets = &SheetsOutput{TileWidth: 32, TileHeight: 18, Columns: 2, Rows: 2, Quality: 80, UseInputAspect: true}
					} else {
						req.Stats = &StatsOutput{Width: 80, CropWidth: 1, CropHeight: 1, BlackThresholds: []int{20}}
					}
					runner := Runner{FFmpegPath: ffmpeg}
					local, err := runner.Run(t.Context(), req)
					if err != nil {
						t.Fatalf("local sampling: %v", err)
					}
					req.Input = input
					remote, err := runner.Run(ctx, req)
					if err != nil {
						t.Fatalf("registered remote sampling: %v", err)
					}
					if !reflect.DeepEqual(local, remote) {
						t.Fatalf("remote frames/timing or sheets differ: local=%+v remote=%+v", local, remote)
					}
					if requests.Load() == 0 {
						t.Fatal("sampling did not fetch source over HTTP")
					}
				})
			}
		})
	}
}

func TestStandaloneSamplingRejectsUnregisteredInputBeforeExec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.mp4")
	if err := os.WriteFile(path, []byte("registered source"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, registered, _ := registeredSamplingInput(t, path)
	for _, input := range []string{"http://unapproved.example/movie.mkv", path, registered + "-other"} {
		var calls int
		runner := Runner{Exec: func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error { calls++; return nil }}
		req := Request{Input: input, Window: &Window{DurationSeconds: 1}, Stats: &StatsOutput{Width: 80, CropWidth: 1, CropHeight: 1, BlackThresholds: []int{20}}}
		if _, err := runner.Run(ctx, req); err == nil || calls != 0 {
			t.Fatalf("unregistered input %q reached executor: err=%v calls=%d", input, err, calls)
		}
	}
}

func TestGeneratedRemoteConcatRejectsOtherInputsAndNestedPlaylists(t *testing.T) {
	ffmpeg, _ := realFFmpeg(t)
	var unexpected atomic.Int64
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		unexpected.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer other.Close()
	for _, script := range []string{
		"ffconcat version 1.0\nfile '" + other.URL + "/unapproved.mkv'\n",
		"#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1,\n" + other.URL + "/unapproved.ts\n#EXT-X-ENDLIST\n",
	} {
		path := filepath.Join(t.TempDir(), "untrusted.mkv")
		if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, input, _ := registeredSamplingInput(t, path)
		list, err := buildConcatList(input, []float64{0}, 0)
		if err != nil {
			t.Fatal(err)
		}
		args := append(quietArgs("error"), concatInputArgs...)
		args = append(args, "-i", concatListInput, "-f", "null", "-")
		args, remoteList, err := prepareSourceInput(ctx, input, args, list)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(remoteList), "file:http:") || !strings.Contains(string(remoteList), "option protocol_whitelist http,tcp\n") {
			t.Fatalf("unsafe generated HTTP list: %s", remoteList)
		}
		for _, line := range strings.Split(string(remoteList), "\n") {
			if formats, ok := strings.CutPrefix(line, "option format_whitelist "); ok {
				for _, format := range strings.Split(formats, ",") {
					if format == "concat" || format == "hls" {
						t.Fatalf("untrusted child format %q was allowed", format)
					}
				}
			}
		}
		// These bytes are a media source, not the service's generated outer
		// concat list. Its nested playlist demuxer must remain forbidden.
		cmd := exec.CommandContext(ctx, ffmpeg, args...)
		cmd.Stdin = bytes.NewReader(remoteList)
		// Newer FFmpeg also refuses to recognize HLS on this nonce URL and
		// octet-stream MIME type. Older builds reach the format whitelist.
		if output, err := cmd.CombinedOutput(); err == nil || (!strings.Contains(string(output), "not on whitelist") && !strings.Contains(string(output), "Not detecting m3u8/hls")) {
			t.Fatalf("nested source playlist accepted or failed for unrelated reason: %v: %s", err, output)
		}
		injected := strings.Replace(string(list), "file 'file:"+input+"'", "file '"+other.URL+"/unapproved.mkv'", 1)
		if _, _, err := prepareSourceInput(ctx, input, args, []byte(injected)); err == nil {
			t.Fatal("sample list admitted a second source")
		}
	}
	if unexpected.Load() != 0 {
		t.Fatalf("nested playlist opened %d unapproved URLs", unexpected.Load())
	}
}
