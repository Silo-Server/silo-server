package overlays

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestNormalizeVideoCodec(t *testing.T) {
	cases := []struct {
		name string
		file *models.MediaFile
		want string
	}{
		{"empty", &models.MediaFile{}, ""},
		{"file codec hevc", &models.MediaFile{CodecVideo: "hevc"}, "H.265"},
		{"file codec h264", &models.MediaFile{CodecVideo: "h264"}, "H.264"},
		{"file codec av1", &models.MediaFile{CodecVideo: "av1"}, "AV1"},
		{"file codec vp9", &models.MediaFile{CodecVideo: "VP9"}, "VP9"},
		{"track overrides file", &models.MediaFile{
			CodecVideo:  "h264",
			VideoTracks: []models.VideoTrack{{Codec: "hevc"}},
		}, "H.265"},
		{"empty track codec falls back to file codec", &models.MediaFile{
			CodecVideo:  "h265",
			VideoTracks: []models.VideoTrack{{Codec: ""}},
		}, "H.265"},
		{"unknown codec passes through uppercased", &models.MediaFile{CodecVideo: "foobar"}, "FOOBAR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeVideoCodec(tc.file); got != tc.want {
				t.Errorf("normalizeVideoCodec = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeAudioChannels(t *testing.T) {
	cases := []struct {
		name string
		file *models.MediaFile
		want string
	}{
		{"empty", &models.MediaFile{}, ""},
		{"file channels 2", &models.MediaFile{AudioChannels: 2}, "Stereo"},
		{"file channels 6", &models.MediaFile{AudioChannels: 6}, "5.1"},
		{"file channels 8", &models.MediaFile{AudioChannels: 8}, "7.1"},
		{"file channels 1", &models.MediaFile{AudioChannels: 1}, "Mono"},
		{"unusual channel count", &models.MediaFile{AudioChannels: 10}, "10ch"},
		{"default track wins over higher", &models.MediaFile{
			AudioTracks: []models.AudioTrack{
				{Channels: 8},
				{Channels: 2, Default: true},
			},
		}, "Stereo"},
		{"highest non-default when no default", &models.MediaFile{
			AudioTracks: []models.AudioTrack{
				{Channels: 2},
				{Channels: 8},
			},
		}, "7.1"},
		{"track beats file fallback", &models.MediaFile{
			AudioChannels: 2,
			AudioTracks:   []models.AudioTrack{{Channels: 6, Default: true}},
		}, "5.1"},
		{"default track with 0 channels does not shadow earlier non-default", &models.MediaFile{
			AudioTracks: []models.AudioTrack{
				{Channels: 8},
				{Channels: 0, Default: true},
			},
		}, "7.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeAudioChannels(tc.file); got != tc.want {
				t.Errorf("normalizeAudioChannels = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeContainer(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"mkv", "MKV"},
		{"MP4", "MP4"},
		{" mov ", "MOV"},
	}
	for _, tc := range cases {
		if got := normalizeContainer(tc.in); got != tc.want {
			t.Errorf("normalizeContainer(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeAspectRatio(t *testing.T) {
	cases := []struct {
		name string
		file *models.MediaFile
		want string
	}{
		{"empty", &models.MediaFile{}, ""},
		{"16:9 string", &models.MediaFile{
			VideoTracks: []models.VideoTrack{{AspectRatio: "16:9"}},
		}, "16:9"},
		{"239:100 normalizes to 2.39:1", &models.MediaFile{
			VideoTracks: []models.VideoTrack{{AspectRatio: "239:100"}},
		}, "2.39:1"},
		{"2.40:1 snaps to 2.39:1", &models.MediaFile{
			VideoTracks: []models.VideoTrack{{AspectRatio: "2.40:1"}},
		}, "2.39:1"},
		{"derives from 1920x1080", &models.MediaFile{
			VideoTracks: []models.VideoTrack{{Width: 1920, Height: 1080}},
		}, "16:9"},
		{"derives from 4096x1716 (cinemascope)", &models.MediaFile{
			VideoTracks: []models.VideoTrack{{Width: 4096, Height: 1716}},
		}, "2.39:1"},
		{"derives from 720x480", &models.MediaFile{
			VideoTracks: []models.VideoTrack{{Width: 720, Height: 480}},
		}, "1.50:1"},
		{"malformed ratio falls back to dimensions", &models.MediaFile{
			VideoTracks: []models.VideoTrack{{AspectRatio: "garbage", Width: 1920, Height: 1080}},
		}, "16:9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeAspectRatio(tc.file); got != tc.want {
				t.Errorf("normalizeAspectRatio = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDetectMultiAudio(t *testing.T) {
	cases := []struct {
		name string
		file *models.MediaFile
		want bool
	}{
		{"empty", &models.MediaFile{}, false},
		{"single track", &models.MediaFile{
			AudioTracks: []models.AudioTrack{{Language: "eng"}},
		}, false},
		{"two same language", &models.MediaFile{
			AudioTracks: []models.AudioTrack{{Language: "eng"}, {Language: "eng"}},
		}, false},
		{"two distinct", &models.MediaFile{
			AudioTracks: []models.AudioTrack{{Language: "eng"}, {Language: "jpn"}},
		}, true},
		{"undefined ignored", &models.MediaFile{
			AudioTracks: []models.AudioTrack{{Language: "und"}, {Language: ""}, {Language: "eng"}},
		}, false},
		{"three distinct returns true at 2", &models.MediaFile{
			AudioTracks: []models.AudioTrack{{Language: "eng"}, {Language: "spa"}, {Language: "fre"}},
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectMultiAudio(tc.file); got != tc.want {
				t.Errorf("detectMultiAudio = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDetectMultiSub(t *testing.T) {
	cases := []struct {
		name string
		file *models.MediaFile
		want bool
	}{
		{"empty", &models.MediaFile{}, false},
		{"one embedded", &models.MediaFile{
			SubtitleTracks: []models.SubtitleTrack{{Language: "eng"}},
		}, true},
		{"one external", &models.MediaFile{
			ExternalSubtitles: []models.ExternalSubtitle{{Language: "eng"}},
		}, true},
		{"both present", &models.MediaFile{
			SubtitleTracks:    []models.SubtitleTrack{{Language: "eng"}},
			ExternalSubtitles: []models.ExternalSubtitle{{Language: "jpn"}},
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectMultiSub(tc.file); got != tc.want {
				t.Errorf("detectMultiSub = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNormalizeReleaseType(t *testing.T) {
	cases := []struct {
		path, want string
	}{
		{"", ""},
		{"/movies/Foo.2020.1080p.REMUX.mkv", "REMUX"},
		{"/movies/Foo.2020.1080p.WEB-DL.mkv", "WEB-DL"},
		{"/movies/Foo.2020.1080p.BluRay.mkv", "BluRay"},
		{"/movies/Foo.2020.480p.DVDRip.mkv", "DVD"},
		// Word-boundary check: "dvd" embedded in a benign word must not match.
		{"/movies/goodvideos/Foo.2020.1080p.mkv", ""},
	}
	for _, tc := range cases {
		if got := normalizeReleaseType(tc.path); got != tc.want {
			t.Errorf("normalizeReleaseType(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestNormalizeHDR(t *testing.T) {
	cases := []struct {
		name string
		file *models.MediaFile
		want string
	}{
		{"sdr", &models.MediaFile{}, ""},
		{"bare boolean", &models.MediaFile{HDR: true}, "HDR"},
		{"hdr10 via color transfer", &models.MediaFile{
			HDR:         true,
			VideoTracks: []models.VideoTrack{{ColorTransfer: "smpte2084"}},
		}, "HDR10"},
		{"hlg via color transfer", &models.MediaFile{
			HDR:         true,
			VideoTracks: []models.VideoTrack{{ColorTransfer: "arib-std-b67"}},
		}, "HLG"},
		{"dv only", &models.MediaFile{
			HDR:         true,
			VideoTracks: []models.VideoTrack{{DolbyVision: "Profile 5"}},
		}, "DV"},
		{"dv with hdr10 base layer", &models.MediaFile{
			HDR: true,
			VideoTracks: []models.VideoTrack{{
				DolbyVision:   "Profile 8",
				ColorTransfer: "smpte2084",
			}},
		}, "DV HDR10"},
		{"dv via profile number only", &models.MediaFile{
			HDR:         true,
			VideoTracks: []models.VideoTrack{{DVProfile: 5}},
		}, "DV"},
		{"dv via DOVI range type only", &models.MediaFile{
			HDR: true,
			VideoTracks: []models.VideoTrack{{
				VideoRangeType: "DOVIWithHDR10",
				ColorTransfer:  "smpte2084",
			}},
		}, "DV HDR10"},
		{"hdr10 via range type without color transfer", &models.MediaFile{
			HDR:         true,
			VideoTracks: []models.VideoTrack{{VideoRangeType: "HDR10"}},
		}, "HDR10"},
		{"hlg via range type without color transfer", &models.MediaFile{
			HDR:         true,
			VideoTracks: []models.VideoTrack{{VideoRangeType: "HLG"}},
		}, "HLG"},
		{"hdr10+ via flag", &models.MediaFile{
			HDR: true,
			VideoTracks: []models.VideoTrack{{
				HDR10Plus:     true,
				ColorTransfer: "smpte2084",
			}},
		}, "HDR10+"},
		{"hdr10+ via range type", &models.MediaFile{
			HDR:         true,
			VideoTracks: []models.VideoTrack{{VideoRangeType: "HDR10Plus"}},
		}, "HDR10+"},
		{"dv with hdr10+ base layer", &models.MediaFile{
			HDR:         true,
			VideoTracks: []models.VideoTrack{{VideoRangeType: "DOVIWithELHDR10Plus"}},
		}, "DV HDR10+"},
		{"sdr range type stays sdr", &models.MediaFile{
			VideoTracks: []models.VideoTrack{{VideoRangeType: "SDR"}},
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeHDR(tc.file); got != tc.want {
				t.Errorf("normalizeHDR = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBestFileRangeTieBreaking(t *testing.T) {
	genericHDR := &models.MediaFile{Resolution: "2160p", HDR: true}
	explicitHDR10 := &models.MediaFile{
		Resolution:  "2160p",
		HDR:         true,
		VideoTracks: []models.VideoTrack{{ColorTransfer: "smpte2084"}},
	}
	dv := &models.MediaFile{
		Resolution: "2160p",
		HDR:        true,
		VideoTracks: []models.VideoTrack{{
			DolbyVision:   "Profile 8",
			ColorTransfer: "smpte2084",
		}},
	}
	sdr := &models.MediaFile{Resolution: "2160p"}
	dvProfileOnly := &models.MediaFile{
		Resolution:  "2160p",
		HDR:         true,
		VideoTracks: []models.VideoTrack{{DVProfile: 5}},
	}
	lowResDV := &models.MediaFile{
		Resolution:  "1080p",
		HDR:         true,
		VideoTracks: []models.VideoTrack{{DolbyVision: "Profile 5"}},
	}
	rangeTypeHDR10 := &models.MediaFile{
		Resolution:  "2160p",
		HDR:         true,
		VideoTracks: []models.VideoTrack{{VideoRangeType: "HDR10"}},
	}

	cases := []struct {
		name  string
		files []*models.MediaFile
		want  *models.MediaFile
	}{
		{"nil files ignored", []*models.MediaFile{nil, genericHDR}, genericHDR},
		{"dv beats first-scanned generic hdr at same resolution",
			[]*models.MediaFile{genericHDR, dv}, dv},
		{"dv beats explicit hdr10 at same resolution",
			[]*models.MediaFile{explicitHDR10, dv}, dv},
		{"dv via profile number only still outranks generic hdr",
			[]*models.MediaFile{genericHDR, dvProfileOnly}, dvProfileOnly},
		{"range-type-only hdr10 outranks bare boolean",
			[]*models.MediaFile{genericHDR, rangeTypeHDR10}, rangeTypeHDR10},
		{"explicit hdr10 beats bare boolean at same resolution",
			[]*models.MediaFile{genericHDR, explicitHDR10}, explicitHDR10},
		{"bare boolean beats sdr at same resolution",
			[]*models.MediaFile{sdr, genericHDR}, genericHDR},
		{"higher resolution still wins over lower-res dv",
			[]*models.MediaFile{lowResDV, genericHDR}, genericHDR},
		{"full tie keeps earliest file",
			[]*models.MediaFile{genericHDR, {Resolution: "2160p", HDR: true}}, genericHDR},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bestFile(tc.files); got != tc.want {
				t.Errorf("bestFile picked %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestBuildSummaryPrefersDolbyVisionSibling(t *testing.T) {
	genericHDR := &models.MediaFile{Resolution: "2160p", HDR: true, CodecVideo: "hevc"}
	dv := &models.MediaFile{
		Resolution: "2160p",
		HDR:        true,
		CodecVideo: "hevc",
		VideoTracks: []models.VideoTrack{{
			Codec:         "hevc",
			DolbyVision:   "Profile 8",
			ColorTransfer: "smpte2084",
		}},
	}
	got := BuildSummary([]*models.MediaFile{genericHDR, dv})
	if got == nil {
		t.Fatal("expected non-nil summary")
	}
	if got.HDR != "DV HDR10" {
		t.Errorf("HDR = %q, want %q", got.HDR, "DV HDR10")
	}
}

func TestBuildSummaryAggregatesNewFields(t *testing.T) {
	file := &models.MediaFile{
		Resolution: "1080p",
		CodecVideo: "hevc",
		Container:  "mkv",
		VideoTracks: []models.VideoTrack{{
			Codec:       "hevc",
			Width:       1920,
			Height:      1080,
			AspectRatio: "16:9",
		}},
		AudioTracks: []models.AudioTrack{
			{Language: "eng", Channels: 8, Default: true, Title: "Atmos"},
			{Language: "spa", Channels: 6},
		},
		SubtitleTracks: []models.SubtitleTrack{{Language: "eng"}},
	}
	got := BuildSummary([]*models.MediaFile{file})
	if got == nil {
		t.Fatal("expected non-nil summary")
	}
	if got.Resolution != "1080p" {
		t.Errorf("Resolution = %q, want %q", got.Resolution, "1080p")
	}
	if got.VideoCodec != "H.265" {
		t.Errorf("VideoCodec = %q, want %q", got.VideoCodec, "H.265")
	}
	if got.AudioChannels != "7.1" {
		t.Errorf("AudioChannels = %q, want %q", got.AudioChannels, "7.1")
	}
	if got.Container != "MKV" {
		t.Errorf("Container = %q, want %q", got.Container, "MKV")
	}
	if got.AspectRatio != "16:9" {
		t.Errorf("AspectRatio = %q, want %q", got.AspectRatio, "16:9")
	}
	if got.Audio != "Atmos" {
		t.Errorf("Audio = %q, want %q", got.Audio, "Atmos")
	}
	if !got.MultiAudio {
		t.Error("MultiAudio = false, want true")
	}
	if !got.MultiSub {
		t.Error("MultiSub = false, want true")
	}
}
