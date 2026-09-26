package jellycompat

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type signalingFileResolver struct {
	requested chan int
}

func (r signalingFileResolver) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	r.requested <- id
	return &models.MediaFile{ID: id}, nil
}

// Jellyfin Web fetches embedded text subtitles from the server, so starting
// playback extracts them ahead of a mid-stream track switch.
func TestHandlePlaybackInfo_WarmsEmbeddedTextSubtitlesTheClientFetches(t *testing.T) {
	handler, routeID := newSubtitleSelectionHandler(t)
	resolver := signalingFileResolver{requested: make(chan int, 1)}
	handler.fileResolver = resolver
	handler.SubtitleCache = playback.NewSubtitleCache(t.TempDir)
	resp := postPlaybackInfo(t, handler, routeID, `{"DeviceProfile":{"SubtitleProfiles":[{"Format":"vtt","Method":"External"}]}}`)
	if !compatFetchesEmbeddedTextSubtitles(resp.MediaSources[0].MediaStreams) {
		t.Fatal("fixture should deliver an embedded text subtitle externally")
	}
	select {
	case id := <-resolver.requested:
		if id != 42 {
			t.Fatalf("warmed file %d, want 42", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PlaybackInfo did not start a subtitle warm")
	}
}

func TestCompatFetchesEmbeddedTextSubtitles(t *testing.T) {
	cases := []struct {
		name   string
		stream mediaStreamDTO
		want   bool
	}{
		{"embedded text fetched by the client", mediaStreamDTO{Type: "Subtitle", IsTextSubtitleStream: true, DeliveryMethod: "External"}, true},
		{"embedded text read from the stream", mediaStreamDTO{Type: "Subtitle", IsTextSubtitleStream: true, DeliveryMethod: "Embed"}, false},
		{"external file", mediaStreamDTO{Type: "Subtitle", IsExternal: true, IsTextSubtitleStream: true, DeliveryMethod: "External"}, false},
		{"bitmap subtitle", mediaStreamDTO{Type: "Subtitle", DeliveryMethod: "Encode"}, false},
		{"audio", mediaStreamDTO{Type: "Audio", DeliveryMethod: "External"}, false},
	}
	for _, tc := range cases {
		if got := compatFetchesEmbeddedTextSubtitles([]mediaStreamDTO{tc.stream}); got != tc.want {
			t.Errorf("%s = %t, want %t", tc.name, got, tc.want)
		}
	}
}

func TestCompatWarmsTextSubtitlesSkipsViewersWithSubtitlesOff(t *testing.T) {
	fetched := []mediaStreamDTO{{Type: "Subtitle", IsTextSubtitleStream: true, DeliveryMethod: "External"}}
	if compatWarmsTextSubtitles(compatSubtitleNone, fetched) {
		t.Fatal("warmed subtitles for a viewer with SubtitleMode None")
	}
	for _, mode := range []string{compatSubtitleDefault, compatSubtitleSmart, compatSubtitleAlways, compatSubtitleOnlyForced} {
		if !compatWarmsTextSubtitles(mode, fetched) {
			t.Fatalf("%s did not warm subtitles the client fetches", mode)
		}
	}
}

// Jellyfin Web plays the first source that direct plays, then direct streams,
// then transcodes, so that is the source worth warming.
func TestCompatLikelyPlayedSourceFollowsJellyfinWeb(t *testing.T) {
	cases := []struct {
		name    string
		sources []PlaybackMediaSource
		want    int
	}{
		{"no sources", nil, -1},
		{"direct play wins over order", []PlaybackMediaSource{{SupportsTranscoding: true}, {SupportsDirectStream: true}, {SupportsDirectPlay: true}}, 2},
		{"direct stream before transcoding", []PlaybackMediaSource{{SupportsTranscoding: true}, {SupportsDirectStream: true}}, 1},
		{"first transcodable source", []PlaybackMediaSource{{}, {SupportsTranscoding: true}}, 1},
		{"nothing playable falls back to the first", []PlaybackMediaSource{{}, {}}, 0},
	}
	for _, tc := range cases {
		if got := compatLikelyPlayedSource(tc.sources); got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, got, tc.want)
		}
	}
}
