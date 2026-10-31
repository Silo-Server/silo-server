package playback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/embeddedsub"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// onDemandTestTracks are three on-demand renditions, the second selected.
func onDemandTestTracks() []SubtitleRenditionTrack {
	return []SubtitleRenditionTrack{
		{StreamIndex: 2, TrackNumber: 3, Name: "English", Language: "eng"},
		{StreamIndex: 3, TrackNumber: 4, Name: "English (Forced)", Language: "en", Forced: true, Default: true},
		{StreamIndex: 4, TrackNumber: 7, Name: "Track \"7\""},
	}
}

func TestSubtitleRenditionNames(t *testing.T) {
	got := SubtitleRenditionNames(
		[]string{"English", "english", "", "", "Say \"hi\"\n", "English"},
		[]string{"en", "en", "fr", "", "", ""},
	)
	want := []string{"English", "english (2)", "fr", "Subtitles", "Say 'hi'", "English (3)"}
	if !slices.Equal(got, want) {
		t.Fatalf("names = %q, want %q", got, want)
	}
}

func TestOnDemandSubtitleRenditionPlaylists(t *testing.T) {
	for _, copyRemux := range []bool{false, true} {
		t.Run(fmt.Sprintf("copy=%v", copyRemux), func(t *testing.T) {
			dir := t.TempDir()
			opts := subtitleRenditionTestOpts(dir)
			opts.SubtitleTrackIndex = -1
			opts.SubtitleRenditionTracks = onDemandTestTracks()
			if copyRemux {
				opts = copyRemuxRenditionOpts(dir, false)
				opts.SubtitleRenditionTracks = onDemandTestTracks()
				writeCopyRemuxPlaylist(t, dir, opts, 0, []float64{4, 4, 4.5}, true)
			}
			session := &TranscodeSession{outputDir: dir, opts: opts}

			master, err := session.BuildPlaybackManifest("segment/", "st=token")
			if err != nil {
				t.Fatal(err)
			}
			text := string(master)
			for _, want := range []string{
				`#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="English",LANGUAGE="en",DEFAULT=NO,AUTOSELECT=YES,FORCED=NO,URI="master.m3u8?silo_rendition=subtitles&silo_track=3&st=token"` + "\n",
				`#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="English (Forced)",LANGUAGE="en",DEFAULT=YES,AUTOSELECT=YES,FORCED=YES,URI="master.m3u8?silo_rendition=subtitles&silo_track=4&st=token"` + "\n",
				`#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="Track '7'",DEFAULT=NO,AUTOSELECT=YES,FORCED=NO,URI="master.m3u8?silo_rendition=subtitles&silo_track=7&st=token"` + "\n",
				`SUBTITLES="subs"`,
				"master.m3u8?silo_rendition=video&st=token\n",
			} {
				if !strings.Contains(text, want) {
					t.Fatalf("multivariant playlist lacks %q:\n%s", want, text)
				}
			}
			if strings.Index(text, "silo_track=3") > strings.Index(text, "silo_track=4") || strings.Index(text, "silo_track=4") > strings.Index(text, "silo_track=7") {
				t.Fatalf("renditions out of inventory order:\n%s", text)
			}

			// Subtitles off: no rendition is the default.
			off := opts
			off.SubtitleRenditionTracks = onDemandTestTracks()
			off.SubtitleRenditionTracks[1].Default = false
			if text := string(subtitleRenditionMultivariantPlaylist(off, "")); strings.Contains(text, "DEFAULT=YES") {
				t.Fatalf("no track selected, yet a default rendition:\n%s", text)
			}

			track, err := session.BuildPlaybackManifest("segment/", "silo_rendition=subtitles&silo_track=4&st=token")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(track), "segment/sub_4_00000.vtt?st=token") || !strings.Contains(string(track), "segment/sub_4_00002.vtt") ||
				strings.Contains(string(track), "sub_00000") || strings.Contains(string(track), "#EXT-X-MAP") || strings.Contains(string(track), "silo_track") {
				t.Fatalf("track playlist:\n%s", track)
			}
			for _, rendition := range []string{"subtitles", "subtitles&silo_track=5", "subtitles&silo_track=x", "subtitles_4"} {
				if _, err := session.BuildPlaybackManifest("segment/", "silo_rendition="+rendition); !errors.Is(err, ErrSubtitleRenditionNotFound) {
					t.Fatalf("%s: err = %v, want ErrSubtitleRenditionNotFound", rendition, err)
				}
			}
			if body, err := session.SubtitleSegmentVTT(t.Context(), 0); !errors.Is(err, ErrSegmentNotFound) || body != nil {
				t.Fatalf("pipeline segment of an on-demand session = %q, %v", body, err)
			}
			if body, err := session.subtitleTrackSegmentVTT(t.Context(), 5, 0); !errors.Is(err, ErrSegmentNotFound) || body != nil {
				t.Fatalf("segment of an unlisted track = %q, %v", body, err)
			}
		})
	}
}

func TestOnDemandSubtitleRenditionsWriteNoCues(t *testing.T) {
	for _, tc := range []struct {
		name  string
		video string
		want  []string
	}{
		{"transcode", "h264", nil},
		// A copy still records the first video packet its cues' clock needs.
		{"copy remux", "copy", []string{"-y", "-map", "0:v:0", "-c:v", "copy", "-frames:v", "1", "-avoid_negative_ts", "disabled",
			"-flush_packets", "1", "-f", "framecrc", "subtitles_2_video.crc"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			opts := subtitleRenditionTestOpts(dir)
			opts.TargetCodecVideo = tc.video
			opts.SubtitleRenditionTracks = onDemandTestTracks()
			opts.subtitleCueFile = subtitleCueFileName(2)
			args := buildFFmpegArgs(opts)
			manifest := slices.Index(args, filepath.Join(dir, "stream.m3u8"))
			if manifest < 0 {
				t.Fatalf("HLS output missing: %v", args)
			}
			got := args[manifest+1:]
			if len(got) > 0 {
				got[len(got)-1] = filepath.Base(got[len(got)-1])
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("after the HLS output: %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOnDemandSubtitleSegmentNames(t *testing.T) {
	for _, tc := range []struct {
		name    string
		track   uint64
		segment int
		ok      bool
	}{
		{"sub_4_00012.vtt", 4, 12, true},
		{"segment/sub_12_00000.vtt", 12, 0, true},
		{"sub_00012.vtt", 0, 12, false},
		{"sub_0_00012.vtt", 0, 0, false},
		{"sub_4_.vtt", 0, 0, false},
		{"sub_x_00012.vtt", 0, 0, false},
		{"seg_00012.ts", 0, 0, false},
	} {
		track, segment, ok := parseSubtitleTrackSegment(filepath.Base(tc.name))
		if ok != tc.ok || ok && (track != tc.track || segment != tc.segment) {
			t.Errorf("%s: track %d segment %d ok %v", tc.name, track, segment, ok)
		}
		if n, ok := ParseSubtitleSegmentNumber(tc.name); ok != (tc.ok || tc.name == "sub_00012.vtt") || ok && n != 12 && n != 0 {
			t.Errorf("%s: ParseSubtitleSegmentNumber = %d, %v", tc.name, n, ok)
		}
	}
}

func TestOnDemandSubtitleRenditionSpansNeedNoCueFile(t *testing.T) {
	dir := t.TempDir()
	opts := subtitleRenditionTestOpts(dir)
	opts.SubtitleRenditionTracks = onDemandTestTracks()
	opts.subtitleCueFile = subtitleCueFileName(0)
	writeSubtitleRenditionPlaylist(t, dir, 0, 4)
	session := &TranscodeSession{outputDir: dir, opts: opts}
	session.startSubtitleSpanLocked(opts)
	session.closeSubtitleSpan()
	session.saveSubtitleSpans()

	// The selection moved while the stream played on, so a rebuilt recipe
	// names another track; on-demand spans don't depend on it.
	rebuilt := opts
	rebuilt.SubtitleTrackIndex = 1
	spans, next := loadSubtitleSpans(rebuilt, nil)
	if len(spans) != 1 || spans[0].start != 0 || spans[0].end != 4 || next != 1 {
		t.Fatalf("on-demand spans = %+v next %d", spans, next)
	}
	// A pipeline recipe on the same directory never serves them.
	pipeline := opts
	pipeline.SubtitleRenditionTracks = nil
	if spans, _ := loadSubtitleSpans(pipeline, nil); len(spans) != 0 {
		t.Fatalf("pipeline recipe kept on-demand spans: %+v", spans)
	}
}

func TestRecipeCardCarriesOnDemandSubtitleRenditions(t *testing.T) {
	opts := subtitleRenditionTestOpts(t.TempDir())
	opts.SubtitleRenditionTracks, opts.SubtitleRenditionSource = onDemandTestTracks(), testRenditionSource
	card := NewRecipeCard(1, "profile", 42, "", opts)
	if got := card.TranscodeOpts(opts.OutputDir, "", nil); !slices.Equal(got.SubtitleRenditionTracks, opts.SubtitleRenditionTracks) ||
		got.SubtitleRenditionSource != testRenditionSource {
		t.Fatalf("card lost the renditions: %+v %+v", got.SubtitleRenditionTracks, got.SubtitleRenditionSource)
	}
	claims := card.ToClaims()
	if !slices.Equal(claims.SubtitleRenditionTracks, []uint64{3, 4, 7}) || claims.SubtitleRenditionDefault != 4 ||
		claims.SubtitleRenditionSourceSize != testRenditionSource.Size || claims.SubtitleRenditionSourceModTime != testRenditionSource.ModTimeUnixNano {
		t.Fatalf("token renditions = %v, default %d, source %d/%d", claims.SubtitleRenditionTracks, claims.SubtitleRenditionDefault,
			claims.SubtitleRenditionSourceSize, claims.SubtitleRenditionSourceModTime)
	}
	// Binaries that predate on-demand renditions refuse the token rather
	// than rebuild the stream with one rendition.
	if claims.PlayMethod != streamtoken.PlayMethodSubtitleRenditionsTranscode {
		t.Fatalf("token method = %q", claims.PlayMethod)
	}
	fromClaims := RecipeCardFromClaims(&claims)
	if fromClaims.PlayMethod != PlayTranscode || !fromClaims.IsTranscodeRecipe() {
		t.Fatalf("method from the token = %q", fromClaims.PlayMethod)
	}
	rebuilt := fromClaims.TranscodeOpts(opts.OutputDir, "", nil)
	want := []SubtitleRenditionTrack{{StreamIndex: -1, TrackNumber: 3}, {StreamIndex: -1, TrackNumber: 4, Default: true}, {StreamIndex: -1, TrackNumber: 7}}
	if !slices.Equal(rebuilt.SubtitleRenditionTracks, want) || !subtitleRenditionsOnDemand(rebuilt) || rebuilt.SubtitleRenditionSource != testRenditionSource {
		t.Fatalf("renditions from the token = %+v %+v", rebuilt.SubtitleRenditionTracks, rebuilt.SubtitleRenditionSource)
	}
	copyOpts := opts
	copyOpts.TargetCodecVideo = "copy"
	copyClaims := NewRecipeCard(1, "profile", 42, "", copyOpts).ToClaims()
	copyCard := RecipeCardFromClaims(&copyClaims)
	if copyClaims.PlayMethod != streamtoken.PlayMethodSubtitleRenditionsCopyFMP4 || copyCard.PlayMethod != playMethodCopyFMP4V1 ||
		ValidateCopyFMP4RecipeCard(copyCard) != nil {
		t.Fatalf("copy token method = %q, card method %q", copyClaims.PlayMethod, copyCard.PlayMethod)
	}
	// A session rebuilt from the token names its renditions by number.
	master := string(subtitleRenditionMultivariantPlaylist(rebuilt, ""))
	if !strings.Contains(master, `NAME="Track 4",DEFAULT=YES`) || !strings.Contains(master, "silo_rendition=subtitles&silo_track=7") {
		t.Fatalf("rebuilt multivariant playlist:\n%s", master)
	}
}

// onDemandRealCue is a cue of the real on-demand sources, on each track's own
// clock before any shift.
type onDemandRealCue struct {
	from, to float64
	text     string
}

// None of these straddles a seek generation's start (20 s), where the
// pipeline rendition, which demuxes from the seek point, drops a cue that is
// already showing.
var onDemandRealCues = [2][]onDemandRealCue{
	{{1, 2.5, "Early A"}, {5, 7.2, "Boundary A"}, {21, 25, "Across A"}, {28, 29.5, "Late A"}},
	{{3, 4, "Uno B"}, {9.5, 12.5, "Dos B"}, {23, 23.8, "Tres B"}, {26, 29.9, "Final B"}},
}

// onDemandRealSources writes a 30 s H.264 Matroska source with two SubRip
// tracks, and a copy whose video starts 4 s after its other tracks, 3 s into
// the container's clock.
func onDemandRealSources(ctx context.Context, t *testing.T, ffmpeg string) (base, shifted string) {
	t.Helper()
	run := func(args ...string) []byte {
		t.Helper()
		output, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("ffmpeg: %v\n%s", err, output)
		}
		return output
	}
	if !strings.Contains(string(run("-hide_banner", "-encoders")), "libx264") {
		t.Skip("libx264 is unavailable")
	}
	work := t.TempDir()
	var inputs []string
	for track, cues := range onDemandRealCues {
		var srt strings.Builder
		for i, cue := range cues {
			at := func(seconds float64) string {
				ms := int(math.Round(seconds * 1000))
				return fmt.Sprintf("00:00:%02d,%03d", ms/1000, ms%1000)
			}
			fmt.Fprintf(&srt, "%d\n%s --> %s\n%s\n\n", i+1, at(cue.from), at(cue.to), cue.text)
		}
		path := filepath.Join(work, fmt.Sprintf("track%d.srt", track))
		if err := os.WriteFile(path, []byte(srt.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, "-i", path)
	}
	base = filepath.Join(work, "base.mkv")
	args := []string{"-v", "error", "-f", "lavfi", "-i", "testsrc2=size=64x64:rate=12",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000"}
	args = append(args, inputs...)
	run(append(args, "-t", "30", "-map", "0", "-map", "1", "-map", "2", "-map", "3",
		"-c:v", "libx264", "-g", "24", "-keyint_min", "24", "-sc_threshold", "0",
		"-c:a", "aac", "-c:s", "srt", "-metadata:s:s:0", "language=eng", "-metadata:s:s:1", "language=spa", base)...)
	shifted = filepath.Join(work, "video-after-subtitles.mkv")
	run("-v", "error", "-itsoffset", "7", "-i", base, "-itsoffset", "3", "-i", base,
		"-map", "0:v", "-map", "1:a", "-map", "1:s", "-c", "copy", shifted)
	return base, shifted
}

// subtitleTrackNumbers reads the source's subtitle track numbers in order.
func subtitleTrackNumbers(t *testing.T, path string) []uint64 {
	t.Helper()
	idx, err := embeddedsub.ReadIndex(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	var numbers []uint64
	for _, track := range idx.Tracks {
		numbers = append(numbers, track.Number)
	}
	return numbers
}

// TestOnDemandSubtitleRenditionsMatchThePipeline runs today's pipeline
// rendition of each track and serves the same segments on demand from the
// source's index on the generation it left: an encode and a copy remux in
// both segment containers, from the start and after a seek, on a source
// whose clock starts at zero and one whose video starts later than its
// subtitles. Every segment must be byte-for-byte the pipeline's.
func TestOnDemandSubtitleRenditionsMatchThePipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath(ffprobePathFromFFmpeg(ffmpeg)); err != nil {
		t.Skip("ffprobe is not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
	defer cancel()
	base, shifted := onDemandRealSources(ctx, t, ffmpeg)

	type recipe struct {
		name string
		opts func(input string) TranscodeOpts
	}
	recipes := []recipe{
		{"encode-ts/start", func(input string) TranscodeOpts {
			return TranscodeOpts{InputPath: input, SourceVideoCodec: "h264", TargetCodecVideo: transcodeCodecH264}
		}},
		{"encode-ts/seek", func(input string) TranscodeOpts {
			return TranscodeOpts{InputPath: input, SourceVideoCodec: "h264", TargetCodecVideo: transcodeCodecH264, SeekSeconds: 20, StartSegmentNumber: 10}
		}},
	}
	for _, mpegts := range []bool{false, true} {
		container := map[bool]string{false: "fmp4", true: "ts"}[mpegts]
		recipes = append(recipes,
			recipe{"copy-" + container + "/start", func(input string) TranscodeOpts {
				return TranscodeOpts{InputPath: input, SourceVideoCodec: "h264", TargetCodecVideo: "copy", CopyVideoMPEGTS: mpegts, CopySeekAnchorResolved: true}
			}},
			recipe{"copy-" + container + "/seek", func(input string) TranscodeOpts {
				return TranscodeOpts{InputPath: input, SourceVideoCodec: "h264", TargetCodecVideo: "copy", CopyVideoMPEGTS: mpegts,
					SeekSeconds: 20.3, StreamOriginSeconds: 20, CopySeekAnchorResolved: true, StartSegmentNumber: 10}
			}},
		)
	}
	// TotalDuration is each source's length, which sets an encode's
	// synthetic grid: the shifted source's video runs to 37 s.
	for _, src := range []struct {
		name, path string
		duration   float64
	}{{"zero", base, 30}, {"video-after-subtitles", shifted, 37}} {
		numbers := subtitleTrackNumbers(t, src.path)
		if len(numbers) != 2 {
			t.Fatalf("%s: subtitle tracks %v", src.name, numbers)
		}
		tracks := []SubtitleRenditionTrack{{StreamIndex: 2, TrackNumber: numbers[0], Name: "A"}, {StreamIndex: 3, TrackNumber: numbers[1], Name: "B"}}
		for _, r := range recipes {
			for track := range tracks {
				t.Run(fmt.Sprintf("%s/%s/track%d", src.name, r.name, track), func(t *testing.T) {
					opts := r.opts(src.path)
					opts.OutputDir, opts.FFmpegPath, opts.HWAccel = t.TempDir(), ffmpeg, HWAccelNone
					opts.TargetCodecAudio, opts.SegmentDuration, opts.TotalDuration = "aac", 2, src.duration
					opts.SubtitleTrackIndex, opts.SubtitleCodec, opts.SubtitleRendition = track, "subrip", true
					opts.subtitleCueFile = subtitleCueFileName(0)
					if output, err := exec.CommandContext(ctx, ffmpeg, buildFFmpegArgs(opts)...).CombinedOutput(); err != nil {
						t.Fatalf("ffmpeg: %v\n%s", err, output)
					}
					pipeline := &TranscodeSession{outputDir: opts.OutputDir, opts: opts}
					pipeline.startSubtitleSpanLocked(opts)
					head, ok := pipeline.liveProducedHead()
					if !ok || head < opts.StartSegmentNumber+3 {
						t.Fatalf("produced through segment %d", head)
					}

					onDemandOpts := opts
					onDemandOpts.SubtitleRenditionTracks = tracks
					onDemand := &TranscodeSession{outputDir: opts.OutputDir, opts: onDemandOpts}
					onDemand.startSubtitleSpanLocked(onDemandOpts)
					cues := 0
					for segment := opts.StartSegmentNumber; segment <= head; segment++ {
						want, err := pipeline.SubtitleSegmentVTT(ctx, segment)
						if err != nil {
							t.Fatal(err)
						}
						got, err := onDemand.subtitleTrackSegmentVTT(ctx, tracks[track].TrackNumber, segment)
						if err != nil {
							t.Fatal(err)
						}
						if string(got) != string(want) {
							t.Fatalf("segment %d on demand:\n%s\npipeline:\n%s", segment, got, want)
						}
						cues += strings.Count(string(got), " --> ")
					}
					if cues == 0 {
						t.Fatal("no segment carried a cue")
					}
				})
			}
		}
	}
}

// TestOnDemandSubtitleSegmentsDoNotWaitForTheStream serves segments of a live
// encode its FFmpeg has not reached, which the pipeline rendition could only
// answer empty, and segments of an earlier generation and of no generation.
func TestOnDemandSubtitleSegmentsDoNotWaitForTheStream(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	base, _ := onDemandRealSources(ctx, t, ffmpeg)
	numbers := subtitleTrackNumbers(t, base)
	dir := t.TempDir()
	opts := TranscodeOpts{
		InputPath: base, OutputDir: dir, FFmpegPath: ffmpeg, SourceVideoCodec: "h264", TargetCodecVideo: transcodeCodecH264, TargetCodecAudio: "aac",
		SegmentDuration: 2, TotalDuration: 30, SubtitleTrackIndex: 0, SubtitleCodec: "subrip", SubtitleRendition: true,
		SubtitleRenditionTracks: []SubtitleRenditionTrack{{TrackNumber: numbers[0], Name: "A", Default: true}, {TrackNumber: numbers[1], Name: "B"}},
	}
	// A generation of segments 0-3, then a seek generation from segment 10
	// that is running and has produced nothing yet. Both origins are known,
	// as once their first segments were seen.
	session := &TranscodeSession{outputDir: dir, opts: opts, running: true, subtitleSpans: []subtitleCueSpan{
		{file: subtitleCueFileName(0), start: 0, end: 3, origin: 0, originKnown: true},
		{file: subtitleCueFileName(1), start: 10, end: -1, startTime: 20, origin: 20, originKnown: true},
	}}

	started := time.Now()
	ahead, err := session.subtitleTrackSegmentVTT(ctx, numbers[0], 11) // [22, 24)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ahead), "00:21.000 --> 00:25.000\nAcross A") || time.Since(started) > 20*time.Second {
		t.Fatalf("segment ahead of the stream after %v:\n%s", time.Since(started), ahead)
	}
	if !strings.HasPrefix(string(ahead), fmt.Sprintf("WEBVTT\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:%d\n", HLSMPEGTSTimestampOffset90k)) {
		t.Fatalf("timestamp map:\n%s", ahead)
	}
	if other, err := session.subtitleTrackSegmentVTT(ctx, numbers[1], 11); err != nil || !strings.Contains(string(other), "Tres B") || strings.Contains(string(other), "Across A") {
		t.Fatalf("the other track's segment = %q, %v", other, err)
	}
	if early, err := session.subtitleTrackSegmentVTT(ctx, numbers[1], 1); err != nil || !strings.Contains(string(early), "Uno B") {
		t.Fatalf("earlier generation's segment = %q, %v", early, err)
	}

	// No generation covers segment 6, so it waits for the restart its video
	// request starts; with none coming it answers empty.
	session.mu.Lock()
	session.running = false
	session.mu.Unlock()
	if gap, err := session.subtitleTrackSegmentVTT(ctx, numbers[1], 6); err != nil || strings.Contains(string(gap), " --> ") {
		t.Fatalf("segment of no generation = %q, %v", gap, err)
	}
}

// TestCueIndexedSubtitleTracksFallsBackWithoutCues reads a Matroska file
// written to a pipe, which has no Cues, and the same streams written to a
// file, which does.
func TestCueIndexedSubtitleTracksFallsBackWithoutCues(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	base, _ := onDemandRealSources(ctx, t, ffmpeg)
	numbers := subtitleTrackNumbers(t, base)
	readable, err := CueIndexedSubtitleTracks(ctx, base)
	info, statErr := os.Stat(base)
	if err != nil || statErr != nil || !readable.Tracks[numbers[0]] || !readable.Tracks[numbers[1]] ||
		readable.Source != (SubtitleRenditionSource{Size: info.Size(), ModTimeUnixNano: info.ModTime().UnixNano()}) {
		t.Fatalf("cued file: %+v, %v", readable, err)
	}

	piped := filepath.Join(t.TempDir(), "piped.mkv")
	out, err := os.Create(piped)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-i", base, "-map", "0", "-c", "copy", "-f", "matroska", "pipe:1")
	var stderr strings.Builder
	cmd.Stdout, cmd.Stderr = out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, stderr.String())
	}
	_ = out.Close()
	readable, err = CueIndexedSubtitleTracks(ctx, piped)
	if err != nil || len(readable.Tracks) != 0 {
		t.Fatalf("file without cues: %v, %v", readable, err)
	}

	// A segment of such a file, were it ever asked for, answers empty
	// instead of failing the player.
	opts := TranscodeOpts{InputPath: piped, OutputDir: t.TempDir(), FFmpegPath: ffmpeg, TargetCodecVideo: transcodeCodecH264,
		SegmentDuration: 2, TotalDuration: 30, SubtitleRendition: true, SubtitleTrackIndex: -1,
		SubtitleRenditionTracks: []SubtitleRenditionTrack{{TrackNumber: numbers[0], Name: "A"}}}
	session := &TranscodeSession{outputDir: opts.OutputDir, opts: opts, subtitleSpans: []subtitleCueSpan{
		{file: subtitleCueFileName(0), start: 0, end: 14, originKnown: true},
	}}
	if body, err := session.subtitleTrackSegmentVTT(ctx, numbers[0], 0); err != nil || strings.Contains(string(body), " --> ") || !strings.HasPrefix(string(body), "WEBVTT\n") {
		t.Fatalf("segment without cues = %q, %v", body, err)
	}
}

// testRenditionSource is the source version planner tests' indexes describe.
var testRenditionSource = SubtitleRenditionSource{Size: 1 << 30, ModTimeUnixNano: 1_800_000_000_000_000_000}

// indexedV3 answers PlannerInputV3.CueIndexedSubtitleTracks with tracks
// readable in testRenditionSource.
func indexedV3(tracks ...uint64) func() *CueIndexedSubtitles {
	readable := map[uint64]bool{}
	for _, track := range tracks {
		readable[track] = true
	}
	return func() *CueIndexedSubtitles {
		return &CueIndexedSubtitles{Tracks: readable, Source: testRenditionSource}
	}
}

// onDemandPlannerInputV3 plans an HLS transcode of a file with an external
// subtitle and four embedded tracks, for a client with every rendition
// feature: English SubRip, a forced English SubRip, PGS, and ASS. The forced
// track is selected.
func onDemandPlannerInputV3(t *testing.T) PlannerInputV3 {
	t.Helper()
	input := subtitleRenditionPlannerInputV3(t, "subrip", SubtitleFidelityCompatibleV3)
	file := input.EffectiveFile
	file.ExternalSubtitles = []models.ExternalSubtitle{{Path: "/media/movie.fr.srt", Language: "fr", Format: "srt"}}
	file.SubtitleTracks = []models.SubtitleTrack{
		{Index: 2, ContainerTrackID: "3", Codec: "subrip", Language: "eng", Title: "English"},
		{Index: 3, ContainerTrackID: "4", Codec: "subrip", Language: "eng", Title: "English", Forced: true},
		{Index: 4, ContainerTrackID: "5", Codec: "hdmv_pgs_subtitle", Language: "eng"},
		{Index: 5, ContainerTrackID: "6", Codec: "ass", Language: "jpn", Title: "Signs"},
	}
	index := 2
	input.Request.SubtitleTrackIndex = &index
	input.Request.SubtitleTrackID = TrackIDV3(file.ID, "subtitle", index)
	input.Request.ClientFeatures = append(input.Request.ClientFeatures, FeatureHLSSubtitleRenditionsAllV3)
	input.OnDemandSubtitleRenditionsExecutable = func() bool { return true }
	input.CueIndexedSubtitleTracks = indexedV3(3, 4, 5, 6)
	return input
}

func TestPlanPlaybackV3ListsEveryEmbeddedTextTrackAsARendition(t *testing.T) {
	input := onDemandPlannerInputV3(t)
	result := PlanPlaybackV3(input)
	if result.Plan == nil || result.Plan.Delivery != DeliveryTranscodeHLSV3 {
		t.Fatalf("want a transcode, got %s", ExplainPlannerResultV3(result))
	}
	wantTracks := []SubtitleRenditionTrack{
		{StreamIndex: 2, TrackNumber: 3, Codec: "subrip", Name: "English", Language: "eng"},
		{StreamIndex: 3, TrackNumber: 4, Codec: "subrip", Name: "English (2)", Language: "eng", Forced: true, Default: true},
		{StreamIndex: 5, TrackNumber: 6, Codec: "ass", Name: "Signs", Language: "jpn"},
	}
	if !result.SubtitleRendition || !slices.Equal(result.SubtitleRenditionTracks, wantTracks) || result.SubtitleRenditionSource != testRenditionSource {
		t.Fatalf("rendition tracks = %+v from %+v (rendition %v)", result.SubtitleRenditionTracks, result.SubtitleRenditionSource, result.SubtitleRendition)
	}
	wantRows := map[int]*SubtitleInventoryHLSRenditionV3{
		1: {GroupID: "subs", Name: "English", Index: 1},
		2: {GroupID: "subs", Name: "English (2)", Index: 2},
		4: {GroupID: "subs", Name: "Signs", Index: 3},
	}
	for _, item := range result.Plan.Subtitle.Inventory {
		want := wantRows[item.CombinedIndex]
		if (item.HLSRendition == nil) != (want == nil) || want != nil && *item.HLSRendition != *want {
			t.Fatalf("row %d hls_rendition = %+v, want %+v", item.CombinedIndex, item.HLSRendition, want)
		}
	}
	subtitle := result.Plan.Subtitle
	wantSelected := HLSSubtitleRenditionV3{GroupID: "subs", Name: "English (2)", Format: "vtt", MIMEType: "text/vtt", Index: 2}
	if subtitle.HLSRendition == nil || *subtitle.HLSRendition != wantSelected || subtitle.Artifact != nil || subtitle.Mode != SubtitleRenderV3 {
		t.Fatalf("subtitle decision = %#v", subtitle)
	}
	recipe := FreezeExecutableRecipeV3(result)
	if restored := recipe.PlannerResult(result.Plan); recipe.Version != executableRecipeVersionRenditionTracksV3 || !recipe.ValidFor(*result.Plan) ||
		!slices.Equal(restored.SubtitleRenditionTracks, wantTracks) || restored.SubtitleRenditionSource != testRenditionSource {
		t.Fatalf("frozen recipe = %#v", recipe)
	}
	older := recipe
	older.Version = executableRecipeVersionCopyMPEGTSV3
	if older.Valid() {
		t.Fatal("a version 5 recipe must not carry on-demand renditions")
	}
}

// Subtitles off keeps the same renditions, none of them the default, on a
// plan of its own, and a failed on-demand plan falls back to the selected
// track's pipeline rendition.
func TestPlanPlaybackV3OnDemandRenditionsWhenOffAndAfterAFailure(t *testing.T) {
	input := onDemandPlannerInputV3(t)
	result := PlanPlaybackV3(input)
	if result.Plan == nil || len(result.SubtitleRenditionTracks) != 3 {
		t.Fatalf("want on-demand renditions, got %s", ExplainPlannerResultV3(result))
	}
	off := onDemandPlannerInputV3(t)
	off.Request.SubtitleTrackIndex, off.Request.SubtitleTrackID = nil, ""
	offResult := PlanPlaybackV3(off)
	if offResult.Plan == nil || !offResult.SubtitleRendition || len(offResult.SubtitleRenditionTracks) != 3 ||
		slices.ContainsFunc(offResult.SubtitleRenditionTracks, func(track SubtitleRenditionTrack) bool { return track.Default }) ||
		offResult.Plan.Subtitle.HLSRendition != nil || offResult.Plan.Subtitle.Mode != SubtitleOffV3 {
		t.Fatalf("subtitles off = %+v %#v", offResult.SubtitleRenditionTracks, offResult.Plan)
	}
	if offResult.Plan.PlanID == result.Plan.PlanID {
		t.Fatal("subtitles off must be a plan of its own")
	}

	retry := input
	retry.AttemptedKeys = []string{result.Plan.PlanAttemptKey}
	fallback := PlanPlaybackV3(retry)
	if fallback.Plan == nil || !fallback.SubtitleRendition || fallback.SubtitleRenditionTracks != nil ||
		fallback.Plan.Subtitle.HLSRendition == nil || fallback.Plan.Subtitle.HLSRendition.Name != SubtitleRenditionName {
		t.Fatalf("on-demand retry = %s", ExplainPlannerResultV3(fallback))
	}
}

func TestPlanPlaybackV3KeepsThePipelineRenditionWithoutOnDemandRenditions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*PlannerInputV3)
		// sidecar is whether the selection is a sidecar rather than the
		// pipeline rendition.
		sidecar bool
	}{
		{"client did not negotiate", func(in *PlannerInputV3) {
			in.Request.ClientFeatures = WithoutFeatureV3(in.Request.ClientFeatures, FeatureHLSSubtitleRenditionsAllV3)
		}, false},
		{"base rendition feature missing", func(in *PlannerInputV3) {
			in.Request.ClientFeatures = WithoutFeatureV3(in.Request.ClientFeatures, FeatureHLSSubtitleRenditionsV3)
		}, true},
		{"no capable executor", func(in *PlannerInputV3) {
			in.OnDemandSubtitleRenditionsExecutable = func() bool { return false }
		}, false},
		{"index unreadable", func(in *PlannerInputV3) {
			in.CueIndexedSubtitleTracks = func() *CueIndexedSubtitles { return nil }
		}, false},
		{"selected track without cues", func(in *PlannerInputV3) {
			in.CueIndexedSubtitleTracks = indexedV3(3, 6)
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := onDemandPlannerInputV3(t)
			tc.mutate(&input)
			result := PlanPlaybackV3(input)
			if result.Plan == nil || result.Plan.Delivery != DeliveryTranscodeHLSV3 {
				t.Fatalf("want a transcode, got %s", ExplainPlannerResultV3(result))
			}
			if result.SubtitleRenditionTracks != nil || slices.ContainsFunc(result.Plan.Subtitle.Inventory, func(item SubtitleInventoryItemV3) bool { return item.HLSRendition != nil }) {
				t.Fatalf("unexpected on-demand renditions: %+v", result.SubtitleRenditionTracks)
			}
			if pipeline := result.Plan.Subtitle.HLSRendition != nil; pipeline == tc.sidecar || result.SubtitleRendition == tc.sidecar {
				t.Fatalf("pipeline rendition = %#v (rendition %v)", result.Plan.Subtitle.HLSRendition, result.SubtitleRendition)
			}
		})
	}
}

// A copy remux lists on-demand renditions only for a client that also
// negotiated remux renditions, and only on an executor that serves them for
// a remux.
func TestPlanPlaybackV3ListsRemuxRenditionsOnDemand(t *testing.T) {
	input := subtitleRenditionRemuxInputV3(t, "subrip")
	input.EffectiveFile.SubtitleTracks = []models.SubtitleTrack{
		{Index: 3, ContainerTrackID: "4", Codec: "subrip", Language: "eng"},
		{Index: 4, ContainerTrackID: "5", Codec: "mov_text", Language: "spa"},
	}
	input.Request.ClientFeatures = append(input.Request.ClientFeatures, FeatureHLSSubtitleRenditionsAllV3)
	input.OnDemandSubtitleRenditionsExecutable = func() bool { return true }
	input.RemuxOnDemandSubtitleRenditionsExecutable = func() bool { return true }
	input.CueIndexedSubtitleTracks = indexedV3(4, 5)
	result := PlanPlaybackV3(input)
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxHLSV3 || len(result.SubtitleRenditionTracks) != 2 ||
		result.Plan.Subtitle.HLSRendition == nil || result.Plan.Subtitle.HLSRendition.Index != 1 {
		t.Fatalf("remux = %s, tracks %+v", ExplainPlannerResultV3(result), result.SubtitleRenditionTracks)
	}
	if got := HLSSubtitleRenditionTransportFeaturesV3(result); !slices.Equal(got, []string{TransportFeatureHLSRemuxSubtitleRenditionV1, TransportFeatureHLSOnDemandSubtitleRenditionsV1}) {
		t.Fatalf("transport features = %v", got)
	}

	for _, mutate := range []func(*PlannerInputV3){
		func(in *PlannerInputV3) { in.RemuxOnDemandSubtitleRenditionsExecutable = func() bool { return false } },
		func(in *PlannerInputV3) {
			in.Request.ClientFeatures = WithoutFeatureV3(in.Request.ClientFeatures, FeatureHLSRemuxSubtitleRenditionsV3)
		},
	} {
		other := subtitleRenditionRemuxInputV3(t, "subrip")
		other.EffectiveFile.SubtitleTracks = input.EffectiveFile.SubtitleTracks
		other.Request.ClientFeatures = append(other.Request.ClientFeatures, FeatureHLSSubtitleRenditionsAllV3)
		other.OnDemandSubtitleRenditionsExecutable = input.OnDemandSubtitleRenditionsExecutable
		other.RemuxOnDemandSubtitleRenditionsExecutable = input.RemuxOnDemandSubtitleRenditionsExecutable
		other.CueIndexedSubtitleTracks = input.CueIndexedSubtitleTracks
		mutate(&other)
		if got := PlanPlaybackV3(other); got.Plan == nil || got.SubtitleRenditionTracks != nil {
			t.Fatalf("remux without on-demand renditions = %s", ExplainPlannerResultV3(got))
		}
	}
}

// A malformed file that repeats a container track number lists the number
// once, under its first track, so the recipe stays valid.
func TestPlanPlaybackV3ListsARepeatedTrackNumberOnce(t *testing.T) {
	input := onDemandPlannerInputV3(t)
	input.EffectiveFile.SubtitleTracks[3].ContainerTrackID = "3"
	result := PlanPlaybackV3(input)
	if result.Plan == nil || len(result.SubtitleRenditionTracks) != 2 || result.SubtitleRenditionTracks[0].StreamIndex != 2 ||
		result.SubtitleRenditionTracks[1].StreamIndex != 3 ||
		!FreezeExecutableRecipeV3(result).ValidFor(*result.Plan) {
		t.Fatalf("renditions = %+v", result.SubtitleRenditionTracks)
	}
}

func TestTicks90kDurationDoesNotOverflow(t *testing.T) {
	for _, tc := range []struct {
		ticks int64
		want  time.Duration
	}{
		{90_000, time.Second},
		{-45_000, -500 * time.Millisecond},
		// 30 hours, past where ticks * time.Second overflows.
		{30 * 3600 * 90_000, 30 * time.Hour},
		{math.MaxInt64, time.Duration(math.MaxInt64)},
		{math.MinInt64, time.Duration(math.MinInt64)},
	} {
		if got := ticks90kDuration(tc.ticks); got != tc.want {
			t.Errorf("ticks90kDuration(%d) = %v, want %v", tc.ticks, got, tc.want)
		}
	}
}

// Like its video segment, a planned copy's segment that no generation
// reaches in the wait answers 404, which the player retries, not an empty
// document it would keep.
func TestPlannedCopyOnDemandSegmentNoGenerationReachesIsNotFound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		opts := copyRemuxRenditionOpts(t.TempDir(), false)
		opts.SubtitleTrackIndex, opts.SubtitleRenditionTracks = -1, onDemandTestTracks()
		opts.StartSegmentNumber, opts.subtitleCueFile = 3, subtitleCueFileName(0)
		session := &TranscodeSession{outputDir: opts.OutputDir, opts: opts, copyPlan: &copySegmentPlan{durations: []float64{2, 2, 2, 2, 2, 2}}, running: true}
		session.startSubtitleSpanLocked(opts)
		started := time.Now()
		if body, err := session.subtitleTrackSegmentVTT(t.Context(), 3, 1); !errors.Is(err, ErrSegmentNotFound) || body != nil ||
			time.Since(started) < subtitleSegmentProductionWait {
			t.Fatalf("segment before the generation = %q, %v after %v", body, err, time.Since(started))
		}
	})
}

// A read that fails for want of time or resources answers 404 for the player
// to retry. A track or file the read can never give, here a source replaced
// since the plan read it, answers an empty segment so the player plays on.
func TestOnDemandSegmentFailures(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	base, _ := onDemandRealSources(ctx, t, ffmpeg)
	numbers := subtitleTrackNumbers(t, base)
	info, err := os.Stat(base)
	if err != nil {
		t.Fatal(err)
	}
	session := func(ffmpegPath string, source SubtitleRenditionSource) *TranscodeSession {
		opts := TranscodeOpts{
			InputPath: base, OutputDir: t.TempDir(), FFmpegPath: ffmpegPath, TargetCodecVideo: transcodeCodecH264,
			SegmentDuration: 2, TotalDuration: 30, SubtitleRendition: true, SubtitleTrackIndex: -1,
			SubtitleRenditionTracks: []SubtitleRenditionTrack{{TrackNumber: numbers[0], Name: "A"}}, SubtitleRenditionSource: source,
		}
		return &TranscodeSession{outputDir: opts.OutputDir, opts: opts, subtitleSpans: []subtitleCueSpan{
			{file: subtitleCueFileName(0), start: 0, end: 14, originKnown: true},
		}}
	}
	planned := SubtitleRenditionSource{Size: info.Size(), ModTimeUnixNano: info.ModTime().UnixNano()}

	// Segment 0 holds "Early A"; the converter can't run.
	rr := httptest.NewRecorder()
	session(filepath.Join(t.TempDir(), "missing-ffmpeg"), planned).ServeSubtitleSegment(rr,
		httptest.NewRequest(http.MethodGet, "/segment/sub_0.vtt", nil), fmt.Sprintf("sub_%d_00000.vtt", numbers[0]))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("failed conversion = %d %q, want 404", rr.Code, rr.Body.String())
	}

	replaced := planned
	replaced.ModTimeUnixNano++
	body, err := session(ffmpeg, replaced).subtitleTrackSegmentVTT(ctx, numbers[0], 0)
	if err != nil || strings.Contains(string(body), " --> ") || !strings.HasPrefix(string(body), "WEBVTT\n") {
		t.Fatalf("segment of a replaced source = %q, %v", body, err)
	}
	if body, err := session(ffmpeg, planned).subtitleTrackSegmentVTT(ctx, numbers[0], 0); err != nil || !strings.Contains(string(body), "Early A") {
		t.Fatalf("segment of the planned source = %q, %v", body, err)
	}

	// A segment past the encode's grid (15 segments of 2 s) is not read,
	// however long the live span runs.
	live := session(ffmpeg, planned)
	live.subtitleSpans[0].end = -1
	if body, err := live.subtitleTrackSegmentVTT(ctx, numbers[0], 15); !errors.Is(err, ErrSegmentNotFound) {
		t.Fatalf("segment past the grid = %q, %v, want ErrSegmentNotFound", body, err)
	}
}

// A window's read stops once every request waiting on it has given up, and a
// miss past the cache's limit on running reads is refused rather than queued.
func TestRenditionCueCacheStopsAbandonedReads(t *testing.T) {
	c := newRenditionCueCache(1<<20, 1)
	key := renditionCueKey{path: "/media/a.mkv", track: 3, to: time.Minute}
	stopped := make(chan struct{})
	read := func(ctx context.Context) ([]byte, error) {
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}
	first, cancelFirst := context.WithCancel(t.Context())
	second, cancelSecond := context.WithCancel(t.Context())
	defer cancelSecond()
	errs := make(chan error, 2)
	go func() { _, err := c.get(first, key, read); errs <- err }()
	waitForCondition(t, func() bool { return loadWaiters(c, key) == 1 }, func() {})
	go func() { _, err := c.get(second, key, read); errs <- err }()
	waitForCondition(t, func() bool { return loadWaiters(c, key) == 2 }, func() {})

	if _, err := c.get(t.Context(), renditionCueKey{path: "/media/b.mkv"}, read); !errors.Is(err, errRenditionLoadsBusy) {
		t.Fatalf("a second window's miss: err = %v, want errRenditionLoadsBusy", err)
	}
	cancelFirst()
	if err := <-errs; !errors.Is(err, context.Canceled) {
		t.Fatalf("first request: err = %v", err)
	}
	select {
	case <-stopped:
		t.Fatal("the read stopped while a request still waited on it")
	default:
	}
	cancelSecond()
	if err := <-errs; !errors.Is(err, context.Canceled) {
		t.Fatalf("second request: err = %v", err)
	}
	<-stopped
}

func loadWaiters(c *renditionCueCache, key renditionCueKey) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if load := c.loading[key]; load != nil {
		return load.waiters
	}
	return 0
}

// keptWindowsV3 stands in for kept tracks (keptsub): it keeps a track the
// way keptsub does, the whole track read through the source's index and
// written with embeddedsub.WriteKept, and cuts windows from that copy the way
// keptsub's Window does.
type keptWindowsV3 struct {
	EmbeddedSubtitleSource
	dir, ffmpeg string
	windows     atomic.Int32
}

func (k *keptWindowsV3) Window(ctx context.Context, path string, ref EmbeddedSubtitleRef, from, to time.Duration) (*SubtitlePacketInput, error) {
	number, err := strconv.ParseUint(ref.ContainerTrackID, 10, 64)
	if err != nil {
		return nil, err
	}
	kept := filepath.Join(k.dir, ref.ContainerTrackID+".mks")
	if _, err := os.Stat(kept); err != nil {
		if err := keepTrackV3(ctx, path, number, kept); err != nil {
			return nil, err
		}
	}
	start, err := SubtitleSourceStartTime(ctx, k.ffmpeg, path)
	if err != nil {
		return nil, err
	}
	idx, err := embeddedsub.ReadIndex(ctx, kept)
	if err != nil {
		return nil, err
	}
	track := &idx.Tracks[0]
	res, err := embeddedsub.ReadPackets(ctx, kept, idx, track.Number, from+start, to+start, embeddedsub.ReadOptions{})
	if err != nil {
		return nil, err
	}
	k.windows.Add(1)
	return &SubtitlePacketInput{Index: idx, Track: track, Packets: res.Packets, SourceStartTime: start}, nil
}

// keepTrackV3 writes track number of path, read whole through its index, as a
// kept stream at kept.
func keepTrackV3(ctx context.Context, path string, number uint64, kept string) error {
	idx, err := embeddedsub.ReadIndex(ctx, path)
	if err != nil {
		return err
	}
	track, _ := idx.Track(number)
	whole, err := embeddedsub.ReadPackets(ctx, path, idx, number, embeddedsub.WholeFrom, embeddedsub.WholeTo, embeddedsub.ReadOptions{})
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := embeddedsub.WriteKept(&buf, idx, track, whole.Packets); err != nil {
		return err
	}
	return os.WriteFile(kept, buf.Bytes(), 0o644)
}

// A segment read from the track's kept copy is byte-for-byte the one read
// through the source's index, on an encode's clock and a copy's, from a
// source whose clock starts at zero and one that starts later.
func TestOnDemandSegmentsReadFromKeptTracksMatchTheSource(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath(ffprobePathFromFFmpeg(ffmpeg)); err != nil {
		t.Skip("ffprobe is not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	base, shifted := onDemandRealSources(ctx, t, ffmpeg)
	previous := renditionCues
	t.Cleanup(func() {
		renditionCues = previous
		SetRenditionKeptTracks(nil)
	})
	for _, src := range []struct{ name, path string }{{"zero", base}, {"video-after-subtitles", shifted}} {
		numbers := subtitleTrackNumbers(t, src.path)
		for _, video := range []string{transcodeCodecH264, "copy"} {
			t.Run(src.name+"/"+video, func(t *testing.T) {
				opts := TranscodeOpts{
					InputPath: src.path, MediaFileID: 42, OutputDir: t.TempDir(), FFmpegPath: ffmpeg, SourceVideoCodec: "h264",
					TargetCodecVideo: video, TargetCodecAudio: "aac", SegmentDuration: 2, TotalDuration: 30,
					SubtitleRendition: true, SubtitleTrackIndex: -1, SubtitleRenditionTracks: []SubtitleRenditionTrack{
						{StreamIndex: 2, TrackNumber: numbers[0], Codec: "subrip", Name: "A"},
						{StreamIndex: 3, TrackNumber: numbers[1], Codec: "subrip", Name: "B"},
					},
				}
				// An observed MPEG-TS origin, so the epoch is chosen too. A copy
				// places its segments by its keyframe plan.
				session := &TranscodeSession{outputDir: opts.OutputDir, opts: opts, subtitleSpans: []subtitleCueSpan{
					{file: subtitleCueFileName(0), start: 0, end: 14, origin: 0, originKnown: true},
				}}
				if video == "copy" {
					session.copyPlan = &copySegmentPlan{durations: slices.Repeat([]float64{2}, 15)}
				}
				segments := func(kept EmbeddedSubtitleSource) map[string]string {
					renditionCues = newRenditionCueCache(renditionCueCacheBytes, renditionLoads)
					SetRenditionKeptTracks(kept)
					got := map[string]string{}
					for _, number := range numbers {
						for segment := range 15 {
							body, err := session.subtitleTrackSegmentVTT(ctx, number, segment)
							if err != nil {
								t.Fatal(err)
							}
							got[fmt.Sprintf("%d/%d", number, segment)] = string(body)
						}
					}
					return got
				}
				direct := segments(nil)
				kept := &keptWindowsV3{dir: t.TempDir(), ffmpeg: ffmpeg}
				fromKept := segments(kept)
				if kept.windows.Load() == 0 {
					t.Fatal("no window was read from the kept track")
				}
				cues := 0
				for key, want := range direct {
					if fromKept[key] != want {
						t.Fatalf("segment %s from the kept track:\n%s\nfrom the source:\n%s", key, fromKept[key], want)
					}
					cues += strings.Count(want, " --> ")
				}
				if cues == 0 {
					t.Fatal("no segment carried a cue")
				}
			})
		}
	}
}
