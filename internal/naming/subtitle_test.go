package naming

import "testing"

func TestSidecarSubtitleTags(t *testing.T) {
	tests := []struct {
		video, subtitle string
		want            SubtitleTags
		ok              bool
	}{
		{"/m/Movie (2020).mkv", "/m/Movie (2020).srt", SubtitleTags{}, true},
		{"/m/Movie (2020).mkv", "/m/Movie (2020).sdh.en.SRT", SubtitleTags{Language: "en", HearingImpaired: true}, true},
		{"/m/Movie.Name.2020.mkv", "/m/Movie.Name.2020.hr.ai.srt", SubtitleTags{Language: "hr"}, true},
		{"/m/Movie.part2.mkv", "/m/Movie.part2.fr.forced.ass", SubtitleTags{Language: "fr", Forced: true}, true},
		{"/m/Movie.mkv", "/m/MovieExtra.srt", SubtitleTags{}, false},
		{"/m/Movie.mkv", "/m/Other.en.srt", SubtitleTags{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.subtitle, func(t *testing.T) {
			got, ok := SidecarSubtitleTags(tc.video, tc.subtitle)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("SidecarSubtitleTags(%q, %q) = %+v, %v; want %+v, %v", tc.video, tc.subtitle, got, ok, tc.want, tc.ok)
			}
		})
	}
}
