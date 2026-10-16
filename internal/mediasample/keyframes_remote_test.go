package mediasample

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"image"
	"image/jpeg"
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

	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerinput"
	"github.com/Silo-Server/silo-server/internal/workersource"
)

type signedKeyframeInput struct {
	//nolint:containedctx // This admitted-reader fixture retains the owning context so reads observe cancellation.
	ctx       context.Context
	handle    *workerinput.Handle
	adapter   *workerinput.Adapter
	authority *workersource.Authority
	path      string
	scratch   string
	reads     atomic.Int64
	blocked   atomic.Bool
	entered   chan struct{}
	ended     chan struct{}
}

func newSignedKeyframeInput(t *testing.T, path string) *signedKeyframeInput {
	t.Helper()
	f := &signedKeyframeInput{path: path, scratch: t.TempDir(), entered: make(chan struct{}, 1), ended: make(chan struct{}, 1)}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	verifier := workeridentity.Verifier{Origin: origin, Domain: workeridentity.DomainSourceRequest, AllowHTTP: true, ResolveKey: func(int) (ed25519.PublicKey, error) { return public, nil }}
	f.authority, err = workersource.New(workersource.Options{
		Store: workersource.NewMemoryStore(), Origin: origin,
		Active: func(_ context.Context, node int, work string, generation uint64) bool {
			return node == 7 && work == "sheet-keyframes" && generation == 1
		},
		PathApprover: func(_ context.Context, input, kind string) error {
			if input != path || kind != "media" {
				return workersource.ErrDenied
			}
			return nil
		},
		VerifyRequest: verifier.Verify,
	})
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			f.reads.Add(1)
			if f.blocked.Load() {
				if err := verifier.Verify(r, 7); err != nil {
					t.Errorf("source request signature: %v", err)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				select {
				case f.entered <- struct{}{}:
				default:
				}
				<-r.Context().Done()
				select {
				case f.ended <- struct{}{}:
				default:
				}
				return
			}
		}
		f.authority.Handler().ServeHTTP(w, r)
	})
	server.Start()
	t.Cleanup(server.Close)
	f.adapter, err = workerinput.New(workerinput.Options{
		Origins: []string{origin}, AllowHTTP: true, LocalRoot: f.scratch,
		Allowed: func(work string, generation uint64) bool { return work == "sheet-keyframes" && generation == 1 },
		Sign: func(r *http.Request) error {
			return workeridentity.Sign(r, origin, workeridentity.DomainSourceRequest, private, 7, true)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.adapter.Close() })
	//nolint:fatcontext // This admitted-reader fixture retains the owning context so reads observe cancellation.
	f.ctx = workerinput.WithContext(t.Context(), f.adapter)
	f.handle = f.open(t)
	return f
}

func (f *signedKeyframeInput) open(t *testing.T) *workerinput.Handle {
	t.Helper()
	source, err := f.authority.Acquire(t.Context(), 7, "sheet-keyframes", 1, f.path, "media")
	if err != nil {
		t.Fatal(err)
	}
	handle, err := f.adapter.Open(f.ctx, workerinput.Source{
		ID: source.Ref.ID, Revision: source.Ref.Revision, URL: source.URL, Grant: source.Grant,
		ETag: source.ETag, Size: source.Size, WorkID: source.WorkID, Generation: source.Generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

// The same six shades as the local shared-keyframe regression have just two
// Cues, so four cells must share a keyframe and then be repaired. Both the
// local and signed HTTP runs must show each sample's shade, including when
// container timestamps start at 11.4 seconds.
func TestRegisteredRemoteSharedKeyframeSheetsMatchLocal(t *testing.T) {
	ffmpeg, caps := realFFmpeg(t)
	grays := []int{20, 60, 100, 140, 180, 220}
	var sources, inputs []string
	for i, gray := range grays {
		sources = append(sources, "-f", "lavfi", "-i", fmt.Sprintf("color=c=0x%02x%02x%02x:s=64x36:r=25:d=10", gray, gray, gray))
		inputs = append(inputs, fmt.Sprintf("[%d:v]", i))
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	clip := filepath.Join(t.TempDir(), "long-gop.mkv")
	args := append([]string{"-hide_banner", "-loglevel", "error"}, sources...)
	args = append(args, "-filter_complex", strings.Join(inputs, "")+"concat=n=6:v=1:a=0,format=yuv420p[v]", "-map", "[v]",
		"-c:v", "libx264", "-threads", "1", "-g", "1000", "-keyint_min", "1000", "-sc_threshold", "0", "-bf", "2", clip)
	if output, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("encode shared-keyframe fixture: %v: %s", err, output)
	}
	shifted := filepath.Join(t.TempDir(), "shifted.mkv")
	if output, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-i", clip, "-c", "copy", "-output_ts_offset", "11.4", shifted).CombinedOutput(); err != nil {
		t.Fatalf("shift fixture: %v: %s", err, output)
	}
	for name, path := range map[string]string{"matroska": clip, "shifted": shifted} {
		t.Run(name, func(t *testing.T) {
			req := Request{Input: path, Samples: &Samples{Seconds: secondsFrom(5, 10, 6)},
				Sheets: &SheetsOutput{TileWidth: 64, TileHeight: 36, Columns: 3, Rows: 2, Quality: 90}, Threads: 1}
			if err := caps.Require(req); err != nil {
				t.Skipf("ffmpeg cannot sample sheets: %v", err)
			}
			runner := Runner{FFmpegPath: ffmpeg}
			local, err := runner.Run(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if local.SheetFrames != (SheetFrames{Decoded: 6, Refined: 4}) {
				t.Fatalf("local fixture did not repair four shared keyframes: %+v", local.SheetFrames)
			}
			f := newSignedKeyframeInput(t, path)
			req.Input = f.handle.URL()
			remote, err := runner.Run(f.ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if f.reads.Load() == 0 || len(remote.Sheets) != 1 {
				t.Fatalf("source reads=%d sheets=%d", f.reads.Load(), len(remote.Sheets))
			}
			img, err := jpeg.Decode(bytes.NewReader(remote.Sheets[0].JPEG))
			if err != nil {
				t.Fatal(err)
			}
			ycc, ok := img.(*image.YCbCr)
			if !ok {
				t.Fatalf("unexpected sheet image %T", img)
			}
			for cell, gray := range grays {
				x, y := (cell%3)*64+32, (cell/3)*36+18
				if got := int(ycc.Y[ycc.YOffset(x, y)]); got < gray-6 || got > gray+6 {
					t.Errorf("remote cell %d luma %d, want %d", cell, got, gray)
				}
			}
			if !reflect.DeepEqual(local, remote) {
				t.Errorf("local/remote sheets differ: local=%+v remote=%+v", local.SheetFrames, remote.SheetFrames)
			}
		})
	}
}

func TestRegisteredSheetKeyframesRequireCurrentSource(t *testing.T) {
	ffmpeg, _ := realFFmpeg(t)
	path := samplingFixture(t, ffmpeg, "mkv")
	f := newSignedKeyframeInput(t, path)
	want := matroskaKeyframes(t.Context(), path)
	if len(want) == 0 {
		t.Fatal("fixture has no keyframes")
	}
	for range 2 {
		if got := matroskaKeyframes(f.ctx, f.handle.URL()); !reflect.DeepEqual(got, want) {
			t.Fatalf("registered keyframes=%v want=%v", got, want)
		}
	}
	before := f.reads.Load()
	for _, input := range []string{path, f.handle.URL() + "-unregistered", "http://unapproved.example/movie.mkv"} {
		if got := matroskaKeyframes(f.ctx, input); got != nil {
			t.Errorf("unapproved input produced keyframes: %v", got)
		}
	}
	if f.reads.Load() != before {
		t.Fatal("unapproved input made a source request")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	owned := filepath.Join(f.scratch, "owned.mkv")
	if err := os.WriteFile(owned, data, 0600); err != nil {
		t.Fatal(err)
	}
	if got := matroskaKeyframes(f.ctx, owned); !reflect.DeepEqual(got, want) {
		t.Fatalf("owned scratch keyframes=%v want=%v", got, want)
	}
	if err := f.handle.Close(); err != nil {
		t.Fatal(err)
	}
	if got := matroskaKeyframes(f.ctx, f.handle.URL()); got != nil || f.reads.Load() != before {
		t.Fatal("closed input returned keyframes or started another read")
	}
	for name, data := range map[string][]byte{"not-matroska": []byte("not media"), "truncated-matroska": {0x1a, 0x45, 0xdf, 0xa3, 0x81, 0x00}} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fixture.mkv")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			f := newSignedKeyframeInput(t, path)
			if got := matroskaKeyframes(f.ctx, f.handle.URL()); got != nil || f.reads.Load() == 0 {
				t.Fatalf("fallback keyframes=%v reads=%d", got, f.reads.Load())
			}
			if err := f.handle.Validate(f.ctx); err != nil {
				t.Fatalf("parser fallback retired source: %v", err)
			}
		})
	}
}

func TestRegisteredSheetKeyframeLookupCancellationReleasesReaders(t *testing.T) {
	ffmpeg, _ := realFFmpeg(t)
	path := samplingFixture(t, ffmpeg, "mkv")
	for _, retire := range []bool{false, true} {
		t.Run(map[bool]string{false: "caller-cancel", true: "source-retired"}[retire], func(t *testing.T) {
			f := newSignedKeyframeInput(t, path)
			f.blocked.Store(true)
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			done := make(chan []float64, 1)
			go func() {
				done <- sheetKeyframes(ctx, Request{Input: f.handle.URL(), Samples: &Samples{Seconds: []float64{1}}, Sheets: &SheetsOutput{}})
			}()
			select {
			case <-f.entered:
			case got := <-done:
				t.Fatalf("lookup skipped admitted source: %v", got)
			case <-time.After(3 * time.Second):
				t.Fatal("lookup did not read signed source")
			}
			if retire {
				if err := f.handle.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			select {
			case got := <-done:
				if got != nil {
					t.Fatalf("canceled lookup returned keyframes: %v", got)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("lookup outlived cancellation")
			}
			select {
			case <-f.ended:
			case <-time.After(3 * time.Second):
				t.Fatal("source read outlived cancellation")
			}
			if !retire {
				if err := f.handle.Validate(f.ctx); err != nil {
					t.Fatalf("caller cancellation retired source: %v", err)
				}
			}
			f.blocked.Store(false)
			handle := f.open(t)
			assertKeyframeReadersReleased(t, handle)
			if got := matroskaKeyframes(f.ctx, handle.URL()); len(got) == 0 {
				t.Fatal("fresh lookup did not recover")
			}
		})
	}
}

func assertKeyframeReadersReleased(t *testing.T, handle *workerinput.Handle) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var readers []*workerinput.RangeReader
		for range 4 {
			reader, err := handle.ReaderAt(ctx, workerinput.ReaderLimits{})
			if err != nil {
				break
			}
			readers = append(readers, reader)
		}
		for _, reader := range readers {
			_ = reader.Close()
		}
		if len(readers) == 4 && len(keyframeLookups) == 0 {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("lookup retained a reader or lookup slot")
		}
	}
}

func TestRegisteredSheetKeyframeRetirementCannotReadURLNamedFile(t *testing.T) {
	ffmpeg, _ := realFFmpeg(t)
	fixture := samplingFixture(t, ffmpeg, "mkv")
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var armed atomic.Bool
	var calls atomic.Int64
	adapter, err := workerinput.New(workerinput.Options{
		Origins: []string{"http://127.0.0.1:1"}, AllowHTTP: true,
		Allowed: func(string, uint64) bool {
			if !armed.Load() {
				return true
			}
			return calls.Add(1) == 1
		},
		Sign: func(*http.Request) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	ctx := workerinput.WithContext(t.Context(), adapter)
	handle, err := adapter.Open(ctx, workerinput.Source{
		ID: "source", Revision: "revision", URL: "http://127.0.0.1:1/source", Grant: "grant",
		ETag: "etag", Size: int64(len(data)), WorkID: "work", Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// On Unix the opaque HTTP URL is also a relative filesystem spelling.
	// Keep that entire path in a temporary directory, outside owned scratch.
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(handle.URL()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(handle.URL(), data, 0600); err != nil {
		t.Fatal(err)
	}
	armed.Store(true)
	if got := matroskaKeyframes(ctx, handle.URL()); len(got) != 0 {
		t.Fatalf("retired input read URL-named local file: keyframes=%v authority checks=%d", got, calls.Load())
	}
	if calls.Load() < 2 {
		t.Fatal("authority transition was not exercised")
	}
	// Ordinary server execution retains its relative local-file behavior.
	if err := os.WriteFile("local.mkv", data, 0600); err != nil {
		t.Fatal(err)
	}
	if got := matroskaKeyframes(t.Context(), "local.mkv"); len(got) == 0 {
		t.Fatal("ordinary relative local input lost its keyframes")
	}
}
