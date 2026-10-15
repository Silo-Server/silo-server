package transcodenode

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/chapterthumbs"
	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/nodecontrol/nodecontroltest"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// integrationCatalog approves its fixture files, by media file id.
type integrationCatalog map[int]string

func (c integrationCatalog) IsActiveFile(_ context.Context, id int, path string) (bool, error) {
	return c[id] != "" && c[id] == path, nil
}

func (integrationCatalog) IsActiveTheme(context.Context, int64, string) (bool, error) {
	return false, nil
}

// integrationNode is a node whose sources open through a real core's source
// endpoints with one source access mode.
type integrationNode struct {
	name   string
	server *Server
	relay  *nodesource.Relay
	grants *nodesource.Grants
	files  integrationCatalog
}

// integrationNodeID is the node every integration core grants sources to.
const integrationNodeID = 7

// grant mints the node's grant for fixture file id.
func (n integrationNode) grant(t *testing.T, id int) string {
	t.Helper()
	grant, err := n.grants.ForFile(t.Context(), integrationNodeID, &models.MediaFile{ID: id, FilePath: n.files[id]})
	if err != nil {
		t.Fatal(err)
	}
	return grant
}

// integrationNodes starts a core that serves files and returns two nodes
// running ffmpeg on them: one reading them from its own mount, one through
// the loopback relay. Neither node has hardware acceleration.
func integrationNodes(t *testing.T, ffmpeg string, files integrationCatalog) (local, core integrationNode) {
	t.Helper()
	return integrationNodesWithCoreFFmpeg(t, ffmpeg, ffmpeg, files)
}

// integrationNodesWithCoreFFmpeg is integrationNodes whose core builds
// subtitle bundles with coreFFmpeg.
func integrationNodesWithCoreFFmpeg(t *testing.T, ffmpeg, coreFFmpeg string, files integrationCatalog) (local, core integrationNode) {
	t.Helper()
	grants := nodesource.NewGrants(func() string { return "integration-grant-secret" })
	coreDir := t.TempDir()
	coreURL, nodeKey := nodecontroltest.SourceCore(t, integrationNodeID, nodecontrol.Options{
		Grants: grants, Files: files,
		Subtitles:  playback.NewSubtitleCache(func() string { return coreDir }),
		FFmpegPath: func() string { return coreFFmpeg },
	})
	newNode := func(mode string) integrationNode {
		relay, err := nodesource.NewRelay(nodesource.RelayOptions{Open: openThrough(t, coreURL, nodeKey), CoreURL: coreURL,
			NodeID: integrationNodeID, NodeKey: nodeKey, Mode: func() string { return mode }})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = relay.Close() })
		server := newTestServer(t)
		server.tracker = nodesessions.NewTracker(nil, "http://node", "node", "transcode")
		server.watcher.Config().Playback.FFmpegPath = ffmpeg
		server.watcher.Config().Playback.HWAccel = playback.HWAccelNone
		server.sources = RelaySources(relay)
		return integrationNode{name: mode, server: server, relay: relay, grants: grants, files: files}
	}
	return newNode(nodesource.ModeLocal), newNode(nodesource.ModeCore)
}

// openThrough is a relay's Open through the node's control-plane client, as
// the node wires it.
func openThrough(t *testing.T, coreURL, nodeKey string) nodesource.OpenFunc {
	t.Helper()
	client, err := nodecontrol.NewClient(coreURL, nodeKey)
	if err != nil {
		t.Fatal(err)
	}
	return client.OpenSource
}

// requireFFmpeg returns the paths of ffmpeg and ffprobe, or skips the test.
func requireFFmpeg(t *testing.T) (ffmpeg, ffprobe string) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobe, err = exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	return ffmpeg, ffprobe
}

// hasEncoder reports whether ffmpeg lists the named encoder.
func hasEncoder(ffmpeg, name string) bool {
	encoders, err := exec.Command(ffmpeg, "-hide_banner", "-encoders").Output()
	return err == nil && bytes.Contains(encoders, []byte(" "+name+" "))
}

// TestCoreSourceAccessMatchesLocal runs real FFmpeg and ffprobe on a small
// generated file, once reading it from the node's own mount and once through
// the loopback relay from the core's source endpoints, and requires the same
// decoded output from both: probing, decoding, a seek, a progressive remux,
// and the node's own jobs (HLS start, download prepare, chapter frame and
// media-sample window).
func TestCoreSourceAccessMatchesLocal(t *testing.T) {
	ffmpeg, ffprobe := requireFFmpeg(t)
	videoCodec := "mpeg4"
	if hasEncoder(ffmpeg, "libx264") {
		videoCodec = "libx264"
	}
	fixture := filepath.Join(t.TempDir(), "fixture.mkv")
	run(t, ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=4", "-c:v", videoCodec, "-g", "25", "-c:a", "aac", "-b:a", "64k",
		"-shortest", fixture)

	const fileID = 42
	local, core := integrationNodes(t, ffmpeg, integrationCatalog{fileID: fixture})
	fixtureGrant := func(node integrationNode) string { return node.grant(t, fileID) }

	open := func(node integrationNode) *nodesource.Handle {
		t.Helper()
		h, err := node.relay.Open(t.Context(), fixtureGrant(node))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = h.Close() })
		return h
	}
	localInput, coreInput := open(local), open(core)
	if localInput.Input() != fixture || !strings.HasPrefix(coreInput.Input(), "http://127.0.0.1:") {
		t.Fatalf("inputs = %q, %q", localInput.Input(), coreInput.Input())
	}

	same := func(name string, produce func(input string) string) {
		t.Helper()
		want, got := produce(localInput.Input()), produce(coreInput.Input())
		if want == "" || got != want {
			t.Fatalf("%s differs through the relay:\nlocal:\n%.2000s\ncore:\n%.2000s", name, want, got)
		}
	}
	same("probe", func(input string) string {
		return run(t, ffprobe, "-v", "error", "-show_entries", "format=duration,size:stream=codec_name,nb_frames", "-of", "json", input)
	})
	same("decode", func(input string) string { return frameHashes(t, ffmpeg, input) })
	same("seek", func(input string) string {
		return run(t, ffmpeg, "-v", "error", "-ss", "2", "-i", input, "-map", "0:v", "-frames:v", "10", "-f", "framemd5", "-")
	})
	same("progressive remux", func(input string) string {
		rec := httptest.NewRecorder()
		if err := playback.ServeRemuxWithOptions(rec, httptest.NewRequest(http.MethodGet, "/remux", nil), input, "mp4", 0, true, 0, 0,
			playback.RemuxServeOptions{FFmpegPath: ffmpeg}); err != nil {
			t.Fatal(err)
		}
		return frameHashes(t, ffmpeg, writeTemp(t, "remux.mp4", rec.Body.Bytes()))
	})

	// The node's own jobs, each opening its source with the grant.
	nodeJobs := func(node integrationNode, name string) map[string]string {
		results := map[string]string{}

		body, _ := json.Marshal(downloadprepare.Request{ArtifactID: "artifact-" + name, SourceGrant: fixtureGrant(node), TargetCodecVideo: "copy", TargetCodecAudio: "copy"})
		rec := httptest.NewRecorder()
		node.server.handleDownloadPrepare(rec, httptest.NewRequest(http.MethodPost, "/downloads/prepare", bytes.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s download prepare = %d %s", name, rec.Code, rec.Body.String())
		}
		results["download prepare"] = frameHashes(t, ffmpeg, filepath.Join(node.server.artifactRoot, "artifact-"+name+".mp4"))

		body, _ = json.Marshal(chapterthumbs.RemoteExtractRequest{SourceGrant: fixtureGrant(node), SeekSeconds: 1.5})
		rec = httptest.NewRecorder()
		node.server.handleChapterThumbnailExtract(rec, httptest.NewRequest(http.MethodPost, "/chapter-thumbnails/extract", bytes.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s chapter frame = %d %s", name, rec.Code, rec.Body.String())
		}
		results["chapter frame"] = frameHashes(t, ffmpeg, writeTemp(t, "frame.jpg", rec.Body.Bytes()))

		body, _ = json.Marshal(mediasample.Request{SourceGrant: fixtureGrant(node), Input: fixture,
			Window: &mediasample.Window{StartSeconds: 1, DurationSeconds: 2},
			Audio:  &mediasample.AudioOutput{Speech: &mediasample.SpeechParams{}}})
		rec = postMediaSample(t, node.server, body, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s media sample = %d %s", name, rec.Code, rec.Body.String())
		}
		results["media sample window"] = rec.Body.String()

		if videoCodec == "libx264" {
			sessionID := "integration-" + name
			body, _ = json.Marshal(TranscodeStartRequest{
				SessionID: sessionID, SourceGrant: fixtureGrant(node), TargetCodecVideo: "copy", TargetCodecAudio: "aac",
				CopyFMP4RecipeVersion: playback.CopyFMP4RecipeVersion, SegmentDuration: 2, TotalDuration: 4, RequireReady: true,
			})
			rec = httptest.NewRecorder()
			node.server.handleStart(rec, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body)))
			if rec.Code != http.StatusAccepted {
				t.Fatalf("%s transcode start = %d %s", name, rec.Code, rec.Body.String())
			}
			node.server.mu.RLock()
			session := node.server.sessions[sessionID]
			node.server.mu.RUnlock()
			deadline := time.Now().Add(time.Minute)
			for session.IsRunning() {
				if time.Now().After(deadline) {
					t.Fatalf("%s transcode did not finish", name)
				}
				time.Sleep(10 * time.Millisecond)
			}
			playlist := filepath.Join(node.server.sessionOutputDir(sessionID), "stream.m3u8")
			results["hls video"] = run(t, ffmpeg, "-v", "error", "-i", playlist, "-map", "0:v", "-f", "framemd5", "-")
			stopSession(t, node.server, sessionID)
		}
		return results
	}
	want, got := nodeJobs(local, "local"), nodeJobs(core, "core")
	for job, result := range want {
		if result == "" || got[job] != result {
			t.Errorf("%s differs through the relay:\nlocal:\n%.1500s\ncore:\n%.1500s", job, result, got[job])
		}
	}
	if coreInput.Err() != nil {
		t.Fatalf("the relayed source failed: %v", coreInput.Err())
	}
}

// frameHashes decodes every stream of input to per-frame MD5s.
func frameHashes(t *testing.T, ffmpeg, input string) string {
	t.Helper()
	return run(t, ffmpeg, "-v", "error", "-i", input, "-map", "0", "-f", "framemd5", "-")
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	var stderr bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", filepath.Base(name), strings.Join(args, " "), err, stderr.String())
	}
	return string(out)
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
