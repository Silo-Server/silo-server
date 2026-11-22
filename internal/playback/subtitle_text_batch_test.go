package playback

import (
	"context"
	"errors"
	"fmt"
	"os"
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

// A node with no room for another demux of a source refuses the request
// before the batch, or the requested track alone, reads it.
func TestExtractTextTracksAnswersBusyWithoutADemux(t *testing.T) {
	occupyDemuxSlots(t)
	cache, source := newTestCache(t)
	var calls atomic.Int32
	_, err := cache.ExtractTextTracks(t.Context(), source, TextSubtitleTrack{Ordinal: 0, Format: "srt"},
		TextSubtitleTracks([]string{"subrip", "subrip"}), writeBatchOutputs(&calls, nil), failSingle(t))
	if !errors.Is(err, ErrSubtitleReadsBusy) || calls.Load() != 0 {
		t.Fatalf("err %v after %d batch demuxes, want busy before any", err, calls.Load())
	}
}
