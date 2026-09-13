package jellycompat

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"

	"context"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/go-chi/chi/v5"
)

func TestBitrateProbeHonorsSize(t *testing.T) {
	for _, tc := range []struct {
		query        string
		status, size int
	}{{"", 200, 102400}, {"?size=1", 200, 1}, {"?SIZE=32769", 200, 32769}, {"?size=0", 400, 0}, {"?size=100000001", 400, 0}, {"?size=no", 400, 0}} {
		t.Run(tc.query, func(t *testing.T) {
			rr := httptest.NewRecorder()
			(&PlaybackHandler{}).HandleBitrateTest(rr, httptest.NewRequest("GET", "/Playback/BitrateTest"+tc.query, nil))
			if rr.Code != tc.status || (tc.status == 200 && rr.Body.Len() != tc.size) {
				t.Fatalf("status=%d bytes=%d", rr.Code, rr.Body.Len())
			}
		})
	}
}

func TestPlaybackConstraintsFreezeTransportAndRecipe(t *testing.T) {
	version := catalog.FileVersion{FileID: 42, Container: "mkv", CodecVideo: "h264", CodecAudio: "aac", Bitrate: 60000, VideoTracks: []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}}, AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 6}}}
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	profile := DeviceProfile{DirectPlayProfiles: []DirectPlayProfile{{Type: "Video", Container: "mkv", VideoCodec: "h264", AudioCodec: "aac"}}, TranscodingProfiles: []TranscodingProfile{{Type: "Video", Container: "ts", Protocol: "hls", VideoCodec: "h264", AudioCodec: "aac"}, {Type: "Video", Container: "mp4", Protocol: "hls", VideoCodec: "h264", AudioCodec: "aac"}}}
	for _, tc := range []struct {
		name       string
		req        playbackInfoRequest
		profileCap int64
	}{{"body", playbackInfoRequest{MaxStreamingBitrate: 4000000}, 0}, {"profile", playbackInfoRequest{}, 4000000}, {"tighter profile", playbackInfoRequest{MaxStreamingBitrate: 8000000}, 4000000}} {
		t.Run(tc.name, func(t *testing.T) {
			p := profile
			p.MaxStreamingBitrate = tc.profileCap
			s := h.buildPlaybackSource("item", "play", version, p, tc.req, true)
			if s.SupportsDirectPlay || s.SupportsDirectStream || s.HLSRemux || !s.SupportsTranscoding || s.TargetBitrateKbps != 3608 {
				t.Fatalf("source=%+v", s)
			}
			data, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			var persisted PlaybackMediaSource
			if err = json.Unmarshal(data, &persisted); err != nil || persisted.TargetBitrateKbps != 3608 {
				t.Fatalf("persisted=%+v err=%v", persisted, err)
			}
		})
	}
	s := h.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{MaxAudioChannels: 1}, true)
	if s.SupportsDirectPlay || s.SupportsDirectStream || s.TargetAudioChannels != 1 {
		t.Fatalf("mono source=%+v", s)
	}
	version.Bitrate = 2000
	s = h.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{MaxStreamingBitrate: 4000000}, true)
	if !s.SupportsDirectPlay {
		t.Fatal("below ceiling original lost")
	}
	q := playbackInfoRequest{MaxStreamingBitrate: 9000000, MaxAudioChannels: 6, StartTimeTicks: 1}
	applyPlaybackQueryOverrides(&q, url.Values{"maxstreamingbitrate": {"4000000"}, "maxaudiochannels": {"1"}, "starttimeticks": {"50000000"}})
	if q.MaxStreamingBitrate != 4000000 || q.MaxAudioChannels != 1 || q.StartTimeTicks != 50000000 {
		t.Fatalf("query=%+v", q)
	}
}

func TestPlaybackInfoMonoOnlyTranscodingProfile(t *testing.T) {
	h, item := newSubtitleSelectionHandler(t)
	response := postPlaybackInfo(t, h, item, `{"MaxAudioChannels":1,"EnableDirectPlay":false,"EnableDirectStream":false,"DeviceProfile":{"TranscodingProfiles":[{"Type":"Video","Protocol":"hls","Container":"ts","VideoCodec":"h264","AudioCodec":"aac","MaxAudioChannels":"1"}]}}`)
	if len(response.MediaSources) != 1 || !response.MediaSources[0].SupportsTranscoding || response.MediaSources[0].TranscodingURL == "" {
		t.Fatalf("mono output unavailable: %+v", response)
	}
	_, source, ok := h.playbackStore.FindByRoute("token-1", response.MediaSources[0].ID)
	if !ok || source == nil || source.TargetAudioChannels != 1 || source.HLSRemux {
		t.Fatalf("mono recipe: %+v", source)
	}
}

func TestMonoOutputProfilePreservesTranscodeRestrictions(t *testing.T) {
	version := subtitleSelectionVersion()
	profile := DeviceProfile{TranscodingProfiles: []TranscodingProfile{{Type: "Video", Protocol: "hls", Container: "ts", VideoCodec: "h264", AudioCodec: "aac", MaxAudioChannels: "1"}}}
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	for _, tc := range []struct {
		name       string
		req        playbackInfoRequest
		resolution string
		allow4K    bool
		want       bool
	}{
		{"mono matches", playbackInfoRequest{MaxAudioChannels: 1}, "1080p", true, true},
		{"stereo does not match", playbackInfoRequest{}, "1080p", true, false},
		{"encoding disabled", playbackInfoRequest{MaxAudioChannels: 1, EnableTranscoding: new(false)}, "1080p", true, false},
		{"4K policy disabled", playbackInfoRequest{MaxAudioChannels: 1}, "2160p", false, false},
		{"bitrate too low", playbackInfoRequest{MaxAudioChannels: 1, MaxStreamingBitrate: 100000}, "1080p", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.req.EnableDirectPlay = new(false)
			tc.req.EnableDirectStream = new(false)
			version.Resolution = tc.resolution
			source := h.buildPlaybackSource("item", "play", version, profile, tc.req, tc.allow4K)
			if source.SupportsTranscoding != tc.want || source.CanBurnSubtitle != tc.want {
				t.Fatalf("transcoding=%v burn=%v want %v", source.SupportsTranscoding, source.CanBurnSubtitle, tc.want)
			}
		})
	}
}

func TestMonoTranscodingProfileMatchesActualContainer(t *testing.T) {
	version := subtitleSelectionVersion()
	version.CodecVideo = "h264"
	version.AudioTracks[0].Codec = "eac3"
	version.AudioTracks[0].Channels = 6
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	for _, container := range []string{"ts", "mp4"} {
		t.Run(container, func(t *testing.T) {
			profile := DeviceProfile{
				DirectPlayProfiles:  []DirectPlayProfile{{Type: "Video", Container: "mkv", VideoCodec: "h264", AudioCodec: "aac"}},
				TranscodingProfiles: []TranscodingProfile{{Type: "Video", Protocol: "hls", Container: container, VideoCodec: "h264", AudioCodec: "aac", MaxAudioChannels: "1"}},
			}
			source := h.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{MaxAudioChannels: 1}, true)
			dto := h.mediaSourceDTO("item", "play", "token", source)
			if source.SupportsDirectPlay || !source.SupportsTranscoding || source.TargetAudioChannels != 1 || source.HLSRemux != (container == "mp4") || dto.TranscodingContainer != container || dto.TranscodingURL == "" {
				t.Fatalf("wrong negotiated container: source=%+v dto=%+v", source, dto)
			}
			stereo := h.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{MaxAudioChannels: 2}, true)
			if stereo.SupportsTranscoding || stereo.HLSRemux {
				t.Fatalf("mono-only profile accepted stereo output: %+v", stereo)
			}
		})
	}
}

func TestRemuxOnlyPublishedURLIsNotStatic(t *testing.T) {
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	dto := h.mediaSourceDTO("item", "play", "token", PlaybackMediaSource{ID: "source", SupportsDirectStream: true})
	u, err := url.Parse(dto.DirectStreamURL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("static") != "false" {
		t.Fatalf("URL=%s", dto.DirectStreamURL)
	}
	dto = h.mediaSourceDTO("item", "play", "token", PlaybackMediaSource{ID: "source", SupportsDirectPlay: true})
	u, _ = url.Parse(dto.DirectStreamURL)
	if u.Query().Get("static") != "true" {
		t.Fatalf("URL=%s", dto.DirectStreamURL)
	}
}

func TestSubtitleTimingRequestTransformsOutput(t *testing.T) {
	r := httptest.NewRequest("GET", "/subtitle?EndPositionTicks=40000000&AddVttTimeMap=true", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("routeFormat", "vtt")
	route.URLParams.Add("routeStartPositionTicks", "20000000")
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
	rr := httptest.NewRecorder()
	(&PlaybackHandler{}).deliverSubtitle(rr, r, "vtt", []byte("WEBVTT\n\n00:00:01.000 --> 00:00:03.000\nfirst\n\n00:00:03.500 --> 00:00:06.000\nsecond\n\n00:00:07.000 --> 00:00:08.000\nlast\n"))
	body := rr.Body.String()
	if rr.Code != 200 || !strings.Contains(body, "MPEGTS:180000") || !strings.Contains(body, "00:00:00.000 --> 00:00:01.000") || !strings.Contains(body, "00:00:01.500 --> 00:00:02.000") || strings.Contains(body, "last") {
		t.Fatalf("status=%d body=%s", rr.Code, body)
	}
}

func TestBitmapSubtitleInventoryAndUnsupportedBurnIn(t *testing.T) {
	version := catalog.FileVersion{VideoTracks: []models.VideoTrack{{Codec: "h264"}}, SubtitleTracks: []catalog.VersionSubtitleTrack{{Index: 1, Codec: "hdmv_pgs_subtitle"}}}
	streams := buildMediaStreams("item", "source", version)
	if len(streams) != 2 || streams[1].IsTextSubtitleStream || streams[1].DeliveryURL != "" {
		t.Fatalf("streams=%+v", streams)
	}
	source := PlaybackMediaSource{Version: version, SupportsDirectPlay: true, SupportsTranscoding: true, SelectedSubtitleStreamIndex: new(1)}
	applyCompatSubtitleDelivery(&source, DeviceProfile{SubtitleProfiles: []SubtitleProfile{{Format: "hdmv_pgs_subtitle", Method: "Embed"}}}, false)
	if !source.SupportsDirectPlay || source.SupportsTranscoding {
		t.Fatalf("source=%+v", source)
	}
	source.SupportsTranscoding = true
	applyCompatSubtitleDelivery(&source, DeviceProfile{SubtitleProfiles: []SubtitleProfile{{Format: "hdmv_pgs_subtitle", Method: "Encode"}}}, false)
	if source.SupportsDirectPlay || source.SupportsTranscoding {
		t.Fatalf("unsupported burn-in=%+v", source)
	}
}

func TestRemoteTranscodeRetainsNegotiatedCeilings(t *testing.T) {
	var received transcodenode.TranscodeStartRequest
	node := fakeTranscodeNode(t, &received)
	h, _, store := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
	source := testRemoteTranscodeSource()
	source.TargetBitrateKbps = 3608
	source.TargetAudioChannels = 1
	store.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1", MediaSources: []PlaybackMediaSource{source}})
	if err := h.startRemoteTranscode(t.Context(), "play-1", "upstream-1", source, &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}, 0, node.URL); err != nil {
		t.Fatal(err)
	}
	if received.TargetBitrateKbps != 3608 || received.TargetAudioChannels != 1 {
		t.Fatalf("request=%+v", received)
	}
	persisted, ok := store.Get("play-1")
	if !ok || persisted.Recipe == nil || persisted.Recipe.TargetBitrateKbps != 3608 || persisted.Recipe.TargetAudioChannels != 1 {
		t.Fatalf("persisted=%+v", persisted)
	}
}

func TestEmbeddedSubtitleBurnInLocalAndRemoteRecipe(t *testing.T) {
	version := testCompatVersion()
	version.SubtitleTracks = []catalog.VersionSubtitleTrack{{Index: 3, Codec: "hdmv_pgs_subtitle"}}
	source := testCompatSource(NewResourceIDCodec(), version)
	source.CanBurnSubtitle = true
	source.SelectedSubtitleStreamIndex = new(3)
	applyCompatSubtitleDelivery(&source, DeviceProfile{SubtitleProfiles: []SubtitleProfile{{Format: "hdmv_pgs_subtitle", Method: "Encode"}}}, false)
	if !source.SubtitleBurnIn || !source.SupportsTranscoding || source.SupportsDirectPlay || source.HLSRemux {
		t.Fatalf("source=%+v", source)
	}
	source.TargetBitrateKbps = 3608
	source.TargetAudioChannels = 1
	file := &models.MediaFile{ID: 42, FilePath: filepath.Join(t.TempDir(), "movie.mkv")}
	if err := os.WriteFile(file.FilePath, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1", MediaSources: []PlaybackMediaSource{source}})
	h := &PlaybackHandler{playbackStore: store, fileResolver: testCompatFileResolver{file: file}, TranscodeDir: t.TempDir(), FFmpegPath: writeCompatTestFFmpeg(t), tm: playback.NewTranscodeManager()}
	live, err := h.ensureTranscodeSession(t.Context(), "play-1", "upstream-1", source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	opts := live.Opts()
	if !opts.SubtitleBurnIn || opts.SubtitleCodec != "hdmv_pgs_subtitle" || opts.SubtitleTrackIndex != 0 || opts.TargetBitrateKbps != 3608 || opts.TargetAudioChannels != 1 {
		t.Fatalf("opts=%+v", opts)
	}
	var received transcodenode.TranscodeStartRequest
	node := fakeTranscodeNode(t, &received)
	remote, _, remoteStore := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
	remoteStore.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1", MediaSources: []PlaybackMediaSource{source}})
	if err := remote.startRemoteTranscode(t.Context(), "play-1", "upstream-1", source, file, 0, node.URL); err != nil {
		t.Fatal(err)
	}
	if !received.SubtitleBurnIn || received.SubtitleCodec != "hdmv_pgs_subtitle" || received.SubtitleTrackIndex != 0 {
		t.Fatalf("remote=%+v", received)
	}
	persisted, _ := remoteStore.Get("play-1")
	if persisted.Recipe == nil || !persisted.Recipe.SubtitleBurnIn || persisted.Recipe.SubtitleCodec != "hdmv_pgs_subtitle" {
		t.Fatalf("recipe=%+v", persisted.Recipe)
	}
}

func TestJSONSubtitleTimingWindow(t *testing.T) {
	for _, copyTimestamps := range []bool{false, true} {
		t.Run(strconv.FormatBool(copyTimestamps), func(t *testing.T) {
			r := httptest.NewRequest("GET", "/subtitle?EndPositionTicks=40000000&CopyTimestamps="+strconv.FormatBool(copyTimestamps), nil)
			route := chi.NewRouteContext()
			route.URLParams.Add("routeFormat", "js")
			route.URLParams.Add("routeStartPositionTicks", "20000000")
			r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
			rr := httptest.NewRecorder()
			(&PlaybackHandler{}).deliverSubtitle(rr, r, "vtt", []byte("WEBVTT\n\n00:00:01.000 --> 00:00:03.000\nfirst\n\n00:00:03.500 --> 00:00:06.000\nsecond\n\n00:00:07.000 --> 00:00:08.000\nlast\n"))
			var response struct {
				TrackEvents []struct {
					Text                                 string
					StartPositionTicks, EndPositionTicks int64
				}
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			offset := int64(0)
			if copyTimestamps {
				offset = 20000000
			}
			if rr.Code != 200 || len(response.TrackEvents) != 2 || response.TrackEvents[0].Text != "first" || response.TrackEvents[0].StartPositionTicks != offset || response.TrackEvents[0].EndPositionTicks != offset+10000000 || response.TrackEvents[1].StartPositionTicks != offset+15000000 || response.TrackEvents[1].EndPositionTicks != offset+20000000 {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestJSONSubtitleTimingEmptyWindow(t *testing.T) {
	r := httptest.NewRequest("GET", "/subtitle?StartPositionTicks=90000000", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("routeFormat", "js")
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
	rr := httptest.NewRecorder()
	(&PlaybackHandler{}).deliverSubtitle(rr, r, "vtt", []byte("WEBVTT\n\n00:00:01.000 --> 00:00:03.000\nfirst\n"))
	if rr.Code != 200 || strings.TrimSpace(rr.Body.String()) != `{"TrackEvents":[]}` {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestProgressiveRemuxChecksMP4OutputContainer(t *testing.T) {
	version := catalog.FileVersion{FileID: 42, Container: "mkv", CodecVideo: "h264", CodecAudio: "aac", Bitrate: 8000,
		VideoTracks: []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080, Bitrate: 7500000}}, AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 6}}}
	handler := &PlaybackHandler{codec: NewResourceIDCodec()}
	for _, tc := range []struct{ property, value string }{
		{"Width", "1280"}, {"VideoBitrate", "4000000"}, {"AudioChannels", "2"},
	} {
		t.Run(tc.property, func(t *testing.T) {
			profile := DeviceProfile{DirectPlayProfiles: []DirectPlayProfile{{Type: "Video", Container: "mkv", VideoCodec: "h264", AudioCodec: "aac"}}, ContainerProfiles: []ContainerProfile{{Type: "Video", Container: "mp4", Conditions: []ProfileCondition{{Condition: "LessThanEqual", Property: tc.property, Value: tc.value, IsRequired: true}}}}}
			// Original MKV remains supported; restrictions apply to the delivered MP4.
			if !profile.SupportsDirectPlay(version) {
				t.Fatal("MP4 condition rejected original MKV")
			}
			if profile.SupportsDirectStream(version) || profile.SupportsVideoCodecForDirectStream(version) || profile.SupportsAudioCodecForDirectStream(version) {
				t.Fatal("progressive MP4 ignored output restriction")
			}
			source := handler.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{EnableDirectPlay: new(false), EnableTranscoding: new(false)}, true)
			if source.SupportsDirectStream || source.SupportsTranscoding {
				t.Fatalf("advertised unsupported remux: %+v", source)
			}
			profile.ContainerProfiles[0].Container = "mkv"
			if profile.SupportsDirectPlay(version) {
				t.Fatal("original MKV ignored source restriction")
			}
			source = handler.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{EnableDirectPlay: new(false), EnableTranscoding: new(false)}, true)
			if !source.SupportsDirectStream || source.HLSRemux {
				t.Fatalf("MKV restriction leaked into progressive MP4: %+v", source)
			}
		})
	}
}

func TestHLSAudioTranscodeChecksAdaptedOutputContainer(t *testing.T) {
	version := catalog.FileVersion{FileID: 42, Container: "mkv", CodecVideo: "h264", CodecAudio: "eac3",
		VideoTracks: []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}}, AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 6}}}
	profile := DeviceProfile{DirectPlayProfiles: []DirectPlayProfile{{Type: "Video", Container: "mp4", VideoCodec: "h264", AudioCodec: "aac"}},
		TranscodingProfiles: []TranscodingProfile{{Type: "Video", Protocol: "hls", Container: "mp4", VideoCodec: "h264", AudioCodec: "aac"}},
		ContainerProfiles:   []ContainerProfile{{Type: "Video", Container: "mp4", Conditions: []ProfileCondition{{Condition: "LessThanEqual", Property: "AudioChannels", Value: "2", IsRequired: true}}}}}
	handler := &PlaybackHandler{codec: NewResourceIDCodec()}
	source := handler.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{}, true)
	if !source.SupportsTranscoding || !source.HLSRemux || !source.TranscodeAudio || source.TargetAudioChannels != 2 {
		t.Fatalf("AAC stereo HLS rejected by source surround channels: %+v", source)
	}
	profile.ContainerProfiles[0].Conditions = append(profile.ContainerProfiles[0].Conditions, ProfileCondition{Condition: "LessThanEqual", Property: "Width", Value: "1280", IsRequired: true})
	source = handler.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{}, true)
	if source.SupportsTranscoding || source.HLSRemux {
		t.Fatal("audio adaptation bypassed MP4 video dimension restriction")
	}
}

func TestProgressiveAndHLSCodecContainerScopes(t *testing.T) {
	version := catalog.FileVersion{Container: "mkv", CodecVideo: "h264", CodecAudio: "aac", VideoTracks: []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}}, AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2}}}
	for _, container := range []string{"hls", "mp4"} {
		t.Run(container, func(t *testing.T) {
			profile := DeviceProfile{TranscodingProfiles: []TranscodingProfile{{Type: "Video", Protocol: "hls", Container: "mp4", VideoCodec: "h264", AudioCodec: "aac"}}, CodecProfiles: []CodecProfile{{Type: "Video", Codec: "h264", Container: container, SubContainer: "mp4", Conditions: []ProfileCondition{{Condition: "LessThanEqual", Property: "Width", Value: "1280", IsRequired: true}}}}}
			if got := profile.SupportsVideoCodecForDirectStream(version); got != (container == "hls") {
				t.Fatalf("progressive support=%v for codec container=%s", got, container)
			}
			if profile.SupportsHLSRemuxForAudioStream(version, nil) {
				t.Fatal("HLS bypassed applicable output codec condition")
			}
			if !profile.SupportsDirectPlay(version) {
				t.Fatal("output codec condition rejected original MKV")
			}
		})
	}
}
