package playback

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// writeBatchOutputs is a fake batch that labels each rendition's contents.
func writeBatchOutputs(calls *atomic.Int32, empty map[TextSubtitleTrack]bool) TextSubtitleBatchFunc {
	return func(_ context.Context, _ string, outputs []TextSubtitleOutput) error {
		calls.Add(1)
		for _, output := range outputs {
			body := fmt.Sprintf("track %d %s", output.Ordinal, output.Format)
			if empty[output.TextSubtitleTrack] {
				body = ""
			}
			if err := os.WriteFile(output.Path, []byte(body), 0o600); err != nil {
				return err
			}
		}
		return nil
	}
}

func failSingle(t *testing.T) func(context.Context) ([]byte, error) {
	return func(context.Context) ([]byte, error) {
		t.Error("the requested track was extracted on its own")
		return nil, errors.New("unexpected single extract")
	}
}

func TestExtractTextTracksFillsEveryUncachedTrackInOneDemux(t *testing.T) {
	cache, source := newTestCache(t)
	tracks := TextSubtitleTracks([]string{"subrip", "hdmv_pgs_subtitle", "ass"})
	var calls atomic.Int32

	data, err := cache.ExtractTextTracks(t.Context(), source, TextSubtitleTrack{Ordinal: 0, Format: "srt"},
		tracks, writeBatchOutputs(&calls, nil), failSingle(t))
	if err != nil || string(data) != "track 0 srt" {
		t.Fatalf("requested track = %q, %v", data, err)
	}
	for _, track := range tracks {
		if got, ok := cache.LookupText(source, track.Ordinal, track.Format); !ok || string(got) != fmt.Sprintf("track %d %s", track.Ordinal, track.Format) {
			t.Fatalf("rendition %+v not cached by the batch: %q %t", track, got, ok)
		}
	}
	if _, ok := cache.LookupText(source, 1, "srt"); ok {
		t.Fatal("a bitmap stream was extracted as text")
	}

	data, err = cache.ExtractTextTracks(t.Context(), source, TextSubtitleTrack{Ordinal: 2, Format: "ass"},
		tracks, writeBatchOutputs(&calls, nil), failSingle(t))
	if err != nil || string(data) != "track 2 ass" || calls.Load() != 1 {
		t.Fatalf("later track = %q, %v after %d demuxes; want the cached rendition from one demux", data, err, calls.Load())
	}
}

func TestExtractTextTracksWaitsForAnInFlightFill(t *testing.T) {
	cache, source := newTestCache(t)
	want := TextSubtitleTrack{Ordinal: 1, Format: "srt"}
	fill := cache.beginFill(source, want.Ordinal, want.Format)
	if fill == nil {
		t.Fatal("could not reserve the fill")
	}
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := cache.ExtractTextTracks(t.Context(), source, want, []TextSubtitleTrack{want},
			func(context.Context, string, []TextSubtitleOutput) error {
				t.Error("started a second demux for a track already being filled")
				return nil
			}, failSingle(t))
		done <- result{data, err}
	}()
	if err := os.WriteFile(fill.tmp.Name(), []byte("filled elsewhere"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fill.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got.err != nil || string(got.data) != "filled elsewhere" {
		t.Fatalf("waiting request = %q, %v", got.data, got.err)
	}
}

func TestExtractTextTracksFallsBackToTheRequestedTrackAlone(t *testing.T) {
	cache, source := newTestCache(t)
	tracks := TextSubtitleTracks([]string{"subrip", "subrip"})
	data, err := cache.ExtractTextTracks(t.Context(), source, TextSubtitleTrack{Ordinal: 1, Format: "srt"}, tracks,
		func(context.Context, string, []TextSubtitleOutput) error {
			return errors.New("one stream cannot convert")
		},
		func(context.Context) ([]byte, error) { return []byte("single extract"), nil })
	if err != nil || string(data) != "single extract" {
		t.Fatalf("fallback = %q, %v", data, err)
	}
	if got, ok := cache.LookupText(source, 1, "srt"); !ok || string(got) != "single extract" {
		t.Fatalf("fallback not cached: %q %t", got, ok)
	}
	if _, ok := cache.LookupText(source, 0, "srt"); ok {
		t.Fatal("a failed demux published another track")
	}
}

// A stream without cues reads as an error, as a direct extract of it does,
// but the demux result is remembered so it never reads the source again.
func TestExtractTextTracksRemembersAnEmptyRendition(t *testing.T) {
	cache, source := newTestCache(t)
	tracks := TextSubtitleTracks([]string{"subrip", "subrip"})
	var calls atomic.Int32
	empty := map[TextSubtitleTrack]bool{{Ordinal: 0, Format: "srt"}: true}
	for range 2 {
		if _, err := cache.ExtractTextTracks(t.Context(), source, TextSubtitleTrack{Ordinal: 0, Format: "srt"}, tracks,
			writeBatchOutputs(&calls, empty), failSingle(t)); err == nil {
			t.Fatal("an empty requested track was returned as a subtitle")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("demuxed %d times; an empty rendition must be remembered", calls.Load())
	}
	cache.WarmTextTracks(source, tracks, func(context.Context, string, []TextSubtitleOutput) error {
		t.Error("warm demuxed a source whose renditions are all cached")
		return nil
	})
	if got, ok := cache.LookupText(source, 1, "srt"); !ok || string(got) != "track 1 srt" {
		t.Fatalf("other track not cached: %q %t", got, ok)
	}
}

// When the batch runs but the requested rendition cannot be published, the
// request still gets its subtitle.
func TestExtractTextTracksFallsBackWhenTheRenditionIsNotPublished(t *testing.T) {
	cache, source := newTestCache(t)
	tracks := TextSubtitleTracks([]string{"subrip"})
	var calls atomic.Int32
	write := writeBatchOutputs(&calls, nil)
	data, err := cache.ExtractTextTracks(t.Context(), source, TextSubtitleTrack{Ordinal: 0, Format: "srt"}, tracks,
		func(ctx context.Context, input string, outputs []TextSubtitleOutput) error {
			if err := os.WriteFile(input, []byte("the source was replaced during the demux"), 0o600); err != nil {
				return err
			}
			return write(ctx, input, outputs)
		},
		func(context.Context) ([]byte, error) { return []byte("single extract"), nil })
	if err != nil || string(data) != "single extract" {
		t.Fatalf("request = %q, %v; want the single-track fallback", data, err)
	}
}

func TestWarmTextTracksServesALaterRequestFromTheWarm(t *testing.T) {
	cache, source := newTestCache(t)
	tracks := TextSubtitleTracks([]string{"subrip", "ssa"})
	release := make(chan struct{})
	var calls atomic.Int32
	warm := writeBatchOutputs(&calls, nil)
	cache.WarmTextTracks(source, tracks, func(ctx context.Context, input string, outputs []TextSubtitleOutput) error {
		<-release
		return warm(ctx, input, outputs)
	})

	done := make(chan []byte, 1)
	go func() {
		data, err := cache.ExtractTextTracks(t.Context(), source, TextSubtitleTrack{Ordinal: 1, Format: "ass"}, tracks,
			func(context.Context, string, []TextSubtitleOutput) error {
				t.Error("a request demuxed the source while the warm was running")
				return nil
			}, failSingle(t))
		if err != nil {
			t.Error(err)
		}
		done <- data
	}()
	close(release)
	if got := <-done; string(got) != "track 1 ass" || calls.Load() != 1 {
		t.Fatalf("request during warm = %q after %d demuxes", got, calls.Load())
	}
}

func TestExtractTextSubtitlesToFilesDemuxesEveryOutput(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required to extract subtitle streams")
	}
	dir := t.TempDir()
	srt := filepath.Join(dir, "a.srt")
	ass := filepath.Join(dir, "b.ass")
	source := filepath.Join(dir, "subs.mkv")
	if err := os.WriteFile(srt, []byte("1\n00:00:01,000 --> 00:00:02,000\nFirst track\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const assScript = "[Script Info]\nScriptType: v4.00+\n\n[V4+ Styles]\n" +
		"Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n" +
		"Style: Default,Arial,20,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,1,0,2,10,10,10,1\n\n" +
		"[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		"Dialogue: 0,0:00:03.00,0:00:04.00,Default,,0,0,0,,Second track\n" //nolint:misspell // ASS event keyword
	if err := os.WriteFile(ass, []byte(assScript), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(t.Context(), ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-i", srt, "-i", ass, "-map", "0", "-map", "1", "-c", "copy", source).CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v: %s", err, out)
	}

	tracks := TextSubtitleTracks([]string{"subrip", "ass"})
	outputs := make([]TextSubtitleOutput, len(tracks))
	for i, track := range tracks {
		outputs[i] = TextSubtitleOutput{TextSubtitleTrack: track, Path: filepath.Join(dir, fmt.Sprintf("out-%d.%s.part-1", track.Ordinal, track.Format))}
	}
	if err := ExtractTextSubtitlesToFiles(t.Context(), source, outputs, ffmpeg); err != nil {
		t.Fatal(err)
	}
	want := []string{"First track", "Second track", "Dialogue: 0,0:00:03.00,0:00:04.00,Default"} //nolint:misspell // ASS event keyword
	for i, output := range outputs {
		data, err := os.ReadFile(output.Path)
		if err != nil || !strings.Contains(string(data), want[i]) {
			t.Fatalf("output %+v = %q, %v; want %q", output.TextSubtitleTrack, data, err, want[i])
		}
	}
}
