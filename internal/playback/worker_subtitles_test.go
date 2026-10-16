package playback

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workerinput"
	"github.com/Silo-Server/silo-server/internal/workersource"
)

func registeredSubtitleSource(t *testing.T, path, scratch string) (context.Context, string, *atomic.Int64) {
	t.Helper()
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	var reads atomic.Int64
	authority, err := workersource.New(workersource.Options{
		Store: workersource.NewMemoryStore(), Origin: origin,
		Active: func(context.Context, int, string, uint64) bool { return true },
		PathApprover: func(_ context.Context, input, kind string) error {
			if input != path || kind != "media" {
				return workersource.ErrDenied
			}
			return nil
		},
		VerifyRequest: func(r *http.Request, _ int) error {
			if r.Header.Get("X-Test-Proof") != "node-proof" {
				return workersource.ErrDenied
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reads.Add(1); authority.Handler().ServeHTTP(w, r) })
	server.Start()
	t.Cleanup(server.Close)
	source, err := authority.Acquire(t.Context(), 7, "subtitle", 1, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := workerinput.New(workerinput.Options{Origins: []string{origin}, AllowHTTP: true, LocalRoot: scratch,
		Allowed: func(work string, generation uint64) bool { return work == "subtitle" && generation == 1 },
		Sign:    func(r *http.Request) error { r.Header.Set("X-Test-Proof", "node-proof"); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	ctx := workerinput.WithContext(t.Context(), adapter)
	handle, err := adapter.Open(ctx, workerinput.Source{ID: source.Ref.ID, Revision: source.Ref.Revision, URL: source.URL, Grant: source.Grant, ETag: source.ETag, Size: source.Size, WorkID: source.WorkID, Generation: source.Generation})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, handle.URL(), &reads
}

//nolint:funlen,gocyclo // Keep this ordered failure-injection scenario and its exact authority assertions in one scope.
func TestWorkerASSBurnInStylesFontsAndSeekMatchLocal(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	listing, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-filters").CombinedOutput()
	if err != nil || !strings.Contains(string(listing), " subtitles ") {
		t.Skip("ffmpeg lacks libass subtitles")
	}
	coreDir := t.TempDir()
	scratch := t.TempDir()
	fontPath := filepath.Join("..", "..", "web", "public", "vendor", "pdfjs", "standard_fonts", "LiberationSans-Regular.ttf")
	font, err := os.ReadFile(fontPath)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "fonts.conf")
	if err := os.WriteFile(configPath, []byte("<?xml version=\"1.0\"?><!DOCTYPE fontconfig SYSTEM \"fonts.dtd\"><fontconfig></fontconfig>"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FONTCONFIG_FILE", configPath)
	header := "[Script Info]\nScriptType: v4.00+\nPlayResX: 160\nPlayResY: 90\n[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\nStyle: Default,Liberation Sans,18,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,1,0,2,5,5,5,1\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n"
	first := filepath.Join(coreDir, "first.ass")
	second := filepath.Join(coreDir, "second.ass")
	for path, text := range map[string]string{first: "WRONG TRACK", second: "{\\c&HFF8800&\\b1}Styled text"} {
		//nolint:misspell // ASS format keyword.
		if err := os.WriteFile(path, []byte(header+"Dialogue: 0,0:00:00.50,0:00:03.50,Default,,0,0,0,,"+text+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	media := filepath.Join(coreDir, "embedded.mkv")
	args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=black:s=160x90:r=24:d=4", "-i", first, "-i", second, "-map", "0:v", "-map", "1:s", "-map", "2:s", "-c:v", "libx264", "-threads", "1", "-preset", "ultrafast", "-g", "24", "-c:s", "ass", "-attach", fontPath, "-metadata:s:t:0", "filename=../../untrusted-name.ttf", "-metadata:s:t:0", "mimetype=font/ttf", "-output_ts_offset", "11.4", media}
	if out, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("generate ASS source: %v: %s", err, out)
	}
	workerCtx, input, reads := registeredSubtitleSource(t, media, scratch)
	for _, seek := range []float64{0, 2} {
		t.Run(formatWorkerSeek(seek), func(t *testing.T) {
			local := TranscodeOpts{InputPath: media, OutputDir: t.TempDir(), FFmpegPath: ffmpeg, HWAccel: "none", TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 1, SubtitleBurnIn: true, SubtitleTrackIndex: 1, SubtitleCodec: "ass", SeekSeconds: seek}
			if err := prepareSubtitleFilterInput(&local); err != nil {
				t.Fatal(err)
			}
			remote := local
			remote.InputPath = input
			remote.OutputDir = filepath.Join(scratch, formatWorkerSeek(seek))
			if err := os.MkdirAll(remote.OutputDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := prepareSubtitleFilterInputContext(workerCtx, &remote); err != nil {
				t.Fatal(err)
			}
			staged, err := os.ReadFile(remote.subtitleFilterInputPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(staged), "Styled text") || strings.Contains(string(staged), "WRONG TRACK") {
				t.Fatal("staged subtitle did not select the original ordinal")
			}
			if remote.SubtitleTrackIndex != 1 {
				t.Fatal("staging changed durable subtitle identity")
			}
			fonts, err := os.ReadDir(remote.subtitleFilterFontsDir)
			if err != nil || len(fonts) != 1 {
				t.Fatalf("staged fonts: %d %v", len(fonts), err)
			}
			fontData, err := os.ReadFile(filepath.Join(remote.subtitleFilterFontsDir, fonts[0].Name()))
			if err != nil || !bytes.Equal(fontData, font) {
				t.Fatalf("font attachment changed: %v", err)
			}
			if strings.Contains(fonts[0].Name(), "untrusted-name") {
				t.Fatal("staging trusted attachment filename")
			}
			for _, o := range []TranscodeOpts{local, remote} {
				runCtx := ctx
				if o.InputPath == input {
					runCtx = workerCtx
				}
				if out, err := workerinput.CommandContext(runCtx, ffmpeg, buildFFmpegArgs(o)...).CombinedOutput(); err != nil {
					t.Fatalf("burn in: %v: %s", err, out)
				}
			}
			localFrames := workerBurnInFrames(t, ctx, ffmpeg, local.OutputDir)
			remoteFrames := workerBurnInFrames(t, ctx, ffmpeg, remote.OutputDir)
			if !bytes.Equal(localFrames, remoteFrames) {
				t.Fatalf("styled subtitle/frame timestamps differ at seek %g", seek)
			}
			plain := local
			plain.SubtitleBurnIn = false
			plain.SubtitleTrackIndex = -1
			plain.OutputDir = t.TempDir()
			if out, err := exec.CommandContext(ctx, ffmpeg, buildFFmpegArgs(plain)...).CombinedOutput(); err != nil {
				t.Fatalf("plain control: %v: %s", err, out)
			}
			if bytes.Equal(localFrames, workerBurnInFrames(t, ctx, ffmpeg, plain.OutputDir)) {
				t.Fatal("subtitle comparison rendered no text")
			}
			if reads.Load() == 0 {
				t.Fatal("worker did not fetch encoded source over HTTP")
			}
			before := reads.Load()
			if err := prepareSubtitleFilterInputContext(workerCtx, &remote); err != nil {
				t.Fatalf("reuse authorized staged assets: %v", err)
			}
			if reads.Load() != before {
				t.Fatal("seek reused bytes through a new media extraction")
			}
		})
	}
	handle, ok := workerinput.Lookup(workerCtx, input)
	if !ok {
		t.Fatal("registered source lost")
	}
	_ = handle.Close()
	denied := TranscodeOpts{InputPath: input, OutputDir: filepath.Join(scratch, "startup"), FFmpegPath: ffmpeg, SubtitleBurnIn: true, SubtitleTrackIndex: 1, SubtitleCodec: "ass"}
	if err := prepareSubtitleFilterInputContext(workerCtx, &denied); err == nil {
		t.Fatal("cached subtitle bypassed expired source authority")
	}
}

func TestWorkerSubtitleFontMetadataAndPayloadLimits(t *testing.T) {
	core := filepath.Join(t.TempDir(), "input.mkv")
	if err := os.WriteFile(core, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, input, _ := registeredSubtitleSource(t, core, t.TempDir())
	binDir := t.TempDir()
	ffmpeg := filepath.Join(binDir, "ffmpeg")
	writeExecutable(t, ffmpeg, "#!/bin/sh\nprintf 'junk'\n")
	for _, metadata := range []string{
		`{"streams":[{"index":2,"codec_type":"attachment","codec_name":"ttf","extradata_size":33554433}]}`,
		`{"streams":[{"index":2,"codec_type":"attachment","codec_name":"ttf","extradata_size":4},{"index":2,"codec_type":"attachment","codec_name":"ttf","extradata_size":4}]}`,
		`{"streams":[{"index":2,"codec_type":"attachment","codec_name":"ttf","extradata_size":4}]}`,
		`{"streams":[{"index":2,"codec_type":"attachment","codec_name":"ttf","extradata_size":5}]}`,
	} {
		writeExecutable(t, filepath.Join(binDir, "ffprobe"), "#!/bin/sh\nprintf '%s' '"+metadata+"'\n")
		if _, err := workerSubtitleFonts(ctx, input, ffmpeg); err == nil {
			t.Fatalf("unsafe font metadata/payload accepted: %s", metadata)
		}
	}
}

func TestWorkerSubRipBurnInAndSeekMatchLocal(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	listing, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-filters").CombinedOutput()
	if err != nil || !strings.Contains(string(listing), " subtitles ") {
		t.Skip("ffmpeg lacks libass subtitles")
	}
	core, scratch := t.TempDir(), t.TempDir()
	subtitle := filepath.Join(core, "text.srt")
	if err := os.WriteFile(subtitle, []byte("1\n00:00:00,500 --> 00:00:03,500\n<i>Text subtitle</i>\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(core, "subrip.mkv")
	generate := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=black:s=160x90:r=24:d=4", "-i", subtitle, "-map", "0:v", "-map", "1:s", "-c:v", "libx264", "-threads", "1", "-preset", "ultrafast", "-g", "24", "-c:s", "srt", "-output_ts_offset", "11.4", media}
	if out, err := exec.CommandContext(ctx, ffmpeg, generate...).CombinedOutput(); err != nil {
		t.Fatalf("generate SRT source: %v: %s", err, out)
	}
	workerCtx, input, _ := registeredSubtitleSource(t, media, scratch)
	for _, seek := range []float64{0, 2} {
		local := TranscodeOpts{InputPath: media, OutputDir: t.TempDir(), FFmpegPath: ffmpeg, HWAccel: "none", TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 1, SubtitleBurnIn: true, SubtitleTrackIndex: 0, SubtitleCodec: "subrip", SeekSeconds: seek}
		if err := prepareSubtitleFilterInput(&local); err != nil {
			t.Fatal(err)
		}
		remote := local
		remote.InputPath = input
		remote.OutputDir = filepath.Join(scratch, formatWorkerSeek(seek))
		if err := os.MkdirAll(remote.OutputDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := prepareSubtitleFilterInputContext(workerCtx, &remote); err != nil {
			t.Fatal(err)
		}
		for _, opts := range []TranscodeOpts{local, remote} {
			runCtx := ctx
			if opts.InputPath == input {
				runCtx = workerCtx
			}
			if out, err := workerinput.CommandContext(runCtx, ffmpeg, buildFFmpegArgs(opts)...).CombinedOutput(); err != nil {
				t.Fatalf("SRT burn in: %v: %s", err, out)
			}
		}
		if !bytes.Equal(workerBurnInFrames(t, ctx, ffmpeg, local.OutputDir), workerBurnInFrames(t, ctx, ffmpeg, remote.OutputDir)) {
			t.Fatalf("converted text rendering/timing differs at seek %g", seek)
		}
	}
}

func TestWorkerSubtitleScratchRejectsEscapeAndSymlinkCache(t *testing.T) {
	core := filepath.Join(t.TempDir(), "input.mkv")
	if err := os.WriteFile(core, []byte("synthetic container"), 0o600); err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	ctx, input, _ := registeredSubtitleSource(t, core, scratch)
	binDir := t.TempDir()
	ffmpeg := filepath.Join(binDir, "ffmpeg")
	writeExecutable(t, ffmpeg, "#!/bin/sh\nprintf '[Script Info]\\n[Events]\\n'\n")
	writeExecutable(t, filepath.Join(binDir, "ffprobe"), "#!/bin/sh\nprintf '{\"streams\":[]}'\n")
	opts := TranscodeOpts{InputPath: input, FFmpegPath: ffmpeg, SubtitleBurnIn: true, SubtitleTrackIndex: 0, SubtitleCodec: "ass", OutputDir: t.TempDir()}
	if err := prepareSubtitleFilterInputContext(ctx, &opts); err == nil {
		t.Fatal("subtitle assets escaped configured worker scratch")
	}
	outside := opts.OutputDir
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("escape wrote scratch outside scope: entries=%d err=%v", len(entries), err)
	}
	opts.OutputDir = scratch
	handle, ok := workerinput.Lookup(ctx, input)
	if !ok {
		t.Fatal("registered source lost")
	}
	key := sha256.Sum256([]byte(handle.Identity() + ":0:ass"))
	cache := filepath.Join(scratch, "subtitle-assets-"+hex.EncodeToString(key[:16]))
	if err := os.WriteFile(filepath.Join(outside, "selected.ass"), []byte("[Events]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(outside, "fonts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, cache); err != nil {
		t.Fatal(err)
	}
	if err := prepareSubtitleFilterInputContext(ctx, &opts); err == nil {
		t.Fatal("subtitle cache followed an escaping symlink")
	}
}

func TestWorkerSubtitleOutputLimitsAndCancellation(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	writeExecutable(t, bin, "#!/bin/sh\nprintf '123456789'\n")
	if _, err := workerSubtitleOutput(t.Context(), bin, nil, 8); err == nil {
		t.Fatal("oversized staging output was accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := workerSubtitleOutput(ctx, bin, nil, 8); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled staging: %v", err)
	}
}

func formatWorkerSeek(seek float64) string {
	if seek == 0 {
		return "startup"
	}
	return "seek"
}
func workerBurnInFrames(t *testing.T, ctx context.Context, ffmpeg, dir string) []byte {
	t.Helper()
	out, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-i", filepath.Join(dir, "stream.m3u8"), "-map", "0:v:0", "-f", "framemd5", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("decode burn-in output: %v: %s", err, out)
	}
	return out
}
