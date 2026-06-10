package scanner

import "testing"

func TestMangaSeriesFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/m/manga/Official/Kurosagi Corpse Delivery Service/V2006/Kurosagi 10.cbz", "Kurosagi Corpse Delivery Service"},
		{"/m/manga/One-Punch Man/One-Punch Man 178 (2023) (Digital) (LuCaZ).cbz", "One-Punch Man"},
		{"/m/manga/Bakuman/v13/Bakuman v13 (2012).cbz", "Bakuman"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := mangaSeriesFromPath(tc.path); got != tc.want {
				t.Fatalf("mangaSeriesFromPath(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestParseMangaIndex(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		wantVol string
		wantIdx float64
		wantHas bool
	}{
		{"bare chapter", "One-Punch Man 178 (2023) (Digital) (LuCaZ)", "", 178, true},
		{"volume", "Bakuman v13 (2012) (Digital) (aKraa)", "v13", 13, true},
		{"chapter c-prefix", "Dead Mount Death Play c128 (2025) (Digital) (UP!) (Oak)", "", 128, true},
		{"vol-year issue", "Berserk Vol.2003 #04 (July, 2004)", "Vol.2003", 4, true},
		{"vol-year no issue", "Berserk Vol.2003 (2004)", "", 0, false},
		{"decimal chapter", "Kindergarten WARS 109.1 (2025) (Digital) (Rillant)", "", 109.1, true},
		{"subtitle then volume", "The Ancient Magus' Bride - Wizard's Blue v04 (2022) (Digital)", "v04", 4, true},
		{"no number", "Some Oneshot (2020) (Digital) (grp)", "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vol, idx, has := parseMangaIndex(tc.file)
			if vol != tc.wantVol || has != tc.wantHas || idx != tc.wantIdx {
				t.Fatalf("parseMangaIndex(%q) = (%q,%v,%v), want (%q,%v,%v)", tc.file, vol, idx, has, tc.wantVol, tc.wantIdx, tc.wantHas)
			}
		})
	}
}

// TestParseMangaIndexCorpus is a regression test against real-world manga
// release filenames following common scanlation naming conventions.
// Extensions are already stripped (as parseMangaIndex expects).
// At least 95% of names must yield has==true; pure one-shots with no number
// are the only legitimate misses.
func TestParseMangaIndexCorpus(t *testing.T) {
	corpus := []string{
		// bare chapter numbers (most common pattern)
		"One-Punch Man 178 (2023) (Digital) (LuCaZ)",
		"One-Punch Man 001 (2012) (Digital) (LuCaZ)",
		"Attack on Titan 139 (2021) (Digital) (Chromatic)",
		"Chainsaw Man 097 (2021) (Digital) (LuCaZ)",
		"Chainsaw Man 001 (2019) (Digital) (LuCaZ)",
		"Spy x Family 090 (2024) (Digital) (Izar)",
		"Demon Slayer - Kimetsu no Yaiba 205 (2020) (Digital) (LuCaZ)",
		"My Hero Academia 430 (2024) (Digital) (LuCaZ)",
		"Jujutsu Kaisen 271 (2024) (Digital) (LuCaZ)",
		"Vinland Saga 215 (2024) (Digital) (dAY)",
		// decimal chapter numbers
		"Kindergarten WARS 109.1 (2025) (Digital) (Rillant)",
		"Bleach 686.5 (2016) (Digital) (LuCaZ)",
		"One Piece 1000.1 (2021) (Digital) (LuCaZ)",
		"Berserk 364.1 (2022) (Digital) (Oak)",
		// volume prefix (vNN form)
		"Bakuman v13 (2012) (Digital) (aKraa)",
		"Fullmetal Alchemist v27 (2011) (Digital) (Izar)",
		"Death Note v12 (2006) (Digital) (Chromatic)",
		"Vinland Saga v26 (2022) (Digital) (dAY)",
		"The Ancient Magus' Bride - Wizard's Blue v04 (2022) (Digital)",
		"Blue Period v14 (2023) (Digital) (LuCaZ)",
		// volume prefix (vol. form)
		"Dragon Ball Vol.001 (2003) (Digital) (Izar)",
		"Naruto Vol.072 (2014) (Digital) (Chromatic)",
		"Bleach Vol.074 (2016) (Digital) (LuCaZ)",
		// chapter c-prefix
		"Dead Mount Death Play c128 (2025) (Digital) (UP!) (Oak)",
		"To Your Eternity c185 (2024) (Digital) (LuCaZ)",
		"Kaiju No. 8 ch.100 (2024) (Digital) (Izar)",
		// zero-padded chapter numbers
		"Berserk 001 (1990) (Digital) (Scans)",
		"Berserk 364 (2021) (Digital) (Oak)",
		"Vagabond 327 (2015) (Digital) (LuCaZ)",
		// series with hyphens and special chars in name
		"One-Punch Man 001 (2012) (Digital) (LuCaZ)",
		"Fullmetal Alchemist - Brotherhood 064 (2010) (Digital) (Izar)",
		"JoJo's Bizarre Adventure - Part 8 - JoJolion 110 (2021) (Digital) (Chromatic)",
		// high chapter numbers
		"One Piece 1100 (2023) (Digital) (LuCaZ)",
		"Fairy Tail 545 (2017) (Digital) (Chromatic)",
		// two-digit volumes
		"Berserk v41 (2022) (Digital) (Oak)",
		"Vagabond v37 (2009) (Digital) (LuCaZ)",
	}

	misses := 0
	for _, name := range corpus {
		if _, _, has := parseMangaIndex(name); !has {
			misses++
			t.Logf("no index parsed: %q", name)
		}
	}
	// Allow a small fraction of legitimate one-shots with no number.
	if float64(misses)/float64(len(corpus)) > 0.05 {
		t.Fatalf("parser missed %d/%d (>5%%); investigate patterns above", misses, len(corpus))
	}
}
