package scanner

import "testing"

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
