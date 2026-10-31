package keptsub

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
