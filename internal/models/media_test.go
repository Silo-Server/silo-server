package models

import "testing"

func TestPersonKindAudiobookRoles(t *testing.T) {
	cases := []struct {
		kind PersonKind
		want string
	}{
		{PersonKindAuthor, "Author"},
		{PersonKindNarrator, "Narrator"},
	}
	for _, tc := range cases {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("%d.String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func TestNormalizeVideoBitDepth(t *testing.T) {
	tests := []struct {
		name        string
		explicit    int
		pixelFormat string
		profile     string
		want        int
	}{
		{name: "explicit wins", explicit: 8, pixelFormat: "yuv420p10le", profile: "Main 10", want: 8},
		{name: "hevc ten bit pixel format", pixelFormat: "yuv420p10le", profile: "Main 10", want: 10},
		{name: "p010 pixel format", pixelFormat: "p010le", profile: "Main 10", want: 10},
		{name: "main ten profile fallback", pixelFormat: "", profile: "Main 10", want: 10},
		{name: "ordinary planar eight bit", pixelFormat: "yuv420p", profile: "Main", want: 8},
		{name: "unknown remains unknown", pixelFormat: "unknown", profile: "", want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := NormalizeVideoBitDepth(test.explicit, test.pixelFormat, test.profile); got != test.want {
				t.Fatalf("NormalizeVideoBitDepth(%d, %q, %q) = %d, want %d", test.explicit, test.pixelFormat, test.profile, got, test.want)
			}
		})
	}
}
