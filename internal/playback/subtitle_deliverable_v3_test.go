package playback

import "testing"

func TestSubtitleFormatDeliverableV3(t *testing.T) {
	for codec, want := range map[string]bool{
		"srt": true, "subrip": true, "ass": true, "webvtt": true,
		"hdmv_pgs_subtitle": true, "dvd_subtitle": true,
		"dvb_teletext": true,
		"sub":          false, "": false, "unknown": false,
	} {
		if got := SubtitleFormatDeliverableV3(codec); got != want {
			t.Errorf("SubtitleFormatDeliverableV3(%q) = %v, want %v", codec, got, want)
		}
	}
}
