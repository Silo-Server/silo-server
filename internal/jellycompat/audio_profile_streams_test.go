package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestBuildMediaStreamsCarriesAudioFacts(t *testing.T) {
	version := catalog.FileVersion{
		VideoTracks: []models.VideoTrack{{Codec: "hevc", Bitrate: 20_000}},
		AudioTracks: []models.AudioTrack{
			{
				Codec:    "eac3",
				Profile:  "Dolby Digital Plus + Dolby Atmos",
				Language: "eng",
				Layout:   "5.1(side)",
				Channels: 6,
				Bitrate:  768,
				Default:  true,
			},
			{
				Codec:    "dts",
				Profile:  "DTS-HD MA + DTS:X",
				Language: "eng",
				Channels: 8,
			},
			{
				Codec:    "aac",
				Profile:  "LC",
				Language: "eng",
				Channels: 2,
			},
		},
	}

	streams := buildMediaStreams("item", "source", version)
	if len(streams) != 4 {
		t.Fatalf("streams length = %d, want 4", len(streams))
	}

	// Silo stores kbps; Jellyfin's BitRate is bits per second.
	if streams[0].BitRate != 20_000_000 {
		t.Fatalf("video BitRate = %d, want 20000000", streams[0].BitRate)
	}

	atmos := streams[1]
	// Jellyfin trims ffprobe's layout variant: "5.1(side)" is reported as "5.1".
	if atmos.ChannelLayout != "5.1" {
		t.Fatalf("atmos ChannelLayout = %q, want 5.1", atmos.ChannelLayout)
	}
	if atmos.BitRate != 768_000 {
		t.Fatalf("atmos BitRate = %d, want 768000", atmos.BitRate)
	}
	if atmos.Profile != "Dolby Digital Plus + Dolby Atmos" {
		t.Fatalf("atmos Profile = %q", atmos.Profile)
	}
	if atmos.AudioSpatialFormat != "DolbyAtmos" {
		t.Fatalf("atmos AudioSpatialFormat = %q, want DolbyAtmos", atmos.AudioSpatialFormat)
	}
	if atmos.DisplayTitle != "English - Dolby Digital Plus + Dolby Atmos 5.1" {
		t.Fatalf("atmos DisplayTitle = %q", atmos.DisplayTitle)
	}

	dtsx := streams[2]
	if dtsx.Profile != "DTS-HD MA + DTS:X" {
		t.Fatalf("dtsx Profile = %q", dtsx.Profile)
	}
	if dtsx.AudioSpatialFormat != "DTSX" {
		t.Fatalf("dtsx AudioSpatialFormat = %q, want DTSX", dtsx.AudioSpatialFormat)
	}

	// The AAC "LC" profile is uninformative: it is carried on the stream but
	// must not replace the codec name in the display title, and it is not a
	// spatial format.
	aac := streams[3]
	if aac.Profile != "LC" {
		t.Fatalf("aac Profile = %q", aac.Profile)
	}
	if aac.AudioSpatialFormat != "None" {
		t.Fatalf("aac AudioSpatialFormat = %q, want None", aac.AudioSpatialFormat)
	}
	if aac.DisplayTitle != "English - AAC Stereo" {
		t.Fatalf("aac DisplayTitle = %q", aac.DisplayTitle)
	}
}

func TestBuildMediaStreamsJellyfin12LocalizedLabels(t *testing.T) {
	version := catalog.FileVersion{
		VideoTracks:    []models.VideoTrack{{Codec: "h264"}},
		AudioTracks:    []models.AudioTrack{{Codec: "aac", Language: "ja", Default: true}, {Codec: "ac3"}},
		SubtitleTracks: []catalog.VersionSubtitleTrack{{Codec: "subrip", Language: "en"}},
	}
	streams := buildMediaStreams("item", "source", version)
	if len(streams) != 4 {
		t.Fatalf("streams length = %d, want 4", len(streams))
	}
	if streams[0].LocalizedLanguage != "" || streams[0].LocalizedOriginal != "" {
		t.Fatalf("video stream carries audio labels: %+v", streams[0])
	}
	if streams[1].LocalizedLanguage != compatLanguageName("ja") || streams[1].LocalizedOriginal != "Original" {
		t.Fatalf("audio labels = %q/%q", streams[1].LocalizedLanguage, streams[1].LocalizedOriginal)
	}
	if streams[2].LocalizedLanguage != "" || streams[2].LocalizedOriginal != "Original" {
		t.Fatalf("an audio stream without a language has no LocalizedLanguage: %+v", streams[2])
	}
	if streams[3].LocalizedLanguage != compatLanguageName("en") || streams[3].LocalizedOriginal != "" {
		t.Fatalf("subtitle labels = %q/%q", streams[3].LocalizedLanguage, streams[3].LocalizedOriginal)
	}
}

// Jellyfin clients label tracks with DisplayTitle and LocalizedLanguage, so a
// tag with a region or script needs a readable name, not "Pt BR".
func TestBuildMediaStreamsNamesRegionalLanguages(t *testing.T) {
	cases := []struct {
		language          string
		wantLanguageName  string
		wantSubtitleTitle string
	}{
		{"pt", "Portuguese", "Portuguese (External, SRT)"},
		{"pt-BR", "Brazilian Portuguese", "Brazilian Portuguese (External, SRT)"},
		{"zh-Hant", "Traditional Chinese", "Traditional Chinese (External, SRT)"},
		{"es-419", "Latin American Spanish", "Latin American Spanish (External, SRT)"},
		{"pt-AO", "Portuguese (Angola)", "Portuguese (Angola) (External, SRT)"},
		{"sr-Latn", "Serbian (Latin)", "Serbian (Latin) (External, SRT)"},
		{"sr-Latn-RS", "Serbian (Latin, Serbia)", "Serbian (Latin, Serbia) (External, SRT)"},
		{"sr-Latn-CS", "Sr Latn CS", "Sr Latn CS (External, SRT)"},
		{"ur-Aran-PK", "Ur Aran PK", "Ur Aran PK (External, SRT)"},
		{"es-lat", "Es Lat", "Es Lat (External, SRT)"},
		{"en-sdh", "En Sdh", "En Sdh (External, SRT)"},
		{"ca-ES-valencia", "Ca ES Valencia", "Ca ES Valencia (External, SRT)"},
	}
	for _, tc := range cases {
		t.Run(tc.language, func(t *testing.T) {
			version := catalog.FileVersion{
				AudioTracks:    []models.AudioTrack{{Codec: "aac", Channels: 2, Language: tc.language}},
				SubtitleTracks: []catalog.VersionSubtitleTrack{{Codec: "subrip", Language: tc.language, External: true}},
			}
			streams := buildMediaStreams("item", "source", version)
			if len(streams) != 2 {
				t.Fatalf("streams length = %d, want 2", len(streams))
			}
			audio, subtitle := streams[0], streams[1]
			if audio.DisplayTitle != tc.wantLanguageName+" - AAC Stereo" || audio.LocalizedLanguage != tc.wantLanguageName {
				t.Fatalf("audio labels = %q/%q", audio.DisplayTitle, audio.LocalizedLanguage)
			}
			if subtitle.DisplayTitle != tc.wantSubtitleTitle || subtitle.LocalizedLanguage != tc.wantLanguageName {
				t.Fatalf("subtitle labels = %q/%q", subtitle.DisplayTitle, subtitle.LocalizedLanguage)
			}
			if audio.Language != tc.language || subtitle.Language != tc.language {
				t.Fatalf("wire languages = %q/%q, want %q", audio.Language, subtitle.Language, tc.language)
			}
		})
	}
}

// The catalog's per-viewer audio choice (language preference, original
// language, remembered series track) drives DefaultAudioStreamIndex.
func TestDefaultAudioStreamIndexUsesEffectiveAudioTrack(t *testing.T) {
	version := catalog.FileVersion{
		VideoTracks: []models.VideoTrack{{Codec: "h264"}},
		AudioTracks: []models.AudioTrack{{Codec: "aac", Language: "en", Default: true}, {Codec: "aac", Language: "ja"}},
	}
	if got := defaultAudioStreamIndex(version); got == nil || *got != 1 {
		t.Fatalf("without a viewer choice the file default wins: %v", got)
	}
	version.EffectiveAudioTrackIndex = intPtr(1)
	if got := defaultAudioStreamIndex(version); got == nil || *got != 2 {
		t.Fatalf("effective track 1 = stream %v, want 2", got)
	}
	version.EffectiveAudioTrackIndex = intPtr(9)
	if got := defaultAudioStreamIndex(version); got == nil || *got != 1 {
		t.Fatalf("an out-of-range choice falls back to the file default: %v", got)
	}
}

// A track FFmpeg cannot decode (ffprobe names no codec for AVS3 audio) is
// never the default, and an explicit choice of one gets no server route.
func TestBuildPlaybackSourceUndecodableAudioTrack(t *testing.T) {
	version := catalog.FileVersion{
		FileID: 7, Container: "mp4", CodecVideo: "hevc", CodecAudio: "eac3", Resolution: "1080p",
		VideoTracks: []models.VideoTrack{{Codec: "hevc", Width: 1920, Height: 1080}},
		AudioTracks: []models.AudioTrack{
			{Codec: "", Channels: 6, Language: "zh", Default: true},
			{Codec: "eac3", Channels: 6, Language: "zh"},
			{Codec: "aac", Channels: 2, Language: "zh"},
		},
	}
	if got := defaultAudioStreamIndex(version); got == nil || *got != 2 {
		t.Fatalf("default audio stream = %v, want the first decodable track at stream 2", got)
	}
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	profile := DefaultDeviceProfile()

	source := h.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{}, true)
	if source.SelectedAudioStreamIndex == nil || *source.SelectedAudioStreamIndex != 2 || !source.SupportsTranscoding {
		t.Fatalf("default source = selected %v transcoding %v, want stream 2 with transcoding", source.SelectedAudioStreamIndex, source.SupportsTranscoding)
	}

	source = h.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{AudioStreamIndex: compatIntValueOf(1)}, true)
	if source.SupportsDirectPlay || source.SupportsDirectStream || source.SupportsTranscoding || source.CanBurnSubtitle || source.HLSRemux || source.TranscodeAudio {
		t.Fatalf("undecodable selection offers a server route: %+v", source)
	}
}

// Jellyfin clients show a video stream's DisplayTitle in the version picker
// and media info. Jellyfin builds it from resolution, codec and range
// ("4K HEVC Dolby Vision Profile 8.1 (HDR10)", "1080p H264 SDR"); Silo also
// names HDR10+, which Jellyfin folds into "HDR".
func TestBuildMediaStreamsVideoDisplayTitle(t *testing.T) {
	const hevcLongName = "H.265 / HEVC (High Efficiency Video Coding)"
	uhd := func(track models.VideoTrack) models.VideoTrack {
		track.Codec, track.Title, track.Width, track.Height = "hevc", hevcLongName, 3840, 2160
		return track
	}
	cases := []struct {
		name  string
		track models.VideoTrack
		hdr   bool
		want  string
	}{
		{"hdr10", uhd(models.VideoTrack{ColorTransfer: "smpte2084"}), true, "4K HEVC HDR"},
		{"hlg", uhd(models.VideoTrack{ColorTransfer: "arib-std-b67"}), true, "4K HEVC HDR"},
		{"hdr10 plus", uhd(models.VideoTrack{ColorTransfer: "smpte2084", HDR10Plus: true}), true, "4K HEVC HDR10+"},
		{"sdr 1080p", models.VideoTrack{Codec: "h264", Title: "H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10", Width: 1920, Height: 1080}, false, "1080p H264 SDR"},
		{"dolby vision 8.1", uhd(models.VideoTrack{ColorTransfer: "smpte2084", DVProfile: 8, DVBLCompatID: 1}), true, "4K HEVC Dolby Vision Profile 8.1 (HDR10)"},
		{"dolby vision 8.1 hdr10 plus", uhd(models.VideoTrack{ColorTransfer: "smpte2084", DVProfile: 8, DVBLCompatID: 1, HDR10Plus: true}), true, "4K HEVC Dolby Vision Profile 8.1 (HDR10+)"},
		{"legacy dolby vision hdr10 plus", uhd(models.VideoTrack{ColorTransfer: "smpte2084", DolbyVision: "Profile 8", HDR10Plus: true}), true, "4K HEVC Dolby Vision Profile 8 (HDR10+)"},
		{"dolby vision 8.4", uhd(models.VideoTrack{ColorTransfer: "arib-std-b67", DVProfile: 8, DVBLCompatID: 4}), true, "4K HEVC Dolby Vision Profile 8.4 (HLG)"},
		{"dolby vision 8.2", uhd(models.VideoTrack{DVProfile: 8, DVBLCompatID: 2}), true, "4K HEVC Dolby Vision Profile 8.2 (SDR)"},
		{"dolby vision 7.6", uhd(models.VideoTrack{ColorTransfer: "smpte2084", DVProfile: 7, DVBLCompatID: 6}), true, "4K HEVC Dolby Vision Profile 7.6 (HDR10)"},
		{"dolby vision 5", uhd(models.VideoTrack{DVProfile: 5}), true, "4K HEVC Dolby Vision Profile 5"},
		{"dolby vision 10.1 hdr10 plus", uhd(models.VideoTrack{ColorTransfer: "smpte2084", DVProfile: 10, DVBLCompatID: 1, HDR10Plus: true}), true, "4K HEVC Dolby Vision Profile 10.1 (HDR10+)"},
		{"dolby vision range type only", uhd(models.VideoTrack{ColorTransfer: "smpte2084", VideoRangeType: "DOVIWithHDR10"}), true, "4K HEVC Dolby Vision"},
		{"dolby vision hdr10 plus range type only", uhd(models.VideoTrack{ColorTransfer: "smpte2084", VideoRangeType: "DOVIWithHDR10Plus"}), true, "4K HEVC Dolby Vision (HDR10+)"},
		{"legacy dolby vision string", uhd(models.VideoTrack{ColorTransfer: "smpte2084", DolbyVision: "Profile 8", DVBLCompatID: 1}), true, "4K HEVC Dolby Vision Profile 8.1 (HDR10)"},
		{"ultrawide 1080p", models.VideoTrack{Codec: "hevc", Width: 2560, Height: 1080}, false, "1080p HEVC SDR"},
		{"scope 4k", models.VideoTrack{Codec: "hevc", Width: 3840, Height: 1600}, false, "4K HEVC SDR"},
		{"interlaced 720", models.VideoTrack{Codec: "h264", Width: 1280, Height: 720, Interlaced: true}, false, "720i H264 SDR"},
		{"pal dvd", models.VideoTrack{Codec: "mpeg2video", Width: 720, Height: 576}, false, "576p MPEG2VIDEO SDR"},
		{"8k", models.VideoTrack{Codec: "hevc", Width: 7680, Height: 4320}, false, "8K HEVC SDR"},
		{"no dimensions", models.VideoTrack{Codec: "h264"}, false, "H264 SDR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			version := catalog.FileVersion{HDR: tc.hdr, VideoTracks: []models.VideoTrack{tc.track}}
			streams := buildMediaStreams("item", "source", version)
			if len(streams) != 1 {
				t.Fatalf("streams length = %d, want 1", len(streams))
			}
			if got := streams[0].DisplayTitle; got != tc.want {
				t.Fatalf("DisplayTitle = %q, want %q", got, tc.want)
			}
		})
	}
}
