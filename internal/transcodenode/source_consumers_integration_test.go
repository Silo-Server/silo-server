package transcodenode

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediaprobe"
	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// The Go-side consumers of a job's source, run with real FFmpeg on generated
// fixtures by a node reading them from its own mount and by one reading them
// through the relay. Each must give the same output both ways.

// startSession starts req on node and waits for FFmpeg to finish, returning
// the session's output directory and its playback manifest. The session is
// stopped when the test ends.
func startSession(t *testing.T, node integrationNode, req TranscodeStartRequest) (dir string, manifest []byte) {
	t.Helper()
	body, _ := json.Marshal(req)
	rec := httptest.NewRecorder()
	node.server.handleStart(rec, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%s start %s = %d %s", node.name, req.SessionID, rec.Code, rec.Body.String())
	}
	node.server.mu.RLock()
	session := node.server.sessions[req.SessionID]
	node.server.mu.RUnlock()
	t.Cleanup(func() { stopSession(t, node.server, req.SessionID) })
	deadline := time.Now().Add(time.Minute)
	for session.IsRunning() {
		if time.Now().After(deadline) {
			t.Fatalf("%s session %s did not finish", node.name, req.SessionID)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := session.WaitError(); err != nil {
		t.Fatalf("%s session %s: %v", node.name, req.SessionID, err)
	}
	manifest, err := session.BuildPlaybackManifest("", "")
	if err != nil {
		t.Fatalf("%s session %s manifest: %v", node.name, req.SessionID, err)
	}
	return node.server.sessionOutputDir(req.SessionID), manifest
}

// hlsVideo decodes a finished session's video to per-frame MD5s.
func hlsVideo(t *testing.T, ffmpeg, dir string) string {
	t.Helper()
	return run(t, ffmpeg, "-v", "error", "-i", filepath.Join(dir, "stream.m3u8"), "-map", "0:v", "-f", "framemd5", "-")
}

// bothModes runs produce on each node and requires the same non-empty result.
func bothModes(t *testing.T, local, core integrationNode, produce func(node integrationNode) string) string {
	t.Helper()
	want, got := produce(local), produce(core)
	if want == "" || got != want {
		t.Fatalf("relayed source differs from local:\nlocal:\n%.1500s\ncore:\n%.1500s", want, got)
	}
	return want
}

// A copy-video session with a Matroska index serves the playlist planned from
// the index, read through the relay's ReaderAt and cached under the source's
// identity: the first start falls back while the index is verified in the
// background, and a later start plans.
func TestCoreSourceKeyframePlaylistMatchesLocal(t *testing.T) {
	ffmpeg, _ := requireFFmpeg(t)
	if !hasEncoder(ffmpeg, "libx264") {
		t.Skip("ffmpeg has no libx264 encoder for a copyable H.264 fixture")
	}
	fixture := filepath.Join(t.TempDir(), "indexed.mkv")
	run(t, ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=8",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=8", "-c:v", "libx264", "-g", "25", "-c:a", "aac", "-b:a", "64k",
		"-shortest", fixture)
	const fileID = 51
	local, core := integrationNodes(t, ffmpeg, integrationCatalog{fileID: fixture})

	results := map[string]string{}
	bothModes(t, local, core, func(node integrationNode) string {
		deadline := time.Now().Add(30 * time.Second)
		for attempt := 0; ; attempt++ {
			req := TranscodeStartRequest{
				SessionID: fmt.Sprintf("planned-%s-%d", node.name, attempt), SourceGrant: node.grant(t, fileID),
				TargetCodecVideo: "copy", TargetCodecAudio: "aac", CopyFMP4RecipeVersion: playback.CopyFMP4RecipeVersion,
				KeyframePlaylist: true, SegmentDuration: 2, TotalDuration: 8, RequireReady: true, HWAccel: playback.HWAccelNone,
			}
			dir, manifest := startSession(t, node, req)
			var record struct {
				SourceIdentity string    `json:"source_identity"`
				Planned        bool      `json:"planned"`
				Durations      []float64 `json:"durations"`
			}
			data, err := os.ReadFile(node.server.copyPlanRecordPath(req.SessionID))
			if err != nil || json.Unmarshal(data, &record) != nil {
				t.Fatalf("%s plan record %s: %v", node.name, data, err)
			}
			if record.Planned {
				if !bytes.Contains(manifest, []byte("#EXT-X-PLAYLIST-TYPE:VOD")) || !bytes.Contains(manifest, []byte("#EXT-X-ENDLIST")) {
					t.Fatalf("%s planned session manifest:\n%s", node.name, manifest)
				}
				h, err := node.relay.Open(t.Context(), node.grant(t, fileID))
				if err != nil {
					t.Fatal(err)
				}
				identity := h.Identity()
				_ = h.Close()
				if record.SourceIdentity != identity {
					t.Fatalf("%s plan record names %q, want the source identity %q", node.name, record.SourceIdentity, identity)
				}
				results[node.name] = hlsVideo(t, ffmpeg, dir)
				return fmt.Sprintf("%v\n%s", record.Durations, manifest)
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s never planned the keyframe playlist after %d starts", node.name, attempt+1)
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	if results["local"] == "" || results["core"] != results["local"] {
		t.Fatalf("planned HLS video differs:\nlocal:\n%.1500s\ncore:\n%.1500s", results["local"], results["core"])
	}
}

// fontconfigWithoutFonts hides the system's fonts from libass, so a subtitle
// renders only with the fonts its source carries.
func fontconfigWithoutFonts(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	conf := filepath.Join(dir, "fonts.conf")
	if err := os.WriteFile(conf, []byte(`<?xml version="1.0"?><fontconfig><cachedir>`+dir+`</cachedir></fontconfig>`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FONTCONFIG_FILE", conf)
}

// attachableFont returns a TrueType font and its family name, or skips.
func attachableFont(t *testing.T) (path, family string) {
	t.Helper()
	for _, candidate := range []struct{ path, family string }{
		{"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf", "DejaVu Sans"},
		{"/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf", "Liberation Sans"},
		{"/usr/share/fonts/TTF/DejaVuSans.ttf", "DejaVu Sans"},
	} {
		if _, err := os.Stat(candidate.path); err == nil {
			return candidate.path, candidate.family
		}
	}
	t.Skip("no known TrueType font under /usr/share/fonts to attach")
	return "", ""
}

// A text subtitle burned in from a relayed source renders as it does from
// the node's own mount: the selected track is staged as an ASS script with
// the source's attached font, which, with the system's fonts hidden, is the
// only font either way.
func TestCoreSourceTextBurnInMatchesLocal(t *testing.T) {
	ffmpeg, _ := requireFFmpeg(t)
	if !hasEncoder(ffmpeg, "libx264") {
		t.Skip("ffmpeg has no libx264 encoder for the burn-in transcode")
	}
	font, family := attachableFont(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "sub.ass")
	if err := os.WriteFile(script, []byte("[Script Info]\nScriptType: v4.00+\nPlayResX: 320\nPlayResY: 240\n\n[V4+ Styles]\n"+
		"Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n"+
		"Style: Default,"+family+",28,&H0000FFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,0,2,10,10,10,1\n\n"+
		"[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n"+
		"Dialogue: 0,0:00:00.20,0:00:01.80,Default,,0,0,0,,Hello: relayed\nDialogue: 0,0:00:02.20,0:00:03.80,Default,,0,0,0,,Second line\n"), 0o600); err != nil { //nolint:misspell // ASS event keyword
		t.Fatal(err)
	}
	fixture := filepath.Join(dir, "subtitled.mkv")
	run(t, ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=4", "-i", script,
		"-attach", font, "-metadata:s:t:0", "mimetype=application/x-truetype-font", "-metadata:s:t:0", "filename=../../escape.ttf",
		"-map", "0", "-map", "1", "-map", "2", "-c:v", "libx264", "-g", "25", "-c:a", "aac", "-c:s", "ass", "-shortest", fixture)
	fontconfigWithoutFonts(t)
	const fileID = 52
	local, core := integrationNodes(t, ffmpeg, integrationCatalog{fileID: fixture})

	start := func(node integrationNode, id string, burnIn bool) string {
		req := TranscodeStartRequest{
			SessionID: id + "-" + node.name, SourceGrant: node.grant(t, fileID), TargetCodecVideo: "h264", TargetCodecAudio: "aac",
			SegmentDuration: 2, TotalDuration: 4, RequireReady: true, HWAccel: playback.HWAccelNone, SubtitleTrackIndex: -1,
		}
		if burnIn {
			req.SubtitleBurnIn, req.SubtitleTrackIndex, req.SubtitleCodec = true, 0, "ass"
		}
		dir, _ := startSession(t, node, req)
		return hlsVideo(t, ffmpeg, dir)
	}
	burned := bothModes(t, local, core, func(node integrationNode) string { return start(node, "burn-in", true) })
	if plain := start(local, "plain", false); plain == burned {
		t.Fatal("the burned-in subtitle rendered nothing")
	}
	staged, _ := filepath.Glob(filepath.Join(core.server.transcodeDir, playback.SubtitleStagingDirName, "*", "fonts", "*"))
	if len(staged) != 1 || filepath.Base(staged[0]) != "attachment-3.ttf" {
		t.Fatalf("staged fonts %q, want the attachment under a generated name", staged)
	}
	if escaped, _ := filepath.Glob(filepath.Join(core.server.transcodeDir, "*", "escape.ttf")); len(escaped) != 0 {
		t.Fatalf("an attachment's own filename became a path: %q", escaped)
	}
}

// openSubtitlesHash is the scanner's file hash: the size plus the 64-bit
// little-endian words of the first and last 64 KiB.
func openSubtitlesHash(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || len(data) < 128<<10 {
		t.Fatalf("hash fixture: %d bytes, %v", len(data), err)
	}
	hash := uint64(len(data))
	for _, block := range [][]byte{data[:64<<10], data[len(data)-64<<10:]} {
		for i := 0; i < len(block); i += 8 {
			hash += binary.LittleEndian.Uint64(block[i : i+8])
		}
	}
	return fmt.Sprintf("%016x", hash)
}

// A tone-map recipe on a relayed source validates its frozen revision through
// the relay (stat, the OpenSubtitles hash's two ranged reads, the live primary
// track) and runs the representative-frame preflight on the loopback URL,
// then tone-maps as the node's own mount does.
func TestCoreSourceToneMapPreflightMatchesLocal(t *testing.T) {
	ffmpeg, ffprobe := requireFFmpeg(t)
	if !hasEncoder(ffmpeg, "libx265") || !hasEncoder(ffmpeg, "libx264") {
		t.Skip("ffmpeg lacks libx265 for an HDR10 fixture or libx264 for the SDR output")
	}
	filters, _ := exec.Command(ffmpeg, "-hide_banner", "-filters").Output()
	if !bytes.Contains(filters, []byte(" zscale ")) || !bytes.Contains(filters, []byte(" tonemap ")) {
		t.Skip("ffmpeg lacks the zscale and tonemap filters of software tone-mapping")
	}
	fixture := filepath.Join(t.TempDir(), "hdr10.mkv")
	run(t, ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=640x360:r=24:d=3", "-vf", "format=yuv420p10le",
		"-c:v", "libx265", "-b:v", "3M", "-x265-params",
		"log-level=error:keyint=24:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:hdr10=1:"+
			"master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1):max-cll=1000,400",
		"-color_primaries", "bt2020", "-color_trc", "smpte2084", "-colorspace", "bt2020nc", fixture)
	info, err := os.Stat(fixture)
	if err != nil {
		t.Fatal(err)
	}
	track, err := mediaprobe.ProbePrimaryVideoTrack(t.Context(), ffprobe, fixture)
	if err != nil {
		t.Fatal(err)
	}
	modified, probed := info.ModTime().UTC().Truncate(time.Microsecond), time.Now().UTC()
	const fileID = 53
	revision := tonemap.RevisionForFile(&models.MediaFile{
		ID: fileID, FileSize: info.Size(), FileModifiedAt: &modified, FileHash: openSubtitlesHash(t, fixture),
		ProbeUpdatedAt: &probed, VideoTracks: []models.VideoTrack{track},
	})
	local, core := integrationNodes(t, ffmpeg, integrationCatalog{fileID: fixture})

	// The frozen revision checks out through the relay as on the mount: the
	// stat, the hash's two ranged reads, and the live primary track.
	t.Run("source revision", func(t *testing.T) {
		bothModes(t, local, core, func(node integrationNode) string {
			h, err := node.relay.Open(t.Context(), node.grant(t, fileID))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = h.Close() }()
			if err := revision.ValidatePath(t.Context(), h.Input()); err != nil {
				t.Fatalf("%s ValidatePath = %v", node.name, err)
			}
			changed := revision
			changed.FileHash = "0000000000000000"
			if err := changed.ValidatePath(t.Context(), h.Input()); !errors.Is(err, tonemap.ErrSourceRevisionChanged) {
				t.Fatalf("%s ValidatePath(another hash) = %v, want ErrSourceRevisionChanged", node.name, err)
			}
			live, err := mediaprobe.ProbePrimaryVideoTrack(t.Context(), ffprobe, h.Input())
			if err != nil {
				t.Fatalf("%s live track: %v", node.name, err)
			}
			if err := tonemap.ValidateLivePrimaryVideoTrack(revision, live); err != nil {
				t.Fatalf("%s live track = %v", node.name, err)
			}
			encoded, _ := json.Marshal(live)
			return string(encoded)
		})
	})

	// The representative-frame preflight reads frames from the loopback URL
	// as from the mount. Its runner refuses -version, so no verdict is cached
	// between the two, and drops the Dolby Vision side-data deletions FFmpeg
	// before 7 lacks (no HDR10 frame carries them).
	t.Run("preflight", func(t *testing.T) {
		bothModes(t, local, core, func(node integrationNode) string {
			h, err := node.relay.Open(t.Context(), node.grant(t, fileID))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = h.Close() }()
			var commands []string
			runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if len(args) == 1 && args[0] == "-version" {
					return nil, errors.New("uncached")
				}
				for i, arg := range args {
					args[i] = strings.ReplaceAll(arg, ",sidedata=mode=delete:type=DOVI_RPU_BUFFER,sidedata=mode=delete:type=DOVI_METADATA", "")
				}
				commands = append(commands, strings.Join(args, " "))
				return exec.CommandContext(ctx, name, args...).CombinedOutput()
			}
			err = tonemap.ValidateSourceWithRunner(t.Context(), tonemap.SourcePreflightRequest{
				FFmpegPath: ffmpeg, InputPath: h.Input(), DurationSeconds: 3, SourceBitDepth: 10,
				Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterHable,
				Kind: tonemap.SourcePQ, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3, SourceRevision: revision,
			}, runner)
			if err != nil {
				t.Fatalf("%s preflight: %v\n%s", node.name, err, strings.Join(commands, "\n"))
			}
			read := 0
			for _, command := range commands {
				if strings.Contains(command, h.Input()) {
					read++
					if h.Remote() && !strings.Contains(command, "-protocol_whitelist http,tcp -format_whitelist "+nodesource.AdmittedFormats) {
						t.Fatalf("relayed preflight command not held to the relay: %s", command)
					}
				}
			}
			if read == 0 {
				t.Fatalf("%s preflight never read the source: %q", node.name, commands)
			}
			return fmt.Sprint(read)
		})
	})

	// The node's tone-mapped start: the same validation, the representative
	// frame preflight on the loopback URL, then the tone-mapped encode.
	t.Run("tone-mapped start", func(t *testing.T) {
		capabilities, err := tonemap.Probe(t.Context(), ffmpeg, tonemap.BackendSoftware, "")
		if err != nil || !capabilities.Supports(tonemap.ModeSoftware, tonemap.SourcePQ) {
			t.Skipf("ffmpeg cannot run the software tone-map executor (its smoke encodes fail; FFmpeg 7's sidedata types are needed): %v", err)
		}
		bothModes(t, local, core, func(node integrationNode) string {
			dir, _ := startSession(t, node, TranscodeStartRequest{
				SessionID: "tone-map-" + node.name, SourceGrant: node.grant(t, fileID),
				SourceVideoCodec: "hevc", SourceVideoBitDepth: 10, TargetCodecVideo: "h264", TargetCodecAudio: "aac",
				SegmentDuration: 2, TotalDuration: 3, RequireReady: true, HWAccel: playback.HWAccelNone, SubtitleTrackIndex: -1,
				ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware, ToneMapSourceKind: tonemap.SourcePQ,
				ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3, ToneMapPreflightRequired: true,
				ToneMapSourceRevision: revision,
			})
			return hlsVideo(t, ffmpeg, dir)
		})
	})
}

// The Dolby Vision strip probe runs on a relayed source instead of failing
// open unprobed, with its input held to the relay. On a source without an
// RPU it keeps the strip; an FFmpeg with the dovi_rpu filter reaches that
// verdict and caches it under the source's identity, so a second job at
// another loopback URL runs nothing.
func TestCoreSourceDVStripProbeRuns(t *testing.T) {
	ffmpeg, _ := requireFFmpeg(t)
	if !hasEncoder(ffmpeg, "libx265") {
		t.Skip("ffmpeg lacks libx265 for an HEVC fixture")
	}
	fixture := filepath.Join(t.TempDir(), "hevc.mkv")
	run(t, ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=320x180:r=24:d=2", "-c:v", "libx265", "-x265-params", "log-level=error", fixture)
	const fileID = 54
	_, core := integrationNodes(t, ffmpeg, integrationCatalog{fileID: fixture})
	bsfs, _ := exec.Command(ffmpeg, "-hide_banner", "-bsfs").Output()
	conclusive := bytes.Contains(bsfs, []byte("dovi_rpu"))

	dir := t.TempDir()
	bin, argsLog := filepath.Join(dir, "ffmpeg"), filepath.Join(dir, "runs")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\" >> "+argsLog+"\nexec "+ffmpeg+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	probe := playback.NewDVRPUProbe()
	for range 2 {
		h, err := core.relay.Open(t.Context(), core.grant(t, fileID))
		if err != nil {
			t.Fatal(err)
		}
		if !probe.CanStrip(context.Background(), bin, h.Input()) {
			t.Fatal("a source without an RPU lost its strip")
		}
		_ = h.Close()
	}
	logged, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatalf("the probe never ran: %v", err)
	}
	runs := strings.Split(strings.TrimSpace(string(logged)), "\n")
	if !strings.Contains(runs[0], "-protocol_whitelist http,tcp -format_whitelist "+nodesource.AdmittedFormats+" -i http://127.0.0.1:") {
		t.Fatalf("probe argv %q, want the relayed input held to the relay", runs[0])
	}
	if conclusive && len(runs) != 1 {
		t.Fatalf("the verdict was not reused for the same source at another URL: %d runs", len(runs))
	}
	if !conclusive {
		// FFmpeg without the filter reaches no verdict, which is not cached.
		t.Logf("ffmpeg lacks the dovi_rpu filter: the probe ran %d times, inconclusive", len(runs))
	}
}

// Sampled reads go through the concat demuxer in its relayed form: trickplay
// sheets and a media-sample Samples run give the same output both ways, as
// does a subtitle-sync speech window.
func TestCoreSourceSampledReadsMatchLocal(t *testing.T) {
	ffmpeg, _ := requireFFmpeg(t)
	fixture := filepath.Join(t.TempDir(), "sampled.mkv")
	run(t, ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=320x180:r=24:d=6",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=6", "-c:v", "mpeg4", "-g", "24", "-c:a", "aac", "-shortest", fixture)
	const fileID = 55
	local, core := integrationNodes(t, ffmpeg, integrationCatalog{fileID: fixture})
	software := []mediasample.Attempt{{TimeoutSeconds: 60}}

	bothModes(t, local, core, func(node integrationNode) string {
		body, _ := json.Marshal(mediasample.Request{
			SourceGrant: node.grant(t, fileID), Input: fixture, Samples: &mediasample.Samples{Seconds: []float64{1, 3, 5}},
			Sheets:   &mediasample.SheetsOutput{TileWidth: 64, TileHeight: 36, Columns: 3, Rows: 1, Quality: 80},
			Attempts: software,
		})
		rec := postTrickplay(t, node.server, string(body))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s trickplay = %d %s", node.name, rec.Code, rec.Body.String())
		}
		var result mediasample.Result
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || len(result.Sheets) != 1 || result.SheetFrames.Decoded != 3 {
			t.Fatalf("%s trickplay result %+v, %v", node.name, result.SheetFrames, err)
		}
		return rec.Body.String()
	})
	bothModes(t, local, core, func(node integrationNode) string {
		body, _ := json.Marshal(mediasample.Request{
			SourceGrant: node.grant(t, fileID), Input: fixture, Samples: &mediasample.Samples{Seconds: []float64{1, 3, 5}},
			Stats:    &mediasample.StatsOutput{CropWidth: 1, CropHeight: 1, Width: 64, BlackThresholds: []int{20}},
			Attempts: software,
		})
		rec := postMediaSample(t, node.server, body, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s media samples = %d %s", node.name, rec.Code, rec.Body.String())
		}
		var result mediasample.Result
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || len(result.Frames) != 3 {
			t.Fatalf("%s media samples %s, %v", node.name, rec.Body.String(), err)
		}
		return rec.Body.String()
	})
	bothModes(t, local, core, func(node integrationNode) string {
		body, _ := json.Marshal(mediasample.Request{SourceGrant: node.grant(t, fileID), Input: fixture,
			Window: &mediasample.Window{StartSeconds: 2, DurationSeconds: 3},
			Audio:  &mediasample.AudioOutput{Speech: &mediasample.SpeechParams{}}})
		rec := postMediaSample(t, node.server, body, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s speech window = %d %s", node.name, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	})
}
