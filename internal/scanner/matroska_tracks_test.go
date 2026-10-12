package scanner

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/mediaprobe"
)

func mkvTrack(number, kind uint64, codec string) mediaprobe.MatroskaTrack {
	return mediaprobe.MatroskaTrack{Number: number, Type: kind, CodecID: codec}
}

const (
	mkvVideo    = mediaprobe.MatroskaTrackTypeVideo
	mkvAudio    = mediaprobe.MatroskaTrackTypeAudio
	mkvSubtitle = mediaprobe.MatroskaTrackTypeSubtitle
)

func TestMatroskaSubtitleTrackIDsMatchesByStreamPosition(t *testing.T) {
	tests := []struct {
		name      string
		tracks    []mediaprobe.MatroskaTrack
		video     int
		audio     int
		subtitles []matroskaSubtitleStream
		want      []string
	}{
		{
			// A remux that dropped tracks 2 and 3 keeps the original numbers,
			// so TrackNumber is not stream index + 1.
			name: "gaps in track numbers",
			tracks: []mediaprobe.MatroskaTrack{
				mkvTrack(1, mkvVideo, "V_MPEG4/ISO/AVC"),
				mkvTrack(4, mkvAudio, "A_EAC3"),
				mkvTrack(5, mkvSubtitle, "S_TEXT/UTF8"),
				mkvTrack(6, mkvSubtitle, "S_TEXT/UTF8"),
			},
			video: 1, audio: 1,
			subtitles: []matroskaSubtitleStream{{2, "subrip"}, {3, "subrip"}},
			want:      []string{"5", "6"},
		},
		{
			name: "subtitles interleaved with audio",
			tracks: []mediaprobe.MatroskaTrack{
				mkvTrack(10, mkvVideo, "V_MPEGH/ISO/HEVC"),
				mkvTrack(20, mkvSubtitle, "S_HDMV/PGS"),
				mkvTrack(30, mkvAudio, "A_TRUEHD"),
				mkvTrack(40, mkvSubtitle, "S_TEXT/ASS"),
			},
			video: 1, audio: 1,
			subtitles: []matroskaSubtitleStream{{1, "hdmv_pgs_subtitle"}, {3, "ass"}},
			want:      []string{"20", "40"},
		},
		{
			// FFmpeg creates no stream for unsupported track types or
			// inconsistent CodecIDs, which shifts every later index.
			name: "entries FFmpeg skips",
			tracks: []mediaprobe.MatroskaTrack{
				mkvTrack(1, mkvVideo, "V_MPEG4/ISO/AVC"),
				mkvTrack(2, 18, "B_VOBBTN"),
				mkvTrack(3, mkvAudio, "V_WRONG"),
				mkvTrack(4, mkvAudio, "A_AAC"),
				mkvTrack(5, mkvSubtitle, ""),
				mkvTrack(6, mkvSubtitle, "S_TEXT/UTF8"),
			},
			video: 1, audio: 1,
			subtitles: []matroskaSubtitleStream{{2, "subrip"}},
			want:      []string{"6"},
		},
		{
			// FFmpeg reads S_TEXT/WEBVTT as no known codec; nothing can
			// confirm what the stream is, so its ID stays empty.
			name: "codec that cannot be corroborated",
			tracks: []mediaprobe.MatroskaTrack{
				mkvTrack(1, mkvVideo, "V_VP9"),
				mkvTrack(2, mkvSubtitle, "S_TEXT/WEBVTT"),
				mkvTrack(3, mkvSubtitle, "S_TEXT/UTF8"),
			},
			video:     1,
			subtitles: []matroskaSubtitleStream{{1, ""}, {2, "subrip"}},
			want:      []string{"", "3"},
		},
		{
			// Legacy ASS aliases still corroborate the layout, but Media3
			// drops these tracks, so their numbers would name nothing.
			name: "legacy ASS codec IDs",
			tracks: []mediaprobe.MatroskaTrack{
				mkvTrack(1, mkvVideo, "V_MPEG4/ISO/AVC"),
				mkvTrack(2, mkvSubtitle, "S_ASS"),
				mkvTrack(3, mkvSubtitle, "S_SSA"),
				mkvTrack(4, mkvSubtitle, "S_TEXT/ASS"),
			},
			video:     1,
			subtitles: []matroskaSubtitleStream{{1, "ass"}, {2, "ass"}, {3, "ass"}},
			want:      []string{"", "", "4"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := matroskaSubtitleTrackIDs(tc.tracks, tc.video, tc.audio, tc.subtitles)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ids = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMatroskaSubtitleTrackIDsRejectsUncertainLayouts(t *testing.T) {
	base := func() []mediaprobe.MatroskaTrack {
		return []mediaprobe.MatroskaTrack{
			mkvTrack(1, mkvVideo, "V_MPEG4/ISO/AVC"),
			mkvTrack(2, mkvAudio, "A_AAC"),
			mkvTrack(3, mkvSubtitle, "S_TEXT/UTF8"),
		}
	}
	subtitles := []matroskaSubtitleStream{{2, "subrip"}}
	tests := []struct {
		name      string
		tracks    func() []mediaprobe.MatroskaTrack
		video     int
		audio     int
		subtitles []matroskaSubtitleStream
	}{
		{"duplicate track number", func() []mediaprobe.MatroskaTrack {
			tracks := base()
			tracks[2].Number = 2
			return tracks
		}, 1, 1, subtitles},
		{"missing track number", func() []mediaprobe.MatroskaTrack {
			tracks := base()
			tracks[2].Number = 0
			return tracks
		}, 1, 1, subtitles},
		{"track number beyond 32 bits", func() []mediaprobe.MatroskaTrack {
			tracks := base()
			tracks[2].Number = 1 << 32
			return tracks
		}, 1, 1, subtitles},
		{"metadata track", func() []mediaprobe.MatroskaTrack {
			return append(base(), mkvTrack(4, mediaprobe.MatroskaTrackTypeMetadata, "D_WEBVTT/METADATA"))
		}, 1, 1, subtitles},
		{"probe saw more audio", base, 1, 2, subtitles},
		{"probe saw an extra subtitle", base, 1, 1, []matroskaSubtitleStream{{2, "subrip"}, {3, "subrip"}}},
		{"subtitle index on an audio entry", func() []mediaprobe.MatroskaTrack {
			tracks := base()
			tracks[1], tracks[2] = tracks[2], tracks[1]
			return tracks
		}, 1, 1, subtitles},
		{"codec disagrees", base, 1, 1, []matroskaSubtitleStream{{2, "ass"}}},
		{"legacy ASS alias on a SubRip stream", func() []mediaprobe.MatroskaTrack {
			tracks := base()
			tracks[2].CodecID = "S_SSA"
			return tracks
		}, 1, 1, subtitles},
		{"index out of range", base, 1, 1, []matroskaSubtitleStream{{7, "subrip"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ids, err := matroskaSubtitleTrackIDs(tc.tracks(), tc.video, tc.audio, tc.subtitles)
			if !errors.Is(err, errMatroskaLayoutMismatch) {
				t.Fatalf("ids = %q, err = %v; want a layout mismatch", ids, err)
			}
		})
	}
}

func TestProbeFileRecordsMatroskaSubtitleTrackNumbers(t *testing.T) {
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}
	probe, err := ProbeFile(context.Background(), ffprobe, "testdata/subtitles.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if probe.Container != "mkv" || len(probe.SubtitleTracks) != 2 {
		t.Fatalf("container %q with subtitle tracks %+v", probe.Container, probe.SubtitleTracks)
	}
	for i, want := range []struct {
		index int
		codec string
		id    string
	}{{2, "subrip", "3"}, {3, "ass", "4"}} {
		got := probe.SubtitleTracks[i]
		if got.Index != want.index || got.Codec != want.codec || got.ContainerTrackID != want.id {
			t.Fatalf("subtitle %d = index %d codec %q id %q, want %+v", i, got.Index, got.Codec, got.ContainerTrackID, want)
		}
	}
}
