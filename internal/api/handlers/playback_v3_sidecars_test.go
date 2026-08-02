package handlers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestAttachExternalTextSidecarsV3IncludesOnlyNegotiatedReadableText(t *testing.T) {
	dir := t.TempDir()
	srtPath := filepath.Join(dir, "movie.en.srt")
	vttPath := filepath.Join(dir, "movie.fr.vtt")
	emptyPath := filepath.Join(dir, "movie.de.srt")
	assPath := filepath.Join(dir, "movie.ja.ass")
	for path, body := range map[string]string{
		srtPath:   "1\n00:00:01,000 --> 00:00:02,000\nHello\n",
		vttPath:   "WEBVTT\n\n00:01.000 --> 00:02.000\nBonjour\n",
		emptyPath: "",
		assPath:   "[Script Info]\nTitle: Test\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	file := &models.MediaFile{
		ID: 42,
		ExternalSubtitles: []models.ExternalSubtitle{
			{Path: srtPath, Format: "subrip", Language: "en"},
			{Path: vttPath, Format: "webvtt", Language: "fr"},
			{Path: emptyPath, Format: "srt", Language: "de"},
			{Path: filepath.Join(dir, "missing.srt"), Format: "srt", Language: "es"},
			{Path: assPath, Format: "ass", Language: "ja"},
		},
		SubtitleTracks: []models.SubtitleTrack{{Index: 0, Codec: "subrip", Language: "it"}},
	}
	request := playback.StartRequestV3{
		ClientFeatures: []string{playback.FeatureExternalTextSidecarSetV3},
		ClientPlaybackContext: playback.ClientPlaybackContextV3{
			Features: []string{playback.FeatureExternalTextSidecarSetV3},
		},
	}
	plan := &playback.PlanV3{Timeline: playback.TimelineV3{StreamOriginSeconds: 12.5}}

	attachExternalTextSidecarsV3(request, "session", file, plan)

	if len(plan.Subtitle.Sidecars) != 2 {
		t.Fatalf("sidecars = %#v, want two readable external text tracks", plan.Subtitle.Sidecars)
	}
	want := []playback.SubtitleSidecarV3{
		{
			TrackID: "file:42:subtitle:0", Index: 0,
			URL:      "/stream/session/subtitles/0.srt?file_id=42",
			MIMEType: "application/x-subrip", Format: "srt", TimingOriginSeconds: 12.5,
		},
		{
			TrackID: "file:42:subtitle:1", Index: 1,
			URL:      "/stream/session/subtitles/1.vtt?file_id=42",
			MIMEType: "text/vtt", Format: "vtt", TimingOriginSeconds: 12.5,
		},
	}
	for i := range want {
		if got := plan.Subtitle.Sidecars[i]; got != want[i] {
			t.Fatalf("sidecar[%d] = %#v, want %#v", i, got, want[i])
		}
	}
}

func TestAttachExternalTextSidecarsV3RequiresFeatureInBothLocations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.en.srt")
	if err := os.WriteFile(path, []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := &models.MediaFile{ID: 42, ExternalSubtitles: []models.ExternalSubtitle{{Path: path, Format: "srt"}}}
	tests := []struct {
		name    string
		request playback.StartRequestV3
	}{
		{name: "neither"},
		{name: "top level only", request: playback.StartRequestV3{ClientFeatures: []string{playback.FeatureExternalTextSidecarSetV3}}},
		{name: "context only", request: playback.StartRequestV3{ClientPlaybackContext: playback.ClientPlaybackContextV3{Features: []string{playback.FeatureExternalTextSidecarSetV3}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := &playback.PlanV3{}
			attachExternalTextSidecarsV3(tt.request, "session", file, plan)
			if len(plan.Subtitle.Sidecars) != 0 {
				t.Fatalf("unnegotiated sidecars = %#v", plan.Subtitle.Sidecars)
			}
		})
	}
}
