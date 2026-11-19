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

// A node with no room for another whole read, or another demux of a source,
// answers the request busy before writing anything, instead of demuxing the
// source.
func TestServeExtractAnswersBusyWithoutADemux(t *testing.T) {
	readsBusy := &fakeEmbeddedSource{whole: ErrSubtitleReadsBusy, format: "matroska"}
	for _, tc := range []struct {
		name   string
		source *fakeEmbeddedSource
		opts   StreamExtractOpts
		// demuxesBusy takes every demux slot first.
		demuxesBusy bool
	}{
		{name: "whole text track, no room to read", source: readsBusy, opts: StreamExtractOpts{SourceCodec: "subrip"}},
		{name: "whole PGS track, no room to read", source: readsBusy, opts: StreamExtractOpts{SourceCodec: "hdmv_pgs_subtitle"}},
		{name: "whole track without a source, no room to demux", opts: StreamExtractOpts{SourceCodec: "hdmv_pgs_subtitle"}, demuxesBusy: true},
		{name: "text window, no room to demux", source: &fakeEmbeddedSource{window: embeddedsub.ErrNoCues},
			opts: StreamExtractOpts{SourceCodec: "subrip", SeekSeconds: 60, DurationSeconds: 60}, demuxesBusy: true},
		{name: "PGS window, no room to demux", source: &fakeEmbeddedSource{window: embeddedsub.ErrNoCues},
			opts: StreamExtractOpts{SourceCodec: "hdmv_pgs_subtitle", AllowWindow: true, SeekSeconds: 60, DurationSeconds: 60}, demuxesBusy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.demuxesBusy {
				occupyDemuxSlots(t)
			}
			c, source := newTestCache(t)
			if tc.source != nil {
				c.SetEmbeddedSource(tc.source)
			}
			var mu sync.Mutex
			var calls []StreamExtractOpts
			rec := httptest.NewRecorder()
			response := middleware.NewWrapResponseWriter(rec, 1)
			opts := tc.opts
			opts.InputPath, opts.Embedded = source, testEmbeddedRef
			err := c.ServeExtract(response, httptest.NewRequest(http.MethodGet, "/subtitle", nil), opts, fakeConvert(&calls, &mu, false))
			// An uncached PGS window still warms the track, in its warm slot.
			waitForCondition(t, func() bool { return len(c.warmSem) == 0 }, func() {})
			mu.Lock()
			defer mu.Unlock()
			demuxes := 0
			for _, call := range calls {
				if call.Packets == nil {
					demuxes++
				}
			}
			if !errors.Is(err, ErrSubtitleReadsBusy) || demuxes != 0 || rec.Body.Len() != 0 || response.Status() != 0 {
				t.Fatalf("err %v, %d demuxes, status %d, body %q", err, demuxes, response.Status(), rec.Body.String())
			}
		})
	}
}

// demuxBlocker is an extract whose demuxes of the source wait to be
// released, for holding the node's demux slots.
type demuxBlocker struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	running sync.WaitGroup
}

// maxDemuxesHeld is more demuxes than the node runs at once.
const maxDemuxesHeld = 64

// newDemuxBlocker returns a blocker its test releases by its end.
func newDemuxBlocker(t *testing.T) *demuxBlocker {
	b := &demuxBlocker{started: make(chan struct{}, maxDemuxesHeld), release: make(chan struct{})}
	t.Cleanup(b.finish)
	return b
}

// occupyDemuxSlots holds every demux slot of the process until the test ends.
func occupyDemuxSlots(t *testing.T) {
	t.Helper()
	c, source := newTestCache(t)
	c.SetEmbeddedSource(&fakeEmbeddedSource{whole: embeddedsub.ErrNoCues})
	newDemuxBlocker(t).fillDemuxSlots(t, c, source)
}

func (b *demuxBlocker) extract(ctx context.Context, opts StreamExtractOpts) error {
	if opts.Packets != nil {
		return errors.New("demuxBlocker converts no packets")
	}
	b.started <- struct{}{}
	select {
	case <-b.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	_, err := fmt.Fprintf(opts.Writer, "demuxed %d", opts.TrackIndex)
	return err
}

// finish releases the demuxes and waits for their requests.
func (b *demuxBlocker) finish() {
	b.once.Do(func() { close(b.release) })
	b.running.Wait()
}

// fillDemuxSlots requests whole tracks of source from c, from ordinal 0 on,
// each demuxed by b, until the node refuses one; the admitted requests wait
// in their demuxes for b.finish. It returns their results in order.
func (b *demuxBlocker) fillDemuxSlots(t *testing.T, c *SubtitleCache, source string) []chan trackResult {
	t.Helper()
	var running []chan trackResult
	for ordinal := range maxDemuxesHeld {
		done := make(chan trackResult, 1)
		b.running.Add(1)
		go func() {
			defer b.running.Done()
			done <- serveWholeTrack(context.Background(), c, source, ordinal, b.extract)
		}()
		select {
		case <-b.started:
			running = append(running, done)
		case r := <-done:
			if !errors.Is(r.err, ErrSubtitleReadsBusy) || r.status != 0 || len(running) == 0 {
				t.Fatalf("track %d after %d demuxes: %+v, want a busy refusal", ordinal, len(running), r)
			}
			return running
		}
	}
	t.Fatalf("%d demuxes ran at once; none was refused", len(running))
	return nil
}

type trackResult struct {
	body   string
	status int
	err    error
}

// serveWholeTrack requests the whole PGS track at ordinal of source from c,
// demuxing it with extract when c's source can't read it.
func serveWholeTrack(ctx context.Context, c *SubtitleCache, source string, ordinal int, extract SUPExtractFunc) trackResult {
	rec := httptest.NewRecorder()
	response := middleware.NewWrapResponseWriter(rec, 1)
	ref := EmbeddedSubtitleRef{MediaFileID: 7, StreamIndex: ordinal, Codec: "hdmv_pgs_subtitle"}
	err := c.ServeExtract(response, httptest.NewRequest(http.MethodGet, "/subtitle.sup", nil).WithContext(ctx),
		StreamExtractOpts{InputPath: source, TrackIndex: ordinal, SourceCodec: "hdmv_pgs_subtitle", Embedded: &ref}, extract)
	return trackResult{body: rec.Body.String(), status: response.Status(), err: err}
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

// slowWindowSource reads a window in delay, as storage with slow random
// reads does: the reporter's took 88 seconds.
type slowWindowSource struct {
	*fakeEmbeddedSource
	delay time.Duration
}

func (s *slowWindowSource) Window(ctx context.Context, path string, ref EmbeddedSubtitleRef, from, to time.Duration) (*SubtitlePacketInput, error) {
	select {
	case <-time.After(s.delay):
		return s.fakeEmbeddedSource.Window(ctx, path, ref, from, to)
	case <-ctx.Done():
		return nil, ctx.Err()
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
