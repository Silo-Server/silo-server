package embeddedsub_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/embeddedsub"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// keptCase is a track to keep and compare with its source.
type keptCase struct {
	name, path string
	x          extract
	// from and to, when to is set, are a window cut from both the source
	// and the kept track.
	from, to time.Duration
}

func keptCases(t *testing.T, ffmpeg string) []keptCase {
	var cases []keptCase
	for _, offset := range []float64{0, 1.5} {
		path := ffmpegFixture(t, ffmpeg, "in.mkv", offset)
		cases = append(cases,
			keptCase{name: "SubRip", path: path, x: extract{ordinal: 0, number: 3, codec: "subrip"}, from: 3500 * time.Millisecond, to: 10 * time.Second},
			keptCase{name: "SubRip to SubRip", path: path, x: extract{ordinal: 0, number: 3, codec: "subrip", target: "srt"}},
			keptCase{name: "ASS", path: path, x: extract{ordinal: 2, number: 5, codec: "ass"}},
		)
	}
	mp4 := ffmpegMP4Fixture(t, ffmpeg, "in.mp4", 0)
	cases = append(cases, keptCase{name: "mov_text", path: mp4, x: extract{ordinal: 0, number: 3, codec: "mov_text"}, from: 3500 * time.Millisecond, to: 10 * time.Second})
	for _, f := range embeddedsub.GoldenFixtures(t) {
		path := filepath.Join(t.TempDir(), "in.mkv")
		if err := os.WriteFile(path, f.Data, 0o600); err != nil {
			t.Fatal(err)
		}
		cases = append(cases, keptCase{name: f.Name, path: path, x: extract{ordinal: f.Ordinal, number: f.Track, codec: f.Codec}, from: 3 * time.Second, to: 7 * time.Second})
	}
	return cases
}

// A kept track has to stand in for the source: converting it gives today's
// bytes, and ReadIndex reads it back to the packets it was written from, so
// windows can be cut from it instead of the source.
func TestKeptTrackStandsInForTheSource(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	ctx := context.Background()
	for _, c := range keptCases(t, ffmpeg) {
		t.Run(c.name, func(t *testing.T) {
			idx, err := embeddedsub.ReadIndex(ctx, c.path)
			if err != nil {
				t.Fatal(err)
			}
			track, _ := idx.Track(c.x.number)
			whole, err := embeddedsub.ReadPackets(ctx, c.path, idx, c.x.number, embeddedsub.WholeFrom, embeddedsub.WholeTo, embeddedsub.ReadOptions{})
			if err != nil {
				t.Fatal(err)
			}
			start, err := playback.SubtitleSourceStartTime(ctx, ffmpeg, c.path)
			if err != nil {
				t.Fatal(err)
			}
			var kept bytes.Buffer
			if err := embeddedsub.WriteKept(&kept, idx, track, whole.Packets); err != nil {
				t.Fatal(err)
			}
			keptPath := filepath.Join(t.TempDir(), "kept")
			if err := os.WriteFile(keptPath, kept.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}

			want, err := fromSource(ctx, ffmpeg, c.path, c.x)
			if err != nil {
				t.Fatal(err)
			}
			stream := &playback.SubtitlePacketInput{
				Stream:          func() (io.ReadCloser, error) { return os.Open(keptPath) },
				StreamFormat:    idx.StreamFormat(),
				SourceStartTime: start,
			}
			got, err := fromPackets(ctx, ffmpeg, stream, c.x)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("kept track converts differently\n%s", firstDifference(want, got))
			}
			checkKeptReadsBack(t, ffmpeg, c, idx, track, whole.Packets, keptPath, start)
		})
	}
}

// checkKeptReadsBack reads the kept track at keptPath back, whole and in
// c's window, against the source's packets.
func checkKeptReadsBack(t *testing.T, ffmpeg string, c keptCase, idx *embeddedsub.Index, track *embeddedsub.Track, packets []embeddedsub.Packet, keptPath string, start time.Duration) {
	t.Helper()
	ctx := context.Background()
	keptIdx, err := embeddedsub.ReadIndex(ctx, keptPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(keptIdx.Tracks) != 1 || keptIdx.Tracks[0].Completeness == embeddedsub.CompletenessNone {
		t.Fatalf("kept index = %+v", keptIdx.Tracks)
	}
	keptTrack := &keptIdx.Tracks[0]
	back, err := embeddedsub.ReadPackets(ctx, keptPath, keptIdx, keptTrack.Number, embeddedsub.WholeFrom, embeddedsub.WholeTo, embeddedsub.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Written again, the packets read back make the same stream: same
	// times, durations, data, lacing and keyframe flags.
	var first, again bytes.Buffer
	if err := embeddedsub.WriteStream(&first, idx, track, packets); err != nil {
		t.Fatal(err)
	}
	if err := embeddedsub.WriteStream(&again, keptIdx, keptTrack, back.Packets); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), again.Bytes()) {
		t.Fatalf("packets read back from the kept track differ: %d packets of %d, %d stream bytes of %d",
			len(back.Packets), len(packets), again.Len(), first.Len())
	}
	if c.to == 0 {
		return
	}
	window := func(path string, idx *embeddedsub.Index, track *embeddedsub.Track) ([]byte, int) {
		res, err := embeddedsub.ReadPackets(ctx, path, idx, track.Number, start+c.from, start+c.to, embeddedsub.ReadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		out, err := fromPackets(ctx, ffmpeg, &playback.SubtitlePacketInput{Index: idx, Track: track, Packets: res.Packets, SourceStartTime: start}, c.x)
		if err != nil {
			t.Fatal(err)
		}
		return out, len(res.Packets)
	}
	sourceOut, sourcePackets := window(c.path, idx, track)
	keptOut, keptPackets := window(keptPath, keptIdx, keptTrack)
	if keptPackets == 0 || !bytes.Equal(sourceOut, keptOut) {
		t.Fatalf("window from the kept track (%d packets) differs from the source's (%d)\n%s",
			keptPackets, sourcePackets, firstDifference(sourceOut, keptOut))
	}
}

// A window can hold no packets: a sparse track, such as forced subtitles,
// between its cues. It converts to what FFmpeg makes of an empty window of
// a Matroska source, the WebVTT header alone. (FFmpeg's own window of the
// MP4 source isn't empty: its seek lands on the cue before.)
func TestEmptyWindowConverts(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	ctx := context.Background()
	mkv := ffmpegFixture(t, ffmpeg, "in.mkv", 0)
	var want bytes.Buffer
	if err := playback.StreamExtractSubtitle(ctx, playback.StreamExtractOpts{
		InputPath: mkv, TrackIndex: 1, SourceCodec: "subrip", SeekSeconds: 8, DurationSeconds: 3, FFmpegPath: ffmpeg, Writer: &want,
	}); err != nil {
		t.Fatal(err)
	}
	if want.String() != "WEBVTT\n" {
		t.Fatalf("FFmpeg's empty window = %q", want.Bytes())
	}
	for _, f := range []struct{ name, path, codec string }{
		{"Matroska", mkv, "subrip"},
		{"MP4", ffmpegMP4Fixture(t, ffmpeg, "in.mp4", 0), "mov_text"},
	} {
		t.Run(f.name, func(t *testing.T) {
			idx, err := embeddedsub.ReadIndex(ctx, f.path)
			if err != nil {
				t.Fatal(err)
			}
			track, _ := idx.Track(4)
			// The forced track's cues end at 7 s; the file runs to 12 s.
			res, err := embeddedsub.ReadPackets(ctx, f.path, idx, 4, 8*time.Second, 11*time.Second, embeddedsub.ReadOptions{})
			if err != nil {
				t.Fatal(err)
			}
			got, err := fromPackets(ctx, ffmpeg, &playback.SubtitlePacketInput{Index: idx, Track: track, Packets: res.Packets}, extract{codec: f.codec})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Packets) != 0 || !bytes.Equal(got, want.Bytes()) {
				t.Fatalf("empty window = %q from %d packets, want %q", got, len(res.Packets), want.Bytes())
			}
		})
	}
}
