package playback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Silo-Server/silo-server/internal/embeddedsub"
)

// fakeEmbeddedSource stands in for keptsub.Store.
type fakeEmbeddedSource struct {
	mu          sync.Mutex
	whole       error // WholeTrack's error; nil returns a stream input
	window      error
	format      string
	wholeCalls  int
	windows     [][2]time.Duration
	keptCopy    chan string
	keptCopyErr error
}

func (f *fakeEmbeddedSource) WholeTrack(context.Context, string, EmbeddedSubtitleRef) (*SubtitlePacketInput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wholeCalls++
	if f.whole != nil {
		return nil, f.whole
	}
	return &SubtitlePacketInput{StreamFormat: "matroska", Stream: func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("kept")), nil
	}}, nil
}

func (f *fakeEmbeddedSource) Window(_ context.Context, _ string, _ EmbeddedSubtitleRef, from, to time.Duration) (*SubtitlePacketInput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.windows = append(f.windows, [2]time.Duration{from, to})
	if f.window != nil {
		return nil, f.window
	}
	return &SubtitlePacketInput{StreamFormat: "matroska", Stream: func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("window")), nil
	}}, nil
}

func (f *fakeEmbeddedSource) DemuxCopy(context.Context, string, EmbeddedSubtitleRef) (DemuxCopy, error) {
	if f.format == "" {
		return DemuxCopy{}, errors.New("not kept")
	}
	return DemuxCopy{Format: f.format, StartTime: time.Second}, nil
}

func (f *fakeEmbeddedSource) KeepDemuxed(_ context.Context, _ string, _ EmbeddedSubtitleRef, demuxCopy DemuxCopy, _ int64, _ time.Time) error {
	data, err := os.ReadFile(demuxCopy.Path)
	if err != nil {
		return err
	}
	if f.keptCopy != nil {
		f.keptCopy <- string(data)
	}
	return f.keptCopyErr
}

var testEmbeddedRef = &EmbeddedSubtitleRef{MediaFileID: 7, StreamIndex: 2, ContainerTrackID: "3", Codec: "subrip"}

// fakeConvert is an extract that writes "<source>:<what it read>": what a
// stream input holds, or "demux" for the source file, and copies "copy" to
// KeepPath when asked. failPackets fails a conversion of packets before it
// writes anything.
func fakeConvert(calls *[]StreamExtractOpts, mu *sync.Mutex, failPackets bool) SUPExtractFunc {
	return func(ctx context.Context, opts StreamExtractOpts) error {
		mu.Lock()
		*calls = append(*calls, opts)
		mu.Unlock()
		if opts.Packets != nil {
			if failPackets {
				return errors.New("conversion failed")
			}
			r, err := opts.Packets.Stream(ctx)
			if err != nil {
				return err
			}
			data, _ := io.ReadAll(r)
			_, err = io.WriteString(opts.Writer, "converted:"+string(data))
			return err
		}
		if opts.KeepPath != "" {
			if err := os.WriteFile(opts.KeepPath, []byte("copy"), 0o600); err != nil {
				return err
			}
		}
		_, err := io.WriteString(opts.Writer, "demuxed")
		return err
	}
}

func serveText(t *testing.T, c *SubtitleCache, opts StreamExtractOpts, extract SUPExtractFunc) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := c.ServeExtract(rec, httptest.NewRequest(http.MethodGet, "/subtitle", nil), opts, extract); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestServeExtractReadsWholeTracksThroughTheSource(t *testing.T) {
	for _, tc := range []struct {
		name        string
		whole       error
		failPackets bool
		format      string
		// want is the body; demuxes counts extracts of the source, and
		// keptCopy what the demux copied for keeping ("" for none).
		want     string
		demuxes  int
		keptCopy string
	}{
		{name: "kept or index-read track converts", want: "converted:kept"},
		{name: "a track the source can't read is demuxed and kept", whole: errors.New("no cues"), format: "matroska", want: "demuxed", demuxes: 1, keptCopy: "copy"},
		{name: "a track the source can't keep is demuxed", whole: errors.New("no cues"), want: "demuxed", demuxes: 1},
		{name: "a failed conversion falls back to the demux", failPackets: true, format: "matroska", want: "demuxed", demuxes: 1, keptCopy: "copy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, source := newTestCache(t)
			src := &fakeEmbeddedSource{whole: tc.whole, format: tc.format, keptCopy: make(chan string, 1)}
			c.SetEmbeddedSource(src)
			var mu sync.Mutex
			var calls []StreamExtractOpts
			extract := fakeConvert(&calls, &mu, tc.failPackets)
			opts := StreamExtractOpts{InputPath: source, SourceCodec: "subrip", Embedded: testEmbeddedRef}
			for range 2 {
				if rec := serveText(t, c, opts, extract); rec.Body.String() != tc.want {
					t.Fatalf("body = %q, want %q", rec.Body.String(), tc.want)
				}
			}
			demuxes := 0
			for _, call := range calls {
				if call.Packets == nil {
					demuxes++
					if (call.KeepPath != "") != (tc.keptCopy != "") {
						t.Errorf("demux KeepPath = %q, want a copy: %v", call.KeepPath, tc.keptCopy != "")
					}
				}
			}
			// The second request is a cache hit.
			if demuxes != tc.demuxes || src.wholeCalls != 1 {
				t.Fatalf("%d demuxes and %d source reads, want %d and 1", demuxes, src.wholeCalls, tc.demuxes)
			}
			if tc.keptCopy != "" {
				select {
				case got := <-src.keptCopy:
					if got != tc.keptCopy {
						t.Fatalf("kept copy %q", got)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("the demuxed copy was never kept")
				}
			}
		})
	}
}

// A node with no room for another whole read answers the request busy
// before writing anything, instead of demuxing the source.
func TestServeExtractAnswersBusyWithoutADemux(t *testing.T) {
	for _, codec := range []string{"subrip", "hdmv_pgs_subtitle"} {
		t.Run(codec, func(t *testing.T) {
			c, source := newTestCache(t)
			c.SetEmbeddedSource(&fakeEmbeddedSource{whole: ErrSubtitleReadsBusy, format: "matroska"})
			var mu sync.Mutex
			var calls []StreamExtractOpts
			rec := httptest.NewRecorder()
			response := middleware.NewWrapResponseWriter(rec, 1)
			err := c.ServeExtract(response, httptest.NewRequest(http.MethodGet, "/subtitle", nil),
				StreamExtractOpts{InputPath: source, SourceCodec: codec, Embedded: testEmbeddedRef}, fakeConvert(&calls, &mu, false))
			if !errors.Is(err, ErrSubtitleReadsBusy) || len(calls) != 0 || rec.Body.Len() != 0 || response.Status() != 0 {
				t.Fatalf("err %v, %d extracts, status %d, body %q", err, len(calls), response.Status(), rec.Body.String())
			}
		})
	}
}

// FFmpeg can't demux an index-only track from the source, so a failed index
// read is the answer: no fallback demuxes it, with or without a source to
// read through.
func TestServeExtractNeverDemuxesAnIndexOnlyTrack(t *testing.T) {
	ref := *testEmbeddedRef
	ref.Codec, ref.IndexOnly = "webvtt", true
	for _, tc := range []struct {
		name   string
		source EmbeddedSubtitleSource
		opts   StreamExtractOpts
	}{
		{"whole track", &fakeEmbeddedSource{whole: embeddedsub.ErrNoCues, format: "matroska"}, StreamExtractOpts{}},
		{"window", &fakeEmbeddedSource{window: embeddedsub.ErrNoCues}, StreamExtractOpts{SeekSeconds: 30, DurationSeconds: 60}},
		{"no source", nil, StreamExtractOpts{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, source := newTestCache(t)
			if tc.source != nil {
				c.SetEmbeddedSource(tc.source)
			}
			opts := tc.opts
			opts.InputPath, opts.SourceCodec, opts.Embedded, opts.FFmpegPath = source, "webvtt", &ref, "ffmpeg-must-not-run"
			err := c.ServeExtract(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/subtitle", nil), opts, StreamExtractSubtitle)
			if !errors.Is(err, ErrSubtitleIndexOnly) {
				t.Fatalf("err = %v, want ErrSubtitleIndexOnly", err)
			}
		})
	}
}

// A demux whose kept copy fails runs again without it and doesn't send the
// viewer what it already has.
func TestServeExtractRetriesADemuxWhoseCopyFailed(t *testing.T) {
	c, source := newTestCache(t)
	c.SetEmbeddedSource(&fakeEmbeddedSource{whole: errors.New("no cues"), format: "matroska"})
	var keeps, plain int
	extract := func(_ context.Context, opts StreamExtractOpts) error {
		if _, err := io.WriteString(opts.Writer, "first cues, "); err != nil {
			return err
		}
		if opts.KeepPath != "" {
			keeps++
			return errors.New("kept copy: no space left on device")
		}
		plain++
		_, err := io.WriteString(opts.Writer, "rest")
		return err
	}
	rec := serveText(t, c, StreamExtractOpts{InputPath: source, SourceCodec: "subrip", Embedded: testEmbeddedRef}, extract)
	if rec.Body.String() != "first cues, rest" || keeps != 1 || plain != 1 {
		t.Fatalf("body %q after %d demuxes with a copy and %d without", rec.Body.String(), keeps, plain)
	}
}

// A real multi-output demux must finish the response even when its optional
// kept output reaches its cutoff; that partial track must never be persisted.
func TestServeExtractDiscardsACappedDemuxCopy(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required to verify independent output cutoffs")
	}
	dir := t.TempDir()
	srt := filepath.Join(dir, "captions.srt")
	var script strings.Builder
	for i := range 48 {
		fmt.Fprintf(&script, "%d\n00:00:%02d,000 --> 00:00:%02d,900\n%s\n\n", i+1, i, i, strings.Repeat("x", 2048))
	}
	if err := os.WriteFile(srt, []byte(script.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"matroska", "mp4"} {
		t.Run(format, func(t *testing.T) {
			codec := "subrip"
			if format == keepFormatMP4 {
				codec = "mov_text"
			}
			path := filepath.Join(dir, "source."+format)
			if out, err := exec.CommandContext(t.Context(), ffmpeg, "-v", "error", "-i", srt, "-c:s", codec, "-f", format, path).CombinedOutput(); err != nil {
				t.Fatalf("make fixture: %v: %s", err, out)
			}
			var want bytes.Buffer
			opts := StreamExtractOpts{InputPath: path, SourceCodec: codec, FFmpegPath: ffmpeg, Writer: &want}
			if err := StreamExtractSubtitle(t.Context(), opts); err != nil {
				t.Fatal(err)
			}
			c, _ := newTestCache(t)
			src := &fakeEmbeddedSource{whole: embeddedsub.ErrNoCues, format: format, keptCopy: make(chan string, 1)}
			c.SetEmbeddedSource(src)
			opts.Embedded, opts.KeepMaxBytes = testEmbeddedRef, 4096
			var copyPath string
			var copySize int64
			calls := 0
			extract := func(ctx context.Context, opts StreamExtractOpts) error {
				calls++
				if err := StreamExtractSubtitle(ctx, opts); err != nil {
					return err
				}
				copyPath = opts.KeepPath
				if info, err := os.Stat(copyPath); err == nil {
					copySize = info.Size()
				}
				return nil
			}
			rec := serveText(t, c, opts, extract)
			if got := strings.TrimSpace(strings.TrimPrefix(rec.Body.String(), webVTTPrefix)); got != strings.TrimSpace(strings.TrimPrefix(want.String(), webVTTPrefix)) || calls != 1 {
				t.Fatalf("response differs from the full demux after %d extracts", calls)
			}
			// FFmpeg's -fs may overshoot by buffered output or a muxer trailer.
			// This fixture's small packets keep that below 16 KiB.
			if copySize < opts.KeepMaxBytes || copySize > 16<<10 {
				t.Fatalf("kept output = %d bytes, want a cutoff near %d", copySize, opts.KeepMaxBytes)
			}
			t.Logf("kept copy stopped at %d bytes; complete response %d bytes", copySize, rec.Body.Len())
			if _, err := os.Stat(copyPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("capped copy remains: %v", err)
			}
			select {
			case <-src.keptCopy:
				t.Fatal("capped track was offered for persistence")
			default:
			}
		})
	}
}

// Requests that don't name a track, such as the proxy's, demux as before.
func TestServeExtractWithoutATrackSkipsTheSource(t *testing.T) {
	c, source := newTestCache(t)
	src := &fakeEmbeddedSource{format: "matroska"}
	c.SetEmbeddedSource(src)
	var mu sync.Mutex
	var calls []StreamExtractOpts
	rec := serveText(t, c, StreamExtractOpts{InputPath: source, SourceCodec: "subrip"}, fakeConvert(&calls, &mu, false))
	if rec.Body.String() != "demuxed" || src.wholeCalls != 0 || calls[0].KeepPath != "" {
		t.Fatalf("body %q, %d source reads, keep %q", rec.Body.String(), src.wholeCalls, calls[0].KeepPath)
	}
}

func TestServeExtractWindowsFromTheSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		opts   StreamExtractOpts
		window error
		sup    bool
		from   time.Duration
		to     time.Duration
		want   string
	}{
		{name: "text window", opts: StreamExtractOpts{SourceCodec: "subrip", SeekSeconds: 600, DurationSeconds: 600}, from: 600 * time.Second, to: 1200 * time.Second, want: "converted:window"},
		{name: "text window to the end", opts: StreamExtractOpts{SourceCodec: "subrip", SeekSeconds: 90.5}, from: 90500 * time.Millisecond, to: embeddedsub.WholeTo, want: "converted:window"},
		{name: "PGS window", sup: true, opts: StreamExtractOpts{SourceCodec: "hdmv_pgs_subtitle", AllowWindow: true, DurationSeconds: 30}, from: embeddedsub.WholeFrom, to: 30 * time.Second, want: "converted:window"},
		{name: "a window the source can't read is demuxed", opts: StreamExtractOpts{SourceCodec: "subrip", SeekSeconds: 10, DurationSeconds: 5}, window: errors.New("no cues"), from: 10 * time.Second, to: 15 * time.Second, want: "demuxed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, source := newTestCache(t)
			src := &fakeEmbeddedSource{window: tc.window}
			c.SetEmbeddedSource(src)
			var mu sync.Mutex
			var calls []StreamExtractOpts
			opts := tc.opts
			opts.InputPath, opts.Embedded = source, testEmbeddedRef
			rec := serveText(t, c, opts, fakeConvert(&calls, &mu, false))
			if rec.Body.String() != tc.want {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.want)
			}
			if len(src.windows) != 1 || src.windows[0] != [2]time.Duration{tc.from, tc.to} {
				t.Fatalf("windows read = %v, want [%v %v]", src.windows, tc.from, tc.to)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q", got)
			}
			if tc.sup {
				// A PGS window answered from the source warms the whole
				// track in the background; wait for the warm to finish, so
				// its commit doesn't race the check or the temp dir cleanup.
				waitForCondition(t, func() bool { return len(c.warmSem) == 0 }, func() {})
				f, _, ok := c.lookup(source, 0, "sup")
				if !ok {
					t.Fatal("the warm did not cache the whole track")
				}
				data, _ := io.ReadAll(f)
				_ = f.Close()
				if string(data) != "converted:kept" {
					t.Fatalf("cached whole track = %q", data)
				}
				return
			}
			if f, _, ok := c.lookup(source, 0, "vtt"); ok {
				_ = f.Close()
				t.Fatal("a window was cached as the whole track")
			}
		})
	}
}

func TestExtractSourcedText(t *testing.T) {
	c, source := newTestCache(t)
	src := &fakeEmbeddedSource{whole: errors.New("no cues")}
	c.SetEmbeddedSource(src)
	if _, ok, err := c.ExtractSourcedText(t.Context(), source, testEmbeddedRef, 0, "srt", "ffmpeg"); ok || err != nil {
		t.Fatalf("a track the source can't read: ok %v, err %v", ok, err)
	}
	if _, ok, err := c.ExtractSourcedText(t.Context(), source, nil, 0, "srt", "ffmpeg"); ok || err != nil {
		t.Fatalf("no track: ok %v, err %v", ok, err)
	}
	fillTextEntry(t, c, source, 0, "srt", "cached")
	data, ok, err := c.ExtractSourcedText(t.Context(), source, testEmbeddedRef, 0, "srt", "ffmpeg")
	if !ok || err != nil || string(data) != "cached" {
		t.Fatalf("cached track: %q, ok %v, err %v", data, ok, err)
	}
}

func fillTextEntry(t *testing.T, c *SubtitleCache, source string, ordinal int, format, payload string) {
	t.Helper()
	fill := c.beginFill(source, ordinal, format)
	if fill == nil {
		t.Fatal("no fill")
	}
	if _, err := fill.Tee(io.Discard).Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if err := fill.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamExtractArgsKeepAnMP4CopyWithItsMoovFirst(t *testing.T) {
	opts := StreamExtractOpts{InputPath: "/media/in.mp4", SourceCodec: "mov_text", KeepPath: "/tmp/kept.part-2", KeepFormat: "mp4"}
	got := strings.Join(streamExtractArgs(opts), " ")
	if !strings.HasSuffix(got, "-c:s copy -output_ts_offset 0.000000 -flush_packets 1 -fs 134217728 -movflags +faststart -f mp4 file:/tmp/kept.part-2") {
		t.Fatalf("args %s", got)
	}
}

func TestStreamExtractArgsKeepACopy(t *testing.T) {
	opts := StreamExtractOpts{InputPath: "/media/in.mkv", TrackIndex: 2, SourceCodec: "subrip", KeepPath: "/tmp/kept.part-1", KeepFormat: "matroska", KeepOffset: 1500 * time.Millisecond}
	got := strings.Join(streamExtractArgs(opts), " ")
	want := "-hide_banner -nostats -loglevel error -y -i /media/in.mkv -map 0:s:2 -c:s webvtt -f webvtt pipe:1 -map 0:s:2 -c:s copy -output_ts_offset 1.500000 -flush_packets 1 -fs 134217728 -f matroska file:/tmp/kept.part-1"
	if got != want {
		t.Fatalf("args\n got %s\nwant %s", got, want)
	}
	// A window never keeps.
	opts.SeekSeconds = 30
	if got := strings.Join(streamExtractArgs(opts), " "); strings.Contains(got, "kept") || strings.Contains(got, " -y ") {
		t.Fatalf("window args keep a copy: %s", got)
	}
}

// The keepalive's ticks and the extract's writes and flushes reach the
// response writer one at a time: it isn't safe for concurrent use.
func TestPrefixKeepaliveSerializesTheWriter(t *testing.T) {
	w := &exclusiveWriter{}
	k := newPrefixKeepalive(w, "WEBVTT\n", time.Hour, time.Hour)
	defer k.stop()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 500 {
			k.tick()
		}
	}()
	go func() {
		defer wg.Done()
		for range 500 {
			k.Flush()
		}
	}()
	wg.Wait()
	if w.overlapped.Load() {
		t.Fatal("a tick and a flush reached the writer at once")
	}
}

// exclusiveWriter records calls that overlap; it is a flushing response
// writer that isn't safe for concurrent use.
type exclusiveWriter struct {
	inUse      atomic.Int32
	overlapped atomic.Bool
	calls      int // unsynchronized, for the race detector
}

func (w *exclusiveWriter) enter() {
	if w.inUse.Add(1) > 1 {
		w.overlapped.Store(true)
	}
	w.calls++
	runtime.Gosched()
	w.inUse.Add(-1)
}

func (w *exclusiveWriter) Write(p []byte) (int, error) {
	w.enter()
	return len(p), nil
}

func (w *exclusiveWriter) Flush() { w.enter() }

func TestPrefixKeepalive(t *testing.T) {
	var out strings.Builder
	// The test sends the keepalive bytes itself.
	k := newPrefixKeepalive(&out, "WEBVTT\n", time.Hour, time.Hour)
	defer k.stop()
	for range 3 {
		k.tick()
	}
	if out.String() != "WEB" {
		t.Fatalf("sent ahead %q", out.String())
	}
	const converted = "WEBVTT\n\n00:00.500 --> 00:02.000\nAt the top\n"
	for _, part := range []string{"WE", "BVTT\n\n00:00.500", " --> 00:02.000\nAt the top\n"} {
		if _, err := k.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if k.tick(); out.String() != converted {
		t.Fatalf("output %q, want %q", out.String(), converted)
	}
	// A header that arrives before the timer sends all of it still needs
	// heartbeats until cue output follows, including across partial writes.
	for _, sent := range []int{0, 2} {
		for name, parts := range map[string][]string{"whole": {webVTTPrefix}, "split": {"WE", "BV", "TT\n"}} {
			t.Run(name, func(t *testing.T) {
				var early strings.Builder
				k := newPrefixKeepalive(&early, webVTTPrefix, time.Hour, time.Hour)
				defer k.stop()
				for range sent {
					k.tick()
				}
				for i, part := range parts {
					if _, err := k.Write([]byte(part)); err != nil {
						t.Fatal(err)
					}
					if i+1 < len(parts) {
						k.tick()
					}
				}
				if !k.tick() || early.String() != webVTTPrefix+"\n" {
					t.Fatalf("header after %d timer bytes in %q: %q", sent, parts, early.String())
				}
				if _, err := k.Write([]byte(converted[len(webVTTPrefix):])); err != nil {
					t.Fatal(err)
				}
				more := k.tick()
				if more || early.String() != webVTTPrefix+"\n"+converted[len(webVTTPrefix):] {
					t.Fatalf("cue output after early header: %q", early.String())
				}
			})
		}
	}
	// A wait past the whole header goes on with blank lines, also while the
	// output has repeated only the header, as a demux of a sparse track does
	// before its first cue; the cues follow unchanged.
	out.Reset()
	k = newPrefixKeepalive(&out, "WEBVTT\n", time.Hour, time.Hour)
	defer k.stop()
	for range len("WEBVTT\n") + 1 {
		k.tick()
	}
	if _, err := k.Write([]byte("WEBVTT\n")); err != nil {
		t.Fatal(err)
	}
	k.tick()
	if _, err := k.Write([]byte(converted[len("WEBVTT\n"):])); err != nil {
		t.Fatal(err)
	}
	if k.tick(); out.String() != "WEBVTT\n\n\n"+converted[len("WEBVTT\n"):] {
		t.Fatalf("output after a long wait %q", out.String())
	}
	// Output that doesn't start with what went ahead fails.
	out.Reset()
	k = newPrefixKeepalive(&out, "WEBVTT\n", time.Hour, time.Hour)
	defer k.stop()
	k.tick()
	if _, err := k.Write([]byte("1\n00:00")); !errors.Is(err, errKeepalivePrefix) {
		t.Fatalf("mismatched output: %v", err)
	}
}

// waitForCondition polls cond, then runs then once it holds.
func waitForCondition(t *testing.T, cond func() bool, then func()) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond) //nolint:forbidigo // polls the observable state cond reads
	}
	then()
}

// A slow whole read gets the WebVTT header ahead of it a byte at a time, and
// the response carries the converted cues after any keepalive blank lines.
func TestServeExtractSendsTheWebVTTHeaderAheadOfASlowRead(t *testing.T) {
	delay, interval := keepaliveDelay, keepaliveInterval
	keepaliveDelay, keepaliveInterval = time.Millisecond, time.Millisecond
	defer func() { keepaliveDelay, keepaliveInterval = delay, interval }()
	c, source := newTestCache(t)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	c.SetEmbeddedSource(&slowEmbeddedSource{fakeEmbeddedSource: &fakeEmbeddedSource{}, release: release})
	const converted = "WEBVTT\n\n00:00.500 --> 00:02.000\nAt the top\n"
	extract := func(_ context.Context, opts StreamExtractOpts) error {
		_, err := io.WriteString(opts.Writer, converted)
		return err
	}
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	var serveErr error
	go func() {
		defer close(done)
		serveErr = c.ServeExtract(rec, httptest.NewRequest(http.MethodGet, "/subtitle", nil), StreamExtractOpts{InputPath: source, SourceCodec: "subrip", Embedded: testEmbeddedRef}, extract)
	}()
	defer func() {
		unblock()
		<-done
	}()
	// Release the read once the whole header has gone ahead. A failed wait
	// must still release the source and join the request before cleanup.
	waitForCondition(t, func() bool {
		fill := inflightFill(c)
		if fill == nil {
			return false
		}
		fill.mu.Lock()
		defer fill.mu.Unlock()
		return fill.written >= int64(len(webVTTPrefix))
	}, unblock)
	<-done
	if serveErr != nil {
		t.Fatal(serveErr)
	}
	withoutBlankLines := func(body string) string {
		return webVTTPrefix + "\n" + strings.TrimLeft(strings.TrimPrefix(body, webVTTPrefix), "\n")
	}
	if withoutBlankLines(rec.Body.String()) != converted {
		t.Fatalf("body %q, want %q", rec.Body.String(), converted)
	}
	if f, _, ok := c.lookup(source, 0, "vtt"); !ok {
		t.Fatal("track not cached")
	} else {
		data, _ := io.ReadAll(f)
		_ = f.Close()
		if withoutBlankLines(string(data)) != converted {
			t.Fatalf("cached %q", data)
		}
	}
}

// slowEmbeddedSource holds WholeTrack until release closes.
type slowEmbeddedSource struct {
	*fakeEmbeddedSource
	release chan struct{}
}

func (s *slowEmbeddedSource) WholeTrack(ctx context.Context, path string, ref EmbeddedSubtitleRef) (*SubtitlePacketInput, error) {
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.fakeEmbeddedSource.WholeTrack(ctx, path, ref)
}

// A conversion of a track with packets that yields no cue, as FFmpeg does
// when it can't read its input yet exits cleanly, isn't served or cached.
func TestServeExtractRejectsAnEmptyConversionOfATrackWithPackets(t *testing.T) {
	c, source := newTestCache(t)
	c.SetEmbeddedSource(&cuesExpectedSource{fakeEmbeddedSource: &fakeEmbeddedSource{format: "matroska"}})
	extract := func(_ context.Context, opts StreamExtractOpts) error {
		if opts.Packets != nil {
			_, err := io.WriteString(opts.Writer, "WEBVTT\n")
			return err
		}
		_, err := io.WriteString(opts.Writer, "WEBVTT\n\n00:01.000 --> 00:02.000\ndemuxed\n")
		return err
	}
	opts := StreamExtractOpts{InputPath: source, SourceCodec: "subrip", Embedded: testEmbeddedRef}
	for range 2 {
		if body := serveText(t, c, opts, extract).Body.String(); !strings.Contains(body, "demuxed") {
			t.Fatalf("body %q", body)
		}
	}
	if _, ok, err := c.ExtractSourcedText(t.Context(), source, testEmbeddedRef, 1, "srt", "/bin/false"); ok || err != nil {
		t.Fatalf("an empty sourced text was used: ok %v, %v", ok, err)
	}
}

// cuesExpectedSource returns whole tracks known to hold packets.
type cuesExpectedSource struct{ *fakeEmbeddedSource }

func (s *cuesExpectedSource) WholeTrack(ctx context.Context, path string, ref EmbeddedSubtitleRef) (*SubtitlePacketInput, error) {
	in, err := s.fakeEmbeddedSource.WholeTrack(ctx, path, ref)
	if in != nil {
		in.ExpectCues = true
	}
	return in, err
}

// An open-ended PGS window comes from a cached .sup before the source, which
// would read the track whole; a bounded one from the source, which also
// warms the .sup when it isn't cached.
func TestServeSUPWindowOrder(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cached     bool
		duration   float64
		wantSource bool
		wantWarm   bool
	}{
		{name: "open-ended, cached", cached: true},
		{name: "bounded, cached", cached: true, duration: 30, wantSource: true},
		{name: "bounded, not cached", duration: 30, wantSource: true, wantWarm: true},
		{name: "open-ended, not cached", wantSource: true, wantWarm: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, source := newTestCache(t)
			if tc.cached {
				fillEntry(t, c, source, 0, "PG cached")
			}
			src := &fakeEmbeddedSource{}
			c.SetEmbeddedSource(src)
			warmed := make(chan struct{}, 1)
			extract := func(_ context.Context, opts StreamExtractOpts) error {
				if !opts.AllowWindow && opts.Packets != nil {
					warmed <- struct{}{}
				}
				_, err := io.WriteString(opts.Writer, "PG")
				return err
			}
			opts := StreamExtractOpts{InputPath: source, SourceCodec: "hdmv_pgs_subtitle", AllowWindow: true, SeekSeconds: 60, DurationSeconds: tc.duration, Embedded: testEmbeddedRef}
			rec := httptest.NewRecorder()
			if err := c.ServeSUPExtract(rec, httptest.NewRequest(http.MethodGet, "/sub.sup", nil), opts, extract); err != nil {
				t.Fatal(err)
			}
			src.mu.Lock()
			fromSource := len(src.windows) == 1
			src.mu.Unlock()
			if fromSource != tc.wantSource {
				t.Fatalf("served from the source: %v, want %v", fromSource, tc.wantSource)
			}
			if tc.wantWarm {
				select {
				case <-warmed:
				case <-time.After(5 * time.Second):
					t.Fatal("no warm after a window from the source")
				}
			}
		})
	}
}

// The warm demuxes the tracks the source can't read in the slot it holds.
func TestWarmEmbeddedTextTracksKeepsItsSlotForTheBatch(t *testing.T) {
	c, source := newTestCache(t)
	c.SetEmbeddedSource(&fakeEmbeddedSource{whole: errors.New("no cues")})
	// Every other slot is busy.
	for len(c.warmSem) < cap(c.warmSem)-1 {
		c.warmSem <- struct{}{}
	}
	batched := make(chan []TextSubtitleOutput, 1)
	c.WarmEmbeddedTextTracks(source, []TextSubtitleTrack{{Ordinal: 0, Format: "srt"}}, func(int) *EmbeddedSubtitleRef { return testEmbeddedRef },
		func(_ context.Context, _ string, outputs []TextSubtitleOutput) error {
			if len(c.warmSem) != cap(c.warmSem) {
				t.Error("the batch ran without the warm's slot")
			}
			for _, o := range outputs {
				if err := os.WriteFile(o.Path, []byte("1\n00:00:01,000 --> 00:00:02,000\nx\n"), 0o600); err != nil {
					return err
				}
			}
			batched <- outputs
			return nil
		}, "ffmpeg")
	select {
	case outputs := <-batched:
		if len(outputs) != 1 {
			t.Fatalf("batch outputs %+v", outputs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the batch never ran")
	}
}

type timedFallbackSource struct{ *fakeEmbeddedSource }

func (s *timedFallbackSource) WholeTrack(context.Context, string, EmbeddedSubtitleRef) (*SubtitlePacketInput, error) {
	time.Sleep(90 * time.Second) //nolint:forbidigo // synctest advances virtual time through the foreground wait
	return nil, errors.New("whole track still being read")
}

type progressWriter struct {
	last       time.Time
	longestGap time.Duration
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.longestGap = max(w.longestGap, time.Since(w.last))
	w.last = time.Now()
	return len(p), nil
}

// The source's foreground wait can expire before a demux finishes. A
// client with a 30-second read timeout still needs progress in that gap.
func TestKeepaliveCoversSlowWholeReadAndFallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, source := newTestCache(t)
		c.SetEmbeddedSource(&timedFallbackSource{fakeEmbeddedSource: &fakeEmbeddedSource{}})
		writer := &progressWriter{last: time.Now()}
		extract := func(_ context.Context, opts StreamExtractOpts) error {
			time.Sleep(40 * time.Second) //nolint:forbidigo // synctest models fallback latency beyond the read timeout
			_, err := io.WriteString(opts.Writer, "WEBVTT\n\n00:01.000 --> 00:02.000\nvisible\n")
			return err
		}
		err := c.sourced(extract)(t.Context(), StreamExtractOpts{
			InputPath: source, SourceCodec: "subrip", Embedded: testEmbeddedRef, Writer: writer,
		})
		if err != nil {
			t.Fatal(err)
		}
		if writer.longestGap > 30*time.Second {
			t.Fatalf("valid extraction left the viewer without bytes for %v; a 30-second read timeout expires", writer.longestGap)
		}
	})
}

// A WebVTT or SubRip cue counts only with text after its timing line.
func TestHasCueNeedsText(t *testing.T) {
	for doc, want := range map[string]bool{
		"WEBVTT\n":                                     false,
		"WEBVTT\n\n00:01.000 --> 00:02.000\n":          false,
		"WEBVTT\n\n00:01.000 --> 00:02.000\n\n":        false,
		"WEBVTT\n\n00:01.000 --> 00:02.000\n  \n":      false,
		"WEBVTT\n\n00:01.000 --> 00:02.000\nHello\n":   true,
		"1\r\n00:00:01,000 --> 00:00:02,000\r\nHi\r\n": true,
	} {
		if got := hasCue(subtitleMuxerWebVTT, []byte(doc)); got != want {
			t.Errorf("hasCue(%q) = %v, want %v", doc, got, want)
		}
	}
}
