package jellycompat

import "testing"

func TestCompatSubtitleModeMappingRoundTrips(t *testing.T) {
	for _, mode := range []string{compatSubtitleDefault, compatSubtitleSmart, compatSubtitleAlways, compatSubtitleOnlyForced, compatSubtitleNone} {
		native, showForced, ok := compatNativeSubtitleSettings(mode)
		if !ok {
			t.Fatalf("%s is not mapped", mode)
		}
		if got := compatJellyfinSubtitleMode(native, true, showForced, mode); got != mode {
			t.Fatalf("%s -> (%s, %v) -> %s", mode, native, showForced, got)
		}
	}
	if _, _, ok := compatNativeSubtitleSettings("Sometimes"); ok {
		t.Fatal("unknown modes must be rejected")
	}
	// Native settings made outside a Jellyfin client.
	for _, tc := range []struct {
		native     string
		set        bool
		showForced bool
		want       string
	}{
		{"auto", false, true, compatSubtitleDefault},
		{"auto", true, true, compatSubtitleSmart},
		{"always", true, true, compatSubtitleAlways},
		{"off", true, true, compatSubtitleOnlyForced},
		{"off", true, false, compatSubtitleNone},
	} {
		if got := compatJellyfinSubtitleMode(tc.native, tc.set, tc.showForced, ""); got != tc.want {
			t.Errorf("native %s set=%v forced=%v = %s, want %s", tc.native, tc.set, tc.showForced, got, tc.want)
		}
	}
}

// Cases follow Jellyfin 12.1 MediaStreamSelector.GetDefaultSubtitleStreamIndex.
func TestCompatDefaultSubtitleStreamIndexMatchesJellyfin(t *testing.T) {
	english := compatSubtitleCandidate{Index: 2, Language: "eng"}
	englishForced := compatSubtitleCandidate{Index: 3, Language: "eng", Forced: true}
	undefinedForced := compatSubtitleCandidate{Index: 4, Language: "und", Forced: true}
	french := compatSubtitleCandidate{Index: 5, Language: "fre"}
	frenchDefault := compatSubtitleCandidate{Index: 6, Language: "fre", Default: true}
	germanExternal := compatSubtitleCandidate{Index: 7, Language: "de", External: true}

	cases := []struct {
		name       string
		candidates []compatSubtitleCandidate
		preferred  []string
		mode       string
		audio      string
		want       int // -1 means none
	}{
		{"None never selects", []compatSubtitleCandidate{frenchDefault}, nil, compatSubtitleNone, "", -1},
		{"Default prefers external over default", []compatSubtitleCandidate{frenchDefault, germanExternal}, []string{"en"}, compatSubtitleDefault, "", 7},
		{"Default ignores unflagged tracks", []compatSubtitleCandidate{english, french}, []string{"en"}, compatSubtitleDefault, "", -1},
		{"Default takes forced when nothing else is flagged", []compatSubtitleCandidate{english, englishForced}, []string{"en"}, compatSubtitleDefault, "", 3},
		{"Smart subtitles foreign audio", []compatSubtitleCandidate{french, english, englishForced}, []string{"en"}, compatSubtitleSmart, "ja", 2},
		{"Smart is OnlyForced for preferred audio", []compatSubtitleCandidate{english, englishForced}, []string{"en"}, compatSubtitleSmart, "en", 3},
		{"Smart without a preference shows any subtitle", []compatSubtitleCandidate{french}, nil, compatSubtitleSmart, "en", 5},
		{"Always prefers a full preferred track", []compatSubtitleCandidate{englishForced, english}, []string{"en"}, compatSubtitleAlways, "en", 2},
		{"Always falls back to forced", []compatSubtitleCandidate{french, englishForced}, []string{"en"}, compatSubtitleAlways, "en", 3},
		{"OnlyForced prefers a preferred-language forced track", []compatSubtitleCandidate{undefinedForced, englishForced}, []string{"en"}, compatSubtitleOnlyForced, "", 3},
		{"OnlyForced accepts an undefined-language forced track", []compatSubtitleCandidate{french, undefinedForced}, []string{"en"}, compatSubtitleOnlyForced, "", 4},
		{"OnlyForced skips other-language forced tracks", []compatSubtitleCandidate{{Index: 8, Language: "ja", Forced: true}}, []string{"en"}, compatSubtitleOnlyForced, "", -1},
		{"regional preference stays exact", []compatSubtitleCandidate{{Index: 9, Language: "pt-PT"}, {Index: 10, Language: "pt-BR"}}, []string{"pt-BR"}, compatSubtitleAlways, "en", 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := compatDefaultSubtitleStreamIndex(tc.candidates, tc.preferred, tc.mode, tc.audio)
			if tc.want < 0 {
				if got != nil {
					t.Fatalf("got %d, want none", *got)
				}
				return
			}
			if got == nil || *got != tc.want {
				t.Fatalf("got %v, want %d", got, tc.want)
			}
		})
	}
}
