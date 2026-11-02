package embeddedsub_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	// mov_text samples don't overlap: the cue at 3 s ends when the next
	// starts at 4 s.
	res, err := embeddedsub.ReadPackets(ctx, path, idx, 3, 3500*time.Millisecond, 10*time.Second, embeddedsub.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Packets) != 2 || res.Stats.Reads > 4 {
		t.Fatalf("window: %d packets in %d reads", len(res.Packets), res.Stats.Reads)
	}
}

// ffmpegMP4Fixture writes an MP4 or MOV file, by name's extension, with
// video, audio and two mov_text tracks; extra are further output options.
func ffmpegMP4Fixture(t *testing.T, ffmpeg, name string, videoOffset float64, extra ...string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(file, data string) string {
		p := filepath.Join(dir, file)
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	en, de := write("en.srt", srtEnglish), write("de.srt", srtGerman)
	out := filepath.Join(dir, name)
	offset := fmt.Sprint(videoOffset)
	args := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-itsoffset", offset, "-f", "lavfi", "-i", "testsrc2=size=64x36:rate=10",
		"-itsoffset", offset, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=8000",
		"-i", en, "-i", de, "-t", "12",
		"-map", "0:v", "-map", "1:a", "-map", "2", "-map", "3",
		"-c:v", "mpeg4", "-c:a", "aac", "-c:s", "mov_text",
		"-metadata:s:s:0", "language=eng", "-metadata:s:s:1", "language=ger",
	}
	if videoOffset != 0 {
		args = append(args, "-copyts")
	}
	args = append(args, extra...)
	if b, err := exec.Command(ffmpeg, append(args, out)...).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg can't make the fixture (%v): %s", err, b)
	}
	return out
}

func TestCueReadMatchesFFmpegOnMP4Files(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	files := []struct {
		name, file string
		offset     float64
		extra      []string
	}{
		{"MP4", "in.mp4", 0, nil},
		{"MP4 with the moov first", "in.mp4", 0, []string{"-movflags", "+faststart"}},
		{"MP4 with video starting at 1.5 s", "in.mp4", 1.5, nil},
		{"MOV", "in.mov", 0, nil},
	}
	for _, f := range files {
		path := ffmpegMP4Fixture(t, ffmpeg, f.file, f.offset, f.extra...)
		// Tracks 1 and 2 are video and audio.
		for _, x := range []extract{
			{ordinal: 0, number: 3, codec: "mov_text"},
			{ordinal: 1, number: 4, codec: "mov_text"},
			{ordinal: 0, number: 3, codec: "mov_text", target: "srt"},
		} {
			t.Run(fmt.Sprintf("%s/track %d/%s", f.name, x.number, cmp(x.target, "default")), func(t *testing.T) {
				compare(t, ffmpeg, path, x)
			})
		}
	}
}

func TestMP4Index(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	ctx := context.Background()
	path := ffmpegMP4Fixture(t, ffmpeg, "in.mp4", 0)
	idx, err := embeddedsub.ReadIndex(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if idx.Container != embeddedsub.ContainerMP4 || len(idx.Tracks) != 2 {
		t.Fatalf("index = %+v", idx)
	}
	en, _ := idx.Track(3)
	de, _ := idx.Track(4)
	if en == nil || de == nil || en.CodecID != "tx3g" || en.Language != "eng" || de.Language != "ger" ||
		en.Completeness != embeddedsub.CompletenessComplete {
		t.Fatalf("tracks = %+v, %+v", en, de)
	}
	// The moov sits after the media: a few header reads find it.
	if idx.Stats.Reads > 4 {
		t.Fatalf("index took %d reads", idx.Stats.Reads)
	}
	// mov_text samples don't overlap: the cue at 3 s ends when the next
	// starts at 4 s.
	res, err := embeddedsub.ReadPackets(ctx, path, idx, 3, 3500*time.Millisecond, 10*time.Second, embeddedsub.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, p := range res.Packets {
		if len(p.Data) > 2 {
			texts = append(texts, string(p.Data[2:]))
		}
	}
	if len(texts) != 2 || !strings.Contains(texts[0], "Two lines") || !strings.Contains(texts[1], "Overlapping") {
		t.Fatalf("window texts = %q", texts)
	}
}

// A window's packets convert to the same cues, at the same times, as the
// whole track's: what an HLS segment or a client's window would get. The
// files start at zero: when a cue starts before the file's start time,
// FFmpeg's muxer shifts the whole track's output later to keep timestamps
// from going negative (avoid_negative_ts), today's extraction included, and
// a window without that cue isn't shifted.
func TestWindowConversionKeepsTimes(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	ctx := context.Background()
	for _, f := range []struct{ name, path, codec string }{
		{"Matroska", ffmpegFixture(t, ffmpeg, "in.mkv", 0), "subrip"},
		{"MP4", ffmpegMP4Fixture(t, ffmpeg, "in.mp4", 0), "mov_text"},
	} {
		t.Run(f.name, func(t *testing.T) {
			idx, err := embeddedsub.ReadIndex(ctx, f.path)
			if err != nil {
				t.Fatal(err)
			}
			track, _ := idx.Track(3)
			start, err := playback.SubtitleSourceStartTime(ctx, ffmpeg, f.path)
			if err != nil {
				t.Fatal(err)
			}
			vtt := func(from, to time.Duration) []string {
				res, err := embeddedsub.ReadPackets(ctx, f.path, idx, 3, from, to, embeddedsub.ReadOptions{})
				if err != nil {
					t.Fatal(err)
				}
				out, err := fromPackets(ctx, ffmpeg, &playback.SubtitlePacketInput{Index: idx, Track: track, Packets: res.Packets, SourceStartTime: start},
					extract{codec: f.codec})
				if err != nil {
					t.Fatal(err)
				}
				// Cue blocks after the WEBVTT header.
				return strings.Split(strings.TrimSpace(string(out)), "\n\n")[1:]
			}
			whole := vtt(embeddedsub.WholeFrom, embeddedsub.WholeTo)
			window := vtt(3500*time.Millisecond, 10*time.Second)
			if len(window) == 0 || len(window) >= len(whole) {
				t.Fatalf("window has %d of %d cues", len(window), len(whole))
			}
			for _, cue := range window {
				if !slices.Contains(whole, cue) {
					t.Errorf("window cue %q isn't in the whole track %q", cue, whole)
				}
			}
		})
	}
}

// FFmpeg can't demux a Matroska S_TEXT/WEBVTT track, but read through the
// index it converts to the bytes FFmpeg gives the same cues stored the WebM
// way, both from WriteStream's stream and from a kept copy read back.
func TestWebVTTTrackConvertsAsWebM(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	ctx := context.Background()
	mkvData, webmData := embeddedsub.WebVTTFixtures()
	dir := t.TempDir()
	mkv, webm := filepath.Join(dir, "matroska.mkv"), filepath.Join(dir, "webm.mkv")
	for path, data := range map[string][]byte{mkv: mkvData, webm: webmData} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fromSource(ctx, ffmpeg, mkv, extract{codec: "webvtt"}); err == nil {
		t.Fatal("FFmpeg demuxed the S_TEXT/WEBVTT source; the index route is no longer needed")
	}

	idx, err := embeddedsub.ReadIndex(ctx, mkv)
	if err != nil {
		t.Fatal(err)
	}
	track, ok := idx.Track(2)
	if !ok {
		t.Fatal("no track 2")
	}
	if got := track.FFmpegCodec(); got != "webvtt" {
		t.Fatalf("FFmpegCodec = %q, want webvtt", got)
	}
	res, err := embeddedsub.ReadPackets(ctx, mkv, idx, 2, embeddedsub.WholeFrom, embeddedsub.WholeTo, embeddedsub.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(dir, "kept.mks")
	f, err := os.Create(kept)
	if err != nil {
		t.Fatal(err)
	}
	if err := embeddedsub.WriteKept(f, idx, track, res.Packets); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	keptIdx, err := embeddedsub.ReadIndex(ctx, kept)
	if err != nil {
		t.Fatal(err)
	}
	keptTrack := &keptIdx.Tracks[0]
	keptRes, err := embeddedsub.ReadPackets(ctx, kept, keptIdx, keptTrack.Number, embeddedsub.WholeFrom, embeddedsub.WholeTo, embeddedsub.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	inputs := map[string]*playback.SubtitlePacketInput{
		"stream":    {Index: idx, Track: track, Packets: res.Packets},
		"kept copy": {Index: keptIdx, Track: keptTrack, Packets: keptRes.Packets},
	}
	for _, target := range []string{"", "srt", "ass"} {
		x := extract{codec: "webvtt", target: target}
		want, err := fromSource(ctx, ffmpeg, webm, x)
		if err != nil || len(want) == 0 {
			t.Fatalf("%s from the WebM source: %q, %v", cmp(target, "vtt"), want, err)
		}
		for name, in := range inputs {
			t.Run(cmp(target, "vtt")+"/"+name, func(t *testing.T) {
				got, err := fromPackets(ctx, ffmpeg, in, x)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("output differs from the WebM track's\n%s", firstDifference(want, got))
				}
			})
		}
	}
}

// A WebVTT file mkvmerge muxed, with a cue identifier, cue settings and a
// comment in its blocks' additions, converts through the index.
func TestMkvmergeWebVTTTrackConverts(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	ctx := context.Background()
	const path = "../scanner/testdata/webvtt.mkv"
	idx, err := embeddedsub.ReadIndex(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	track, ok := idx.Track(2)
	if !ok || track.CodecID != "S_TEXT/WEBVTT" || track.Completeness == embeddedsub.CompletenessNone {
		t.Fatalf("track 2 = %+v", track)
	}
	res, err := embeddedsub.ReadPackets(ctx, path, idx, 2, embeddedsub.WholeFrom, embeddedsub.WholeTo, embeddedsub.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	start, err := playback.SubtitleSourceStartTime(ctx, ffmpeg, path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fromPackets(ctx, ffmpeg, &playback.SubtitlePacketInput{Index: idx, Track: track, Packets: res.Packets, SourceStartTime: start}, extract{codec: "webvtt"})
	if err != nil {
		t.Fatal(err)
	}
	const want = "WEBVTT\n\n00:00.500 --> 00:01.500\nHello <b>world</b>.\n\n00:02.000 --> 00:02.800\nTwo lines\nof text\n"
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
