package embeddedsub_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
				Stream:          func(context.Context) (io.ReadCloser, error) { return os.Open(keptPath) },
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

// A demux of the whole source that also copies the track (KeepPath) serves
// the same bytes as one that doesn't, and its copy, shifted back onto the
// source's timeline, converts to them again and indexes.
func TestDemuxCopyStandsInForTheSource(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	ctx := context.Background()
	type demuxCase struct {
		name, path, codec, format string
		ordinal                   int
	}
	var cases []demuxCase
	for _, offset := range []float64{0, 1.5} {
		path := ffmpegFixture(t, ffmpeg, "in.mkv", offset)
		cases = append(cases,
			demuxCase{name: "SubRip", path: path, codec: "subrip", format: "matroska"},
			demuxCase{name: "ASS", path: path, codec: "ass", format: "matroska", ordinal: 2},
		)
	}
	cases = append(cases,
		demuxCase{name: "mov_text", path: ffmpegMP4Fixture(t, ffmpeg, "in.mp4", 1.5), codec: "mov_text", format: "mp4"},
		// Past a few dozen kilobytes, an MP4 copy with its moov after the
		// samples converts from a pipe to nothing.
		demuxCase{name: "mov_text with 1,500 cues", path: manyCuesMP4(t, ffmpeg, 1500), codec: "mov_text", format: "mp4"},
	)
	for _, f := range embeddedsub.GoldenFixtures(t) {
		if f.Codec != "hdmv_pgs_subtitle" {
			continue
		}
		path := filepath.Join(t.TempDir(), "in.mkv")
		if err := os.WriteFile(path, f.Data, 0o600); err != nil {
			t.Fatal(err)
		}
		cases = append(cases, demuxCase{name: f.Name, path: path, codec: f.Codec, format: "matroska", ordinal: f.Ordinal})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x := extract{ordinal: c.ordinal, codec: c.codec}
			want, err := fromSource(ctx, ffmpeg, c.path, x)
			if err != nil {
				t.Fatal(err)
			}
			copyPath := filepath.Join(t.TempDir(), "kept.part-1")
			if err := os.WriteFile(copyPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			start, err := playback.SubtitleSourceStartTime(ctx, ffmpeg, c.path)
			if err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			if err := playback.StreamExtractSubtitle(ctx, playback.StreamExtractOpts{
				InputPath: c.path, TrackIndex: c.ordinal, SourceCodec: c.codec, FFmpegPath: ffmpeg, Writer: &got,
				KeepPath: copyPath, KeepFormat: c.format, KeepOffset: start,
			}); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Bytes(), want) {
				t.Fatalf("demux with a copy serves differently\n%s", firstDifference(want, got.Bytes()))
			}
			fromCopy, err := fromPackets(ctx, ffmpeg, &playback.SubtitlePacketInput{
				Stream: func(context.Context) (io.ReadCloser, error) { return os.Open(copyPath) }, StreamFormat: c.format, SourceStartTime: start,
			}, x)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(fromCopy, want) {
				t.Fatalf("the copy converts differently\n%s", firstDifference(want, fromCopy))
			}
			idx, err := embeddedsub.ReadIndex(ctx, copyPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(idx.Tracks) != 1 {
				t.Fatalf("copy index = %+v", idx.Tracks)
			}
			res, err := embeddedsub.ReadPackets(ctx, copyPath, idx, idx.Tracks[0].Number, embeddedsub.WholeFrom, embeddedsub.WholeTo, embeddedsub.ReadOptions{})
			if err != nil || len(res.Packets) == 0 {
				t.Fatalf("copy packets: %d, %v", len(res.Packets), err)
			}
			// The index counts the copy's packets without reading them: a cue
			// per Matroska block, a sample per MP4 packet.
			counted := len(idx.Tracks[0].Cues)
			if idx.Container == embeddedsub.ContainerMP4 {
				counted = int(idx.Tracks[0].FrameCount)
			}
			if counted != len(res.Packets) {
				t.Fatalf("the index counts %d packets, the copy holds %d", counted, len(res.Packets))
			}
		})
	}
}

// manyCuesMP4 writes an MP4 file whose only track is a mov_text track of n
// two-second cues.
func manyCuesMP4(t *testing.T, ffmpeg string, n int) string {
	t.Helper()
	dir := t.TempDir()
	var srt strings.Builder
	for i := range n {
		start := time.Duration(i) * 2 * time.Second
		fmt.Fprintf(&srt, "%d\n%s --> %s\nCue number %d, a line of ordinary length\n\n", i+1, srtTime(start), srtTime(start+1500*time.Millisecond), i+1)
	}
	in, out := filepath.Join(dir, "many.srt"), filepath.Join(dir, "many.mp4")
	if err := os.WriteFile(in, []byte(srt.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", in, "-c:s", "mov_text",
		"-metadata:s:s:0", "language=eng", out).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg can't make the fixture (%v): %s", err, b)
	}
	return out
}

func srtTime(d time.Duration) string {
	ms := d.Milliseconds()
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3_600_000, ms/60_000%60, ms/1000%60, ms%1000)
}
