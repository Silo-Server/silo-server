package jellycompat

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

func TestBuildPlaybackSourceWebOSDOVIMKVUsesVersionedContainerRules(t *testing.T) {
	version := catalog.FileVersion{
		FileID:     42,
		Resolution: "2160p",
		Container:  "mkv",
		CodecVideo: "hevc",
		CodecAudio: "eac3",
		HDR:        true,
		VideoTracks: []models.VideoTrack{{
			Codec: "hevc", Profile: "Main 10", Width: 3840, Height: 2160, BitDepth: 10,
			DVProfile: 8, VideoRangeType: "DOVIWithHDR10",
		}},
		AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}},
	}
	tests := []struct {
		name                    string
		restrictedContainers    string
		wantDirectPlay          bool
		wantHLSRemux            bool
		wantSupportsTranscoding bool
	}{
		{
			name:                    "WebOS 25 direct plays Dolby Vision MKV",
			restrictedContainers:    "-mp4,ts,mkv",
			wantDirectPlay:          true,
			wantHLSRemux:            false,
			wantSupportsTranscoding: false,
		},
		{
			name:                    "WebOS 24 remuxes Dolby Vision MKV",
			restrictedContainers:    "-mp4,ts",
			wantDirectPlay:          false,
			wantHLSRemux:            true,
			wantSupportsTranscoding: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, err := decodeDeviceProfile(strings.NewReader(fmt.Sprintf(`{
				"DirectPlayProfiles": [{
					"Type": "Video", "Container": "mkv", "VideoCodec": "hevc", "AudioCodec": "eac3"
				}],
				"TranscodingProfiles": [{
					"Type": "Video", "Protocol": "hls", "Container": "mp4", "VideoCodec": "hevc", "AudioCodec": "eac3"
				}],
				"CodecProfiles": [{
					"Type": "Video", "Container": %q, "Codec": "hevc",
					"Conditions": [{
						"Condition": "EqualsAny", "Property": "VideoRangeType", "Value": "SDR|HDR10|HDR10Plus|HLG", "IsRequired": false
					}]
				}]
			}`, tt.restrictedContainers)))
			if err != nil {
				t.Fatal(err)
			}

			source := (&PlaybackHandler{codec: NewResourceIDCodec()}).buildPlaybackSource(
				"item", "play", version, profile, playbackInfoRequest{}, false,
			)
			if source.SupportsDirectPlay != tt.wantDirectPlay {
				t.Fatalf("SupportsDirectPlay = %v, want %v", source.SupportsDirectPlay, tt.wantDirectPlay)
			}
			if source.HLSRemux != tt.wantHLSRemux {
				t.Fatalf("HLSRemux = %v, want %v", source.HLSRemux, tt.wantHLSRemux)
			}
			if source.SupportsTranscoding != tt.wantSupportsTranscoding {
				t.Fatalf("SupportsTranscoding = %v, want %v", source.SupportsTranscoding, tt.wantSupportsTranscoding)
			}
		})
	}
}

func TestWebOSDolbyVisionRemuxUsesMPEGTS(t *testing.T) {
	source := PlaybackMediaSource{
		HLSRemux: true,
		Version: catalog.FileVersion{VideoTracks: []models.VideoTrack{{
			Codec: "hevc", DVProfile: 8, VideoRangeType: "DOVIWithHDR10",
		}}},
	}

	if !compatWebOSDVMPEGTS("Mozilla/5.0 (Web0S; Linux/SmartTV)", source) {
		t.Fatal("WebOS Dolby Vision remux did not select MPEG-TS")
	}
	if compatWebOSDVMPEGTS("Mozilla/5.0 (Macintosh)", source) {
		t.Fatal("non-WebOS Dolby Vision remux unexpectedly selected MPEG-TS")
	}
	source.SupportsDirectPlay = true
	if compatWebOSDVMPEGTS("Mozilla/5.0 (WebOS; Linux/SmartTV)", source) {
		t.Fatal("direct-play source unexpectedly selected an HLS segment format")
	}
}

func TestHLSRemuxCodecProfileUsesHLSSubContainer(t *testing.T) {
	profile, err := decodeDeviceProfile(strings.NewReader(`{
		"TranscodingProfiles": [{
			"Type": "Video", "Protocol": "hls", "Container": "mp4", "VideoCodec": "hevc", "AudioCodec": "eac3"
		}],
		"CodecProfiles": [{
			"Type": "Video", "Container": "hls", "SubContainer": "ts", "Codec": "hevc",
			"Conditions": [{
				"Condition": "EqualsAny", "Property": "VideoRangeType", "Value": "SDR", "IsRequired": false
			}]
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	version := catalog.FileVersion{
		FileID: 42, Container: "mkv", CodecVideo: "hevc", CodecAudio: "eac3", HDR: true,
		VideoTracks: []models.VideoTrack{{Codec: "hevc", DVProfile: 8, VideoRangeType: "DOVIWithHDR10"}},
		AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}},
	}

	if !profile.SupportsHLSRemuxForAudioStream(version, defaultAudioStreamIndex(version)) {
		t.Fatal("HLS MP4 remux was rejected by a codec profile scoped only to the HLS TS sub-container")
	}
}

func TestHLSRemuxCodecProfileKeepsLiteralContainerListWithHLSToken(t *testing.T) {
	// Jellyfin substitutes SubContainer only when Container is exactly "hls".
	// A multi-token list containing hls keeps its literal token list, so this
	// incompatible profile never applies to the MP4 remux output.
	profile, err := decodeDeviceProfile(strings.NewReader(`{
		"TranscodingProfiles": [{
			"Type": "Video", "Protocol": "hls", "Container": "mp4", "VideoCodec": "hevc", "AudioCodec": "eac3"
		}],
		"CodecProfiles": [{
			"Type": "Video", "Container": "hls,dash", "SubContainer": "mp4", "Codec": "hevc",
			"Conditions": [{
				"Condition": "EqualsAny", "Property": "VideoRangeType", "Value": "SDR", "IsRequired": false
			}]
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	version := catalog.FileVersion{
		FileID: 42, Container: "mkv", CodecVideo: "hevc", CodecAudio: "eac3", HDR: true,
		VideoTracks: []models.VideoTrack{{Codec: "hevc", DVProfile: 8, VideoRangeType: "DOVIWithHDR10"}},
		AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}},
	}

	if !profile.SupportsHLSRemuxForAudioStream(version, defaultAudioStreamIndex(version)) {
		t.Fatal("HLS MP4 remux was rejected by an hls,dash-scoped codec profile that Jellyfin would never apply to MP4 output")
	}
}

func TestBuildPlaybackSourceHLSOutputAudioProfilePrefersCopyOverSourceRestriction(t *testing.T) {
	profile, err := decodeDeviceProfile(strings.NewReader(`{
		"DirectPlayProfiles": [{
			"Type": "Video", "Container": "mkv", "VideoCodec": "hevc", "AudioCodec": "eac3"
		}],
		"TranscodingProfiles": [{
			"Type": "Video", "Protocol": "hls", "Container": "mp4", "VideoCodec": "hevc", "AudioCodec": "eac3"
		}],
		"CodecProfiles": [{
			"Type": "VideoAudio", "Container": "-mp4,ts", "Codec": "eac3",
			"Conditions": [{
				"Condition": "LessThanEqual", "Property": "AudioChannels", "Value": "2", "IsRequired": false
			}]
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	version := catalog.FileVersion{
		FileID: 42, Resolution: "2160p", Container: "mkv", CodecVideo: "hevc", CodecAudio: "eac3", HDR: true,
		VideoTracks: []models.VideoTrack{{Codec: "hevc", DVProfile: 8, VideoRangeType: "DOVIWithHDR10"}},
		AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}},
	}

	source := (&PlaybackHandler{codec: NewResourceIDCodec()}).buildPlaybackSource(
		"item", "play", version, profile, playbackInfoRequest{}, false,
	)
	if source.SupportsDirectPlay {
		t.Fatal("SupportsDirectPlay = true, want source-scoped 2-channel restriction to block MKV direct play")
	}
	if !source.HLSRemux || !source.SupportsTranscoding {
		t.Fatalf("HLS remux = %v, SupportsTranscoding = %v; want MP4 HLS route", source.HLSRemux, source.SupportsTranscoding)
	}
	if source.TranscodeAudio {
		t.Fatal("TranscodeAudio = true, want output-scoped MP4 profile to preserve EAC3 copy")
	}
	if len(source.HLSRemuxAudioStreamIndexes) != 1 || source.HLSRemuxAudioStreamIndexes[0] != 1 {
		t.Fatalf("HLSRemuxAudioStreamIndexes = %v, want EAC3 stream 1 copy-compatible", source.HLSRemuxAudioStreamIndexes)
	}
}

func TestBuildPlaybackSourceHLSRemuxTranscodesAudioWhenCopyIsDisabled(t *testing.T) {
	profile := DeviceProfile{
		DirectPlayProfiles: []DirectPlayProfile{{
			Type: "Video", Container: "mp4", VideoCodec: "hevc", AudioCodec: "eac3",
		}},
		TranscodingProfiles: []TranscodingProfile{{
			Type: "Video", Protocol: "hls", Container: "mp4", VideoCodec: "hevc", AudioCodec: "aac",
		}},
	}
	version := catalog.FileVersion{
		FileID: 42, Resolution: "2160p", Container: "mkv", CodecVideo: "hevc", CodecAudio: "eac3", HDR: true,
		VideoTracks: []models.VideoTrack{{Codec: "hevc", DVProfile: 8, VideoRangeType: "DOVIWithHDR10"}},
		AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}},
	}

	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	source := h.buildPlaybackSource(
		"item",
		"play",
		version,
		profile,
		playbackInfoRequest{AllowAudioStreamCopy: boolPtr(false)},
		false,
	)
	if source.SupportsDirectPlay || source.SupportsDirectStream {
		t.Fatalf("source unexpectedly supports direct playback: direct=%v stream=%v", source.SupportsDirectPlay, source.SupportsDirectStream)
	}
	if !source.HLSRemux || !source.TranscodeAudio || !source.SupportsTranscoding {
		t.Fatalf(
			"source route = remux %v transcode-audio %v supports-transcoding %v, want video-copy HLS with AAC audio transcode",
			source.HLSRemux,
			source.TranscodeAudio,
			source.SupportsTranscoding,
		)
	}

	dto := h.mediaSourceDTO("item", "play", "token", source)
	if dto.TranscodingURL == "" || dto.TranscodingContainer != "mp4" {
		t.Fatalf("transcoding route = URL %q container %q, want usable MP4 HLS route", dto.TranscodingURL, dto.TranscodingContainer)
	}
}

func TestBuildPlaybackSourceKeepsLegacyTranscodingGateWithoutHLSProfile(t *testing.T) {
	// The client's DirectPlayProfiles accept the video codec but not the audio
	// codec, and its only TranscodingProfile is progressive HTTP: no profile
	// vouches for any HLS output, so no TranscodingURL may be minted.
	profile := DeviceProfile{
		DirectPlayProfiles: []DirectPlayProfile{{
			Type: "Video", Container: "mp4", VideoCodec: "hevc", AudioCodec: "aac",
		}},
		TranscodingProfiles: []TranscodingProfile{{
			Type: "Video", Protocol: "http", Container: "mp4", VideoCodec: "hevc", AudioCodec: "aac",
		}},
	}
	version := catalog.FileVersion{
		FileID: 42, Resolution: "2160p", Container: "mkv", CodecVideo: "hevc", CodecAudio: "eac3", HDR: true,
		VideoTracks: []models.VideoTrack{{Codec: "hevc", DVProfile: 8, VideoRangeType: "DOVIWithHDR10"}},
		AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}},
	}

	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	source := h.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{}, false)
	if source.SupportsDirectPlay || source.SupportsDirectStream {
		t.Fatalf("direct methods = play %v stream %v, want none", source.SupportsDirectPlay, source.SupportsDirectStream)
	}
	if source.SupportsTranscoding {
		t.Fatal("SupportsTranscoding = true, want no unverified fMP4 HLS offer for an HTTP-only profile")
	}
	if dto := h.mediaSourceDTO("item", "play", "token", source); dto.TranscodingURL != "" {
		t.Fatalf("TranscodingURL = %q, want empty", dto.TranscodingURL)
	}
}

func TestSupportedHLSRemuxAudioStreamIndexesFreezeCodecAndChannelLayout(t *testing.T) {
	// The copy playlist reuses the same EXT-X-MAP URL across a switch, so only
	// tracks interchangeable with the selected track's init segment (same
	// codec, same channel layout) may enter the switch allowlist even when the
	// device profile accepts the other codecs.
	profile := DeviceProfile{
		DirectPlayProfiles: []DirectPlayProfile{{
			Type: "Video", Container: "mp4", VideoCodec: "hevc", AudioCodec: "eac3,ac3",
		}},
		TranscodingProfiles: []TranscodingProfile{{
			Type: "Video", Protocol: "hls", Container: "mp4", VideoCodec: "hevc", AudioCodec: "eac3,ac3",
		}},
	}
	version := catalog.FileVersion{
		FileID: 42, Resolution: "2160p", Container: "mkv", CodecVideo: "hevc", CodecAudio: "eac3", HDR: true,
		VideoTracks: []models.VideoTrack{{Codec: "hevc", DVProfile: 8, VideoRangeType: "DOVIWithHDR10"}},
		AudioTracks: []models.AudioTrack{
			{Codec: "eac3", Channels: 6, Default: true},
			{Codec: "ac3", Channels: 6},
			{Codec: "eac3", Channels: 2},
			{Codec: "eac3", Channels: 6},
		},
	}

	source := (&PlaybackHandler{codec: NewResourceIDCodec()}).buildPlaybackSource(
		"item", "play", version, profile, playbackInfoRequest{}, false,
	)
	if !source.HLSRemux || source.TranscodeAudio {
		t.Fatalf("route = remux %v transcode-audio %v, want audio-copy remux", source.HLSRemux, source.TranscodeAudio)
	}
	if want := []int{1, 4}; !slices.Equal(source.HLSRemuxAudioStreamIndexes, want) {
		t.Fatalf("HLSRemuxAudioStreamIndexes = %v, want %v (6-channel EAC3 tracks only)", source.HLSRemuxAudioStreamIndexes, want)
	}
}

func TestBuildPlaybackSourceSafariHEVCMKVOffersHLSRemux(t *testing.T) {
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	version := catalog.FileVersion{
		FileID:     42,
		Resolution: "2160p",
		Container:  "mkv",
		CodecVideo: "hevc",
		CodecAudio: "eac3",
		HDR:        true,
		VideoTracks: []models.VideoTrack{{
			Codec:          "hevc",
			Profile:        "Main 10",
			Level:          153,
			Width:          3840,
			Height:         1606,
			BitDepth:       10,
			DVProfile:      8,
			HDR10Plus:      true,
			VideoRangeType: "DOVIWithHDR10Plus",
		}},
		AudioTracks: []models.AudioTrack{
			{Codec: "eac3", Channels: 6, Default: true},
			{Codec: "truehd", Channels: 8},
		},
	}
	profile := DeviceProfile{
		DirectPlayProfiles: []DirectPlayProfile{{
			Type:       "Video",
			Container:  "mp4,m4v",
			VideoCodec: "hevc,h264",
			AudioCodec: "aac,mp3,ac3,eac3,flac,alac",
		}},
		TranscodingProfiles: []TranscodingProfile{{
			Type:       "Video",
			Protocol:   "hls",
			Container:  "mp4",
			VideoCodec: "hevc,h264,vp9",
			AudioCodec: "aac,ac3,eac3,flac,alac",
		}},
		CodecProfiles: []CodecProfile{{
			Type:  "Video",
			Codec: "hevc",
			Conditions: []ProfileCondition{{
				Condition:  "EqualsAny",
				Property:   "VideoCodecTag",
				Value:      "hvc1|dvh1",
				IsRequired: true,
			}},
		}},
	}

	source := h.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{}, false)
	if source.SupportsDirectPlay {
		t.Fatal("SupportsDirectPlay = true, want false for MKV vs MP4")
	}
	if source.SupportsDirectStream {
		t.Fatal("SupportsDirectStream = true, want false because its static URL would serve MKV")
	}
	if !source.SupportsTranscoding {
		t.Fatal("SupportsTranscoding = false, want an HLS remux route")
	}
	if source.TranscodeAudio {
		t.Fatal("TranscodeAudio = true, want EAC3 copied because the HLS profile accepts it")
	}
	if len(source.HLSRemuxAudioStreamIndexes) != 1 || source.HLSRemuxAudioStreamIndexes[0] != 1 {
		t.Fatalf("HLSRemuxAudioStreamIndexes = %v, want only the EAC3 stream frozen as copy-compatible", source.HLSRemuxAudioStreamIndexes)
	}

	dto := h.mediaSourceDTO("item", "play", "token", source)
	if dto.TranscodingURL == "" {
		t.Fatal("TranscodingURL is empty")
	}
	if !strings.HasPrefix(dto.TranscodingURL, "/Videos/item/remux-v1/master.m3u8?") {
		t.Fatalf("TranscodingURL = %q, want rolling-deploy-safe remux-v1 route", dto.TranscodingURL)
	}
	if dto.TranscodingContainer != "mp4" {
		t.Fatalf("TranscodingContainer = %q, want mp4", dto.TranscodingContainer)
	}
	if dto.DirectStreamURL != "" {
		t.Fatalf("DirectStreamURL = %q, want empty", dto.DirectStreamURL)
	}
}

func TestStartRemoteSafariHEVCMKVRemuxCopiesAdvertisedCodecs(t *testing.T) {
	var request transcodenode.TranscodeStartRequest
	node := fakeTranscodeNode(t, &request)
	recipeStore := &stubRecipeNodeStore{}
	handler, _, playbackStore := newRemoteTranscodeHandler(t, node.URL, recipeStore)
	playbackStore.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1"})

	source := PlaybackMediaSource{
		ID:       "source-1",
		FileID:   42,
		HLSRemux: true,
		Version: catalog.FileVersion{
			FileID:     42,
			Resolution: "2160p",
			Container:  "mkv",
			CodecVideo: "hevc",
			CodecAudio: "eac3",
			HDR:        true,
			VideoTracks: []models.VideoTrack{{
				Codec: "hevc", DVProfile: 8, Width: 3840, Height: 1606, BitDepth: 10,
			}},
			AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}},
		},
	}
	file := &models.MediaFile{
		ID: 42, FilePath: "/media/movie.mkv", CodecVideo: "hevc", CodecAudio: "eac3",
		Resolution: "2160p", HDR: true,
		VideoTracks: []models.VideoTrack{{Codec: "hevc", DVProfile: 8, Width: 3840, Height: 1606, BitDepth: 10}},
		AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}},
	}

	if err := handler.startRemoteTranscode(context.Background(), "play-1", "upstream-1", source, file, 0, node.URL); err != nil {
		t.Fatalf("startRemoteTranscode: %v", err)
	}
	if request.TargetCodecVideo != "copy" || request.TargetCodecAudio != "copy" {
		t.Fatalf("remote codecs = video %q audio %q, want copy/copy", request.TargetCodecVideo, request.TargetCodecAudio)
	}
	if request.VideoSampleEntry != playback.VideoSampleEntryDVH1 {
		t.Fatalf("VideoSampleEntry = %q, want dvh1", request.VideoSampleEntry)
	}
	if request.CopyFMP4RecipeVersion != playback.CopyFMP4RecipeVersion {
		t.Fatalf("CopyFMP4RecipeVersion = %q, want %q", request.CopyFMP4RecipeVersion, playback.CopyFMP4RecipeVersion)
	}
	if request.SourceAudioChannels != 0 || request.TargetAudioChannels != 0 || request.AudioRecipeVersion != "" {
		t.Fatalf("remote request unexpectedly encodes audio: %+v", request)
	}
	if request.ToneMapMode != "" {
		t.Fatalf("ToneMapMode = %q, want no tone map for video copy", request.ToneMapMode)
	}

	card, ok := recipeStore.Get("upstream-1")
	if !ok {
		t.Fatal("remote recipe was not persisted")
	}
	if card.TargetCodecVideo != "copy" || card.TargetCodecAudio != "copy" || card.VideoSampleEntry != playback.VideoSampleEntryDVH1 {
		t.Fatalf("persisted recipe = video %q audio %q sample-entry %q, want copy/copy/dvh1", card.TargetCodecVideo, card.TargetCodecAudio, card.VideoSampleEntry)
	}
}

func TestRequiredVideoCodecTagUsesRemuxOutputOnly(t *testing.T) {
	profile := DeviceProfile{
		DirectPlayProfiles: []DirectPlayProfile{{
			Type: "Video", Container: "mp4", VideoCodec: "hevc", AudioCodec: "eac3",
		}},
		TranscodingProfiles: []TranscodingProfile{{
			Type: "Video", Protocol: "hls", Container: "mp4", VideoCodec: "hevc", AudioCodec: "eac3",
		}},
		CodecProfiles: []CodecProfile{{
			Type: "Video", Codec: "hevc",
			Conditions: []ProfileCondition{{
				Condition: "EqualsAny", Property: "VideoCodecTag", Value: "hvc1|dvh1", IsRequired: true,
			}},
		}},
	}
	version := catalog.FileVersion{
		FileID: 1, Resolution: "2160p", Container: "mp4", CodecVideo: "hevc", CodecAudio: "eac3",
		VideoTracks: []models.VideoTrack{{Codec: "hevc", DVProfile: 8}},
		AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}},
	}

	source := (&PlaybackHandler{codec: NewResourceIDCodec()}).buildPlaybackSource(
		"item", "play", version, profile, playbackInfoRequest{}, false,
	)
	if source.SupportsDirectPlay {
		t.Fatal("SupportsDirectPlay = true, but the source MP4 sample entry was never probed")
	}
	if !source.HLSRemux || !source.SupportsTranscoding {
		t.Fatalf("HLS remux route = remux %v transcode %v, want remuxed dvh1 output", source.HLSRemux, source.SupportsTranscoding)
	}
}

func TestHLSRemuxDoesNotClaimUnwrittenHVC1Tag(t *testing.T) {
	profile := DeviceProfile{
		TranscodingProfiles: []TranscodingProfile{{
			Type: "Video", Protocol: "hls", Container: "mp4", VideoCodec: "hevc", AudioCodec: "eac3",
		}},
		CodecProfiles: []CodecProfile{{
			Type: "Video", Codec: "hevc",
			Conditions: []ProfileCondition{{
				Condition: "Equals", Property: "VideoCodecTag", Value: "hvc1", IsRequired: true,
			}},
		}},
	}
	version := catalog.FileVersion{
		FileID: 1, Resolution: "2160p", Container: "mkv", CodecVideo: "hevc", CodecAudio: "eac3",
		VideoTracks: []models.VideoTrack{{Codec: "hevc"}},
		AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}},
	}

	source := (&PlaybackHandler{codec: NewResourceIDCodec()}).buildPlaybackSource(
		"item", "play", version, profile, playbackInfoRequest{}, false,
	)
	if source.HLSRemux || source.SupportsTranscoding {
		t.Fatalf("unwritten hvc1 tag produced route: remux %v transcode %v", source.HLSRemux, source.SupportsTranscoding)
	}
}
