package keptsub

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// serve asks the cache for a subtitle the way the stream handler does.
func serve(t *testing.T, c *playback.SubtitleCache, opts playback.StreamExtractOpts) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := c.ServeExtract(rec, httptest.NewRequest(http.MethodGet, "/subtitle", nil), opts, playback.StreamExtractSubtitle); err != nil {
		t.Fatal(err)
	}
	return rec
}

// The subtitle cache with the store behind it, end to end: a whole track
// comes from the local cache, else the kept track, else a read through the
// index (kept), else a demux (kept); windows from the kept track or the
// index. Every answer is today's bytes.
func TestSubtitleCacheServesThroughTheStore(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	for _, cues := range []bool{true, false} {
		name := map[bool]string{true: "with cues", false: "without cues"}[cues]
		t.Run(name, func(t *testing.T) {
			path, file := fixtureFile(t, ffmpeg, cues)
			s, repo, _ := newTestStore(t, ffmpeg)
			transcode := t.TempDir()
			cache := playback.NewSubtitleCache(func() string { return transcode })
			cache.SetEmbeddedSource(s)
			opts := playback.StreamExtractOpts{InputPath: path, SourceCodec: "subrip", FFmpegPath: ffmpeg, Embedded: playback.EmbeddedSubtitleRefFor(file, 0)}
			want := demux(t, ffmpeg, path, 0, 0, 0)

			// (c) or (d): the first request reads the track and keeps it.
			if got := serve(t, cache, opts).Body.String(); got != want {
				t.Fatalf("first request %q, want %q", got, want)
			}
			waitFor(t, "the track to be kept", func() bool {
				tr, _ := repo.Track(t.Context(), file.ID, 1)
				return tr != nil
			})
			kept, _ := repo.Track(t.Context(), file.ID, 1)
			if wantMethod := map[bool]string{true: MethodCues, false: MethodFFmpeg}[cues]; kept.Method != wantMethod {
				t.Fatalf("kept by %s, want %s", kept.Method, wantMethod)
			}
			// (a): the local cache.
			rec := serve(t, cache, opts)
			if rec.Body.String() != want || rec.Header().Get("Cache-Control") != "private, no-cache" {
				t.Fatalf("cache hit %q (%s)", rec.Body.String(), rec.Header().Get("Cache-Control"))
			}
			// (b): another node, or an evicted cache, converts the kept track.
			if err := os.RemoveAll(filepath.Join(transcode, "subtitle-cache")); err != nil {
				t.Fatal(err)
			}
			_, claims := repo.counts()
			if got := serve(t, cache, opts).Body.String(); got != want {
				t.Fatalf("from the kept track %q, want %q", got, want)
			}
			if puts, again := repo.counts(); again != claims || puts != 1 {
				t.Fatalf("serving the kept track read the source again (%d reads, %d keeps)", again-claims, puts)
			}

			// A window comes from the kept track, holding the cue that shows.
			window := opts
			window.SeekSeconds, window.DurationSeconds = 3.5, 2
			body := serve(t, cache, window).Body.String()
			if !strings.Contains(body, "Second") || strings.Contains(body, "Last") {
				t.Fatalf("window %q", body)
			}

			// The buffered text route (jellycompat, translation) too.
			srt, ok, err := cache.ExtractSourcedText(t.Context(), path, opts.Embedded, 0, "srt", ffmpeg)
			if err != nil || !ok {
				t.Fatalf("sourced text: ok %v, %v", ok, err)
			}
			wantSRT, err := playback.ExtractSubtitleWithFormat(t.Context(), path, 0, "srt", ffmpeg)
			if err != nil || !bytes.Equal(srt, wantSRT) {
				t.Fatalf("sourced SRT %q, want %q (%v)", srt, wantSRT, err)
			}
		})
	}
}

// A failed response writer cancels conversion, including a kept stream
// whose storage open or read is stalled while mirror admission is full.
func TestStorageFallbackStopsOnWriterFailure(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	path, file := fixtureFile(t, ffmpeg, true)
	ref := *playback.EmbeddedSubtitleRefFor(file, 0)
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf 'WEBVTT\\n\\n'\nexec cat >/dev/null\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"open", "read", "close"} {
		t.Run(phase, func(t *testing.T) {
			s, _, blobs := newTestStore(t, ffmpeg)
			if _, err := s.WholeTrack(t.Context(), path, ref); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(s.mirrorDir()); err != nil {
				t.Fatal(err)
			}
			fillMirrorFetches(t, s)
			entered := make(chan struct{})
			closed := make(chan struct{})
			unblock := sync.OnceFunc(func() { close(closed) })
			s.blobs = stalledObjectBlobs{Blobs: blobs, phase: phase, entered: entered, closed: closed, close: unblock}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			in, err := s.WholeTrack(ctx, path, ref)
			if err != nil {
				t.Fatal(err)
			}
			writeErr := errors.New("response writer disconnected")
			done := make(chan error, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				done <- playback.StreamExtractSubtitle(ctx, playback.StreamExtractOpts{
					Packets: in, SourceCodec: "subrip", FFmpegPath: bin,
					Writer: storageFailureWriter{canceled: ctx.Done(), entered: entered, err: writeErr},
				})
			}()
			// Release and join the feeder even when an assertion fails.
			t.Cleanup(func() {
				cancel()
				unblock()
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Error("storage fallback worker did not stop during cleanup")
				}
			})
			select {
			case err := <-done:
				if !errors.Is(err, writeErr) || ctx.Err() != nil {
					t.Fatalf("conversion error %v, parent error %v", err, ctx.Err())
				}
			case <-time.After(2 * time.Second):
				t.Fatal("writer failure left storage fallback blocked")
			}
		})
	}
}

type stalledObjectBlobs struct {
	Blobs
	phase   string
	entered chan struct{}
	closed  <-chan struct{}
	close   func()
}

func (b stalledObjectBlobs) Get(ctx context.Context, _ string) (io.ReadCloser, blobstore.ObjectInfo, error) {
	if b.phase == "open" {
		close(b.entered)
		<-ctx.Done()
		return nil, blobstore.ObjectInfo{}, ctx.Err()
	}
	return &stalledObjectReader{canceled: ctx.Done(), entered: b.entered, closed: b.closed,
		close: b.close, onlyClose: b.phase == "close"}, blobstore.ObjectInfo{}, nil
}

type stalledObjectReader struct {
	canceled  <-chan struct{}
	entered   chan struct{}
	closed    <-chan struct{}
	close     func()
	onlyClose bool
}

func (r *stalledObjectReader) Read([]byte) (int, error) {
	close(r.entered)
	if r.onlyClose {
		<-r.closed
	} else {
		select {
		case <-r.canceled:
		case <-r.closed:
		}
	}
	return 0, io.ErrClosedPipe
}

func (r *stalledObjectReader) Close() error { r.close(); return nil }

type storageFailureWriter struct {
	canceled <-chan struct{}
	entered  <-chan struct{}
	err      error
}

func (w storageFailureWriter) Write([]byte) (int, error) {
	select {
	case <-w.entered:
		return 0, w.err
	case <-w.canceled:
		return 0, context.Canceled
	}
}
