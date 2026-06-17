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

func TestMangaSeriesGroupKey(t *testing.T) {
	a := mangaSeriesGroupKey(8, "One-Punch Man")
	b := mangaSeriesGroupKey(8, "  one-punch man ")
	c := mangaSeriesGroupKey(8, "Bakuman")
	d := mangaSeriesGroupKey(9, "One-Punch Man")
	if a == "" || a != b {
		t.Fatalf("same series must yield same key: %q vs %q", a, b)
	}
	if a == c || a == d {
		t.Fatalf("different series/library must differ: a=%q c=%q d=%q", a, c, d)
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
		{"vol-year issue", "Berserk Vol.2003 #04 (July, 2004)", "v04", 4, true},
		{"vol-year issue real-world", "10 Things I Want to Do Before I Turn 40 Vol.2025 #01 (May, 2025)", "v01", 1, true},
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

func TestCleanMangaSeriesName(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		// Trailing parentheticals stripped.
		{"404 Demons (Digital) (Oak)", "404 Demons"},
		{"Arifureta - From Commonplace to World's Strongest (Digital) (1r0n)", "Arifureta - From Commonplace to World's Strongest"},
		{"Angels of Death Episode.0 (2019-2024) (Digital) (LuCaZ)", "Angels of Death Episode.0"},
		{"Angel of the Night - Lucian - One-shot (2026) (Digital)", "Angel of the Night - Lucian - One-shot"},
		{"'Tis Time for 'Torture,' Princess (2019-2026) (Digital) (Antrill-Oak)", "'Tis Time for 'Torture,' Princess"},
		{"A Certain Scientific Railgun - Astral Buddy (2019)", "A Certain Scientific Railgun - Astral Buddy"},
		// No junk — must be returned unchanged.
		{"Amefurashi", "Amefurashi"},
		// Guardrail: folder that is ONLY parentheticals — return original trimmed input.
		{"(2025) (Digital)", "(2025) (Digital)"},
		// Middle parentheticals must be preserved.
		{"JoJo's Bizarre Adventure - Part 8 - JoJolion (something) extra", "JoJo's Bizarre Adventure - Part 8 - JoJolion (something) extra"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := cleanMangaSeriesName(tc.input)
			if got != tc.want {
				t.Fatalf("cleanMangaSeriesName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestMangaIndexForFile(t *testing.T) {
	cases := []struct {
		name       string
		base       string
		seriesName string
		wantVol    string
		wantIdx    float64
		wantHas    bool
	}{
		{
			name:       "404 Demons ch01 — series prefix stripped",
			base:       "404 Demons 01 (Digital-Compilation) (Oak)",
			seriesName: "404 Demons",
			wantVol:    "",
			wantIdx:    1,
			wantHas:    true,
		},
		{
			name:       "404 Demons ch09",
			base:       "404 Demons 09 (Digital-Compilation) (Oak)",
			seriesName: "404 Demons",
			wantVol:    "",
			wantIdx:    9,
			wantHas:    true,
		},
		{
			name:       "404 Demons v01 — volume token unambiguous",
			base:       "404 Demons v01 (Digital-Compilation) (Oak)",
			seriesName: "404 Demons",
			wantVol:    "v01",
			wantIdx:    1,
			wantHas:    true,
		},
		{
			name:       "404 Demons v10",
			base:       "404 Demons v10 (Digital-Compilation) (Oak)",
			seriesName: "404 Demons",
			wantVol:    "v10",
			wantIdx:    10,
			wantHas:    true,
		},
		{
			name:       "One-Punch Man ch178",
			base:       "One-Punch Man 178 (2023) (Digital) (LuCaZ)",
			seriesName: "One-Punch Man",
			wantVol:    "",
			wantIdx:    178,
			wantHas:    true,
		},
		{
			name:       "365 Days to the Wedding v03 — series number not grabbed",
			base:       "365 Days to the Wedding v03 (2024)",
			seriesName: "365 Days to the Wedding",
			wantVol:    "v03",
			wantIdx:    3,
			wantHas:    true,
		},
		{
			name:       "404 Demons one-shot — no number after prefix",
			base:       "404 Demons (2025) (Digital)",
			seriesName: "404 Demons",
			wantVol:    "",
			wantIdx:    0,
			wantHas:    false,
		},
		{
			name:       "fallback — base does not start with series name",
			base:       "Random 12",
			seriesName: "Different Series",
			wantVol:    "",
			wantIdx:    12,
			wantHas:    true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vol, idx, has := mangaIndexForFile(tc.base, tc.seriesName)
			if vol != tc.wantVol || idx != tc.wantIdx || has != tc.wantHas {
				t.Fatalf("mangaIndexForFile(%q, %q) = (%q, %v, %v), want (%q, %v, %v)",
					tc.base, tc.seriesName, vol, idx, has, tc.wantVol, tc.wantIdx, tc.wantHas)
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
