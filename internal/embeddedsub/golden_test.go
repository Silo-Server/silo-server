package embeddedsub_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/embeddedsub"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// These tests hold the reader to its contract with today's extraction:
// reading a track through the index and converting the packets with
// playback's FFmpeg conversion gives the same bytes as FFmpeg demuxing the
// whole source file. They skip when ffmpeg or ffprobe isn't installed.

func requireFFmpeg(t *testing.T) string {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	return ffmpeg
}

const (
	srtEnglish = `1
00:00:00,500 --> 00:00:02,000
{\an8}At the top

2
00:00:03,000 --> 00:00:04,250
Two lines,
<i>one in italics</i>

3
00:00:04,000 --> 00:00:09,500
Overlapping the one before

4
00:00:10,100 --> 00:00:11,000
Last
`
	srtGerman = `1
00:00:01,000 --> 00:00:02,500
Erzwungen

2
00:00:06,000 --> 00:00:07,000
Noch einer
`
	assStyled = `[Script Info]
ScriptType: v4.00+
PlayResX: 640
PlayResY: 360

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Default,Arial,28,&H00FFFFFF,&H000000FF,&H00000000,&H64000000,0,0,0,0,100,100,0,0,1,2,1,2,20,20,20,1
Style: Top,Georgia,22,&H0000FFFF,&H000000FF,&H00101010,&H00000000,-1,0,0,0,100,100,0,0,1,3,0,8,10,10,10,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
` +
		"Dialogue: 0,0:00:01.00,0:00:03.50,Default,,0,0,0,,{\\i1}Styled{\\i0} line\n" + //nolint:misspell // ASS event keyword
		"Dialogue: 1,0:00:01.50,0:00:08.00,Top,Sign,0,0,0,,{\\pos(320,30)}A long sign\n" + //nolint:misspell // ASS event keyword
		"Dialogue: 0,0:00:05.25,0:00:06.75,Default,,0,0,0,,Later\n" //nolint:misspell // ASS event keyword
)

// ffmpegFixture writes a small file with video, audio, two SubRip tracks
// (the second forced) and an ASS track; extra are further output options.
// videoOffset delays the video and audio, which moves the file's start time.
func ffmpegFixture(t *testing.T, ffmpeg, name string, videoOffset float64, extra ...string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(file, data string) string {
		p := filepath.Join(dir, file)
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	en, de, ass := write("en.srt", srtEnglish), write("de.srt", srtGerman), write("styled.ass", assStyled)
	out := filepath.Join(dir, name)
	offset := fmt.Sprint(videoOffset)
	args := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-itsoffset", offset, "-f", "lavfi", "-i", "testsrc2=size=64x36:rate=10",
		"-itsoffset", offset, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=8000",
		"-i", en, "-i", de, "-i", ass, "-t", "12",
		"-map", "0:v", "-map", "1:a", "-map", "2", "-map", "3", "-map", "4",
		"-c:v", "mpeg4", "-c:a", "aac", "-c:s:0", "srt", "-c:s:1", "srt", "-c:s:2", "ass",
		"-metadata:s:s:0", "language=eng", "-metadata:s:s:1", "language=ger", "-disposition:s:1", "forced",
	}
	if videoOffset != 0 {
		args = append(args, "-copyts")
	}
	args = append(args, extra...)
	cmd := exec.Command(ffmpeg, append(args, out)...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg can't make the fixture (%v): %s", err, b)
	}
	return out
}

type extract struct {
	ordinal int
	number  uint64
	codec   string
	// target is StreamExtractOpts.TargetFormat; srt and ass use the buffered
	// ExtractSubtitleWithFormat instead.
	target string
}

// compare extracts one track both ways and checks the bytes match.
func compare(t *testing.T, ffmpeg, path string, x extract) {
	t.Helper()
	ctx := context.Background()
	want, err := fromSource(ctx, ffmpeg, path, x)
	if err != nil {
		t.Fatalf("today's extraction: %v", err)
	}
	idx, err := embeddedsub.ReadIndex(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	track, ok := idx.Track(x.number)
	if !ok {
		t.Fatalf("no track %d", x.number)
	}
	res, err := embeddedsub.ReadPackets(ctx, path, idx, x.number, embeddedsub.WholeFrom, embeddedsub.WholeTo, embeddedsub.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	start, err := playback.SubtitleSourceStartTime(ctx, ffmpeg, path)
	if err != nil {
		t.Fatal(err)
	}
	in := &playback.SubtitlePacketInput{Index: idx, Track: track, Packets: res.Packets, SourceStartTime: start}
	got, err := fromPackets(ctx, ffmpeg, in, x)
	if err != nil {
		t.Fatalf("extraction from packets: %v", err)
	}
	if len(want) == 0 {
		t.Fatal("today's extraction is empty")
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("output differs from today's (start time %v)\n%s", start, firstDifference(want, got))
	}
}

func fromSource(ctx context.Context, ffmpeg, path string, x extract) ([]byte, error) {
	if x.target == "srt" || x.target == "ass" {
		return playback.ExtractSubtitleWithFormat(ctx, path, x.ordinal, x.target, ffmpeg)
	}
	var out bytes.Buffer
	err := playback.StreamExtractSubtitle(ctx, playback.StreamExtractOpts{
		InputPath: path, TrackIndex: x.ordinal, SourceCodec: x.codec, TargetFormat: x.target, FFmpegPath: ffmpeg, Writer: &out,
	})
	return out.Bytes(), err
}

func fromPackets(ctx context.Context, ffmpeg string, in *playback.SubtitlePacketInput, x extract) ([]byte, error) {
	if x.target == "srt" || x.target == "ass" {
		return playback.ExtractSubtitlePacketsWithFormat(ctx, in, x.target, ffmpeg)
	}
	var out bytes.Buffer
	err := playback.StreamExtractSubtitle(ctx, playback.StreamExtractOpts{
		Packets: in, SourceCodec: x.codec, TargetFormat: x.target, FFmpegPath: ffmpeg, Writer: &out,
	})
	return out.Bytes(), err
}

func firstDifference(want, got []byte) string {
	wl, gl := strings.Split(string(want), "\n"), strings.Split(string(got), "\n")
	for i := range max(len(wl), len(gl)) {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			return fmt.Sprintf("line %d:\n want %q\n  got %q\n(%d vs %d bytes)", i+1, w, g, len(want), len(got))
		}
	}
	return fmt.Sprintf("same lines, %d vs %d bytes", len(want), len(got))
}

func TestCueReadMatchesFFmpegOnFFmpegFiles(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	// Tracks 1 and 2 are video and audio; the subtitles are 3 to 5.
	extracts := []struct {
		name string
		x    extract
	}{
		{"SubRip to WebVTT", extract{ordinal: 0, number: 3, codec: "subrip"}},
		{"forced SubRip to WebVTT", extract{ordinal: 1, number: 4, codec: "subrip"}},
		{"SubRip to SubRip", extract{ordinal: 0, number: 3, codec: "subrip", target: "srt"}},
		{"ASS to ASS", extract{ordinal: 2, number: 5, codec: "ass"}},
		{"ASS to WebVTT", extract{ordinal: 2, number: 5, codec: "ass", target: "vtt"}},
		{"ASS to ASS, buffered", extract{ordinal: 2, number: 5, codec: "ass", target: "ass"}},
	}
	for _, file := range []struct {
		name   string
		offset float64
	}{{"starting at zero", 0}, {"video starting at 1.5 s", 1.5}} {
		path := ffmpegFixture(t, ffmpeg, "in.mkv", file.offset)
		for _, e := range extracts {
			t.Run(file.name+"/"+e.name, func(t *testing.T) { compare(t, ffmpeg, path, e.x) })
		}
	}
}

func TestCueReadMatchesFFmpegOnHandBuiltFiles(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	for _, f := range embeddedsub.GoldenFixtures(t) {
		path := filepath.Join(t.TempDir(), "in.mkv")
		if err := os.WriteFile(path, f.Data, 0o600); err != nil {
			t.Fatal(err)
		}
		targets := []string{""}
		if f.Codec == "subrip" {
			targets = append(targets, "srt")
		}
		for _, target := range targets {
			t.Run(f.Name+"/"+cmp(target, "default"), func(t *testing.T) {
				compare(t, ffmpeg, path, extract{ordinal: f.Ordinal, number: f.Track, codec: f.Codec, target: target})
			})
		}
	}
}

func cmp(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func TestFFmpegFilesIndexState(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	ctx := context.Background()
	// FFmpeg writes stream metadata as Matroska tags, so the statistics
	// tag can be set: right for one track, wrong for the other.
	tagged := ffmpegFixture(t, ffmpeg, "tagged.mkv", 0,
		"-metadata:s:s:0", "NUMBER_OF_FRAMES=4", "-metadata:s:s:1", "NUMBER_OF_FRAMES=9")
	idx, err := embeddedsub.ReadIndex(ctx, tagged)
	if err != nil {
		t.Fatal(err)
	}
	for number, want := range map[uint64]embeddedsub.Completeness{
		3: embeddedsub.CompletenessComplete, 4: embeddedsub.CompletenessIncomplete, 5: embeddedsub.CompletenessUnknown,
	} {
		if tr, _ := idx.Track(number); tr == nil || tr.Completeness != want {
			t.Errorf("track %d: %+v, want %s", number, tr, want)
		}
	}
	forced, _ := idx.Track(4)
	if !forced.Forced || forced.Language != "ger" {
		t.Errorf("forced track = %+v", forced)
	}
	if _, err := embeddedsub.ReadPackets(ctx, tagged, idx, 4, embeddedsub.WholeFrom, embeddedsub.WholeTo, embeddedsub.ReadOptions{}); !errors.Is(err, embeddedsub.ErrIncompleteCues) {
		t.Errorf("incomplete track: err = %v", err)
	}

	// Written to a pipe, FFmpeg can't go back to write Cues.
	piped := filepath.Join(t.TempDir(), "piped.mkv")
	out, err := os.Create(piped)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-i", tagged, "-map", "0", "-c", "copy", "-f", "matroska", "pipe:1")
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = out, &stderr
	if err := cmd.Run(); err != nil {
		t.Skipf("can't pipe a fixture (%v): %s", err, stderr.Bytes())
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	idx, err = embeddedsub.ReadIndex(ctx, piped)
	if err != nil {
		t.Fatal(err)
	}
	if tr, _ := idx.Track(3); tr == nil || tr.Completeness != embeddedsub.CompletenessNone {
		t.Fatalf("piped file track = %+v", tr)
	}
	if _, err := embeddedsub.ReadPackets(ctx, piped, idx, 3, embeddedsub.WholeFrom, embeddedsub.WholeTo, embeddedsub.ReadOptions{}); !errors.Is(err, embeddedsub.ErrNoCues) {
		t.Fatalf("piped file: err = %v, want ErrNoCues", err)
	}

	// Cues in front, then the file cut off after the first subtitles: the
	// index still points at blocks that are gone.
	front := ffmpegFixture(t, ffmpeg, "front.mkv", 0, "-cues_to_front", "1", "-cluster_time_limit", "2000")
	idx, err = embeddedsub.ReadIndex(ctx, front)
	if err != nil {
		t.Fatal(err)
	}
	en, _ := idx.Track(3)
	data, err := os.ReadFile(front)
	if err != nil {
		t.Fatal(err)
	}
	cut := en.Cues[2].ClusterPosition + 8
	lost := 0
	for _, c := range en.Cues {
		if c.ClusterPosition >= cut-8 {
			lost++
		}
	}
	if err := os.WriteFile(front, data[:cut], 0o600); err != nil {
		t.Fatal(err)
	}
	idx, err = embeddedsub.ReadIndex(ctx, front)
	if err != nil {
		t.Fatal(err)
	}
	res, err := embeddedsub.ReadPackets(ctx, front, idx, 3, embeddedsub.WholeFrom, embeddedsub.WholeTo, embeddedsub.ReadOptions{})
	var damaged *embeddedsub.DamagedError
	if !errors.As(err, &damaged) || damaged.Cues != lost || len(res.Packets) != len(en.Cues)-lost {
		t.Fatalf("cut file: err = %v, %d packets; want %d of %d cues lost", err, len(res.Packets), lost, len(en.Cues))
	}
}

func TestOverrideTagsInFFmpegFile(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	ctx := context.Background()
	path := ffmpegFixture(t, ffmpeg, "in.mkv", 0)
	idx, err := embeddedsub.ReadIndex(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for number, want := range map[uint64]bool{3: true, 4: false} {
		res, err := embeddedsub.ReadPackets(ctx, path, idx, number, embeddedsub.WholeFrom, embeddedsub.WholeTo, embeddedsub.ReadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := embeddedsub.HasOverrideTags(res.Packets); got != want {
			t.Errorf("track %d: HasOverrideTags = %v, want %v", number, got, want)
		}
	}
	// A 6 s window is a handful of reads.
	res, err := embeddedsub.ReadPackets(ctx, path, idx, 3, 4*time.Second, 10*time.Second, embeddedsub.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Packets) != 2 || res.Stats.Reads > 4 {
		t.Fatalf("window: %d packets in %d reads", len(res.Packets), res.Stats.Reads)
	}
}
