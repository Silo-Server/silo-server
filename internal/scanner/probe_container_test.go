package scanner

import (
	"maps"
	"testing"
)

// The format names are what ffprobe reports for each container.
func TestConvertProbeDataContainer(t *testing.T) {
	cases := []struct {
		filename, formatName, want string
	}{
		{"/m/a.mkv", "matroska,webm", "mkv"},
		{"/m/a.webm", "matroska,webm", "mkv"},
		{"/m/a.mp4", "mov,mp4,m4a,3gp,3g2,mj2", "mp4"},
		{"/m/a.mov", "mov,mp4,m4a,3gp,3g2,mj2", "mp4"},
		{"/m/a.3gp", "mov,mp4,m4a,3gp,3g2,mj2", "mp4"},
		{"/m/a.f4v", "mov,mp4,m4a,3gp,3g2,mj2", "mp4"},
		{"/m/a.avi", "avi", "avi"},
		{"/m/a.divx", "avi", "avi"},
		{"/m/a.ts", "mpegts", "ts"},
		{"/m/a.m2ts", "mpegts", "m2ts"},
		{"/m/a.MTS", "mpegts", "m2ts"},
		{"/m/a.mpg", "mpeg", "mpeg"},
		{"/m/a.mpeg", "mpeg", "mpeg"},
		{"/m/a.wmv", "asf", "wmv"},
		{"/m/a.asf", "asf", "wmv"},
		{"/m/a.flv", "flv", "flv"},
		{"/m/a.ogv", "ogg", "ogg"},
		{"/m/a.ogm", "ogg", "ogg"},
		// No filename (older callers and fixtures): format name alone.
		{"", "mpegts", "ts"},
	}
	for _, tc := range cases {
		raw := &ffprobeOutput{Format: ffprobeFormat{Filename: tc.filename, FormatName: tc.formatName}}
		if got := convertProbeData(raw).Container; got != tc.want {
			t.Errorf("container for %q (%s) = %q, want %q", tc.filename, tc.formatName, got, tc.want)
		}
	}
}

// Ogg (Vorbis, Opus) carries its Vorbis comments on the audio stream, and
// ffprobe leaves format.tags empty, so the book's tags come from that
// stream. Other containers keep format tags only: an m4b audio stream's
// language and handler tags are not book metadata.
func TestConvertProbeDataFormatTagsFromOggAudioStream(t *testing.T) {
	cover := ffprobeStream{CodecType: "video", CodecName: "mjpeg", Tags: map[string]string{"comment": "Cover (front)"}}
	opus := ffprobeStream{CodecType: "audio", CodecName: "opus", Tags: map[string]string{
		"TITLE": " Orchard ", "ARTIST": "Zoë Ångström", "narrator": "Søren", "DATE": "2018",
	}}
	cases := []struct {
		name       string
		formatName string
		formatTags map[string]string
		streams    []ffprobeStream
		want       map[string]string
	}{
		{
			name:       "ogg stream comments",
			formatName: "ogg",
			streams:    []ffprobeStream{cover, opus},
			want:       map[string]string{"title": "Orchard", "artist": "Zoë Ångström", "narrator": "Søren", "date": "2018"},
		},
		{
			name:       "ogg format tag wins over stream comment",
			formatName: "ogg",
			formatTags: map[string]string{"Title": "Format Title"},
			streams:    []ffprobeStream{opus},
			want:       map[string]string{"title": "Format Title", "artist": "Zoë Ångström", "narrator": "Søren", "date": "2018"},
		},
		{
			name:       "m4b ignores stream tags",
			formatName: "mov,mp4,m4a,3gp,3g2,mj2",
			formatTags: map[string]string{"title": "Book"},
			streams:    []ffprobeStream{{CodecType: "audio", CodecName: "aac", Tags: map[string]string{"language": "und", "handler_name": "SoundHandler"}}},
			want:       map[string]string{"title": "Book"},
		},
	}
	for _, tc := range cases {
		raw := &ffprobeOutput{Format: ffprobeFormat{FormatName: tc.formatName, Tags: tc.formatTags}, Streams: tc.streams}
		got := convertProbeData(raw).FormatTags
		if !maps.Equal(got, tc.want) {
			t.Errorf("%s: FormatTags = %v, want %v", tc.name, got, tc.want)
		}
	}
}
