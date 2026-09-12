package scanner

import "testing"

func TestConvertProbeDataCanonicalizesSubtitleLanguageTags(t *testing.T) {
	raw := &ffprobeOutput{Streams: []ffprobeStream{
		{CodecType: "subtitle", CodecName: "subrip", Tags: map[string]string{"language": "eng"}},
		{CodecType: "subtitle", CodecName: "subrip", Tags: map[string]string{"language": "pt-BR"}},
		{CodecType: "subtitle", CodecName: "subrip", Tags: map[string]string{"language": "zh-Hant"}},
		{CodecType: "subtitle", CodecName: "subrip", Tags: map[string]string{"language": "Arabic"}},
		{CodecType: "subtitle", CodecName: "subrip", Tags: map[string]string{"language": "???"}},
		{CodecType: "subtitle", CodecName: "subrip", Tags: map[string]string{}},
	}}
	got := convertProbeData(raw).SubtitleTracks
	want := []string{"en", "pt-BR", "zh-Hant", "ar", "", ""}
	if len(got) != len(want) {
		t.Fatalf("tracks = %d, want %d", len(got), len(want))
	}
	for i, track := range got {
		if track.Language != want[i] {
			t.Errorf("track %d language = %q, want %q", i, track.Language, want[i])
		}
	}
}
