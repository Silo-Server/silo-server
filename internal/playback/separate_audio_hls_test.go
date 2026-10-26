package playback

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeparateAudioHLSProducesVideoAndAudioPlaylists(t *testing.T) {
	opts := TranscodeOpts{InputPath: "/media/movie.mkv", OutputDir: t.TempDir(), TargetCodecVideo: "copy", SourceVideoCodec: "hevc", TargetCodecAudio: "copy", AudioTrackIndex: 1, SegmentDuration: 2}
	if err := json.Unmarshal([]byte(`{"separate_audio_hls":true}`), &opts); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(buildFFmpegArgs(opts), " ")
	for _, want := range []string{"-an", "stream.m3u8", "-map 0:a:1", "-hls_segment_type mpegts", "audio_%05d.ts", "audio.m3u8"} {
		if !strings.Contains(args, want) {
			t.Fatalf("separate audio output missing %q", want)
		}
	}
}

func TestSeparateAudioHLSManifestAuthorizesBothRenditions(t *testing.T) {
	s := &TranscodeSession{outputDir: t.TempDir(), opts: TranscodeOpts{SeparateAudioHLS: true, TargetCodecVideo: "copy", TargetBitrateKbps: 60000}}
	master, err := s.BuildPlaybackManifest("segment/", "st=fixture&cache=1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`TYPE=AUDIO,GROUP-ID="audio"`, `URI="master.m3u8?cache=1&rendition=audio&st=fixture"`, `AUDIO="audio"`, `master.m3u8?cache=1&rendition=video&st=fixture`} {
		if !strings.Contains(string(master), want) {
			t.Fatalf("master missing %q: %s", want, master)
		}
	}
}

func TestSeparateAudioHLSChildPreservesAuthorization(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"audio.m3u8": "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.016,\naudio_00000.ts\n#EXT-X-ENDLIST\n", "audio_00000.ts": "fixture"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := &TranscodeSession{outputDir: dir, opts: TranscodeOpts{SeparateAudioHLS: true, TargetCodecVideo: "copy"}}
	manifest, err := s.BuildPlaybackManifest("segment/", "rendition=audio&st=fixture")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), "segment/audio_00000.ts?st=fixture") {
		t.Fatalf("missing authorized audio segment: %s", manifest)
	}
}

func TestSeparateAudioHLSRelayRetainsAuthorization(t *testing.T) {
	master := []byte("#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,URI=\"?rendition=audio\"\n#EXT-X-STREAM-INF:BANDWIDTH=60000000,AUDIO=\"audio\"\n?rendition=video\n")
	got := string(AppendManifestQueryParam(master, "st", "fixture"))
	if !strings.Contains(got, `URI="?rendition=audio&st=fixture"`) {
		t.Fatalf("audio grant missing: %s", got)
	}
}

func TestSeparateAudioHLSMasterCarriesFrozenCodecs(t *testing.T) {
	opts := TranscodeOpts{SeparateAudioHLS: true, TargetCodecVideo: "copy"}
	if err := json.Unmarshal([]byte(`{"hls_codecs":"dvh1.08.06,mp4a.40.2","hls_video_range":"PQ"}`), &opts); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "init.mp4"), nativeHLSDVInitFixture(t), 0600); err != nil {
		t.Fatal(err)
	}
	s := &TranscodeSession{outputDir: dir, opts: opts}
	master, err := s.BuildPlaybackManifest("segment/", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(master), `CODECS="dvh1.08.06,mp4a.40.2",VIDEO-RANGE=PQ`) {
		t.Fatalf("frozen codecs missing: %s", master)
	}
}

func TestSeparateAudioHLSRecipeSurvivesReconstruction(t *testing.T) {
	opts := TranscodeOpts{SeparateAudioHLS: true, TargetCodecVideo: "copy", TargetCodecAudio: "copy", SessionID: "fixture"}
	card := NewRecipeCard(1, "profile", 42, "", opts)
	restored := RecipeCardFromClaims(new(card.ToClaims())).TranscodeOpts(t.TempDir(), "ffmpeg", nil)
	if !restored.SeparateAudioHLS {
		t.Fatal("reconstructed route lost separate audio")
	}
	if card.CopyFMP4RecipeVersion == CopyFMP4RecipeVersion {
		t.Fatal("old workers can accept the new byte recipe")
	}
}

func TestSeparateAudioHLSWaitsForAudioReadiness(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.0,\nseg_00000.m4s\n#EXT-X-ENDLIST\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &TranscodeSession{outputDir: dir, opts: TranscodeOpts{SeparateAudioHLS: true, TargetCodecVideo: "copy"}}
	if _, err := s.GetManifest(); !errors.Is(err, ErrManifestNotReady) {
		t.Fatalf("video-only readiness = %v", err)
	}
}

func TestSeparateAudioHLSRejectsConflictingRecipes(t *testing.T) {
	for _, opts := range []TranscodeOpts{
		{SeparateAudioHLS: true, TargetCodecVideo: "h264"},
		{SeparateAudioHLS: true, TargetCodecVideo: "copy", CopyVideoMPEGTS: true},
		{SeparateAudioHLS: true, TargetCodecVideo: "copy", HLSCodecs: "hevc\n#injected"},
	} {
		opts.FFmpegPath = "/unavailable/ffmpeg"
		opts.OutputDir = t.TempDir()
		_, err := StartTranscode(context.Background(), opts)
		if err == nil || !strings.Contains(err.Error(), "separate-audio HLS recipe") {
			t.Fatalf("invalid recipe reached execution: %v", err)
		}
	}
}
