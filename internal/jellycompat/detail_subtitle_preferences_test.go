package jellycompat

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

func TestItemDetailSubtitlePreferenceCarriesIntoPlayback(t *testing.T) {
	for _, kind := range []string{"movie", "episode"} {
		for _, tc := range []struct {
			name, mode, audio                            string
			forced, downloaded, fileDefault, trackForced bool
			want                                         int
		}{
			{name: "always selects English", mode: "always", audio: "en", forced: true, want: 2},
			{name: "smart with English audio", mode: "auto", audio: "en", forced: true, want: -1},
			{name: "smart with foreign audio", mode: "auto", audio: "fr", forced: true, want: 2},
			{name: "off overrides file default", mode: "off", audio: "en", fileDefault: true, want: -1},
			{name: "forced only", mode: "off", audio: "en", forced: true, trackForced: true, want: 2},
			{name: "forced hidden", mode: "always", audio: "en", trackForced: true, want: -1},
			{name: "downloaded preferred", mode: "always", audio: "en", forced: true, downloaded: true, want: 4},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				version := catalog.FileVersion{FileID: 42, Container: "mkv", Duration: 100,
					VideoTracks:    []models.VideoTrack{{Codec: "h264"}},
					AudioTracks:    []models.AudioTrack{{Codec: "aac", Language: tc.audio, Default: true}},
					SubtitleTracks: []catalog.VersionSubtitleTrack{{Index: 2, Codec: "subrip", Language: "en", Default: tc.fileDefault, Forced: tc.trackForced}, {Index: 3, Codec: "subrip", Language: "fr"}},
				}
				detail := &upstreamItemDetail{ContentID: "item-1", Type: kind, Versions: []catalog.FileVersion{version}, SubtitleLanguage: "en", SubtitleMode: tc.mode, SubtitleModeSet: true, ShowForcedSubtitles: tc.forced}
				codec := NewResourceIDCodec()
				routeID := codec.EncodeStringID(EncodedIDItem, detail.ContentID)
				content := &stubContentService{detail: detail}
				repo := fakeSubtitleRepository{}
				if tc.downloaded {
					repo.downloaded = map[int][]subtitles.DownloadedSubtitle{42: {{MediaFileID: 42, Language: "en", Format: subtitles.FormatSRT}}}
				}
				h := NewItemsHandler(content, nil, codec, &config.Config{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, repo)
				rec := httptest.NewRecorder()
				h.HandleItem(rec, viewerRequest("GET", "/Items/"+routeID, "", "id", routeID, &Session{Token: "token-1"}))
				if rec.Code != 200 {
					t.Fatalf("detail: %d %s", rec.Code, rec.Body.String())
				}
				var dto baseItemDTO
				if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
					t.Fatal(err)
				}
				if len(dto.MediaSources) != 1 {
					t.Fatalf("sources = %d", len(dto.MediaSources))
				}
				selected := -1
				if index := dto.MediaSources[0].DefaultSubtitleStreamIndex; index != nil {
					selected = *index
				}
				if selected != tc.want {
					t.Fatalf("detail subtitle = %d, want %d", selected, tc.want)
				}
				playback := &PlaybackHandler{content: content, codec: codec, deviceProfiles: NewDeviceProfileStore(time.Hour, nil), playbackStore: NewPlaybackSessionStore(time.Hour, nil), SubtitleRepo: repo}
				body, _ := json.Marshal(map[string]int{"SubtitleStreamIndex": selected})
				for _, request := range []string{`{}`, string(body)} {
					response := postPlaybackInfo(t, playback, routeID, request)
					got := -1
					if index := response.MediaSources[0].DefaultSubtitleStreamIndex; index != nil {
						got = *index
					}
					if got != tc.want {
						t.Fatalf("request %s: playback subtitle = %d, want %d", request, got, tc.want)
					}
				}
				off := postPlaybackInfo(t, playback, routeID, `{"SubtitleStreamIndex":-1}`)
				if off.MediaSources[0].DefaultSubtitleStreamIndex != nil {
					t.Fatal("explicit Off was overridden by the preference")
				}
			})
		}
	}
}

func TestItemDetailUsesSavedJellyfinModeForCurrentProfile(t *testing.T) {
	store := newJellycompatUserStore(t)
	if err := store.SetSetting(t.Context(), configurationKey("profile-1"), `{"SubtitleMode":"Default"}`); err != nil {
		t.Fatal(err)
	}
	codec := NewResourceIDCodec()
	detail := &upstreamItemDetail{ContentID: "item-1", Type: "movie", SubtitleMode: "auto", SubtitleModeSet: true, SubtitleLanguage: "en", ShowForcedSubtitles: true,
		Versions: []catalog.FileVersion{{FileID: 42, Container: "mkv", VideoTracks: []models.VideoTrack{{Codec: "h264"}}, AudioTracks: []models.AudioTrack{{Language: "en", Codec: "aac", Default: true}}, SubtitleTracks: []catalog.VersionSubtitleTrack{{Index: 2, Language: "en", Codec: "subrip", Default: true}}}},
	}
	h := NewItemsHandler(&stubContentService{detail: detail}, nil, codec, &config.Config{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.storeProvider = compatTestUserStoreProvider{store: store}
	routeID := codec.EncodeStringID(EncodedIDItem, detail.ContentID)
	for _, tc := range []struct {
		profile string
		want    int
	}{{"profile-1", 2}, {"profile-2", -1}} {
		rec := httptest.NewRecorder()
		h.HandleItem(rec, viewerRequest("GET", "/Items/"+routeID, "", "id", routeID, &Session{StreamAppUserID: 1, ProfileID: tc.profile}))
		if rec.Code != 200 {
			t.Fatalf("detail: %d %s", rec.Code, rec.Body.String())
		}
		var dto baseItemDTO
		if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
			t.Fatal(err)
		}
		got := -1
		if index := dto.MediaSources[0].DefaultSubtitleStreamIndex; index != nil {
			got = *index
		}
		if got != tc.want {
			t.Fatalf("profile %s selected %d, want %d", tc.profile, got, tc.want)
		}
	}
}

// Smart follows the negotiated audio only while subtitles remain automatic.
// A client-provided Off cannot be distinguished from an intentional user choice.
func TestSmartSubtitlesFollowPlaybackAudioUnlessExplicitlyOff(t *testing.T) {
	for _, tc := range []struct {
		name, codec, body       string
		wantAudio, wantSubtitle int
	}{
		{"English audio", "aac", `{}`, 1, -1},
		{"requested French audio", "aac", `{"AudioStreamIndex":2}`, 2, 3},
		{"requested French audio with Off", "aac", `{"AudioStreamIndex":2,"SubtitleStreamIndex":-1}`, 2, -1},
		{"device falls back to French", "truehd", `{}`, 2, 3},
		{"device fallback with Off", "truehd", `{"SubtitleStreamIndex":-1}`, 2, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version := catalog.FileVersion{FileID: 42, Container: "mkv", VideoTracks: []models.VideoTrack{{Codec: "h264"}},
				AudioTracks:    []models.AudioTrack{{Codec: tc.codec, Language: "en", Default: true}, {Codec: "aac", Language: "fr"}},
				SubtitleTracks: []catalog.VersionSubtitleTrack{{Index: 3, Codec: "subrip", Language: "en", Default: true}},
			}
			detail := &upstreamItemDetail{ContentID: "item-1", Type: "movie", Versions: []catalog.FileVersion{version}, SubtitleMode: "auto", SubtitleModeSet: true, SubtitleLanguage: "en", ShowForcedSubtitles: true}
			codec := NewResourceIDCodec()
			h := &PlaybackHandler{content: &stubContentService{detail: detail}, codec: codec, deviceProfiles: NewDeviceProfileStore(time.Hour, nil), playbackStore: NewPlaybackSessionStore(time.Hour, nil)}
			response := postPlaybackInfo(t, h, codec.EncodeStringID(EncodedIDItem, detail.ContentID), tc.body)
			source := response.MediaSources[0]
			if source.DefaultAudioStreamIndex == nil || *source.DefaultAudioStreamIndex != tc.wantAudio {
				t.Fatalf("audio = %v, want %d", source.DefaultAudioStreamIndex, tc.wantAudio)
			}
			got := -1
			if source.DefaultSubtitleStreamIndex != nil {
				got = *source.DefaultSubtitleStreamIndex
			}
			if got != tc.wantSubtitle {
				t.Fatalf("subtitle = %d, want %d", got, tc.wantSubtitle)
			}
		})
	}
}
